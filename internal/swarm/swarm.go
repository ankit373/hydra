// SPDX-License-Identifier: MIT

package swarm

import (
	"context"
	"fmt"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/provider"
)

// SwarmMode is the response selection strategy.
type SwarmMode string

const (
	ModeRace SwarmMode = "race" // first success wins; all others are canceled
	ModeBest SwarmMode = "best" // fire all; LLM judge (with CapScore fallback) picks winner
	ModeAll  SwarmMode = "all"  // fire all; return ranked by CapScore, no judge
	// ModeSPRT labels cost rows from RunSPRT's adaptive optimal-stopping ensemble.
	// It is a cost-log label, not a value Options.Mode ever takes — RunSPRT is
	// entered via Options.Confidence, not via Mode.
	ModeSPRT SwarmMode = "sprt"
)

// Options configures a swarm run.
type Options struct {
	Mode SwarmMode

	// Head selection — at most one of TierHint / HeadIDs should be set.
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

	// Judge (ModeBest only).
	JudgeTierHint string        // "" → use config.Cortex head
	JudgeTimeout  time.Duration // 0 → 30 s

	// SPRT mode (RunSPRT).
	Confidence float64 // target P(correct); >0 selects the SPRT ensemble
	Domain     string  // calibration domain ("" → "default")

	// RunID/TaskID group this swarm's attempt rows with the invocation and the
	// logical task they belong to. Every head racing or voting on one prompt is
	// working the same task, so they share a TaskID — that is what lets a reader
	// tell "5 heads on one task" from "5 separate tasks" (#181).
	RunID  string
	TaskID string
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
// pricing may be nil — cost estimation and pre-flight guard will be skipped.
func New(d *dispatch.Dispatcher, heads []provider.Head, pricing PricingReader) *Swarm {
	return &Swarm{d: d, heads: heads, pricing: pricing}
}

// Run executes the swarm: selects heads, optionally checks cost, fires them,
// collects results, optionally judges, logs costs.
func (s *Swarm) Run(ctx context.Context, prompt string, opts Options) (*SwarmResult, error) {
	if opts.Mode == "" {
		opts.Mode = ModeBest
	}

	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("swarm: config load: %w", err)
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
	default:
		return nil, fmt.Errorf("swarm: unknown mode %q", opts.Mode)
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
			// Judge completely failed — fall back to CapScore winner.
			result.Winner = capScoreWinner(attempts)
		}
	case ModeAll:
		rankByCapScore(attempts)
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

func buildJudge(d *dispatch.Dispatcher, opts Options, cfg *config.Config) Judge {
	tierHint := opts.JudgeTierHint
	if tierHint == "" {
		// Default to the configured Cortex head's tier (tier 1).
		tierHint = "1"
	}
	llm := newLLMJudge(d, tierHint, opts.JudgeTimeout)
	cap_ := &CapScoreJudge{}
	return newCompositeJudge(llm, cap_)
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
