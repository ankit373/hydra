// SPDX-License-Identifier: MIT

package waterfall

import (
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/runlog"
)

func at(sec int) string {
	return time.Date(2026, 9, 8, 6, 0, sec, 0, time.UTC).Format(time.RFC3339Nano)
}

// A selection and its outcome share a span id and must fold into one row, not
// two. This is the whole reason spans exist.
func TestBuild_FoldsASelectionAndItsOutcomeIntoOneSpan(t *testing.T) {
	span := "aaaaaaaaaaaaaaaa"
	tr := Build([]runlog.Event{
		{RunID: "r1", TaskID: "t1", Kind: runlog.KindRunStarted, TS: at(0), Detail: "do the thing"},
		{RunID: "r1", TaskID: "t1", Kind: runlog.KindHeadSelected, TS: at(1),
			SpanID: span, Head: "h1", Model: "Model One", Tier: 8, Detail: "candidate 1 of 2"},
		{RunID: "r1", TaskID: "t1", Kind: runlog.KindDispatchFinished, TS: at(5),
			SpanID: span, Head: "h1", Model: "Model One", Tier: 8, Status: "ok",
			InputTokens: 11, OutputTokens: 7, CostUSD: 0.0001, DurationMS: 4000, TTFTMs: 380},
		{RunID: "r1", TaskID: "t1", Kind: runlog.KindRunFinished, TS: at(6)},
	})

	if tr.Detail != "do the thing" {
		t.Errorf("run detail = %q, want the run_started description", tr.Detail)
	}
	spans := tr.Flatten()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1 folded span", len(spans))
	}
	s := spans[0]
	// The closing kind describes the span better than the opening one.
	if s.Kind != runlog.KindDispatchFinished {
		t.Errorf("span kind = %q, want the finishing kind", s.Kind)
	}
	if s.Status != "ok" || s.InputTokens != 11 || s.OutputTokens != 7 || s.TTFTMs != 380 {
		t.Errorf("the outcome's fields did not fold in: %+v", s)
	}
	// Fields only the selection carried must survive the fold.
	if s.Detail != "candidate 1 of 2" {
		t.Errorf("span detail = %q, want the selection's", s.Detail)
	}
	if got := s.Elapsed(); got != 4*time.Second {
		t.Errorf("elapsed = %v, want 4s from first to last event", got)
	}
	// End-Start is not DurationMS: the span also covers the policy check.
	if s.DurationMS != 4000 {
		t.Errorf("reported duration = %d, want the executor's own number", s.DurationMS)
	}
}

// Two attempts on one head are distinct spans. A supervision tree collapses
// them by design; a waterfall must not.
func TestBuild_KeepsTwoAttemptsOnOneHeadApart(t *testing.T) {
	tr := Build([]runlog.Event{
		{RunID: "r1", TaskID: "t1", Kind: runlog.KindHeadSelected, TS: at(1), SpanID: "1111111111111111", Head: "h1"},
		{RunID: "r1", TaskID: "t1", Kind: runlog.KindError, TS: at(2), SpanID: "1111111111111111", Head: "h1", Status: "failed"},
		{RunID: "r1", TaskID: "t1", Kind: runlog.KindHeadSelected, TS: at(3), SpanID: "2222222222222222", Head: "h1"},
		{RunID: "r1", TaskID: "t1", Kind: runlog.KindDispatchFinished, TS: at(4), SpanID: "2222222222222222", Head: "h1", Status: "ok"},
	})
	spans := tr.Flatten()
	if len(spans) != 2 {
		t.Fatalf("got %d spans, want 2: retrying one head is two attempts", len(spans))
	}
	if spans[0].Level != runlog.LevelError {
		t.Errorf("the failed attempt reads as %q", spans[0].Level)
	}
	if spans[1].Status != "ok" {
		t.Errorf("the second attempt reads as %q", spans[1].Status)
	}
}

// A swarm nests: one fan-out span with the attempts under it.
func TestBuild_NestsChildrenUnderTheirParent(t *testing.T) {
	root := runlog.SpanIDFor("t1/swarm")
	tr := Build([]runlog.Event{
		{RunID: "r1", TaskID: "t1", Kind: runlog.KindTaskStarted, TS: at(1),
			SpanID: root, ParentSpanID: runlog.SpanIDFor("t1"), Agent: "swarm"},
		{RunID: "r1", TaskID: "t1", Kind: runlog.KindAttempt, TS: at(2),
			SpanID: runlog.SpanIDFor("t1/swarm/a"), ParentSpanID: root, Head: "a", Status: "ok"},
		{RunID: "r1", TaskID: "t1", Kind: runlog.KindAttempt, TS: at(3),
			SpanID: runlog.SpanIDFor("t1/swarm/b"), ParentSpanID: root, Head: "b", Status: "ok"},
	})
	if len(tr.Roots) != 1 {
		t.Fatalf("got %d roots, want 1 fan-out span", len(tr.Roots))
	}
	if got := len(tr.Roots[0].Children); got != 2 {
		t.Fatalf("the fan-out has %d children, want 2", got)
	}
	for _, c := range tr.Roots[0].Children {
		if c.Depth != 1 {
			t.Errorf("child %s has depth %d, want 1", c.Head, c.Depth)
		}
	}
	// Depth-first is the order a waterfall prints.
	flat := tr.Flatten()
	if len(flat) != 3 || flat[0] != tr.Roots[0] {
		t.Fatalf("Flatten is not depth-first from the root: %d spans", len(flat))
	}
}

// A v1 run carries no span ids. It must still read as a fallback chain rather
// than as one row per event, or every run recorded before #719 renders wrong.
func TestBuild_GroupsV1EventsWithNoSpanIDs(t *testing.T) {
	tr := Build([]runlog.Event{
		{V: 1, RunID: "r1", TaskID: "t1", Kind: runlog.KindRunStarted, TS: at(0), Detail: "hi"},
		{V: 1, RunID: "r1", TaskID: "t1", Kind: runlog.KindHeadSelected, TS: at(1), Head: "h1", Tier: 2},
		{V: 1, RunID: "r1", TaskID: "t1", Kind: runlog.KindError, TS: at(1), Head: "h1", Tier: 2, Status: "failed"},
		{V: 1, RunID: "r1", TaskID: "t1", Kind: runlog.KindHeadSelected, TS: at(2), Head: "h2", Tier: 10},
		{V: 1, RunID: "r1", TaskID: "t1", Kind: runlog.KindDispatchFinished, TS: at(7), Head: "h2", Tier: 10, Status: "ok"},
	})
	spans := tr.Flatten()
	if len(spans) != 2 {
		t.Fatalf("got %d spans from a v1 run, want 2 (one per head)", len(spans))
	}
	if spans[0].Head != "h1" || spans[0].Level != runlog.LevelError {
		t.Errorf("first span = %+v, want the failed h1", spans[0])
	}
	if spans[1].Head != "h2" || spans[1].Status != "ok" {
		t.Errorf("second span = %+v, want the successful h2", spans[1])
	}
	if tr.Unspanned != 0 {
		t.Errorf("%d events were dropped from a v1 run", tr.Unspanned)
	}
}

// A parent naming a span no event declared must become a root. Materialising it
// would print a row with no head, no timing and no explanation.
func TestBuild_DoesNotInventAMissingParent(t *testing.T) {
	tr := Build([]runlog.Event{
		{RunID: "r1", TaskID: "t1", Kind: runlog.KindAttempt, TS: at(1),
			SpanID: "1111111111111111", ParentSpanID: "9999999999999999", Head: "a"},
	})
	if len(tr.Roots) != 1 {
		t.Fatalf("got %d roots, want the orphan promoted to one", len(tr.Roots))
	}
	if tr.Roots[0].Head != "a" {
		t.Fatalf("the root is not the real span: %+v", tr.Roots[0])
	}
	for _, s := range tr.Flatten() {
		if s.Head == "" && s.Kind == "" {
			t.Fatal("a phantom span was materialised for the missing parent")
		}
	}
}

// An event with no span and no identity to derive one from is counted, not
// silently dropped: a partial trace rendered as a whole one is the failure this
// package exists to avoid.
func TestBuild_CountsEventsItCannotPlace(t *testing.T) {
	tr := Build([]runlog.Event{
		{RunID: "r1", Kind: runlog.KindError, TS: at(1)}, // no span, no task, no head
	})
	if tr.Unspanned != 1 {
		t.Fatalf("Unspanned = %d, want 1", tr.Unspanned)
	}
	if len(tr.Flatten()) != 0 {
		t.Fatal("an unplaceable event was rendered as a span")
	}
}

// Run-level events describe the invocation, not work inside it.
func TestBuild_RunLevelEventsAreNotSpans(t *testing.T) {
	tr := Build([]runlog.Event{
		{RunID: "r1", TaskID: "t1", Kind: runlog.KindRunStarted, TS: at(0), Detail: "the ask"},
		{RunID: "r1", TaskID: "t1", Kind: runlog.KindRunFinished, TS: at(9)},
	})
	if len(tr.Flatten()) != 0 {
		t.Fatalf("run-level events produced %d spans", len(tr.Flatten()))
	}
	if tr.Detail != "the ask" {
		t.Errorf("run detail = %q", tr.Detail)
	}
	if tr.Unspanned != 0 {
		t.Errorf("run-level events were counted as unplaceable: %d", tr.Unspanned)
	}
}

func TestFind_MatchesAnIDOrAUniquePrefix(t *testing.T) {
	tr := Build([]runlog.Event{
		{RunID: "r1", TaskID: "t1", Kind: runlog.KindAttempt, TS: at(1), SpanID: "abc1111111111111", Head: "a"},
		{RunID: "r1", TaskID: "t1", Kind: runlog.KindAttempt, TS: at(2), SpanID: "abc2222222222222", Head: "b"},
		{RunID: "r1", TaskID: "t1", Kind: runlog.KindAttempt, TS: at(3), SpanID: "def3333333333333", Head: "c"},
	})
	if s := tr.Find("abc1111111111111"); s == nil || s.Head != "a" {
		t.Error("an exact id did not match")
	}
	if s := tr.Find("def"); s == nil || s.Head != "c" {
		t.Error("a unique prefix did not match")
	}
	// Ambiguity must return nothing rather than an arbitrary one of the two.
	if s := tr.Find("abc"); s != nil {
		t.Errorf("an ambiguous prefix matched %s", s.Head)
	}
	if s := tr.Find("zzz"); s != nil {
		t.Error("an unknown prefix matched something")
	}
}

func TestTotals_SumsTheWholeTree(t *testing.T) {
	root := runlog.SpanIDFor("t1/swarm")
	tr := Build([]runlog.Event{
		{RunID: "r1", TaskID: "t1", Kind: runlog.KindTaskStarted, TS: at(1), SpanID: root, Agent: "swarm"},
		{RunID: "r1", TaskID: "t1", Kind: runlog.KindAttempt, TS: at(2), SpanID: "1111111111111111",
			ParentSpanID: root, InputTokens: 10, OutputTokens: 3, CostUSD: 0.01},
		{RunID: "r1", TaskID: "t1", Kind: runlog.KindAttempt, TS: at(3), SpanID: "2222222222222222",
			ParentSpanID: root, InputTokens: 20, OutputTokens: 7, CostUSD: 0.02},
	})
	cost, in, out := tr.Totals()
	if in != 30 || out != 10 {
		t.Errorf("tokens = %d/%d, want 30/10 summed across children", in, out)
	}
	if cost < 0.0299 || cost > 0.0301 {
		t.Errorf("cost = %v, want 0.03", cost)
	}
}

// A span with no end, or an end before its start, has no meaningful extent. It
// must read as zero rather than as a negative bar the renderer would clamp
// somewhere arbitrary.
func TestElapsed_IsZeroRatherThanNegative(t *testing.T) {
	now := time.Now()
	for name, s := range map[string]*Span{
		"no end":           {Start: now},
		"end before start": {Start: now, End: now.Add(-time.Second)},
	} {
		if got := s.Elapsed(); got != 0 {
			t.Errorf("%s: elapsed = %v, want 0", name, got)
		}
	}
}

// An empty run must not panic. A live viewer is pointed at whatever is newest,
// including a run that logged nothing.
func TestBuild_HandlesNoEvents(t *testing.T) {
	tr := Build(nil)
	if tr == nil {
		t.Fatal("Build returned nil")
	}
	if len(tr.Flatten()) != 0 || tr.RunID != "" {
		t.Fatalf("an empty run produced %+v", tr)
	}
	cost, in, out := tr.Totals()
	if cost != 0 || in != 0 || out != 0 {
		t.Error("an empty run has non-zero totals")
	}
}
