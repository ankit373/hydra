// SPDX-License-Identifier: MIT

package aver

import (
	"math"
	"slices"
	"testing"
)

func TestWeights_SpendSumsToOne(t *testing.T) {
	for n := 1; n <= 12; n++ {
		w, err := Weights(n, RuleSpend)
		if err != nil {
			t.Fatal(err)
		}
		var sum float64
		for _, x := range w {
			sum += x
		}
		if sum != 1 {
			t.Fatalf("n=%d: weights %v sum to %v", n, w, sum)
		}
	}
	// Worked example: 4 candidates get 1/2, 1/4, 1/8 and the remaining 1/8.
	w, _ := Weights(4, RuleSpend)
	if !slices.Equal(w, []float64{0.5, 0.25, 0.125, 0.125}) {
		t.Fatalf("spend(4) = %v", w)
	}
}

func TestWeights_BonferroniRefusedAboveThree(t *testing.T) {
	w, err := Weights(3, RuleBonferroni)
	if err != nil || math.Abs(w[0]-1.0/3) > 1e-15 || len(w) != 3 {
		t.Fatalf("bonferroni(3) = %v, %v", w, err)
	}
	if _, err := Weights(4, RuleBonferroni); err == nil {
		t.Fatal("bonferroni accepted 4 candidates")
	}
	if _, err := Weights(2, "equal"); err == nil {
		t.Fatal("unknown rule accepted")
	}
	if _, err := Weights(0, RuleSpend); err == nil {
		t.Fatal("zero candidates accepted")
	}
}

func TestBar_SecondCandidateNeedsMoreEvidence(t *testing.T) {
	// alpha 0.05 over 3 candidates by spend: bars 1/(0.05/2) = 40, then 80, then 80.
	w, _ := Weights(3, RuleSpend)
	bars := []float64{Bar(0.05, w[0]), Bar(0.05, w[1]), Bar(0.05, w[2])}
	if math.Abs(bars[0]-40) > 1e-9 || math.Abs(bars[1]-80) > 1e-9 || math.Abs(bars[2]-80) > 1e-9 {
		t.Fatalf("bars %v, want 40, 80, 80", bars)
	}
	if !(bars[1] > bars[0]) {
		t.Fatal("the second candidate must need more evidence than the first")
	}
}
