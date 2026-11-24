// SPDX-License-Identifier: MIT

package aver

import (
	"math"
	"slices"
	"testing"
)

// labelled makes n answers, the given features passing on the first `pass`.
func labelled(correct bool, n, pass int, features ...string) []Answer {
	var out []Answer
	for i := range n {
		v := map[string]bool{}
		for _, f := range features {
			v[f] = i < pass
		}
		out = append(out, Answer{Kind: "k", Head: "m", Correct: correct, Verdicts: v})
	}
	return out
}

func TestPattern_RBarOverJointPasses(t *testing.T) {
	// a passes 10 wrong answers, b passes 10 others: the joint pattern {a, b}
	// matched none of 200, so r̄ = 1 - (0.05/3)^(1/200) (Clopper-Pearson, k = 0).
	var in []Answer
	for i := range 200 {
		in = append(in, Answer{Verdicts: map[string]bool{"a": i < 10, "b": i >= 10 && i < 20}})
	}
	in = append(in, labelled(true, 50, 50, "a", "b")...)
	fam := Family([]string{"a", "b"}, nil)
	if len(fam) != 3 {
		t.Fatalf("family %v, want a, b, a+b", fam)
	}
	p, nv := FitPattern(in, fam, 20, 0.05)
	if nv != nil {
		t.Fatalf("refused: %+v", nv)
	}
	want := 1 - math.Pow(0.05/3, 1.0/200)
	if !slices.Equal(p.Features, []string{"a", "b"}) || p.Matched != 0 || math.Abs(p.RBar-want) > 1e-9 {
		t.Fatalf("pattern %+v, want {a, b} with r̄ %.6f", p, want)
	}
}

func TestPattern_DeltaSplitOverFamily(t *testing.T) {
	in := append(labelled(false, 100, 0, "a", "b", "c"), labelled(true, 10, 10, "a", "b", "c")...)
	fam := Family([]string{"c", "a", "b"}, []string{AgreeFeature(1)})
	// 7 check sets, the agreement alone, and it added to the 6 sets under 3 features.
	if len(fam) != 14 {
		t.Fatalf("family has %d patterns, want 14: %v", len(fam), fam)
	}
	p, nv := FitPattern(in, fam, 2, 0.05)
	if nv != nil {
		t.Fatal(nv)
	}
	if want := 1 - math.Pow(0.05/14, 1.0/100); math.Abs(p.RBar-want) > 1e-12 {
		t.Fatalf("r̄ %v, want %v at delta/14", p.RBar, want)
	}
}

func TestPattern_NeedCountMatchesFormula(t *testing.T) {
	// One pattern, delta 0.05, bar 20: need = ceil(ln(1/0.05) * 20) = ceil(59.9) = 60.
	if n := NeedWrong(1, 0.05, 20); n != 60 {
		t.Fatalf("need %d, want 60", n)
	}
	fam := [][]string{{"a"}}
	right := labelled(true, 10, 10, "a")
	if _, nv := FitPattern(append(labelled(false, 60, 0, "a"), right...), fam, 20, 0.05); nv != nil {
		t.Fatalf("60 clean wrong answers should clear bar 20: %+v", nv)
	}
	_, nv := FitPattern(append(labelled(false, 41, 0, "a"), right...), fam, 20, 0.05)
	if nv == nil || nv.Reason != ReasonTooFewWrong || nv.Have != 41 || nv.Need != 60 {
		t.Fatalf("want too few labelled wrong answers, 41 of 60, got %+v", nv)
	}
	_, nv = FitPattern(append(labelled(false, 100, 50, "a"), right...), fam, 20, 0.05)
	if nv == nil || nv.Reason != ReasonNoPattern {
		t.Fatalf("enough data but a leaky pattern should say so, got %+v", nv)
	}
	if _, nv := FitPattern(right, fam, 20, 0.05); nv == nil || nv.Have != 0 {
		t.Fatalf("no wrong answers at all: %+v", nv)
	}
}

func TestPattern_ChosenOnCalibrationOnly(t *testing.T) {
	// On these answers a alone never matches a wrong answer and passes every
	// right one, so it wins over the stricter {a, b}. Nothing else is read.
	in := append(labelled(false, 200, 0, "a"), labelled(true, 20, 20, "a")...)
	for i := range in {
		in[i].Verdicts["b"] = in[i].Correct && i%2 == 0
	}
	p, nv := FitPattern(in, Family([]string{"a", "b"}, nil), 10, 0.05)
	if nv != nil || !slices.Equal(p.Features, []string{"a"}) || p.S != 1 {
		t.Fatalf("want {a} with s 1, got %+v %v", p, nv)
	}
}
