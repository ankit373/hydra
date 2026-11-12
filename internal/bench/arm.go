// SPDX-License-Identifier: MIT

package bench

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/provider"
)

// Arm answers one item. Call reserves each provider call's worst case on b before
// making it, and hands every call that may have been billed to spend the moment it
// returns, before any retry, sleep or grade, so a hard kill loses no spend. An error
// from Call stops the run; an item-level failure is an Attempt with ErrKind set.
// WorstCaseUSD bounds one item, for --dry-run.
type Arm interface {
	Name() string
	Config() ArmConfig
	Preflight(ctx context.Context) error
	WorstCaseUSD(req executor.Request) (float64, error)
	Call(ctx context.Context, it Item, req executor.Request, b *Budget, spend func(Call) error) (Attempt, error)
}

// retryBackoff is the wait before each retry of a transient failure on one model;
// a Retry-After the server stated replaces it, up to maxRetryAfter.
var retryBackoff = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}

const maxRetryAfter = time.Minute

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
	ErrKind  string        `json:"err_kind,omitempty"` // "transport" | "status:<code>" | "contract" | "interrupted" | "grade" | ""
	// transient: the last error was transport, 429 or 5xx, so the row is unscored.
	transient bool
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
	sleep  func(context.Context, time.Duration) error
}

func NewDirect(name string, cfg ArmConfig, prices PriceSnapshot, exec func(provider.Head) executor.Executor,
	faults *FaultPlan, sleep func(context.Context, time.Duration) error) (*Direct, error) {
	if len(cfg.Models) == 0 {
		return nil, fmt.Errorf("bench: arm %s has no model", name)
	}
	d := &Direct{name: name, cfg: cfg, exec: exec, prices: prices, faults: faults, sleep: sleep}
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

// WorstCaseUSD is every model in the list, each retried as often as it can be.
func (d *Direct) WorstCaseUSD(req executor.Request) (float64, error) {
	var sum float64
	for _, m := range d.models {
		c, err := d.prices.worstCase(m, req)
		if err != nil {
			return 0, err
		}
		sum += c * float64(1+len(retryBackoff))
	}
	return sum, nil
}

// Call retries a transient failure (network, 429, 5xx) on the same model with
// bounded backoff, then moves to the next model. A 4xx, a local error or a wrong
// answer never moves; a 401 or 403 stops the run, since no item can succeed.
func (d *Direct) Call(ctx context.Context, it Item, req executor.Request, b *Budget, spend func(Call) error) (Attempt, error) {
	att := Attempt{Decision: "accept"}
	var err error
	for i, h := range d.heads {
		model := d.models[i]
		r := req
		r.Head = h
		wc, werr := d.prices.worstCase(model, r)
		if werr != nil {
			return att, werr
		}
		for try := 0; ; try++ {
			if code := d.faults.code(it.ID); i == 0 && code != 0 {
				// An injected fault never reaches the provider, costs nothing, and is
				// not retried: it stands for a model that is down.
				err = &executor.StatusError{HeadID: h.ID, Code: code}
				att.Calls = append(att.Calls, Call{Head: h.ID, PriceRef: "injected-fault"})
				break
			}
			if rerr := b.Reserve(wc); rerr != nil {
				return att, rerr
			}
			var resp *executor.Response
			resp, err = d.exec(h).Execute(ctx, r)
			c, perr := d.price(h, model, wc, resp, err, ctx.Err() != nil)
			b.Settle(wc, c.CostUSD)
			att.Calls = append(att.Calls, c)
			if c.CostUSD > 0 {
				if serr := spend(c); serr != nil {
					return att, serr
				}
			}
			switch {
			case perr != nil:
				return att, perr
			case err == nil: // answered, so kept even if the kill came just after
				att.Answer, att.TTFT, att.ErrKind = resp.Output, resp.TTFT, ""
				return att, nil
			case ctx.Err() != nil:
				return att, ctx.Err()
			}
			var se *executor.StatusError
			if errors.As(err, &se) && (se.Code == http.StatusUnauthorized || se.Code == http.StatusForbidden) {
				return att, fmt.Errorf("bench: arm %s: %s refused the credentials: %w", d.name, h.ID, err)
			}
			if !transient(err) || try == len(retryBackoff) {
				break
			}
			if serr := d.sleep(ctx, backoff(try, err)); serr != nil {
				return att, serr
			}
		}
		if !transient(err) {
			break
		}
	}
	att.ErrKind, att.transient = errKind(err), transient(err)
	return att, nil
}

// price records one provider call. A call that may have been billed costs its ceiling
// until the provider's own token counts price it; one turned away (429, 503, 401, 403)
// costs nothing. A local error costs its ceiling too: the executor's error does not say
// whether it came before the send or from a billed 200 (decode, empty answer).
// perr: answered, but unpriceable.
func (d *Direct) price(h provider.Head, model string, wc float64, resp *executor.Response, err error, killed bool) (Call, error) {
	var se *executor.StatusError
	switch {
	case err == nil: // a call that completed has token counts, even if a kill came after
		c := Call{Head: h.ID, Model: resp.Model, InputTokens: resp.InputTokens, OutputTokens: resp.OutputTokens,
			TokensEstimated: resp.TokensEstimated, PriceRef: d.prices[model].Ref}
		cost, perr := d.prices.Cost(model, resp.InputTokens, resp.OutputTokens)
		if perr != nil {
			c.CostUSD, c.PriceRef = wc, "unpriced"
			return c, perr
		}
		c.CostUSD = cost
		return c, nil
	case killed:
		return Call{Head: h.ID, CostUSD: wc, PriceRef: "reserved"}, nil
	case errors.As(err, &se) && slices.Contains([]int{http.StatusTooManyRequests, http.StatusServiceUnavailable,
		http.StatusUnauthorized, http.StatusForbidden}, se.Code):
		return Call{Head: h.ID, PriceRef: "refused-unbilled"}, nil
	case se == nil && !network(err):
		return Call{Head: h.ID, CostUSD: wc, PriceRef: "local-error"}, nil
	}
	return Call{Head: h.ID, CostUSD: wc, PriceRef: "reserved"}, nil
}

// backoff is the fixed schedule, or the server's Retry-After when it named a longer
// wait, never more than maxRetryAfter.
func backoff(try int, err error) time.Duration {
	d := retryBackoff[try]
	var se *executor.StatusError
	if errors.As(err, &se) && se.RetryAfter > d {
		d = min(se.RetryAfter, maxRetryAfter)
	}
	return d
}

func errKind(err error) string {
	var se *executor.StatusError
	switch {
	case errors.As(err, &se):
		return "status:" + strconv.Itoa(se.Code)
	case network(err):
		return "transport"
	}
	return "local"
}

// network is a failure on the wire: a net.Error (timeouts, refused or reset
// connections, DNS) or a connection cut mid-body.
func network(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, context.DeadlineExceeded)
}

// transient is a failure that says nothing about the item: a network error, a 429 or
// a 5xx. Its row is unscored and the pair is asked again on resume. A local error
// (a bad request, a decode error, an empty answer) is not: it fails the row, no retry.
func transient(err error) bool {
	var se *executor.StatusError
	if errors.As(err, &se) {
		return se.Code == http.StatusTooManyRequests || se.Code >= 500
	}
	return network(err)
}
