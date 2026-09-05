// SPDX-License-Identifier: MIT

package swarm

import (
	"context"
	"fmt"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/ankit373/hydra/internal/a2a"
	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/policy"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/trust"
)

// SwarmMode is the response selection strategy.
type SwarmMode string

const (
	ModeRace SwarmMode = "race" // first success wins; all others are canceled
	ModeBest SwarmMode = "best" // fire all; LLM judge (with CapScore fallback) picks winner
	ModeAll  SwarmMode = "all"  // fire all; return ranked by CapScore, no judge
	// ModeSPRT labels cost rows from RunSPRT's adaptive optimal-stopping ensemble.
	// It is a cost-log label, not a value Options.Mode ever takes, RunSPRT is
	// entered via Options.Confidence, not via Mode.
	ModeSPRT SwarmMode = "sprt"
)

// Options configures a swarm run.
type Options struct {
	Mode SwarmMode

	// Head selection, at most one of TierHint / HeadIDs should be set.
	// When both are empty, CapScoreSelector picks the top-N available heads.
	TierHint    string
	HeadIDs     []string // explicit head IDs; bypasses tier
	MinCapScore int      // exclude heads below this score (0 = no filter)

	// Execution constraints.
	MaxHeads       int           // hard cap on fan-out (0 → defaultMaxHeads = 5)
	PerHeadTimeout time.Duration // per-head deadline (0 → defaultPerHeadTimeout, 300s; never unbounded)
	MaxEstCostUSD  float64       // pre-flight guard; 0 = no limit
	LocalOnly      bool

	// Prompt parameters.
	System    string
	MaxTokens int

	// A2AFile is a path to an A2A handoff JSON file (--a2a): its structured
	// context is prepended to the prompt before any head fires. Mirrors the
	// fail-loudly contract dispatch.Options.A2AFile gives plain dispatch, a
	// missing or malformed file fails Plan/Run/RunSPRT instead of silently
	// dropping the handoff, which is what happened before this field existed
	// at all (#530).
	A2AFile string

	// Judge (ModeBest only).
	JudgeTierHint string        // "" → use config.Cortex head
	JudgeTimeout  time.Duration // 0 → 30 s

	// SPRT mode (RunSPRT).
	Confidence float64 // target P(correct); >0 selects the SPRT ensemble
	Domain     string  // calibration domain ("" → "default")

	// RunID/TaskID group this swarm's attempt rows with the invocation and the
	// logical task they belong to. Every head racing or voting on one prompt is
	// working the same task, so they share a TaskID, that is what lets a reader
	// tell "5 heads on one task" from "5 separate tasks" (#181).
	RunID  string
	TaskID string

	// Classification is prompt's already-computed PII/injection verdict
	// (policy.Classify), cmdDispatch computes this once and passes it here so
	// Run/RunSPRT resolve it once, before firing any head, instead of every
	// concurrent executeHead call re-scanning the same prompt. Nil means "not
	// computed yet"; Run/RunSPRT derive and fill it in before dispatching.
	Classification *policy.Classification
}

// SwarmResult is the complete outcome of a swarm dispatch.
type SwarmResult struct {
	Mode         SwarmMode
	Prompt       string
	Attempts     []Attempt     // all heads, in order fired; some may be Canceled
	Winner       *Attempt      // nil only when every head failed
	Verdict      *JudgeVerdict // non-nil only in ModeBest
	TotalCostUSD float64       // sum of all EstCostUSD
	WallDuration time.Duration
	StartedAt    time.Time
}

// SucceededCount returns the number of attempts that produced output.
func (r *SwarmResult) SucceededCount() int {
	n := 0
	for _, a := range r.Attempts {
		if a.Status == StatusOK {
			n++
		}
	}
	return n
}

// Swarm orchestrates fan-out dispatch. It is stateless after construction;
// each Run call is independent.
type Swarm struct {
	d       *dispatch.Dispatcher
	heads   []provider.Head
	pricing PricingReader
}

// New constructs a Swarm.
// heads is the already-probed set of live heads (from probe.Run or Dispatcher.Heads).
// pricing may be nil, cost estimation and pre-flight guard will be skipped.
func New(d *dispatch.Dispatcher, heads []provider.Head, pricing PricingReader) *Swarm {
	return &Swarm{d: d, heads: heads, pricing: pricing}
}

// validateMode rejects any SwarmMode Run does not know how to execute. Plan
// calls this too, so a `--dry-run` reports a plan only for a mode Run would
// actually accept, instead of previewing a run that Run then refuses (#453).
func validateMode(m SwarmMode) error {
	switch m {
	case ModeRace, ModeBest, ModeAll:
		return nil
	default:
		return fmt.Errorf("swarm: unknown mode %q", m)
	}
}

// Plan reports what Run or RunSPRT would do without executing anything: which
// heads would be engaged, and what one round of fan-out is estimated to cost.
//
// It exists so --dry-run can mean the same thing in every mode. It used to mean
// nothing in swarm and SPRT modes, the flag was read only by the single-dispatch
// path, which both ensemble branches returned before reaching, so a dry run
// fired a paid ensemble (#167).
//
// Selection deliberately mirrors Run and RunSPRT, which resolve the same
// selector against the same options; a plan that picked different heads from the
// run it describes would be worse than no plan.
func (s *Swarm) Plan(prompt string, opts Options) (heads []provider.Head, estUSD float64, err error) {
	if opts.Mode == "" {
		opts.Mode = ModeBest
	}
	if err := validateMode(opts.Mode); err != nil {
		return nil, 0, err
	}

	cfg, err := config.Load()
	if err != nil {
		return nil, 0, fmt.Errorf("swarm: config load: %w", err)
	}
	if err := validateSwarmTiers(cfg, opts); err != nil {
		return nil, 0, err
	}
	prompt, err = injectA2A(prompt, opts)
	if err != nil {
		return nil, 0, err
	}
	selected, err := resolveSelector(opts, cfg).Select(s.heads, opts)
	if err != nil {
		return nil, 0, err
	}
	if len(selected) == 0 {
		return nil, 0, fmt.Errorf("swarm: no heads available for the requested configuration")
	}
	return selected, estimateFanoutCost(selected, prompt, s.pricing), nil
}

// Run executes the swarm: selects heads, optionally checks cost, fires them,
// collects results, optionally judges, logs costs.
func (s *Swarm) Run(ctx context.Context, prompt string, opts Options) (*SwarmResult, error) {
	if opts.Mode == "" {
		opts.Mode = ModeBest
	}
	if err := validateMode(opts.Mode); err != nil {
		return nil, err
	}

	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("swarm: config load: %w", err)
	}
	// An invalid --tier/--swarm-judge-tier must fail here, before any heads
	// are fired or judged, never silently widen to CapScoreSelector's top-N
	// fan-out (#501).
	if err := validateSwarmTiers(cfg, opts); err != nil {
		return nil, err
	}
	prompt, err = injectA2A(prompt, opts)
	if err != nil {
		return nil, err
	}

	// 1. Head selection.
	selector := resolveSelector(opts, cfg)
	selected, err := selector.Select(s.heads, opts)
	if err != nil {
		return nil, err
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("swarm: no heads available for the requested configuration")
	}

	// Classify once, before any head fires, every concurrent executeHead call
	// below reuses this instead of each re-scanning the same prompt (#522).
	if opts.Classification == nil {
		c := policy.Classify(prompt)
		opts.Classification = &c
	}

	// 2. Pre-flight cost guard.
	_, err = preflightCost(selected, prompt, s.pricing, opts.MaxEstCostUSD)
	if err != nil {
		return nil, err
	}

	// 3. Execute.
	startedAt := time.Now()
	var attempts []Attempt

	switch opts.Mode {
	case ModeRace:
		attempts = runRace(ctx, selected, prompt, opts)
	case ModeBest, ModeAll:
		attempts = runAll(ctx, selected, prompt, opts)
	}

	wallDuration := time.Since(startedAt)

	// 4. Enrich cost on each attempt.
	enrichCosts(attempts, s.pricing)

	// 5. Determine winner + verdict.
	result := &SwarmResult{
		Mode:         opts.Mode,
		Prompt:       prompt,
		Attempts:     attempts,
		WallDuration: wallDuration,
		StartedAt:    startedAt,
	}

	switch opts.Mode {
	case ModeRace:
		result.Winner = raceWinner(attempts)
	case ModeBest:
		judge := buildJudge(s.d, opts, cfg)
		verdict, judgeErr := judge.Judge(ctx, prompt, attempts)
		if judgeErr == nil && verdict != nil {
			result.Verdict = verdict
			winner := &attempts[verdict.WinnerIndex]
			winner.Rank = 1
			result.Winner = winner
		} else {
			// Judge completely failed, fall back to CapScore winner.
			result.Winner = capScoreWinner(attempts)
		}
	case ModeAll:
		if cal, domain := loadCalibrationFor(opts); cal != nil {
			rankByCalibratedScore(attempts, cal, domain)
		} else {
			rankByCapScore(attempts)
		}
		if w := firstSuccessful(attempts); w != nil {
			result.Winner = w
		}
	}

	// 6. Sum total cost.
	for _, a := range attempts {
		result.TotalCostUSD += a.EstCostUSD
	}

	// 7. Log to cost.jsonl.
	logAttempts(result.Attempts, result.Mode, opts, truncate(prompt, 80))
	logRunEvents(result.Attempts, result.Mode, opts)

	return result, nil
}

// ── private helpers ───────────────────────────────────────────────────────────

// validateSwarmTiers rejects an invalid TierHint or JudgeTierHint before any
// selection or judging happens, using the identical rule dispatch.Dispatch
// applies. Run, RunSPRT and Plan all call this so --tier/--swarm-judge-tier
// fail the same way regardless of mode (#501).
func validateSwarmTiers(cfg *config.Config, opts Options) error {
	if err := dispatch.ValidateTierHint(cfg, opts.TierHint); err != nil {
		return fmt.Errorf("swarm: %w", err)
	}
	if err := dispatch.ValidateTierHint(cfg, opts.JudgeTierHint); err != nil {
		return fmt.Errorf("swarm: judge tier: %w", err)
	}
	return nil
}

// injectA2A prepends opts.A2AFile's handoff context to prompt when set, using
// the identical fail-loudly contract dispatch.Dispatch applies to --a2a. Plan,
// Run and RunSPRT all call this so a bad handoff file is rejected the same way
// regardless of mode, before this, swarm.Options had no A2AFile field at all,
// so --a2a was silently dropped the moment --swarm or --confidence was
// combined with it (#530).
func injectA2A(prompt string, opts Options) (string, error) {
	if opts.A2AFile == "" {
		return prompt, nil
	}
	injected, err := a2a.Inject(opts.A2AFile, prompt)
	if err != nil {
		return prompt, fmt.Errorf("swarm: --a2a %s: %w", opts.A2AFile, err)
	}
	return injected, nil
}

func buildJudge(d *dispatch.Dispatcher, opts Options, cfg *config.Config) Judge {
	tierHint := opts.JudgeTierHint
	if tierHint == "" {
		// Default to the configured Cortex head's tier (tier 1).
		tierHint = "1"
	}
	llm := newLLMJudge(d, tierHint, opts.JudgeTimeout)
	cap_ := &CapScoreJudge{}
	fallback := newCompositeJudge(llm, cap_)

	cal, domain := loadCalibrationFor(opts)
	if cal == nil {
		return fallback
	}
	return newCompositeJudge(newCalibratedJudge(cal, domain, nil), fallback)
}

// loadCalibrationFor degrades to (nil, domain) on a load error, callers
// treat nil as "no calibration data" and fall back rather than fail.
func loadCalibrationFor(opts Options) (*trust.Calibrator, string) {
	domain := opts.Domain
	if domain == "" {
		domain = "default"
	}
	cal, err := trust.New(trust.DefaultPath())
	if err != nil {
		return nil, domain
	}
	return cal, domain
}

// rankByCalibratedScore ranks by D(source, domain) instead of static CapScore,
// falling back to rankByCapScore's exact order when no attempt has any D.
func rankByCalibratedScore(attempts []Attempt, cal *trust.Calibrator, domain string) {
	type indexed struct {
		idx int
		d   float64
	}
	var ok []indexed
	maxD := 0.0
	for i, a := range attempts {
		if a.Status != StatusOK {
			continue
		}
		d := cal.D(a.Head.ID, domain)
		ok = append(ok, indexed{i, d})
		if d > maxD {
			maxD = d
		}
	}
	if maxD <= 0 {
		rankByCapScore(attempts)
		return
	}
	sort.Slice(ok, func(i, j int) bool {
		if ok[i].d != ok[j].d {
			return ok[i].d > ok[j].d
		}
		return attempts[ok[i].idx].Head.CapScore > attempts[ok[j].idx].Head.CapScore
	})
	for rank, item := range ok {
		attempts[item.idx].Rank = rank + 1
	}
}

func raceWinner(attempts []Attempt) *Attempt {
	for i := range attempts {
		if attempts[i].Rank == 1 {
			return &attempts[i]
		}
	}
	// Fallback: first OK attempt (should not happen if runRace set Rank correctly).
	return firstSuccessful(attempts)
}

func capScoreWinner(attempts []Attempt) *Attempt {
	var best *Attempt
	for i := range attempts {
		if attempts[i].Status != StatusOK {
			continue
		}
		if best == nil || attempts[i].Head.CapScore > best.Head.CapScore {
			best = &attempts[i]
		}
	}
	if best != nil {
		best.Rank = 1
	}
	return best
}

func rankByCapScore(attempts []Attempt) {
	type indexed struct {
		idx   int
		score int
	}
	var ok []indexed
	for i, a := range attempts {
		if a.Status == StatusOK {
			ok = append(ok, indexed{i, a.Head.CapScore})
		}
	}
	sort.Slice(ok, func(i, j int) bool { return ok[i].score > ok[j].score })
	for rank, item := range ok {
		attempts[item.idx].Rank = rank + 1
	}
}

func firstSuccessful(attempts []Attempt) *Attempt {
	for i := range attempts {
		if attempts[i].Status == StatusOK {
			return &attempts[i]
		}
	}
	return nil
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:n]) + "…"
}
