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
	// NeedsVerdict refuses to accept on the prior alone, however sure it is:
	// the counted verdicts must also, together, support the answer.
	NeedsVerdict bool

	confidences []float64
	spent       float64
	decision    Decision
	accepted    int // index of the accepted answer, valid when decision is Accepted
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
	measured, evidence := false, 0.0
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
		evidence += oracle.LLR(c.Cal, v.Source, c.Domain, verdict)
	}
	logOdds += evidence
	conf := 1 / (1 + math.Exp(-logOdds))
	c.confidences = append(c.confidences, conf)
	c.spent += a.CostUSD

	switch {
	// A failed verdict from a weak verifier barely moves a sure prior, so it must not be what accepts.
	case conf >= c.Target.Confidence && (evidence > 0 || !c.NeedsVerdict):
		c.decision, c.accepted = Accepted, len(c.confidences)-1
	// decide.Router's rule: with nothing calibrated no further answer can move the posterior.
	case !measured:
		c.decision = NoEvidence
	case len(c.confidences) > c.MaxEscalations || (c.Target.MaxCostUSD > 0 && c.spent >= c.Target.MaxCostUSD):
		c.decision = BudgetExhausted
	case conf >= c.Target.Confidence:
		return true, "no verifier verdict supported it"
	default:
		return true, fmt.Sprintf("P(correct) %.3f < %.2f", conf, c.Target.Confidence)
	}
	return false, ""
}

// Outcome serves the accepted answer, else the most confident, the earliest on a
// tie. Candidates running out before the cascade stopped is budget_exhausted.
func (c *Cascade) Outcome() ChatOutcome {
	if c.decision == Accepted {
		return ChatOutcome{Index: c.accepted, Decision: Accepted}
	}
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
