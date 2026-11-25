// SPDX-License-Identifier: MIT

package aver

import "testing"

// jump is a judge whose false passes go from 0 of the first `before` wrong
// answers to all of the next `after`. Every right answer passes, one per wrong one.
func jump(before, after int) []Answer {
	var out []Answer
	for i := range before + after {
		out = append(out,
			Answer{Kind: "summary", Head: "m", Verdicts: map[string]bool{"judge": i >= before, "ground": false}},
			Answer{Kind: "summary", Head: "m", Correct: true, Verdicts: map[string]bool{"judge": true, "ground": true}})
	}
	return out
}

func TestFit_UsesOnlyDataSinceChange(t *testing.T) {
	in := jump(60, 30)
	ch := Changes(in)
	if at, ok := ch[[3]string{"summary", "judge", "m"}]; !ok || at != 60 {
		t.Fatalf("judge changed at %v (found %v), want after 60 wrong answers", at, ok)
	}
	if _, ok := ch[[3]string{"summary", "ground", "m"}]; ok {
		t.Fatal("ground never changed but has a change point")
	}
	tab, err := FitSince(in, 0.05, ch)
	if err != nil {
		t.Fatal(err)
	}
	// Right is 31: the right answer just after the 60th wrong one is past the change.
	judge := cell(t, tab, "summary", "judge", "m")
	if !judge.Changed || judge.Wrong != 30 || judge.FalsePass != 30 || judge.Right != 31 || judge.QBar != 1 || !judge.Skip {
		t.Fatalf("judge should count only the 30 answers since the change: %+v", judge)
	}
	ground := cell(t, tab, "summary", "ground", "m")
	if ground.Changed || ground.Wrong != 90 || ground.Skip {
		t.Fatalf("ground keeps its whole record: %+v", ground)
	}
	// Fit alone ignores time: the judge's pooled 30 of 90 still reads as usable.
	all, _ := Fit(in, 0.05)
	if c := cell(t, all, "summary", "judge", "m"); c.Wrong != 90 || c.Changed {
		t.Fatalf("Fit should see every answer: %+v", c)
	}
}

func TestFit_RecentlyChangedCheckIsSkipped(t *testing.T) {
	// The judge's false passes jump, then it is fixed: 12 more wrong answers it
	// rejects are not yet enough to trust it again.
	in := jump(60, 30)
	for range 12 {
		in = append(in,
			Answer{Kind: "summary", Head: "m", Verdicts: map[string]bool{"judge": false}},
			Answer{Kind: "summary", Head: "m", Correct: true, Verdicts: map[string]bool{"judge": true}})
	}
	ch := Changes(in)
	tab, err := FitSince(in, 0.05, ch)
	if err != nil {
		t.Fatal(err)
	}
	judge := cell(t, tab, "summary", "judge", "m")
	if !judge.Changed || judge.Wrong >= MinWrongSinceChange || !judge.Skip || judge.Reason != "changed recently" {
		t.Fatalf("want skipped as changed recently, got %+v (change at %d)", judge, ch[[3]string{"summary", "judge", "m"}])
	}
	if judge.EPass() != 1 {
		t.Fatal("a recently changed check must be worth 1")
	}
}

func TestChanges_OnTheHeadsClock(t *testing.T) {
	// "late" first judges at the head's 31st wrong answer, then jumps 30 later.
	in := jump(60, 30)
	for i, a := range in {
		if !a.Correct && i/2 >= 30 {
			a.Verdicts["late"] = i/2 >= 60
		}
	}
	if at := Changes(in)[[3]string{"summary", "late", "m"}]; at != 60 {
		t.Fatalf("late changed at %d on its own clock, want 60 on its head's clock", at)
	}
}

func TestChanges_OtherHeadsDoNotMoveTheClock(t *testing.T) {
	// Another model's 200 wrong answers, interleaved, must not shift m's change point.
	var in []Answer
	for i, a := range jump(60, 30) {
		in = append(in, a)
		if i%2 == 0 {
			in = append(in, Answer{Kind: "summary", Head: "n", Verdicts: map[string]bool{"judge": false}},
				Answer{Kind: "summary", Head: "n", Verdicts: map[string]bool{"judge": false}})
		}
	}
	ch := Changes(in)
	if at := ch[[3]string{"summary", "judge", "m"}]; at != 60 {
		t.Fatalf("m changed at %d, want 60 on its own wrong answers", at)
	}
	tab, err := FitSince(in, 0.05, ch)
	if err != nil {
		t.Fatal(err)
	}
	if c := cell(t, tab, "summary", "judge", "m"); c.Wrong != 30 || !c.Changed {
		t.Fatalf("m should count its 30 wrong answers since the change: %+v", c)
	}
}
