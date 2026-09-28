// SPDX-License-Identifier: MIT

package runlog

import (
	"errors"
	"math"
	"testing"
)

// seedSpan writes one finished span and returns its id.
func seedSpan(t *testing.T, runID, head string) string {
	t.Helper()
	span := NewSpanID()
	if err := New(runID).Append(Event{
		Kind: KindDispatchFinished, TaskID: "t1", SpanID: span,
		Head: head, Model: head, Tier: 8, Status: "ok",
	}); err != nil {
		t.Fatal(err)
	}
	return span
}

// The point of a score is that it lands on work already written, in a later
// process: a test suite finishes long after the dispatch it judges.
func TestAppendScore_AttachesToAnExistingSpan(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())
	span := seedSpan(t, "r1", "h1")

	if err := AppendScore("r1", span, Score{
		Name: "tests", Value: 1, Comment: "suite passed", Source: "verifier:go",
	}); err != nil {
		t.Fatal(err)
	}
	events, err := Load("r1")
	if err != nil {
		t.Fatal(err)
	}
	got := Scores(events, span)
	if len(got) != 1 {
		t.Fatalf("got %d scores, want 1", len(got))
	}
	if got[0].Name != "tests" || got[0].Value != 1 || got[0].Source != "verifier:go" {
		t.Fatalf("score lost fields: %+v", got[0])
	}
	// The score carries the span's context so a reader of the raw log can see
	// what was judged without joining back to the work event.
	for _, e := range events {
		if e.Kind == KindScore && e.Head != "h1" {
			t.Errorf("the score event does not name the head it judges: %+v", e)
		}
	}
}

// A waterfall row shows a short prefix, so that is what someone will paste.
func TestAppendScore_ResolvesAUniquePrefix(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())
	span := seedSpan(t, "r1", "h1")

	if err := AppendScore("r1", span[:8], Score{Name: "tests", Value: 1}); err != nil {
		t.Fatal(err)
	}
	events, err := Load("r1")
	if err != nil {
		t.Fatal(err)
	}
	// Stored against the full id, not the prefix, or nothing would find it.
	if len(Scores(events, span)) != 1 {
		t.Fatal("a prefix-addressed score did not attach to the full span id")
	}
}

// A verdict on a span nobody can find is invisible to every reader, so
// accepting it would report success for something that will never be seen.
func TestAppendScore_RefusesAnUnknownSpan(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())
	seedSpan(t, "r1", "h1")

	err := AppendScore("r1", "deadbeefdeadbeef", Score{Name: "tests", Value: 1})
	if !errors.Is(err, ErrNoSuchSpan) {
		t.Fatalf("err = %v, want ErrNoSuchSpan", err)
	}
	events, _ := Load("r1")
	for _, e := range events {
		if e.Kind == KindScore {
			t.Fatal("a refused score was written anyway")
		}
	}
}

// Attaching a verdict to the wrong span is worse than not attaching it, so an
// ambiguous prefix is refused rather than resolved arbitrarily.
func TestAppendScore_RefusesAnAmbiguousPrefix(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())
	l := New("r1")
	for _, id := range []string{"abc1111111111111", "abc2222222222222"} {
		if err := l.Append(Event{Kind: KindDispatchFinished, TaskID: "t1", SpanID: id, Head: "h"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := AppendScore("r1", "abc", Score{Name: "tests", Value: 1}); !errors.Is(err, ErrNoSuchSpan) {
		t.Fatalf("err = %v, want a refusal for an ambiguous prefix", err)
	}
}

// A score with no name cannot be read back meaningfully, and a non-finite value
// breaks every aggregation over it.
func TestAppendScore_RefusesAMeaninglessScore(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())
	span := seedSpan(t, "r1", "h1")

	for name, sc := range map[string]Score{
		"no name":    {Name: "", Value: 1},
		"blank name": {Name: "   ", Value: 1},
		"NaN":        {Name: "tests", Value: math.NaN()},
		"+Inf":       {Name: "tests", Value: math.Inf(1)},
		"-Inf":       {Name: "tests", Value: math.Inf(-1)},
	} {
		if err := AppendScore("r1", span, sc); !errors.Is(err, ErrBadScore) {
			t.Errorf("%s: err = %v, want ErrBadScore", name, err)
		}
	}
}

// Several verdicts on one span must all survive, in order: a test result and a
// lint result are separate facts.
func TestScores_KeepsEveryVerdictInOrder(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())
	span := seedSpan(t, "r1", "h1")
	other := seedSpan(t, "r1", "h2")

	for _, sc := range []Score{{Name: "tests", Value: 1}, {Name: "lint", Value: 0}} {
		if err := AppendScore("r1", span, sc); err != nil {
			t.Fatal(err)
		}
	}
	if err := AppendScore("r1", other, Score{Name: "tests", Value: 1}); err != nil {
		t.Fatal(err)
	}

	events, err := Load("r1")
	if err != nil {
		t.Fatal(err)
	}
	got := Scores(events, span)
	if len(got) != 2 {
		t.Fatalf("got %d scores for the first span, want 2", len(got))
	}
	if got[0].Name != "tests" || got[1].Name != "lint" {
		t.Fatalf("scores are out of append order: %+v", got)
	}
	// A score on one span must not read back against another.
	if len(Scores(events, other)) != 1 {
		t.Fatal("scores leaked between spans")
	}
}

// A score must never be readable as the work it judges, or a verdict would be
// counted as a dispatch.
func TestAppendScore_IsNotItselfScorable(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())
	span := seedSpan(t, "r1", "h1")
	if err := AppendScore("r1", span, Score{Name: "tests", Value: 1}); err != nil {
		t.Fatal(err)
	}
	events, err := Load("r1")
	if err != nil {
		t.Fatal(err)
	}
	// findSpan skips score events, so a second score still resolves to the
	// dispatch rather than to the first score.
	target, ok := findSpan(events, span)
	if !ok {
		t.Fatal("the span became unfindable once it had a score")
	}
	if target.Kind != KindDispatchFinished {
		t.Fatalf("the span resolved to a %q event", target.Kind)
	}
}

// Verdict is the one place that decides what a span's scores add up to, so it
// is tested here rather than only through the renderers that call it.
func TestVerdict(t *testing.T) {
	cases := []struct {
		name          string
		scores        []Score
		passed, known bool
	}{
		{"nothing judged it", nil, false, false},
		{"empty slice is also unjudged", []Score{}, false, false},
		{"one pass", []Score{{Name: "tests", Value: 1}}, true, true},
		{"one fail", []Score{{Name: "tests", Value: 0}}, false, true},
		{"zero is a fail, not a pass", []Score{{Name: "lint", Value: 0}}, false, true},
		{"negative is a fail", []Score{{Name: "lint", Value: -3}}, false, true},
		{"all pass", []Score{{Name: "tests", Value: 1}, {Name: "lint", Value: 0.5}}, true, true},
		// The asymmetry is the point: one failing check must not be hidden by
		// any number of passing ones.
		{"a single fail sinks the span", []Score{{Name: "tests", Value: 1}, {Name: "lint", Value: 0}}, false, true},
		{"order does not matter", []Score{{Name: "lint", Value: 0}, {Name: "tests", Value: 1}}, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			passed, known := Verdict(c.scores)
			if passed != c.passed || known != c.known {
				t.Errorf("Verdict(%v) = (%v, %v), want (%v, %v)", c.scores, passed, known, c.passed, c.known)
			}
		})
	}
}

// An unjudged span is not a passing span. Conflating them would render work
// nobody checked as verified.
func TestVerdict_UnknownIsNotAPass(t *testing.T) {
	if passed, known := Verdict(nil); passed || known {
		t.Errorf("Verdict(nil) = (%v, %v), want (false, false)", passed, known)
	}
}
