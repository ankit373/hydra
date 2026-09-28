// SPDX-License-Identifier: MIT

package trust

import (
	"math"
	"testing"
)

// ledger builds a run ledger from (source, agreed) pairs all voting on one
// candidate, the shape of a run that never pivoted.
func ledger(candidate string, votes ...any) []Evidence {
	var out []Evidence
	for i := 0; i < len(votes); i += 2 {
		out = append(out, Evidence{
			Source:    votes[i].(string),
			Agreed:    votes[i+1].(bool),
			Candidate: candidate,
		})
	}
	return out
}

// The defect this fixes: generator-only observations leave sp on its prior, so
// LLR is capped at ln2 = 0.693 nats however much data arrives, while a 95%
// target needs ln(0.95/0.05) = 2.944. Guards the #698 failure mode.
func TestPositiveOnlyObservations_CapLLRAtLn2(t *testing.T) {
	c, _ := New("")
	for i := 0; i < 500; i++ {
		mustUpdate(t, c, "qwen", "go", true, OutcomeCorrect)
	}
	if sp := spOf(c, "qwen", "go"); math.Abs(sp-0.5) > 1e-9 {
		t.Fatalf("sp = %v after 500 positive observations, want it stuck on the 0.5 prior", sp)
	}
	llr := c.LLR("qwen", "go", true)
	if llr >= math.Ln2 {
		t.Errorf("LLR = %.4f nats, must stay under the ln2 = %.4f ceiling while sp is pinned", llr, math.Ln2)
	}
	if need := math.Log(0.95 / 0.05); llr >= need {
		t.Errorf("LLR %.4f already clears the %.4f nats a 95%% target needs; the ceiling is gone", llr, need)
	}
}

// The general form of the same defect: false positives drag sp *below* the
// prior rather than leaving it there, so the honest claim is that sp can never
// rise above 0.5 without negatives, and LLR stays under ln2 either way.
func TestPositiveOnlyObservations_SpNeverRisesAboveHalf(t *testing.T) {
	c, _ := New("")
	for i := 0; i < 200; i++ {
		mustUpdate(t, c, "keen", "go", true, OutcomeCorrect)
	}
	for i := 0; i < 50; i++ {
		mustUpdate(t, c, "keen", "go", true, OutcomeIncorrect)
	}
	sp := spOf(c, "keen", "go")
	if sp > 0.5 {
		t.Errorf("sp = %v, must never exceed 0.5 with no negative verdicts recorded", sp)
	}
	if llr := c.LLR("keen", "go", true); llr >= math.Ln2 {
		t.Errorf("LLR = %.4f nats, must stay under the ln2 = %.4f ceiling", llr, math.Ln2)
	}
	if st := statFor(c, "keen", "go"); st.Neg != 0 {
		t.Errorf("Neg = %v, want 0 so the report can flag this cell as unusable", st.Neg)
	}
}

// The fix: a source that disagrees with a candidate later verified incorrect is
// a true negative, which is the only thing that can move sp off its prior.
func TestApplyRunOutcome_MovesSpecificityOffThePrior(t *testing.T) {
	c, _ := New("")
	// A run whose candidate turned out wrong: "yes" backed it, "no" flagged it.
	n, err := ApplyRunOutcome(c, "go", ledger("A", "yes", true, "no", false), OutcomeIncorrect)
	if err != nil {
		t.Fatalf("ApplyRunOutcome: %v", err)
	}
	if n != 2 {
		t.Fatalf("recorded %d observations, want 2", n)
	}
	if sp := spOf(c, "no", "go"); sp <= 0.5 {
		t.Errorf("sp(dissenter) = %v, want above the 0.5 prior after a true negative", sp)
	}
	if sp := spOf(c, "yes", "go"); sp >= 0.5 {
		t.Errorf("sp(backer) = %v, want below the 0.5 prior after a false positive", sp)
	}
}

// With both run polarities present all four cells fill, so a discriminating
// source clears the confidence bar a positive-only corpus can never reach.
func TestApplyRunOutcome_IdentifiesBothRates(t *testing.T) {
	c, _ := New("")
	for i := 0; i < 40; i++ {
		// Wrong answers: the dissenter catches them.
		if _, err := ApplyRunOutcome(c, "go", ledger("A", "keen", true, "picky", false), OutcomeIncorrect); err != nil {
			t.Fatalf("ApplyRunOutcome: %v", err)
		}
		// Right answers: both back them.
		if _, err := ApplyRunOutcome(c, "go", ledger("B", "keen", true, "picky", true), OutcomeCorrect); err != nil {
			t.Fatalf("ApplyRunOutcome: %v", err)
		}
	}
	llr := c.LLR("picky", "go", true)
	if llr <= math.Ln2 {
		t.Errorf("LLR(picky) = %.4f nats, want past the ln2 ceiling once sp is identified", llr)
	}
	if d := c.D("picky", "go"); d <= c.D("keen", "go") {
		t.Errorf("D(picky) = %.4f must exceed D(keen) = %.4f; only picky discriminates", d, c.D("keen", "go"))
	}
}

// The leader can change mid-run, so an entry's Agreed bit is relative to
// whatever was leading then. Re-expressing it against the verified answer is
// what keeps a superseded vote from being counted backwards.
func TestApplyRunOutcome_RelativizesVotesAcrossALeaderChange(t *testing.T) {
	c, _ := New("")
	led := []Evidence{
		{Source: "early", Agreed: true, Candidate: "A"},  // backed A, which lost
		{Source: "quiet", Agreed: false, Candidate: "A"}, // rejected A, said nothing about B
		{Source: "late", Agreed: true, Candidate: "B"},   // backed B, which was verified
	}
	n, err := ApplyRunOutcome(c, "go", led, OutcomeCorrect)
	if err != nil {
		t.Fatalf("ApplyRunOutcome: %v", err)
	}
	if n != 2 {
		t.Fatalf("recorded %d observations, want 2 with the indeterminate vote skipped", n)
	}
	// "early" asserted A, so against the verified B it said incorrect: an FN,
	// which drags se down rather than up.
	if se := seOf(c, "early", "go"); se >= 0.5 {
		t.Errorf("se(early) = %v, want below 0.5: backing the losing answer is a negative verdict on B", se)
	}
	if se := seOf(c, "late", "go"); se <= 0.5 {
		t.Errorf("se(late) = %v, want above 0.5 after backing the verified answer", se)
	}
	// "quiet" ruled out A only, so it must carry no observation at all.
	if se, sp := seOf(c, "quiet", "go"), spOf(c, "quiet", "go"); se != 0.5 || sp != 0.5 {
		t.Errorf("quiet moved to se=%v sp=%v; an indeterminate vote must record nothing", se, sp)
	}
}

func TestApplyRunOutcome_RefusesUnusableInput(t *testing.T) {
	c, _ := New("")
	if _, err := ApplyRunOutcome(nil, "go", ledger("A", "x", true), OutcomeCorrect); err == nil {
		t.Error("nil calibrator must error rather than silently drop the outcome")
	}
	if n, err := ApplyRunOutcome(c, "go", ledger("A", "x", true), OutcomeUnknown); err != nil || n != 0 {
		t.Errorf("OutcomeUnknown = (%d, %v), want (0, nil): no ground truth, no training", n, err)
	}
	if n, err := ApplyRunOutcome(c, "go", nil, OutcomeCorrect); err != nil || n != 0 {
		t.Errorf("empty ledger = (%d, %v), want (0, nil)", n, err)
	}
}

func statFor(c *Calibrator, source, domain string) Stat {
	for _, s := range c.Report() {
		if s.Source == source && s.Domain == domain {
			return s
		}
	}
	return Stat{}
}

func seOf(c *Calibrator, source, domain string) float64 {
	se, _ := c.rates(source, domain)
	return se
}

func spOf(c *Calibrator, source, domain string) float64 {
	_, sp := c.rates(source, domain)
	return sp
}
