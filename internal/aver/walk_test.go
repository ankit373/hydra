// SPDX-License-Identifier: MIT

package aver

import (
	"math"
	"testing"
)

func TestWalk_WeightsFixedBeforeFirstCall(t *testing.T) {
	// alpha 0.05, 3 candidates by spend: weights 1/2, 1/4, 1/4, bars 40, 80, 80.
	w, err := NewWalk(0.05, 3, RuleSpend)
	if err != nil {
		t.Fatal(err)
	}
	if w.Budget() != 3 || w.Bought() != 0 {
		t.Fatalf("budget %d bought %d before any call", w.Budget(), w.Bought())
	}
	for i, want := range []float64{40, 80, 80} {
		bar, ok := w.Next()
		if !ok || math.Abs(bar-want) > 1e-9 {
			t.Fatalf("candidate %d: bar %v ok %v, want %v", i+1, bar, ok, want)
		}
	}
	if _, ok := w.Next(); ok || w.Bought() != 3 {
		t.Fatal("a fourth candidate got a bar past the budget")
	}
	if _, err := NewWalk(0.05, 4, RuleBonferroni); err == nil {
		t.Fatal("bonferroni over 4 candidates accepted")
	}
}
