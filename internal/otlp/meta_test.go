// SPDX-License-Identifier: MIT

package otlp

import (
	"testing"

	"github.com/ankit373/hydra/internal/cost"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/waterfall"
)

// Meta is an open map, so it holds whatever a writer put there. Every value has
// to land on a typed OTLP AnyValue: a number rendered as a string is not
// queryable in a collector, and an unknown type must degrade rather than panic.
func TestMetaAttrs_MapsEachTypeToItsOwnAnyValue(t *testing.T) {
	got := map[string]Value{}
	for _, kv := range metaAttrs(map[string]any{
		"model_param": "top_p",
		"stream":      true,
		"temperature": 0.2,    // float64, which is how JSON decodes every number
		"retries":     int(3), // an in-process writer, never round-tripped
		"budget":      int64(9),
		"weird":       []string{"a", "b"}, // no AnyValue of its own
	}) {
		got[kv.Key] = kv.Value
	}

	if v := got["hydra.meta.model_param"].StringValue; v == nil || *v != "top_p" {
		t.Errorf("string meta = %v, want top_p", v)
	}
	if v := got["hydra.meta.stream"].BoolValue; v == nil || !*v {
		t.Errorf("bool meta = %v, want true", v)
	}
	if v := got["hydra.meta.temperature"].DoubleValue; v == nil || *v != 0.2 {
		t.Errorf("float meta = %v, want 0.2", v)
	}
	for _, k := range []string{"hydra.meta.retries", "hydra.meta.budget"} {
		if got[k].IntValue == nil {
			t.Errorf("%s is not an intValue; a number rendered as a string cannot be queried", k)
		}
	}
	// A type with no AnyValue must still export, as its printed form.
	if v := got["hydra.meta.weird"].StringValue; v == nil || *v == "" {
		t.Errorf("a meta value of an unmapped type exported as %v, want its printed form", v)
	}
}

func TestMetaAttrs_EmptyMapAddsNothing(t *testing.T) {
	if got := metaAttrs(nil); got != nil {
		t.Errorf("metaAttrs(nil) = %v, want nil: an empty map must add no attributes", got)
	}
}

// The optional spend fields must appear only when the row carries them. An
// empty swarm.mode reads as a swarm that ran, and a zero cost on a row that
// never joined reads as a free dispatch.
func TestSpendAttrs_OnlyWhatTheRowCarries(t *testing.T) {
	if got := spendAttrs(cost.Row{}); got != nil {
		t.Errorf("a row with no span id contributed %d attributes, want none", len(got))
	}

	keys := func(rows ...cost.Row) map[string]bool {
		out := map[string]bool{}
		for _, r := range rows {
			for _, kv := range spendAttrs(r) {
				out[kv.Key] = true
			}
		}
		return out
	}

	plain := keys(cost.Row{SpanID: "2222222222222222", Enum: "STANDARD"})
	for _, k := range []string{"hydra.swarm.mode", "hydra.config.breadcrumb", "hydra.pool"} {
		if plain[k] {
			t.Errorf("a plain row carries %q", k)
		}
	}
	if !plain["hydra.enum"] {
		t.Error("a row with an enum does not carry hydra.enum")
	}

	rich := keys(cost.Row{SpanID: "2222222222222222", Pool: "sonnet",
		SwarmMode: "best", SwarmWinner: true, Config: "abc123"})
	for _, k := range []string{"hydra.swarm.mode", "hydra.swarm.winner", "hydra.config.breadcrumb", "hydra.pool"} {
		if !rich[k] {
			t.Errorf("attribute %q is missing from a swarm row", k)
		}
	}
}

// A span id the run log cannot supply still has to reach the collector as a
// well-formed, non-zero id, or the span is dropped on arrival.
func TestBuild_DerivesAnIDForAnUnusableSpanID(t *testing.T) {
	tr := &waterfall.Trace{RunID: "run-xyz", Roots: []*waterfall.Span{
		{ID: "short", TaskID: "task-abc", Kind: runlog.KindDispatchFinished},
	}}
	p, err := Build([]*waterfall.Trace{tr}, nil, "hydra", "1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	s := p.ResourceSpans[0].ScopeSpans[0].Spans[0]
	if len(s.SpanID) != 16 {
		t.Errorf("spanId %q is %d hex chars, want 16", s.SpanID, len(s.SpanID))
	}
	if !validSpanID(s.SpanID) {
		t.Errorf("spanId %q is not a valid non-zero OTLP id", s.SpanID)
	}
}
