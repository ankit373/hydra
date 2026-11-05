// SPDX-License-Identifier: MIT

package decide

import (
	"context"
	"fmt"
	"math"

	"github.com/ankit373/hydra/internal/oracle"
	"github.com/ankit373/hydra/internal/trust"
)

// Decision is how a routed request ended, in one vocabulary for both endpoints.
type Decision string

const (
	Accepted        Decision = "accept"
	BudgetExhausted Decision = "budget_exhausted"
	NoEvidence      Decision = "no_evidence"
)

// Verifier is one check and the calibration key its verdicts are weighed under.
type Verifier struct {
	Source string
	Oracle oracle.Oracle
}

// Answer is one source's reply as the cascade judges it.
type Answer struct {
	Text    string
	PPass   float64 // P̂(pass | source, class), the prior the verdicts update
	CostUSD float64
}

// Cascade judges a chat request's answers in the order they arrive and says when
// to stop, on decide.Router's target and rules. One per request; not shared.
type Cascade struct {
	Target         trust.Target
	MaxEscalations int
	Cal            *trust.Calibrator
	Domain         string
	Verifiers      []Verifier

	confidences []float64
	spent       float64
	decision    Decision
}

// ChatOutcome is which judged answer to serve and why the cascade stopped.
type ChatOutcome struct {
	Index    int
	Decision Decision
}

// pPassFloor keeps a prior of 0 or 1 finite in log-odds, where no verdict could move it.
const pPassFloor = 0.01

// Judge weighs a by every measured verifier and reports whether to ask the next source.
func (c *Cascade) Judge(ctx context.Context, a Answer) (next bool, reason string) {
	p := min(max(a.PPass, pPassFloor), 1-pPassFloor)
	logOdds := math.Log(p / (1 - p))
	measured := false
	for _, v := range c.Verifiers {
		// An unmeasured verifier's LLR is the Beta prior's, which reads like evidence and is not.
		if ok, _ := oracle.Measured(c.Cal, v.Source, c.Domain); !ok {
			continue
		}
		measured = true
		verdict, err := v.Oracle.Verify(ctx, a.Text, trust.Task{Domain: c.Domain})
		if err != nil {
			continue
		}
		logOdds += oracle.LLR(c.Cal, v.Source, c.Domain, verdict)
	}
	conf := 1 / (1 + math.Exp(-logOdds))
	c.confidences = append(c.confidences, conf)
	c.spent += a.CostUSD

	switch {
	case conf >= c.Target.Confidence:
		c.decision = Accepted
	// decide.Router's rule: with nothing calibrated no further answer can move the posterior.
	case !measured:
		c.decision = NoEvidence
	case len(c.confidences) > c.MaxEscalations || (c.Target.MaxCostUSD > 0 && c.spent >= c.Target.MaxCostUSD):
		c.decision = BudgetExhausted
	default:
		return true, fmt.Sprintf("P(correct) %.3f < %.2f", conf, c.Target.Confidence)
	}
	return false, ""
}

// Outcome serves the most confident answer, the earliest on a tie. That is the
// accepted one, or the only one under no_evidence, so one rule covers every end.
// Candidates running out before the cascade stopped is budget_exhausted.
func (c *Cascade) Outcome() ChatOutcome {
	best := 0
	for i, conf := range c.confidences {
		if conf > c.confidences[best] {
			best = i
		}
	}
	d := c.decision
	if d == "" {
		d = BudgetExhausted
	}
	return ChatOutcome{Index: best, Decision: d}
}
