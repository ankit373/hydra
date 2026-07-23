package trust

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
)

// correlationDiscount caps the evidence from a model family that has already
// voted: two heads backed by the same base model are not independent, so a
// repeat vote counts for half. Prevents false agreement from inflating Λ.
const correlationDiscount = 0.5

// Target configures an SPRT run. The domain used for calibration lookups comes
// from the Task, not here, so there is exactly one source of truth for it.
type Target struct {
	Confidence float64 // desired P(correct), e.g. 0.95 → α = 1-conf
	MaxCostUSD float64 // hard spend ceiling; 0 = no limit
}

// Source is the minimal view of a head that SPRT needs. Kept independent of
// provider.Head so the trust package stays a low-level dependency; callers
// (swarm/dispatch) adapt their heads to this in Phase 2b.
type Source struct {
	ID         string // calibration key, e.g. "model:claude-sonnet"
	Family     string // base-model family, for the correlation discount ("" = independent)
	EstCostUSD float64
}

// Answer is what a source returned for the task.
type Answer struct {
	Text    string
	CostUSD float64 // actual cost if known; falls back to Source.EstCostUSD
}

// Executor runs one source against a task. Production wraps the swarm executor;
// tests inject a deterministic fake.
type Executor interface {
	Execute(ctx context.Context, src Source, task Task) (Answer, error)
}

// Decision is how an SPRT run ended.
type Decision int

const (
	// DecisionAccept: Λ crossed the accept threshold A at the target confidence.
	DecisionAccept Decision = iota
	// DecisionStoppedOnBudget: ran out of budget/heads before reaching A — the
	// residual uncertainty is where a human oracle (review) should be spent.
	DecisionStoppedOnBudget
)

func (d Decision) String() string {
	if d == DecisionAccept {
		return "accept"
	}
	return "stopped_on_budget"
}

// Evidence is one entry in the LLR ledger — a single source's calibrated
// contribution to the running log-odds that the candidate is correct.
type Evidence struct {
	Source      string  `json:"source"`
	Agreed      bool    `json:"agreed"` // matched the candidate at the time
	LLR         float64 `json:"llr"`    // calibrated contribution (nats), after any discount
	Candidate   string  `json:"candidate"`
	LambdaAfter float64 `json:"lambda_after"`
	CostUSD     float64 `json:"cost_usd"`
}

// Result is the outcome of Run.
type Result struct {
	Candidate  string     `json:"candidate"`
	Confidence float64    `json:"confidence"` // σ(Λ)
	Decision   Decision   `json:"decision"`
	Lambda     float64    `json:"lambda"`
	SpentUSD   float64    `json:"spent_usd"`
	Samples    int        `json:"samples"`
	Ledger     []Evidence `json:"ledger"`
}

// Run executes the sequential probability ratio test: it samples sources in
// decreasing evidence-per-dollar order, accumulating the calibrated
// log-likelihood ratio Λ that the current candidate answer is correct, and
// stops as soon as Λ crosses the Wald accept threshold A (target confidence) or
// runs out of budget. A source that disagrees with the candidate pushes Λ down;
// if Λ crosses the reject threshold B the run pivots to that source's answer
// (destructive interference collapses to the better branch).
func Run(ctx context.Context, task Task, sources []Source, exec Executor, cal *Calibrator, t Target) (*Result, error) {
	if len(sources) == 0 {
		return nil, fmt.Errorf("sprt: no sources provided")
	}
	alpha := 1 - t.Confidence
	if alpha <= 0 || alpha >= 1 {
		return nil, fmt.Errorf("sprt: confidence must be in (0,1), got %v", t.Confidence)
	}
	// Symmetric Wald thresholds (α = β for v1).
	A := math.Log((1 - alpha) / alpha)
	B := -A

	// Sample most-evidence-per-dollar first.
	order := append([]Source(nil), sources...)
	sort.SliceStable(order, func(i, j int) bool {
		return evidencePerCost(cal, order[i], task.Domain) > evidencePerCost(cal, order[j], task.Domain)
	})

	res := &Result{Decision: DecisionStoppedOnBudget}
	var lambda float64
	candidate := ""
	familySeen := map[string]bool{}

	for _, src := range order {
		cost := src.EstCostUSD
		if t.MaxCostUSD > 0 && res.SpentUSD+cost > t.MaxCostUSD {
			break // next sample would blow the budget
		}
		ans, err := exec.Execute(ctx, src, task)
		if err != nil {
			continue // a dead head is not evidence — skip it
		}
		if ans.CostUSD > 0 {
			cost = ans.CostUSD
		}
		res.SpentUSD += cost
		res.Samples++

		if candidate == "" {
			candidate = ans.Text // first answer seeds the candidate
		}
		agreed := normalizeAnswer(ans.Text) == normalizeAnswer(candidate)

		llr := cal.LLR(src.ID, task.Domain, agreed)
		if src.Family != "" && familySeen[src.Family] {
			llr *= correlationDiscount
		}
		familySeen[src.Family] = true

		lambda += llr
		res.Ledger = append(res.Ledger, Evidence{
			Source: src.ID, Agreed: agreed, LLR: llr,
			Candidate: candidate, LambdaAfter: lambda, CostUSD: cost,
		})

		if lambda >= A {
			res.Decision = DecisionAccept
			break
		}
		if lambda <= B {
			// The weight of evidence says the candidate is wrong. Collapse to the
			// disagreeing answer and re-seed Λ with this source asserting it.
			candidate = ans.Text
			lambda = cal.LLR(src.ID, task.Domain, true)
			// The pivoting source's own vote now supports the new candidate.
			res.Ledger[len(res.Ledger)-1].Candidate = candidate
			res.Ledger[len(res.Ledger)-1].LambdaAfter = lambda
		}
	}

	res.Candidate = candidate
	res.Lambda = lambda
	res.Confidence = sigmoid(lambda)
	return res, nil
}

// evidencePerCost ranks sources by diagnostic power per dollar. Zero-cost
// sources (local) sort by raw D.
func evidencePerCost(cal *Calibrator, src Source, domain string) float64 {
	d := cal.D(src.ID, domain)
	if src.EstCostUSD <= 0 {
		return d * 1e6 // effectively free → sample first
	}
	return d / src.EstCostUSD
}

// normalizeAnswer defines answer equality for agreement detection. v1 is a
// conservative trim; semantic/structural equivalence is future work.
func normalizeAnswer(s string) string { return strings.TrimSpace(s) }

// sigmoid maps the log-odds Λ back to a probability for display.
func sigmoid(x float64) float64 { return 1 / (1 + math.Exp(-x)) }
