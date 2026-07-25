// SPDX-License-Identifier: MIT

package swarm

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/rank"
	"github.com/ankit373/hydra/internal/trust"
)

// SPRTResult wraps a trust.Run outcome with the executions it drove, so callers
// can render both the decision (candidate, confidence, ledger) and the per-head
// attempts.
type SPRTResult struct {
	Trust    *trust.Result
	Attempts []Attempt
	Domain   string
	Prompt   string
	Target   float64 // requested target confidence
}

// RunSPRT routes a prompt through the SPRT optimal-stopping ensemble: it samples
// the selected heads adaptively, in most-diagnostic-per-dollar order, stopping as
// soon as the calibrated confidence reaches opts.Confidence. It is the production
// caller of trust.Run — the swarm executor is adapted to trust.Executor here.
func (s *Swarm) RunSPRT(ctx context.Context, prompt string, opts Options) (*SPRTResult, error) {
	if opts.Confidence <= 0 || opts.Confidence >= 1 {
		return nil, fmt.Errorf("swarm sprt: confidence must be in (0,1), got %v", opts.Confidence)
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("swarm sprt: config load: %w", err)
	}

	selected, err := resolveSelector(opts, cfg).Select(s.heads, opts)
	if err != nil {
		return nil, err
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("swarm sprt: no heads available")
	}

	domain := opts.Domain
	if domain == "" {
		domain = "default"
	}

	cal, err := trust.New(trust.DefaultPath())
	if err != nil {
		return nil, fmt.Errorf("swarm sprt: calibration load: %w", err)
	}

	// Map heads → trust.Source, estimating per-call cost for ordering + budget.
	estIn := len(prompt) / 4
	sources := make([]trust.Source, 0, len(selected))
	byID := make(map[string]provider.Head, len(selected))
	for _, h := range selected {
		var cost float64
		if s.pricing != nil {
			cost = s.pricing.EstimateCost(rank.UITier(h), estIn, estIn/2)
		}
		sources = append(sources, trust.Source{ID: h.ID, Family: h.Provider, EstCostUSD: cost})
		byID[h.ID] = h
	}

	adapter := &sprtExecutor{swarm: s, prompt: prompt, opts: opts, byID: byID}

	// Behavior-based agreement: two independently-correct answers that differ only
	// in wording must count as agreement, not disagreement — the effect that capped
	// real SPRT confidence at 32.9% (see the findings doc §3). nil when no
	// dispatcher is available, in which case trust.Run keeps its textual default.
	var equiv trust.AnswerEquivalence
	if s.d != nil {
		equiv = s.judgeEquivalence(ctx, prompt, opts)
	}

	res, err := trust.Run(ctx, trust.Task{Domain: domain}, sources, adapter, cal, trust.Target{
		Confidence: opts.Confidence,
		MaxCostUSD: opts.MaxEstCostUSD,
	}, trust.WithEquivalence(equiv))
	if err != nil {
		return nil, err
	}

	// Mark the winning attempt (the one whose output became the candidate).
	for i := range adapter.attempts {
		if adapter.attempts[i].Output == res.Candidate {
			adapter.attempts[i].Rank = 1
			break
		}
	}

	return &SPRTResult{Trust: res, Attempts: adapter.attempts, Domain: domain, Prompt: prompt, Target: opts.Confidence}, nil
}

// sprtExecutor adapts the swarm's per-head execution to the trust.Executor
// interface and records every attempt it makes.
type sprtExecutor struct {
	swarm    *Swarm
	prompt   string
	opts     Options
	byID     map[string]provider.Head
	attempts []Attempt
}

func (e *sprtExecutor) Execute(ctx context.Context, src trust.Source, _ trust.Task) (trust.Answer, error) {
	h, ok := e.byID[src.ID]
	if !ok {
		return trust.Answer{}, fmt.Errorf("sprt: unknown source %q", src.ID)
	}
	a := executeHead(ctx, h, e.prompt, e.opts)

	// Price the attempt so cost logging and the budget guard see real numbers.
	if e.swarm.pricing != nil && a.Status == StatusOK {
		a.EstCostUSD = round6(e.swarm.pricing.EstimateCost(rank.UITier(h), a.InputTokens, a.OutputTokens))
	}
	a.FinishedAt = time.Now()
	e.attempts = append(e.attempts, a)

	if a.Status != StatusOK {
		return trust.Answer{}, fmt.Errorf("sprt: head %s failed: %s", h.ID, a.Status)
	}
	return trust.Answer{Text: a.Output, CostUSD: a.EstCostUSD}, nil
}

// judgeEquivalence returns a behavior-based trust.AnswerEquivalence backed by the
// LLM judge: it asks whether two answers are equivalent solutions to the prompt,
// so two independently-correct answers that differ only in wording count as
// agreement rather than disagreement. Identical text (mod formatting) short-
// circuits without a call; any dispatch/parse failure degrades to the textual
// default, so the ensemble never blocks on the judge. Like the ModeBest judge,
// these calls are ensemble overhead and are not added to the SPRT sample spend.
func (s *Swarm) judgeEquivalence(ctx context.Context, prompt string, opts Options) trust.AnswerEquivalence {
	timeout := opts.JudgeTimeout
	if timeout <= 0 {
		timeout = defaultJudgeTimeout
	}
	return func(candidate, answer string) bool {
		if trust.TextEquivalence(candidate, answer) {
			return true // identical mod formatting — no need to spend a judge call
		}
		jctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		out, err := s.d.Dispatch(jctx, buildEquivalencePrompt(prompt, candidate, answer), dispatch.Options{
			TierHint: opts.JudgeTierHint,
			System:   "You compare two answers for equivalence. Reply with exactly one word: YES or NO.",
		})
		if err != nil {
			return trust.TextEquivalence(candidate, answer) // degrade, never block
		}
		return parseYesNo(out.Output)
	}
}

// buildEquivalencePrompt asks whether two answers are equivalent solutions.
func buildEquivalencePrompt(prompt, a, b string) string {
	var sb strings.Builder
	sb.WriteString("Two answers were given to the same task. Decide whether they are EQUIVALENT — ")
	sb.WriteString("both correct/valid solutions that satisfy the task, even if they differ in ")
	sb.WriteString("wording, identifiers, formatting, or style.\n\nTask:\n---\n")
	sb.WriteString(prompt)
	sb.WriteString("\n---\n\nAnswer A:\n---\n")
	sb.WriteString(a)
	sb.WriteString("\n---\n\nAnswer B:\n---\n")
	sb.WriteString(b)
	sb.WriteString("\n---\n\nAre A and B equivalent? Reply with exactly one word: YES or NO.")
	return sb.String()
}

// parseYesNo extracts a yes/no decision from a judge reply. Ambiguous or empty
// replies are treated as NO — the conservative direction for a trust check, so a
// confused judge never inflates agreement.
func parseYesNo(s string) bool {
	for _, f := range strings.Fields(strings.ToLower(s)) {
		switch {
		case strings.HasPrefix(f, "yes"):
			return true
		case strings.HasPrefix(f, "no"):
			return false
		}
	}
	return false
}
