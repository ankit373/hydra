// SPDX-License-Identifier: MIT

package otlp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/cost"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/waterfall"
)

// fallbackRun is a task whose first head errors and whose second succeeds: the
// shape the export flattened before #841. The children carry no ParentSpanID,
// so they also exercise the elision runlog.ParentSpan resolves on read.
func fallbackRun() []runlog.Event {
	task := runlog.SpanIDFor("task-abc")
	return []runlog.Event{
		{V: 2, Seq: 1, TS: "2026-09-01T10:00:00Z", RunID: "run-xyz", TaskID: "task-abc",
			Kind: runlog.KindTaskStarted, SpanID: task},
		{V: 2, Seq: 2, TS: "2026-09-01T10:00:01Z", RunID: "run-xyz", TaskID: "task-abc",
			Kind: runlog.KindDispatchStarted, SpanID: "1111111111111111", Head: "agy/opus", Tier: 2},
		{V: 2, Seq: 3, TS: "2026-09-01T10:00:02Z", RunID: "run-xyz", TaskID: "task-abc",
			Kind: runlog.KindError, SpanID: "1111111111111111", Level: runlog.LevelError, Status: "failed"},
		{V: 2, Seq: 4, TS: "2026-09-01T10:00:03Z", RunID: "run-xyz", TaskID: "task-abc",
			Kind: runlog.KindDispatchStarted, SpanID: "2222222222222222", Head: "ollama/qwen3", Tier: 10},
		{V: 2, Seq: 5, TS: "2026-09-01T10:00:05Z", RunID: "run-xyz", TaskID: "task-abc",
			Kind: runlog.KindDispatchFinished, SpanID: "2222222222222222", Model: "qwen3:4b",
			Status: "ok", InputTokens: 1200, OutputTokens: 340, TTFTMs: 87,
			Meta: map[string]any{"temperature": 0.2, "stream": true}},
		{V: 2, Seq: 6, TS: "2026-09-01T10:00:05Z", RunID: "run-xyz", TaskID: "task-abc",
			Kind: runlog.KindTaskFinished, SpanID: task, Status: "ok"},
	}
}

func spansByID(p Payload) map[string]Span {
	out := map[string]Span{}
	for _, s := range p.ResourceSpans[0].ScopeSpans[0].Spans {
		out[s.SpanID] = s
	}
	return out
}

// The defect this issue exists for: the Span struct had no parent field at all,
// so every span reached a collector as a root and the fallback chain that
// `hyctl trace view` renders as a tree arrived flat.
func TestBuild_CarriesTheParentSpan(t *testing.T) {
	tr := waterfall.Build(fallbackRun())
	p, err := Build([]*waterfall.Trace{tr}, nil, "hydra", "1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	spans := spansByID(p)
	task := runlog.SpanIDFor("task-abc")

	for _, id := range []string{"1111111111111111", "2222222222222222"} {
		s, ok := spans[id]
		if !ok {
			t.Fatalf("span %s is not in the payload at all", id)
		}
		if s.ParentSpanID != task {
			t.Errorf("span %s has parentSpanId %q, want the task span %q: without it a "+
				"collector shows the fallback chain as unrelated roots", id, s.ParentSpanID, task)
		}
	}
	if got := spans[task].ParentSpanID; got != "" {
		t.Errorf("the task span has parentSpanId %q, want empty: it is the root", got)
	}
}

// The parent is elided on the wire when it is the task's own derived span, so
// reading ParentSpanID off the event rather than resolving it would export a
// root for every dispatch and look correct in a log dump.
func TestBuild_ResolvesAnElidedParent(t *testing.T) {
	events := fallbackRun()
	for _, e := range events {
		if e.SpanID == "2222222222222222" && e.ParentSpanID != "" {
			t.Fatalf("fixture writes an explicit parent, so it cannot show elision is handled")
		}
	}
	tr := waterfall.Build(events)
	p, err := Build([]*waterfall.Trace{tr}, nil, "hydra", "1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	if got := spansByID(p)["2222222222222222"].ParentSpanID; got != runlog.SpanIDFor("task-abc") {
		t.Errorf("elided parent resolved to %q, want %q", got, runlog.SpanIDFor("task-abc"))
	}
}

// Found by exporting real logs, not by a fixture: waterfall promotes a span
// whose parent no event declared to a root but leaves ParentID set, so 14 of 74
// spans pointed at a parent that was nowhere in the payload. A collector renders
// that as a broken trace rather than a flat one.
func TestBuild_NeverPointsAtASpanNotInThePayload(t *testing.T) {
	orphan := &waterfall.Trace{RunID: "run-xyz", Roots: []*waterfall.Span{{
		ID: "5555555555555555", ParentID: "6666666666666666", // never declared
		Kind: runlog.KindDispatchFinished,
	}}}
	p, err := Build([]*waterfall.Trace{orphan}, nil, "hydra", "1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	if got := p.ResourceSpans[0].ScopeSpans[0].Spans[0].ParentSpanID; got != "" {
		t.Errorf("parentSpanId = %q, but no span in the payload has that id", got)
	}

	// And the whole-payload invariant, which is what actually broke: every
	// parent named anywhere must be a span the export carries.
	full, err := Build([]*waterfall.Trace{waterfall.Build(fallbackRun()), orphan}, nil, "hydra", "1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	spans := full.ResourceSpans[0].ScopeSpans[0].Spans
	have := map[string]bool{}
	for _, s := range spans {
		have[s.SpanID] = true
	}
	for _, s := range spans {
		if s.ParentSpanID != "" && !have[s.ParentSpanID] {
			t.Errorf("span %s names parent %s, which is not in the payload", s.SpanID, s.ParentSpanID)
		}
	}
}

// A parent id a collector cannot address is worse than no parent: it hangs the
// span off something that does not exist.
func TestBuild_DropsAnUnaddressableParent(t *testing.T) {
	tr := &waterfall.Trace{RunID: "run-xyz", Roots: []*waterfall.Span{
		{ID: "3333333333333333", ParentID: "nothex", Kind: runlog.KindDispatchFinished},
	}}
	p, err := Build([]*waterfall.Trace{tr}, nil, "hydra", "1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	if got := p.ResourceSpans[0].ScopeSpans[0].Spans[0].ParentSpanID; got != "" {
		t.Errorf("parentSpanId = %q for an unaddressable parent, want it dropped", got)
	}
}

// The v2 span fields are the reason the run log is worth exporting over the
// cost log. TTFT is the one with a sentinel: zero means the provider never
// reported it, so exporting it would claim a measurement nobody made.
func TestBuild_CarriesTheV2SpanFields(t *testing.T) {
	tr := waterfall.Build(fallbackRun())
	p, err := Build([]*waterfall.Trace{tr}, nil, "hydra", "1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	spans := spansByID(p)

	got := map[string]Value{}
	for _, a := range spans["2222222222222222"].Attributes {
		got[a.Key] = a.Value
	}
	for _, k := range []string{
		"hydra.ttft_ms", "hydra.level", "hydra.meta.temperature", "hydra.meta.stream",
		"gen_ai.usage.input_tokens", "gen_ai.usage.output_tokens",
	} {
		if _, ok := got[k]; !ok {
			t.Errorf("attribute %q is missing from the span that recorded it", k)
		}
	}
	if v := got["hydra.ttft_ms"].IntValue; v == nil || *v != "87" {
		t.Errorf("hydra.ttft_ms = %v, want 87", v)
	}

	// The failed attempt reported no TTFT, so it must carry none.
	for _, a := range spans["1111111111111111"].Attributes {
		if a.Key == "hydra.ttft_ms" {
			t.Error("a span with no reported TTFT exports one anyway, which reads as 0ms measured")
		}
	}
}

// An error span, and a span whose verdict failed, both have to reach the
// collector as errors or a trace shows a clean run that was not one.
func TestBuild_StatusReflectsErrorAndVerdict(t *testing.T) {
	tr := waterfall.Build(fallbackRun())
	p, err := Build([]*waterfall.Trace{tr}, nil, "hydra", "1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	if got := spansByID(p)["1111111111111111"].Status.Code; got != 2 {
		t.Errorf("an error span exports status %d, want 2", got)
	}

	scored := &waterfall.Trace{RunID: "run-xyz", Roots: []*waterfall.Span{{
		ID: "4444444444444444", Kind: runlog.KindDispatchFinished,
		Scores: []runlog.Score{{Name: "tests", Value: 0}},
	}}}
	p, err = Build([]*waterfall.Trace{scored}, nil, "hydra", "1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	if got := p.ResourceSpans[0].ScopeSpans[0].Spans[0].Status.Code; got != 2 {
		t.Errorf("a span that failed its verdict exports status %d, want 2: a dispatch "+
			"that returned cleanly and then failed its tests is not a success", got)
	}
}

// cost.Row.SpanID is the join between spend and the span that spent it. Without
// it the cost row would export a second, duplicate span for the same work.
func TestBuild_JoinsSpendOntoTheSpanThatSpentIt(t *testing.T) {
	tr := waterfall.Build(fallbackRun())
	row := sampleRow()
	row.SpanID = "2222222222222222"
	row.EstCostUSD = 0.0182

	p, err := Build([]*waterfall.Trace{tr}, []cost.Row{row}, "hydra", "1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	spans := p.ResourceSpans[0].ScopeSpans[0].Spans
	if len(spans) != 3 {
		t.Fatalf("got %d spans, want 3: the cost row must join its span, not add one", len(spans))
	}
	var cost float64
	for _, a := range spansByID(p)["2222222222222222"].Attributes {
		if a.Key == "hydra.cost.est_usd" && a.Value.DoubleValue != nil {
			cost = *a.Value.DoubleValue
		}
	}
	if cost != 0.0182 {
		t.Errorf("hydra.cost.est_usd = %v on the joined span, want 0.0182", cost)
	}
}

// Rows written before span ids existed have no span to join. Dropping them
// would lose spend a collector used to see, so they still export as roots.
func TestBuild_RowWithNoRunLogSpanStillExports(t *testing.T) {
	tr := waterfall.Build(fallbackRun())
	old := sampleRow()
	old.SpanID = "" // pre-#719

	p, err := Build([]*waterfall.Trace{tr}, []cost.Row{old}, "hydra", "1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	spans := p.ResourceSpans[0].ScopeSpans[0].Spans
	if len(spans) != 4 {
		t.Fatalf("got %d spans, want 4: 3 from the run plus the unjoinable row", len(spans))
	}
	for _, s := range spans {
		if s.SpanID == "" || strings.Trim(s.SpanID, "0") == "" {
			t.Errorf("span has an unusable id %q, which a collector drops", s.SpanID)
		}
	}
}

// Meta is an open map, so its iteration order is random. Two exports of one run
// have to be byte-identical or diffing them says nothing.
func TestBuild_IsDeterministic(t *testing.T) {
	first, err := Build([]*waterfall.Trace{waterfall.Build(fallbackRun())}, nil, "hydra", "1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	a, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		next, err := Build([]*waterfall.Trace{waterfall.Build(fallbackRun())}, nil, "hydra", "1.4.0")
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(next)
		if err != nil {
			t.Fatal(err)
		}
		if string(a) != string(b) {
			t.Fatalf("two exports of one run differ on pass %d; Meta ordering is not pinned", i)
		}
	}
}
