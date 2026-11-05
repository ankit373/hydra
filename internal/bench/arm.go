// SPDX-License-Identifier: MIT

package bench

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/provider"
)

// Arm answers one item. WorstCaseUSD bounds a call before it is made, so the budget
// can refuse it; Call returns what the provider reported. An error from Call stops
// the run: an item-level failure is an Attempt with ErrKind set, and counts.
type Arm interface {
	Name() string
	Config() ArmConfig
	Preflight(ctx context.Context) error
	WorstCaseUSD(req executor.Request) (float64, error)
	Call(ctx context.Context, it Item, req executor.Request) (Attempt, error)
}

// ArmConfig is what run.json freezes about an arm.
type ArmConfig struct {
	Kind      string   `json:"kind"`   // "direct"
	Models    []string `json:"models"` // "<provider>:<model id>", in fallback order
	FaultRate float64  `json:"fault_rate,omitempty"`
}

// Attempt is one arm's answer to one item, everything the provider reported.
type Attempt struct {
	Answer   string        `json:"answer"`
	Calls    []Call        `json:"calls"`    // every billed call, failed ones included
	Decision string        `json:"decision"` // direct arms: "accept"
	Latency  time.Duration `json:"latency_ns"`
	TTFT     time.Duration `json:"ttft_ns,omitempty"`  // 0 = unknown, never instant
	ErrKind  string        `json:"err_kind,omitempty"` // "transport" | "status:<code>" | "contract" | ""
}

// Call is one provider call, priced by the harness from the snapshot.
type Call struct {
	Head            string  `json:"head"`
	Model           string  `json:"model"` // as the response reported it
	InputTokens     int     `json:"input_tokens"`
	OutputTokens    int     `json:"output_tokens"`
	TokensEstimated bool    `json:"tokens_estimated"`
	CostUSD         float64 `json:"cost_usd"`
	PriceRef        string  `json:"price_ref"` // the snapshot row used; "reserved" when an error left it unknown
}

// ParseModel splits "<provider>:<model id>" and refuses an id nobody pinned yet.
func ParseModel(spec string) (prov, model string, err error) {
	prov, model, ok := strings.Cut(spec, ":")
	if !ok || model == "" || strings.ContainsAny(model, "<> ") {
		return "", "", fmt.Errorf("bench: model %q is not a pinned \"<provider>:<model id>\"", spec)
	}
	if prov != "bedrock" && prov != "openrouter" {
		return "", "", fmt.Errorf("bench: model %q: provider must be bedrock or openrouter", spec)
	}
	return prov, model, nil
}

// headFor builds the head a direct arm calls. The executor sends Meta["model"], so
// any enabled model is reachable whatever the router's own heads are.
func headFor(spec string) (provider.Head, string, error) {
	prov, model, err := ParseModel(spec)
	if err != nil {
		return provider.Head{}, "", err
	}
	id := "bench/" + prov + "/" + model
	return provider.Head{ID: id, Name: id, Provider: prov, Source: "env", Meta: map[string]string{"model": model}}, model, nil
}

// FaultPlan injects a 503 or 429 on the first head of static-fallback without calling
// the provider, so a fault costs nothing. Which items fault is a function of the seed.
type FaultPlan struct {
	Rate float64
	Seed uint64
}

func (f *FaultPlan) code(id string) int {
	if f == nil {
		return 0
	}
	h := hash64("fault", strconv.FormatUint(f.Seed, 10), id)
	if float64(h%10000) >= f.Rate*10000 {
		return 0
	}
	if h&(1<<32) != 0 {
		return http.StatusTooManyRequests
	}
	return http.StatusServiceUnavailable
}

// Direct calls providers through hydra's own executors: frontier, cheapest and mid
// with one model, static-fallback with an ordered list.
type Direct struct {
	name   string
	cfg    ArmConfig
	heads  []provider.Head
	models []string
	exec   func(provider.Head) executor.Executor
	prices PriceSnapshot
	faults *FaultPlan
}

func NewDirect(name string, cfg ArmConfig, prices PriceSnapshot, exec func(provider.Head) executor.Executor, faults *FaultPlan) (*Direct, error) {
	if len(cfg.Models) == 0 {
		return nil, fmt.Errorf("bench: arm %s has no model", name)
	}
	d := &Direct{name: name, cfg: cfg, exec: exec, prices: prices, faults: faults}
	for _, spec := range cfg.Models {
		h, model, err := headFor(spec)
		if err != nil {
			return nil, err
		}
		if _, ok := prices[model]; !ok {
			return nil, fmt.Errorf("bench: arm %s: %s is not in the price snapshot", name, model)
		}
		d.heads, d.models = append(d.heads, h), append(d.models, model)
	}
	return d, nil
}

func (d *Direct) Name() string      { return d.name }
func (d *Direct) Config() ArmConfig { return d.cfg }

// Preflight names a missing key or region up front rather than on item 1.
func (d *Direct) Preflight(context.Context) error {
	for _, h := range d.heads {
		if why := executor.Unroutable(h); why != "" {
			return fmt.Errorf("bench: arm %s cannot reach %s: %s", d.name, h.ID, why)
		}
	}
	return nil
}

// WorstCaseUSD reserves every model in the list, since any of them may be called.
func (d *Direct) WorstCaseUSD(req executor.Request) (float64, error) {
	var sum float64
	for _, m := range d.models {
		c, err := d.prices.worstCase(m, req)
		if err != nil {
			return 0, err
		}
		sum += c
	}
	return sum, nil
}

// Call moves to the next model only on a transport error, a 429 or a 5xx. A 4xx or a
// wrong answer never moves; a 401 or 403 stops the run, since no item can succeed.
func (d *Direct) Call(ctx context.Context, it Item, req executor.Request) (Attempt, error) {
	att := Attempt{Decision: "accept"}
	for i, h := range d.heads {
		model := d.models[i]
		var err error
		if code := d.faults.code(it.ID); i == 0 && code != 0 {
			err = &executor.StatusError{HeadID: h.ID, Code: code}
			att.Calls = append(att.Calls, Call{Head: h.ID, PriceRef: "injected-fault"})
		} else {
			r := req
			r.Head = h
			var resp *executor.Response
			resp, err = d.exec(h).Execute(ctx, r)
			if ctx.Err() != nil {
				return Attempt{}, ctx.Err() // killed mid-call: no row, so a resume asks again
			}
			if err == nil {
				cost, cerr := d.prices.Cost(model, resp.InputTokens, resp.OutputTokens)
				if cerr != nil {
					return Attempt{}, cerr
				}
				att.Calls = append(att.Calls, Call{Head: h.ID, Model: resp.Model, InputTokens: resp.InputTokens,
					OutputTokens: resp.OutputTokens, TokensEstimated: resp.TokensEstimated, CostUSD: cost, PriceRef: d.prices[model].Ref})
				att.Answer, att.TTFT, att.ErrKind = resp.Output, resp.TTFT, ""
				return att, nil
			}
			var se *executor.StatusError
			if errors.As(err, &se) && (se.Code == http.StatusUnauthorized || se.Code == http.StatusForbidden) {
				return Attempt{}, fmt.Errorf("bench: arm %s: %s refused the credentials: %w", d.name, h.ID, err)
			}
			// The provider may have billed a call that errored, so it settles at its ceiling.
			wc, _ := d.prices.worstCase(model, r)
			att.Calls = append(att.Calls, Call{Head: h.ID, CostUSD: wc, PriceRef: "reserved"})
		}
		att.ErrKind = errKind(err)
		if !fallsBack(err) {
			break
		}
	}
	return att, nil
}

func errKind(err error) string {
	var se *executor.StatusError
	if errors.As(err, &se) {
		return "status:" + strconv.Itoa(se.Code)
	}
	return "transport"
}

func fallsBack(err error) bool {
	var se *executor.StatusError
	if errors.As(err, &se) {
		return se.Code == http.StatusTooManyRequests || se.Code >= 500
	}
	return true
}
