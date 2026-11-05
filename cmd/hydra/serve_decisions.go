// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"fmt"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/decide"
	"github.com/ankit373/hydra/internal/decision"
	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/policy"
	"github.com/ankit373/hydra/internal/pricing"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/rank"
	"github.com/ankit373/hydra/internal/runid"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/serve"
	"github.com/ankit373/hydra/internal/trust"
)

// decisionRouter adapts decide.Router to the endpoint, opening one run per
// request the way the chat path does.
type decisionRouter struct {
	router   decide.Router
	unlisted int // decision sources the serve allowlist dropped, which the banner reports
}

var _ serve.Decider = decisionRouter{}

func (r decisionRouter) Decide(ctx context.Context, c decide.Call) (decide.Outcome, error) {
	taskID := runid.New()
	runlog.DeclareRun(c.RunID, taskID, c.Request.Input)
	defer runlog.FinishRun(c.RunID, taskID)
	return r.router.Decide(ctx, c)
}

// newDecisionRouter builds the router over this machine's decision heads. --local keeps only
// local ones, allows applies the serve allowlist, and policy.yaml's max_cost_usd is the budget.
func newDecisionRouter(ctx context.Context, confidence float64, localOnly bool, allows func(id string) bool) (decisionRouter, error) {
	if !(confidence > 0 && confidence < 1) {
		return decisionRouter{}, fmt.Errorf("--confidence must be in (0, 1), got %v", confidence)
	}
	cal, err := trust.New(trust.DefaultPath())
	if err != nil {
		return decisionRouter{}, fmt.Errorf("calibration load: %w", err)
	}
	var heads []provider.Head
	unlisted := 0
	for _, h := range executor.DecisionHeads(ctx) {
		switch {
		case localOnly && !h.LocalOnly:
		case !allows(h.ID):
			unlisted++
		default:
			heads = append(heads, h)
		}
	}
	ceiling, _ := costCeiling(policy.ForFile(config.ScriptHome(), policy.Spec{}), false, 0)
	return decisionRouter{router: decide.Router{
		Heads:  heads,
		Price:  decisionPrice(pricing.Load()),
		Ask:    executor.DecisionExecutor{},
		Cal:    cal,
		Target: trust.Target{Confidence: confidence, MaxCostUSD: ceiling},
	}, unlisted: unlisted}, nil
}

// decisionPrice estimates one call from the input's size; decision sources bill input only.
func decisionPrice(db *pricing.DB) func(provider.Head, decision.Request) float64 {
	return func(h provider.Head, r decision.Request) float64 {
		return db.CostForModel(h.Meta["model"], rank.UITier(h), executor.EstimateTokens(r.Input), 0)
	}
}
