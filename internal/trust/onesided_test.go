// SPDX-License-Identifier: MIT

package trust

import (
	"context"
	"errors"
	"math"
	"testing"
)

// generatorCell trains the shape `hyctl edit` and `hyctl review` produce: the
// head asserted its own output every time (saidCorrect=true), and the validator
// judged it. TN and FN never move, so the cell is one-sided by construction.
func generatorCell(c *Calibrator, id, domain string, passed, failed int) {
	for i := 0; i < passed; i++ {
		_ = c.Update(id, domain, true, OutcomeCorrect)
	}
	for i := 0; i < failed; i++ {
		_ = c.Update(id, domain, true, OutcomeIncorrect)
	}
}

// A source that has only ever answered one way has not discriminated between
// anything, so it carries no information whatever its accuracy. Before #1142 the
// Laplace prior turned that into a *negative* LLR: with TN pinned at the prior
// and FP growing, sp = prior/(prior+FP) falls, and ln(se/(1-sp)) crosses zero at
// FP≈7. A head whose own edits had failed eight times then counted as evidence
// against any answer it agreed with.
func TestLLR_OneSidedSourceCarriesNoEvidence(t *testing.T) {
	for _, failed := range []int{0, 1, 4, 7, 8, 20, 100} {
		c, _ := New("")
		generatorCell(c, "head", "go", 7, failed)

		agree := c.LLR("head", "go", true)
		differ := c.LLR("head", "go", false)
		d := c.D("head", "go")

		if agree != 0 || differ != 0 || d != 0 {
			t.Errorf("failed=%d: one-sided cell is evidence: LLR(agree)=%+.4f LLR(differ)=%+.4f D=%+.4f, want all 0",
				failed, agree, differ, d)
		}
	}
}

// The specific inversion, named: at eight failures the old arithmetic made
// agreement worth less than nothing. Asserted as a sign rather than a magnitude,
// because the magnitude depends on the prior and the sign is the defect.
func TestLLR_AgreementIsNeverEvidenceAgainst(t *testing.T) {
	c, _ := New("")
	generatorCell(c, "head", "go", 7, 8)

	se, sp := c.rates("head", "go")
	raw := math.Log(se / (1 - sp)) // what the ungated arithmetic still yields
	if raw >= 0 {
		t.Fatalf("fixture no longer reproduces the inversion: se=%.3f sp=%.3f raw LLR=%+.4f, want negative", se, sp, raw)
	}
	if got := c.LLR("head", "go", true); got < 0 {
		t.Errorf("a head agreeing counted against the answer: LLR(agree)=%+.4f", got)
	}
}

// The fix must not cost the router its ranking input: Commitments reads the
// positive row, which a generator observation legitimately fills.
func TestCommitments_StillCountsGeneratorObservations(t *testing.T) {
	c, _ := New("")
	generatorCell(c, "head", "go", 7, 3)

	correct, total := c.Commitments("head")
	if correct != 7 || total != 10 {
		t.Errorf("Commitments = %d/%d, want 7/10", correct, total)
	}
}

// The user-visible property: with only one-sided calibration no hypothesis may
// fall below the 0.5 prior. Before #1142 two heads proposing different answers
// drove *both* candidates negative, so the run reported less than a coin flip
// whichever it picked, which is what `hyctl trust stats` measured as "achieved
// 50.1%" across 17 real runs.
func TestSPRT_OneSidedCalibrationNeverLandsBelowThePrior(t *testing.T) {
	c, _ := New("")
	generatorCell(c, "a", "d", 7, 4)
	generatorCell(c, "b", "d", 7, 4)

	exec := &scriptExec{seq: []string{"X", "Y"}}
	sources := []Source{{ID: "a", EstCostUSD: 0}, {ID: "b", EstCostUSD: 0}}

	res, err := Run(context.Background(), Task{Domain: "d"}, sources, exec, c,
		Target{Confidence: 0.95}, AllowNoEvidence())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, h := range res.Hypotheses {
		if h.Confidence < 0.5 {
			t.Errorf("hypothesis %q ended below the prior at %.3f (Lambda %+.4f); "+
				"uninformative evidence must not move a hypothesis down",
				h.Answer, h.Confidence, h.Lambda)
		}
	}
}

// And without the test-only override the run refuses outright, rather than
// spending on heads whose votes cannot move anything. anyEvidence gates on D,
// which is 0 for a one-sided cell, so this follows from the same change.
func TestSPRT_OneSidedCalibrationRefusesInsteadOfSpending(t *testing.T) {
	c, _ := New("")
	generatorCell(c, "a", "d", 7, 4)
	generatorCell(c, "b", "d", 7, 4)

	exec := &scriptExec{seq: []string{"X", "Y"}}
	sources := []Source{{ID: "a", EstCostUSD: 0.01}, {ID: "b", EstCostUSD: 0.01}}

	_, err := Run(context.Background(), Task{Domain: "d"}, sources, exec, c, Target{Confidence: 0.95})
	var ne *NoEvidenceError
	if !errors.As(err, &ne) {
		t.Fatalf("a domain with only one-sided cells ran anyway: err=%v", err)
	}
	if exec.i != 0 {
		t.Errorf("refused after sampling %d head(s); the refusal must come before any spend", exec.i)
	}
}
