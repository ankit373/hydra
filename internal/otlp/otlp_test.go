// SPDX-License-Identifier: MIT

package otlp

import (
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/cost"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/waterfall"
)

func sampleRow() cost.Row {
	return cost.Row{
		TS: "2026-09-01T10:00:00Z", Tier: 3, Enum: "STANDARD",
		Model: "claude-sonnet-5", Executor: "agy", Pool: "sonnet",
		PromptTokens: 1200, ResponseTokens: 340, EstCostUSD: 0.0182,
		WallMS: 1500, TokensSource: "actual", TaskID: "task-abc", RunID: "run-xyz",
		ActProb: 0.84, KeepProb: 1,
	}
}

// A collector rejects a span whose ids are the wrong length or all zero, and
// drops it silently. Getting these right is the difference between an export
// that works and one that reports success into a void.
func TestBuild_ProducesValidSpanIdentity(t *testing.T) {
	p, err := Build(nil, []cost.Row{sampleRow()}, "hydra", "1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	spans := p.ResourceSpans[0].ScopeSpans[0].Spans
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	s := spans[0]
	if len(s.TraceID) != 32 {
		t.Errorf("traceId is %d hex chars, want 32 (16 bytes)", len(s.TraceID))
	}
	if len(s.SpanID) != 16 {
		t.Errorf("spanId is %d hex chars, want 16 (8 bytes)", len(s.SpanID))
	}
	for _, id := range []string{s.TraceID, s.SpanID} {
		if _, err := hex.DecodeString(id); err != nil {
			t.Errorf("id %q is not hex: %v", id, err)
		}
		if strings.Trim(id, "0") == "" {
			t.Errorf("id %q is all zeroes, which collectors drop", id)
		}
	}
}

// A row with no ids must still export. An unlinked span is data; a dropped one
// is not, and an all-zero id is dropped.
func TestBuild_RowWithNoIdsStillGetsUsableIdentity(t *testing.T) {
	r := sampleRow()
	r.RunID, r.TaskID = "", ""
	p, err := Build(nil, []cost.Row{r}, "hydra", "1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	s := p.ResourceSpans[0].ScopeSpans[0].Spans[0]
	if strings.Trim(s.TraceID, "0") == "" || strings.Trim(s.SpanID, "0") == "" {
		t.Errorf("ids are all zeroes for an id-less row: %q / %q", s.TraceID, s.SpanID)
	}
}

// Spans from one run must share a trace id, or a run cannot be reassembled.
func TestBuild_SpansFromOneRunShareATraceID(t *testing.T) {
	a, b := sampleRow(), sampleRow()
	b.TaskID = "task-def"
	p, err := Build(nil, []cost.Row{a, b}, "hydra", "1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	spans := p.ResourceSpans[0].ScopeSpans[0].Spans
	if spans[0].TraceID != spans[1].TraceID {
		t.Errorf("two rows of run %q got trace ids %q and %q", a.RunID, spans[0].TraceID, spans[1].TraceID)
	}
	if spans[0].SpanID == spans[1].SpanID {
		t.Error("two different tasks share a span id")
	}
}

// OTLP/JSON encodes 64-bit values as strings. A JSON number loses precision
// above 2^53, and unix nanos passed that in 1970, so a numeric timestamp is
// silently wrong, not rejected.
func TestMarshal_EncodesSixtyFourBitValuesAsStrings(t *testing.T) {
	p, err := Build(nil, []cost.Row{sampleRow()}, "hydra", "1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	body, err := Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]any
	if err := json.Unmarshal(body, &generic); err != nil {
		t.Fatalf("the payload is not valid JSON: %v", err)
	}
	span := generic["resourceSpans"].([]any)[0].(map[string]any)["scopeSpans"].([]any)[0].(map[string]any)["spans"].([]any)[0].(map[string]any)

	for _, k := range []string{"startTimeUnixNano", "endTimeUnixNano"} {
		v, ok := span[k].(string)
		if !ok {
			t.Errorf("%s is %T, want a string, a JSON number loses nanosecond precision", k, span[k])
			continue
		}
		if _, err := strconv.ParseInt(v, 10, 64); err != nil {
			t.Errorf("%s = %q, not an integer", k, v)
		}
	}
	// Token counts are intValue, which is also a string in OTLP/JSON.
	for _, attr := range span["attributes"].([]any) {
		a := attr.(map[string]any)
		if a["key"] == "gen_ai.usage.input_tokens" {
			val := a["value"].(map[string]any)
			if _, ok := val["intValue"].(string); !ok {
				t.Errorf("intValue is %T, want a string", val["intValue"])
			}
		}
	}
}

func TestBuild_SpanCoversTheDispatchDuration(t *testing.T) {
	p, err := Build(nil, []cost.Row{sampleRow()}, "hydra", "1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	s := p.ResourceSpans[0].ScopeSpans[0].Spans[0]
	start, _ := strconv.ParseInt(s.StartTimeUnixNano, 10, 64)
	end, _ := strconv.ParseInt(s.EndTimeUnixNano, 10, 64)
	if got := time.Duration(end - start); got != 1500*time.Millisecond {
		t.Errorf("span duration = %v, want 1.5s from wall_ms", got)
	}
	want, _ := time.Parse(time.RFC3339, "2026-09-01T10:00:00Z")
	if start != want.UnixNano() {
		t.Errorf("start = %d, want %d", start, want.UnixNano())
	}
}

// The fields worth exporting are the ones OTel has no place for. Dropping them
// would leave a trace that says less than the log it came from.
func TestBuild_CarriesHydrasOwnFieldsAlongsideGenAI(t *testing.T) {
	p, err := Build(nil, []cost.Row{sampleRow()}, "hydra", "1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, a := range p.ResourceSpans[0].ScopeSpans[0].Spans[0].Attributes {
		got[a.Key] = true
	}
	for _, k := range []string{
		"gen_ai.system", "gen_ai.request.model", "gen_ai.usage.input_tokens", "gen_ai.usage.output_tokens",
		"hydra.tier", "hydra.enum", "hydra.cost.est_usd", "hydra.routing.act_prob", "hydra.routing.keep_prob",
	} {
		if !got[k] {
			t.Errorf("attribute %q is missing from the span", k)
		}
	}
}

// The first pick goes out beside act_prob, which is its probability. A row
// from before it was logged exports none, not a step of 0.
func TestBuild_CarriesTheFirstPickOnlyWhenLogged(t *testing.T) {
	logged := sampleRow()
	logged.SpanID, logged.FirstHead, logged.FallbackStep = "0102030405060708", "cheap", 2
	old := sampleRow()
	old.SpanID = "0102030405060709"
	p, err := Build(nil, []cost.Row{logged, old}, "hydra", "1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	spans := p.ResourceSpans[0].ScopeSpans[0].Spans
	for i, attrs := range [][]KeyValue{spans[0].Attributes, spendAttrs(logged), spans[1].Attributes, spendAttrs(old)} {
		got := map[string]Value{}
		for _, a := range attrs {
			got[a.Key] = a.Value
		}
		head, hasHead := got["hydra.routing.first_head"]
		step, hasStep := got["hydra.routing.fallback_step"]
		if i >= 2 {
			if hasHead || hasStep {
				t.Errorf("attrs %d: an old row exported a first pick", i)
			}
			continue
		}
		if !hasHead || head.StringValue == nil || *head.StringValue != "cheap" {
			t.Errorf("attrs %d: first_head %+v, want cheap", i, head)
		}
		if !hasStep || step.IntValue == nil || *step.IntValue != "2" {
			t.Errorf("attrs %d: fallback_step %+v, want 2", i, step)
		}
	}
}

// A row whose timestamp will not parse cannot be placed on a timeline.
// Defaulting to now would file a months-old dispatch under today.
func TestBuild_RefusesAnUnparseableTimestamp(t *testing.T) {
	r := sampleRow()
	r.TS = "not a timestamp"
	if _, err := Build(nil, []cost.Row{r}, "hydra", "1.4.0"); err == nil {
		t.Error("Build accepted a row with an unparseable timestamp")
	}
}

func TestBuild_EmptyLogProducesAWellFormedEmptyPayload(t *testing.T) {
	p, err := Build(nil, nil, "hydra", "1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	body, err := Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var check Payload
	if err := json.Unmarshal(body, &check); err != nil {
		t.Fatalf("empty payload does not round-trip: %v", err)
	}
	if len(check.ResourceSpans) != 1 {
		t.Errorf("got %d resourceSpans, want 1 even with no spans", len(check.ResourceSpans))
	}
}

func TestBuild_SchemaURLIsPinned(t *testing.T) {
	p, err := Build(nil, []cost.Row{sampleRow()}, "hydra", "1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	if p.ResourceSpans[0].SchemaURL != SchemaURL {
		t.Errorf("schemaUrl = %q, want %q", p.ResourceSpans[0].SchemaURL, SchemaURL)
	}
}

// Swarm and breadcrumb fields are optional on a row, so they must appear only
// when set, an empty swarm.mode attribute would read as a swarm that ran.
func TestBuild_OptionalFieldsAppearOnlyWhenPresent(t *testing.T) {
	plain, err := Build(nil, []cost.Row{sampleRow()}, "hydra", "1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	if keysOf(plain)["hydra.swarm.mode"] {
		t.Error("a non-swarm row carries a swarm.mode attribute")
	}
	if keysOf(plain)["hydra.config.breadcrumb"] {
		t.Error("a row with no breadcrumb carries one")
	}

	r := sampleRow()
	r.SwarmMode, r.SwarmWinner, r.Config = "best", true, "abc123"
	rich, err := Build(nil, []cost.Row{r}, "hydra", "1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"hydra.swarm.mode", "hydra.swarm.winner", "hydra.config.breadcrumb"} {
		if !keysOf(rich)[k] {
			t.Errorf("attribute %q is missing from a swarm row", k)
		}
	}
}

// A span with no name is hard to find in any UI; a row with no model still
// needs to say what it was.
func TestBuild_NamesASpanWithNoModel(t *testing.T) {
	r := sampleRow()
	r.Model = ""
	p, err := Build(nil, []cost.Row{r}, "hydra", "1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	if got := p.ResourceSpans[0].ScopeSpans[0].Spans[0].Name; got != "dispatch" {
		t.Errorf("span name = %q, want %q", got, "dispatch")
	}
}

func keysOf(p Payload) map[string]bool {
	out := map[string]bool{}
	for _, a := range p.ResourceSpans[0].ScopeSpans[0].Spans[0].Attributes {
		out[a.Key] = true
	}
	return out
}

// A row that carries a recorded span id must export under it, or the OTel trace
// and Hydra's own run log name the same work with two different identities.
func TestSpanFor_UsesTheRecordedSpanID(t *testing.T) {
	const recorded = "a1b2c3d4e5f60718"
	span, err := spanFor(cost.Row{
		TS: "2026-09-07T19:08:23Z", RunID: "r1", TaskID: "t1",
		SpanID: recorded, Model: "m", WallMS: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if span.SpanID != recorded {
		t.Fatalf("spanId = %q, want the recorded %q", span.SpanID, recorded)
	}
}

// A malformed or all-zero id must fall back rather than export a span the
// collector silently drops.
func TestSpanFor_FallsBackOnAnUnusableSpanID(t *testing.T) {
	for _, bad := range []string{"", "short", "zzzzzzzzzzzzzzzz", "0000000000000000"} {
		t.Run(bad, func(t *testing.T) {
			span, err := spanFor(cost.Row{
				TS: "2026-09-07T19:08:23Z", RunID: "r1", TaskID: "t1",
				SpanID: bad, Model: "m", WallMS: 100,
			})
			if err != nil {
				t.Fatal(err)
			}
			if span.SpanID == bad {
				t.Fatalf("exported the unusable id %q instead of falling back", bad)
			}
			if len(span.SpanID) != 16 {
				t.Fatalf("fallback span id %q is not 8 bytes of hex", span.SpanID)
			}
		})
	}
}

func TestExport_CacheTokenAttributes(t *testing.T) {
	attrs := func(p Payload) map[string]string {
		out := map[string]string{}
		for _, kv := range p.ResourceSpans[0].ScopeSpans[0].Spans[0].Attributes {
			if kv.Value.IntValue != nil {
				out[kv.Key] = *kv.Value.IntValue
			}
		}
		return out
	}
	const read, write = "gen_ai.usage.cache_read.input_tokens", "gen_ai.usage.cache_creation.input_tokens"

	r := sampleRow()
	r.CacheReadTokens, r.CacheWriteTokens = 900, 100
	p, err := Build(nil, []cost.Row{r}, "hydra", "1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	if a := attrs(p); a[read] != "900" || a[write] != "100" {
		t.Errorf("cost-row span cache attrs = %q/%q, want 900/100", a[read], a[write])
	}

	tr := &waterfall.Trace{RunID: "run-xyz", Roots: []*waterfall.Span{{
		ID: "0102030405060708", TaskID: "task-abc", Kind: runlog.KindDispatchFinished,
		Start: time.Unix(1, 0), End: time.Unix(2, 0), InputTokens: 1000, CacheReadTokens: 900,
	}}}
	p, err = Build([]*waterfall.Trace{tr}, nil, "hydra", "1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	a := attrs(p)
	if a[read] != "900" {
		t.Errorf("run-log span cache read = %q, want 900", a[read])
	}
	if _, ok := a[write]; ok {
		t.Error("a span with no cache writes exports a write count")
	}
}
