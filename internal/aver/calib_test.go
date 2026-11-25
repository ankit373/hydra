// SPDX-License-Identifier: MIT

package aver

import (
	"math"
	"testing"
)

// answers makes wrong and right answers from one head, the check passing the
// first falsePass wrong ones and the first truePass right ones.
func answers(kind, head, check string, wrong, falsePass, right, truePass int) []Answer {
	var out []Answer
	for i := range wrong {
		out = append(out, Answer{Kind: kind, Head: head, Verdicts: map[string]bool{check: i < falsePass}})
	}
	for i := range right {
		out = append(out, Answer{Kind: kind, Head: head, Correct: true, Verdicts: map[string]bool{check: i < truePass}})
	}
	return out
}

func cell(t *testing.T, tab Table, kind, check, head string) Cell {
	t.Helper()
	for _, c := range tab.Cells {
		if c.Kind == kind && c.Check == check && c.Head == head {
			return c
		}
	}
	t.Fatalf("no cell (%s, %s, %s) in %+v", kind, check, head, tab.Cells)
	return Cell{}
}

func TestFit_QBarIsClopperPearsonAtDeltaOverM(t *testing.T) {
	// Two checks in the kind, so each bound is at 0.05/2. With 0 false passes in
	// 20, Clopper-Pearson (1934) solves (1-p)^20 = 0.025: q̄ = 1 - 0.025^(1/20).
	in := answers("math", "m", "format", 20, 0, 10, 9)
	in = append(in, answers("math", "m", "arith", 20, 0, 10, 9)...)
	tab, err := Fit(in, 0.05)
	if err != nil {
		t.Fatal(err)
	}
	want := 1 - math.Pow(0.025, 1.0/20) // 0.16843
	c := cell(t, tab, "math", "format", "m")
	if math.Abs(c.QBar-want) > 1e-9 {
		t.Fatalf("q̄ = %.6f, want %.6f", c.QBar, want)
	}
	if c.Wrong != 20 || c.FalsePass != 0 || c.Right != 10 || c.TruePass != 9 || c.S != 0.9 {
		t.Fatalf("counts wrong: %+v", c)
	}
	if c.Skip || math.Abs(c.EPass()-0.9/want) > 1e-9 {
		t.Fatalf("e on pass = %v skip=%v, want %v", c.EPass(), c.Skip, 0.9/want)
	}
}

func TestFit_NoWrongAnswersGivesQBarOne(t *testing.T) {
	tab, err := Fit(answers("code", "m", "tests", 0, 0, 5, 5), 0.05)
	if err != nil {
		t.Fatal(err)
	}
	c := cell(t, tab, "code", "tests", "m")
	if c.QBar != 1 || !c.Skip || c.EPass() != 1 {
		t.Fatalf("want q̄ 1, skipped, e 1; got %+v e=%v", c, c.EPass())
	}
}

func TestFit_SkipWhenSBelowQBar(t *testing.T) {
	// 10 of 40 wrong answers pass, so q̄ is above 0.25; 1 of 10 right ones pass.
	tab, err := Fit(answers("label", "m", "match", 40, 10, 10, 1), 0.05)
	if err != nil {
		t.Fatal(err)
	}
	c := cell(t, tab, "label", "match", "m")
	if !(c.S <= c.QBar) || !c.Skip || c.EPass() != 1 {
		t.Fatalf("want skip with s <= q̄, got %+v", c)
	}
}

func TestFit_DoesNotPoolAcrossHeads(t *testing.T) {
	// The pilot's shape: tests pass 0 of a weak head's wrong answers, 16 of 40 of a strong one's.
	in := answers("code", "weak", "tests", 40, 0, 40, 36)
	in = append(in, answers("code", "strong", "tests", 40, 16, 40, 38)...)
	tab, err := Fit(in, 0.05)
	if err != nil {
		t.Fatal(err)
	}
	if len(tab.Cells) != 2 {
		t.Fatalf("want two cells, got %+v", tab.Cells)
	}
	weak, strong := cell(t, tab, "code", "tests", "weak"), cell(t, tab, "code", "tests", "strong")
	if weak.Wrong != 40 || strong.Wrong != 40 || weak.FalsePass != 0 || strong.FalsePass != 16 {
		t.Fatalf("cells mixed heads: weak %+v strong %+v", weak, strong)
	}
	if !(strong.QBar > 0.4 && weak.QBar < 0.1) {
		t.Fatalf("q̄ weak %.3f strong %.3f", weak.QBar, strong.QBar)
	}
}

func TestFit_RefusesDeltaOutsideUnit(t *testing.T) {
	for _, d := range []float64{0, 1, -0.1, math.NaN()} {
		if _, err := Fit(nil, d); err == nil {
			t.Errorf("delta %v accepted", d)
		}
	}
}

func TestFit_SortedByKindCheckHead(t *testing.T) {
	in := answers("b", "y", "c2", 1, 0, 1, 1)
	in = append(in, answers("a", "y", "c1", 1, 0, 1, 1)...)
	in = append(in, answers("b", "x", "c2", 1, 0, 1, 1)...)
	in = append(in, answers("b", "z", "c1", 1, 0, 1, 1)...)
	tab, err := Fit(in, 0.05)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range tab.Cells {
		got = append(got, c.Kind+"/"+c.Check+"/"+c.Head)
	}
	want := []string{"a/c1/y", "b/c1/z", "b/c2/x", "b/c2/y"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order %v, want %v", got, want)
		}
	}
}
