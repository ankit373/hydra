// SPDX-License-Identifier: MIT

package trust

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
)

// CorrelationDiscount caps the evidence from a model family that has already
// voted: two heads backed by the same base model are not independent, so a
// repeat vote counts for half. Prevents false agreement from inflating Λ.
// Exported so other ensembling paths (internal/swarm's CalibratedJudge) share
// this one source of truth instead of hardcoding a second copy of the same
// constant; a future empirical per-family coupling (issue #118) replaces this
// single definition and every caller upgrades with it.
const CorrelationDiscount = 0.5

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

	// ConfidenceAfter is σ(LambdaAfter) — the running P(correct) once this
	// source had been weighed. Stored rather than left for readers to derive:
	// the ledger is a public JSON type written to trust.jsonl and read by the
	// run log, and a second copy of the sigmoid in each reader is a third place
	// for the confidence to drift.
	ConfidenceAfter float64 `json:"confidence_after"`
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
func Run(ctx context.Context, task Task, sources []Source, exec Executor, cal *Calibrator, t Target, opts ...RunOption) (*Result, error) {
	if len(sources) == 0 {
		return nil, fmt.Errorf("sprt: no sources provided")
	}
	cfg := runConfig{equiv: TextEquivalence}
	for _, o := range opts {
		o(&cfg)
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
		agreed := cfg.equiv(candidate, ans.Text)

		llr := cal.LLR(src.ID, task.Domain, agreed)
		if src.Family != "" && familySeen[src.Family] {
			llr *= CorrelationDiscount
		}
		familySeen[src.Family] = true

		lambda += llr
		res.Ledger = append(res.Ledger, Evidence{
			Source: src.ID, Agreed: agreed, LLR: llr,
			Candidate: candidate, LambdaAfter: lambda, CostUSD: cost,
			ConfidenceAfter: sigmoid(lambda),
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

// AnswerEquivalence decides whether a source's answer counts as agreeing with the
// current candidate for the purpose of accumulating correctness evidence.
//
// The v1 default (TextEquivalence) compares normalized text — which miscounts two
// independently-correct answers that differ only in wording (variable names,
// println vs. fmt.Println) as *disagreement*, pushing Λ the wrong way. In the real
// benchmark this capped achieved confidence at 32.9% even though both sources were
// oracle-verified correct (see TRUST_CONTROL_PLANE_BENCHMARK_FINDINGS.md §3).
// Callers that can compare *behavior* — an oracle verdict in a benchmark, an LLM
// judge in production — inject a semantic comparator via WithEquivalence.
type AnswerEquivalence func(candidate, answer string) bool

// RunOption configures an SPRT run without changing Run's core signature.
type RunOption func(*runConfig)

type runConfig struct {
	equiv AnswerEquivalence
}

// WithEquivalence overrides how answer agreement is decided (default:
// TextEquivalence). A nil comparator is ignored, keeping the default.
func WithEquivalence(fn AnswerEquivalence) RunOption {
	return func(c *runConfig) {
		if fn != nil {
			c.equiv = fn
		}
	}
}

// TextEquivalence is the v1 default agreement check: case-insensitive with
// collapsed whitespace. It removes trivial-formatting false disagreements but is
// NOT semantic — two behaviorally-equivalent answers with different identifiers
// still register as disagreement. Supply WithEquivalence for behavior-based
// comparison.
func TextEquivalence(candidate, answer string) bool {
	return normalizeAnswer(candidate) == normalizeAnswer(answer)
}

// normalizeAnswer canonicalizes text for TextEquivalence: lowercase, with runs of
// whitespace collapsed to single spaces and leading/trailing space removed.
func normalizeAnswer(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// sigmoid maps the log-odds Λ back to a probability for display.
func sigmoid(x float64) float64 { return 1 / (1 + math.Exp(-x)) }
