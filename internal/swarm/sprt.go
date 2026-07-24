package swarm

import (
	"context"
	"fmt"
	"time"

	"github.com/ankit373/hydra/internal/config"
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
	res, err := trust.Run(ctx, trust.Task{Domain: domain}, sources, adapter, cal, trust.Target{
		Confidence: opts.Confidence,
		MaxCostUSD: opts.MaxEstCostUSD,
	})
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
