// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/ground"
	"github.com/ankit373/hydra/internal/oracle"
	"github.com/ankit373/hydra/internal/policy"
	// The same discovery hyctl registers; env and port find nothing in the sandbox.
	_ "github.com/ankit373/hydra/internal/provider/agy"
	_ "github.com/ankit373/hydra/internal/provider/cli"
	_ "github.com/ankit373/hydra/internal/provider/env"
	_ "github.com/ankit373/hydra/internal/provider/port"
	"github.com/ankit373/hydra/internal/rank"
	"github.com/ankit373/hydra/internal/signals"
	"github.com/ankit373/hydra/internal/swarm"
	"github.com/ankit373/hydra/internal/testutil"
	"github.com/ankit373/hydra/internal/trust"
	"github.com/ankit373/hydra/internal/util"
	"github.com/ankit373/hydra/internal/workspace"
)

//go:embed suite.json
var suiteJSON []byte

// Suite is the pinned machine and prompt set: which CLI heads exist, what
// calibration they carry, and the prompts routed against them.
type Suite struct {
	Binaries    []string     `json:"binaries"`
	LocalModels []string     `json:"local_models"`
	Config      string       `json:"config"`
	Calibration []Calibrated `json:"calibration"`
	Prompts     []Prompt     `json:"prompts"`
}

// Calibrated is one head's confusion counts in one domain, replayed into the store.
type Calibrated struct {
	Head   string `json:"head"`
	Domain string `json:"domain"`
	TP     int    `json:"tp"`
	FP     int    `json:"fp"`
	TN     int    `json:"tn"`
	FN     int    `json:"fn"`
}

// Prompt is one routing case. Confidence > 0 also runs the SPRT ensemble, whose
// heads answer Answer unless Answers names theirs; Expected is the verifier's key.
// Work is the kind of work it is, for the report; Kind is hyctl's --kind, if passed.
type Prompt struct {
	ID         string            `json:"id"`
	Work       string            `json:"work"`
	Prompt     string            `json:"prompt"`
	Context    string            `json:"context,omitempty"`
	Enum       string            `json:"enum,omitempty"`
	Domain     string            `json:"domain,omitempty"`
	Kind       string            `json:"kind,omitempty"`
	Confidence float64           `json:"confidence,omitempty"`
	Answer     string            `json:"answer,omitempty"`
	Answers    map[string]string `json:"answers,omitempty"`
	Expected   string            `json:"expected,omitempty"`
	Check      *CheckSpec        `json:"check,omitempty"`
}

// CheckSpec is one --check, built with the oracles hyctl builds: ground,
// match on Labels (named ID, as a labels file is) or Pattern, or cmd.
type CheckSpec struct {
	Type    string   `json:"type"`
	ID      string   `json:"id,omitempty"`
	Labels  []string `json:"labels,omitempty"`
	Pattern string   `json:"pattern,omitempty"`
	Command string   `json:"command,omitempty"`
}

// checker is the one command a cmd check can run: it passes when its two arguments match.
const checker = "bench-check"

// text is the prompt as the head sees it, context fenced the way hyctl parallel fences it.
func (p Prompt) text() string {
	if p.Context == "" {
		return p.Prompt
	}
	return util.WrapUntrusted("CONTEXT", p.Context) + "\n\nTASK:\n" + p.Prompt
}

// domain is the routing cell, refusing what hyctl dispatch refuses before it routes.
func (p Prompt) domain() (string, error) {
	if !slices.Contains(trust.Kinds, p.Work) {
		return "", fmt.Errorf("%s: work %q is not a kind of work", p.ID, p.Work)
	}
	if p.Check != nil && p.Confidence > 0 {
		return "", fmt.Errorf("%s: --check does not combine with a confidence run", p.ID)
	}
	if p.Kind == "" {
		return p.Domain, nil
	}
	if p.Domain != "" {
		return "", fmt.Errorf("%s: --kind and --domain both name the routing cell", p.ID)
	}
	k, err := trust.ParseKind(p.Kind)
	if err != nil {
		return "", fmt.Errorf("%s: %w", p.ID, err)
	}
	if work, _, _ := strings.Cut(k, "."); work != p.Work {
		return "", fmt.Errorf("%s: --kind %s is not work %s", p.ID, p.Kind, p.Work)
	}
	return k, nil
}

// deadProxy blocks real egress (the pricing fetch); loopback is never proxied.
var deadProxy = map[string]string{"HTTP_PROXY": "127.0.0.1:1", "HTTPS_PROXY": "127.0.0.1:1"}

// runSuite routes every prompt in a hermetic home and returns the decisions.
// It clears the whole environment first: no key, host or override can leak in.
func runSuite(ctx context.Context, raw []byte) (Report, error) {
	var s Suite
	if err := json.Unmarshal(raw, &s); err != nil {
		return Report{}, fmt.Errorf("suite: %w", err)
	}
	root, err := os.MkdirTemp("", "benchdiff-*")
	if err != nil {
		return Report{}, err
	}
	defer os.RemoveAll(root)
	defer restoreEnv(os.Environ())
	if err := sandbox(root, s); err != nil {
		return Report{}, err
	}
	stop, err := fakeOllama(s.LocalModels)
	if err != nil {
		return Report{}, err
	}
	defer stop()

	d, err := dispatch.New(ctx)
	if err != nil {
		return Report{}, err
	}
	defer d.Close()
	cal, err := trust.New(trust.DefaultPath())
	if err != nil {
		return Report{}, err
	}
	sw := swarm.New(d, d.Heads(), d)

	sum := sha256.Sum256(raw)
	rep := Report{Schema: schema, Suite: hex.EncodeToString(sum[:6])}
	for _, p := range s.Prompts {
		domain, err := p.domain()
		if err != nil {
			return Report{}, err
		}
		var chk *answerCheck
		if p.Check != nil {
			if chk, err = build(*p.Check, p.text()); err != nil {
				return Report{}, fmt.Errorf("%s: %w", p.ID, err)
			}
		}
		rep.Results = append(rep.Results, route(ctx, d, sw, cal, p, domain, chk))
	}
	return rep, nil
}

func sandbox(root string, s Suite) error {
	home, hydra, bin, tmp := filepath.Join(root, "home"), filepath.Join(root, "hydra"),
		filepath.Join(root, "bin"), filepath.Join(root, "tmp")
	for _, dir := range []string{home, hydra, bin, tmp} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	// Windows cannot open a socket without SYSTEMROOT, and it names no credential.
	env := map[string]string{"HOME": home, "USERPROFILE": home, "HYDRA_HOME": hydra, "PATH": bin,
		"TMPDIR": tmp, "TMP": tmp, "TEMP": tmp, "SYSTEMROOT": os.Getenv("SYSTEMROOT")}
	os.Clearenv()
	// Every local-server probe hits a closed port, so a developer's server is not found.
	for _, dead := range []map[string]string{testutil.DeadAddressVars, deadProxy} {
		for k, v := range dead {
			env[k] = v
		}
	}
	for k, v := range env {
		if err := os.Setenv(k, v); err != nil {
			return err
		}
	}
	// Discovery only looks the binaries up; a dry run never executes them.
	script, check, ext := "#!/bin/sh\nexit 1\n", "#!/bin/sh\n[ \"$1\" = \"$2\" ]\n", ""
	if runtime.GOOS == "windows" {
		script, ext = "@echo off\r\nexit /b 1\r\n", ".bat"
		check = "@echo off\r\nif \"%~1\"==\"%~2\" exit /b 0\r\nexit /b 1\r\n"
	}
	for _, name := range s.Binaries {
		if err := os.WriteFile(filepath.Join(bin, name+ext), []byte(script), 0o700); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(bin, checker+ext), []byte(check), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(config.Path(), []byte(s.Config), 0o600); err != nil {
		return err
	}
	return seed(s.Calibration)
}

// restoreEnv puts the caller's environment back, so a second run starts clean.
func restoreEnv(saved []string) {
	os.Clearenv()
	for _, kv := range saved {
		k, v := splitEnv(kv)
		_ = os.Setenv(k, v)
	}
}

// splitEnv cuts at the first '=' after index 0, as syscall.Clearenv does:
// Windows keeps "=C:=C:\dir" entries whose name starts with '='.
func splitEnv(kv string) (string, string) {
	i := strings.Index(kv[min(1, len(kv)):], "=") + 1
	if i <= 0 {
		return kv, ""
	}
	return kv[:i], kv[i+1:]
}

// fakeOllama answers discovery on loopback, so the suite has local heads at
// the free tier 10. Nothing is ever executed against it.
func fakeOllama(models []string) (func(), error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	type model struct {
		Name         string   `json:"name"`
		Capabilities []string `json:"capabilities"`
	}
	tags := struct {
		Models []model `json:"models"`
	}{}
	for _, m := range models {
		tags.Models = append(tags.Models, model{Name: m, Capabilities: []string{"completion"}})
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(tags)
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	go func() { _ = srv.Serve(ln) }()
	if err := os.Setenv("OLLAMA_HOST", "http://"+ln.Addr().String()); err != nil {
		_ = srv.Close()
		return nil, err
	}
	return func() { _ = srv.Close() }, nil
}

// seed replays the pinned counts through the store's own writer.
func seed(rows []Calibrated) error {
	cal, err := trust.New(trust.DefaultPath())
	if err != nil {
		return err
	}
	for _, r := range rows {
		for _, c := range []struct {
			n        int
			said     bool
			received trust.Outcome
		}{
			{r.TP, true, trust.OutcomeCorrect}, {r.FP, true, trust.OutcomeIncorrect},
			{r.TN, false, trust.OutcomeIncorrect}, {r.FN, false, trust.OutcomeCorrect},
		} {
			for range c.n {
				if err := cal.Update(r.Head, r.Domain, c.said, c.received); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// route makes the decisions `hyctl dispatch --dry-run [--confidence]` makes,
// then runs the SPRT over the planned heads with answers from the suite.
func route(ctx context.Context, d *dispatch.Dispatcher, sw *swarm.Swarm, cal *trust.Calibrator, p Prompt, domain string, chk *answerCheck) Result {
	prompt := p.text()
	dec := d.Decide(ctx, prompt, domain, nil, nil, "")
	opts := dispatch.Options{DryRun: true, Enum: p.Enum, Domain: domain, Decision: &dec}
	if p.Enum != "" {
		opts.TierHint = dispatch.EnumToTier(p.Enum)
	}
	r := Result{ID: p.ID, Work: p.Work, Rule: dec.Rule}
	res, err := d.Dispatch(ctx, prompt, opts)
	if err != nil {
		r.Refused = refusal(err)
		return r
	}
	r.Head, r.Tier, r.Fallbacks = res.Head.ID, rank.UITier(res.Head), len(res.Fallbacks)
	in := executor.EstimateTokens(prompt)
	r.CostUSD = round(d.EstimateCost(r.Tier, in, in/2), 6)

	// As cmdDispatch does: personal data raises the bar to the defect model's
	// and keeps the ensemble local (#500), as does a require_confidence rule.
	target, localOnly := p.Confidence, dec.Action.LocalOnly
	if dec.Action.Type == signals.ActionRequireConfidence {
		target = max(target, dec.Action.Value)
	}
	if policy.Classify(prompt).PII {
		pii := trust.Task{Domain: domain, BlastRadius: 1, TouchesPII: true}
		target = max(target, trust.NewDefectModel().RequiredConfidence(pii))
		localOnly = localOnly || d.PIILocalOnly()
	}
	if target > 0 {
		r.SPRT = ensemble(ctx, sw, cal, p, prompt, opts, localOnly, target)
		if r.SPRT.Samples > 0 {
			r.CostUSD = r.SPRT.SpentUSD
		}
	}
	if chk != nil {
		r.Check = chk.judge(ctx, cal, p.Answer, domain)
	}
	return r
}

// answerCheck is one built --check and the calibration source it records under.
type answerCheck struct {
	oracle oracle.Oracle
	source string
}

// judge checks the scripted answer as runChecks does, without training the
// head on it, so no prompt's verdict moves another prompt's routing.
func (c answerCheck) judge(ctx context.Context, cal *trust.Calibrator, answer, domain string) *Check {
	r := &Check{Source: c.source}
	v, err := c.oracle.Verify(ctx, answer, trust.Task{Domain: domain})
	if err != nil {
		r.NotChecked = true
		return r
	}
	r.Passed = v.Passed
	if measured, _ := oracle.Measured(cal, c.source, domain); measured {
		r.LLR = round(oracle.LLR(cal, c.source, domain, v), 3)
	}
	return r
}

// build makes the oracle hyctl's parseCheck makes for the same --check.
func build(c CheckSpec, prompt string) (*answerCheck, error) {
	switch c.Type {
	case "ground":
		g := &ground.Oracle{Prompt: prompt}
		return &answerCheck{g, g.Key()}, nil
	case "match":
		id := c.ID
		if c.Pattern != "" {
			id = c.Pattern
		}
		m, err := oracle.NewMatch(id, c.Labels, c.Pattern)
		if err != nil {
			return nil, err
		}
		return &answerCheck{m, m.Source}, nil
	case "cmd":
		fields := strings.Fields(c.Command)
		if len(fields) == 0 || fields[0] != checker || !strings.Contains(c.Command, "{answer}") {
			return nil, fmt.Errorf("a cmd check runs %s on {answer}", checker)
		}
		o := &oracle.CommandOracle{Template: c.Command, Source: "verifier:" + fields[0],
			Env: workspace.ValidatorEnvNames(config.ScriptHome())}
		return &answerCheck{o, o.Source}, nil
	}
	return nil, fmt.Errorf("unknown check %q", c.Type)
}

const noEvidence = "no_evidence"

// ensemble runs swarm.RunSPRT's plan and sources, with a scripted executor and
// text equivalence in place of live heads and the LLM judge.
func ensemble(ctx context.Context, sw *swarm.Swarm, cal *trust.Calibrator,
	p Prompt, prompt string, opts dispatch.Options, localOnly bool, target float64) *SPRT {
	heads, _, err := sw.Plan(prompt, swarm.Options{
		Mode: swarm.ModeBest, TierHint: opts.TierHint, Enum: p.Enum,
		LocalOnly: localOnly, Confidence: target, Domain: opts.Domain,
	})
	if err != nil {
		return &SPRT{Decision: "unplanned"}
	}
	res, err := trust.Run(ctx, trust.Task{Domain: trust.Domain(opts.Domain)}, sw.Sources(prompt, heads),
		scripted{p}, cal, trust.Target{Confidence: target})
	if errors.Is(err, trust.ErrNoEvidence) {
		return &SPRT{Decision: noEvidence}
	}
	if err != nil {
		return &SPRT{Decision: "error"}
	}
	// A candidate stopped on budget goes to a human, so it was never verified.
	accepted := res.Decision == trust.DecisionAccept
	return &SPRT{
		Decision:   res.Decision.String(),
		Samples:    res.Samples,
		Confidence: round(res.Confidence, 4),
		SpentUSD:   round(res.SpentUSD, 6),
		Correct:    accepted && p.Expected != "" && trust.TextEquivalence(res.Candidate, p.Expected),
	}
}

// scripted answers for every head from the suite, so the ensemble is deterministic.
type scripted struct{ p Prompt }

func (s scripted) Execute(_ context.Context, src trust.Source, _ trust.Task) (trust.Answer, error) {
	if a, ok := s.p.Answers[src.ID]; ok {
		return trust.Answer{Text: a}, nil
	}
	return trust.Answer{Text: s.p.Answer}, nil
}

// refusal names why no head was chosen without the message, which can carry paths.
func refusal(err error) string {
	var blocked *dispatch.ErrBlocked
	switch {
	case errors.As(err, &blocked):
		return "blocked by " + blocked.Rule
	case errors.Is(err, dispatch.ErrNoHeads):
		return "no heads"
	default:
		return "error"
	}
}

func round(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}
