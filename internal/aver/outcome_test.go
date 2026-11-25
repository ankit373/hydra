// SPDX-License-Identifier: MIT

package aver

import "testing"

func TestOutcome_LineForEachMode(t *testing.T) {
	for _, c := range []struct {
		o    Outcome
		want string
	}{
		{Outcome{Mode: ModeAnyDependence, Alpha: 0.05, Product: 212, Bar: 40},
			"certified (any dependence) at alpha 0.05: product 212 >= bar 40"},
		{Outcome{Mode: ModeIndependence, Alpha: 0.01, Product: 129.1, Bar: 100},
			"certified (under independence) at alpha 0.01: product 129 >= bar 100"},
		{Outcome{Alpha: 0.05, Reason: ReasonTooFewWrong, Have: 41, Need: 60},
			"not verified: 41 labelled wrong answers, need 60"},
		{Outcome{Alpha: 0.05, Reason: ReasonFutile, Product: 3.2, Bar: 40},
			"not verified: futility, product 3.2 cannot reach bar 40"},
		{Outcome{Alpha: 0.05, Reason: ReasonNoCheck},
			"not verified: no check for this kind"},
		{Outcome{Mode: ModeIndependence, Alpha: 0.05, Product: 50.6, Bar: 40, Candidates: 2, Budget: 3},
			"certified (under independence) at alpha 0.05: product 50.6 >= bar 40, candidate 2 of 3"},
		{Outcome{Alpha: 0.05, Reason: ReasonCandidates, Product: 0, Bar: 80, Candidates: 3, Budget: 3},
			"not verified: candidate budget after 3 of 3 candidates, last product 0 < bar 80"},
		{Outcome{Alpha: 0.05, Reason: ReasonCostCeiling, Product: 1, Bar: 40, Candidates: 1, Budget: 3},
			"not verified: cost ceiling after 1 of 3 candidates, last product 1 < bar 40"},
	} {
		if got := c.o.Line(); got != c.want {
			t.Errorf("Line() = %q, want %q", got, c.want)
		}
		if c.o.Certified() != (c.o.Mode != "") {
			t.Errorf("Certified() wrong for %+v", c.o)
		}
	}
}

func TestStepFor_CarriesCalibrationAndE(t *testing.T) {
	c := Cell{Check: "tests", S: 0.9, QBar: 0.1}
	s := StepFor(c, false)
	if s.Check != "tests" || s.Passed || s.QBar != 0.1 || s.S != 0.9 || s.E != EValue(c, false) {
		t.Fatalf("step %+v", s)
	}
}
