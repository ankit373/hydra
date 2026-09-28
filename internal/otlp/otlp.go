// SPDX-License-Identifier: MIT

// Package otlp renders Hydra's dispatch log as OpenTelemetry spans.
//
// Export is a bridge, not a migration. Hydra's own schema stays authoritative:
// every `gen_ai.*` attribute is still Development-stage in the OTel semantic
// conventions, and tier, enum, confidence, blast radius and routing propensity
// have no OTel equivalent at all. So the gen_ai fields are populated where they
// genuinely correspond and everything else is carried under `hydra.*` rather
// than forced into a shape that loses it.
//
// OTLP/HTTP with a JSON body, because that is what the collectors people
// actually run accept, Langfuse ingests at /api/public/otel/v1/traces and
// offers no gRPC endpoint at all.
package otlp

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/ankit373/hydra/internal/cost"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/waterfall"
)

// SchemaURL pins the semantic-convention version these attribute names follow.
const SchemaURL = "https://opentelemetry.io/schemas/1.27.0"

// Payload is an OTLP/HTTP ExportTraceServiceRequest in its JSON encoding.
type Payload struct {
	ResourceSpans []ResourceSpans `json:"resourceSpans"`
}

type ResourceSpans struct {
	Resource   Resource     `json:"resource"`
	ScopeSpans []ScopeSpans `json:"scopeSpans"`
	SchemaURL  string       `json:"schemaUrl,omitempty"`
}

type Resource struct {
	Attributes []KeyValue `json:"attributes"`
}

type ScopeSpans struct {
	Scope Scope  `json:"scope"`
	Spans []Span `json:"spans"`
}

type Scope struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// Span is one dispatch. Times are strings because OTLP/JSON encodes 64-bit
// values as strings, a JSON number loses precision above 2^53, and unix nanos
// passed that in 1970.
type Span struct {
	TraceID string `json:"traceId"`
	SpanID  string `json:"spanId"`
	// ParentSpanID is omitted on a root. An empty string is a valid encoding
	// of "no parent"; an invalid one makes a collector drop the nesting.
	ParentSpanID      string     `json:"parentSpanId,omitempty"`
	Name              string     `json:"name"`
	Kind              int        `json:"kind"`
	StartTimeUnixNano string     `json:"startTimeUnixNano"`
	EndTimeUnixNano   string     `json:"endTimeUnixNano"`
	Attributes        []KeyValue `json:"attributes"`
	Status            Status     `json:"status"`
}

type Status struct {
	Code int `json:"code"` // 0 unset, 1 ok, 2 error
}

type KeyValue struct {
	Key   string `json:"key"`
	Value Value  `json:"value"`
}

// Value is an OTLP AnyValue. Exactly one field is set.
type Value struct {
	StringValue *string  `json:"stringValue,omitempty"`
	IntValue    *string  `json:"intValue,omitempty"` // string, per OTLP/JSON
	DoubleValue *float64 `json:"doubleValue,omitempty"`
	BoolValue   *bool    `json:"boolValue,omitempty"`
}

func str(k, v string) KeyValue {
	return KeyValue{Key: k, Value: Value{StringValue: &v}}
}

func num(k string, v int64) KeyValue {
	s := strconv.FormatInt(v, 10)
	return KeyValue{Key: k, Value: Value{IntValue: &s}}
}

func flt(k string, v float64) KeyValue {
	return KeyValue{Key: k, Value: Value{DoubleValue: &v}}
}

// Build renders runs and dispatch rows as a single OTLP payload.
//
// Spans come from the run log, so a fallback chain reaches a collector as the
// tree `hyctl trace view` shows rather than as a flat list. Cost rows attach
// spend to the span that spent it, which is what cost.Row.SpanID is for.
//
// serviceName names the resource; version is stamped so a collector can tell
// which Hydra produced a span.
func Build(traces []*waterfall.Trace, rows []cost.Row, serviceName, version string) (Payload, error) {
	spend := map[string]cost.Row{}
	for _, r := range rows {
		if validSpanID(r.SpanID) {
			spend[r.SpanID] = r
		}
	}

	var spans []Span
	claimed := map[string]bool{}
	for _, t := range traces {
		traceID, err := idHex(16, t.RunID)
		if err != nil {
			return Payload{}, err
		}
		// Two passes: a parent has to be resolved against the ids this payload
		// actually carries. waterfall promotes a span whose parent no event
		// declared to a root but keeps ParentID set, so emitting it unchecked
		// points a collector at a span that is not in the export.
		flat := flatten(t.Roots)
		emitted := make(map[string]string, len(flat))
		for _, s := range flat {
			id, err := otlpSpanID(s)
			if err != nil {
				return Payload{}, err
			}
			emitted[s.ID] = id
		}
		for _, s := range flat {
			span, err := spanForWaterfall(s, traceID, emitted, spend[s.ID])
			if err != nil {
				return Payload{}, err
			}
			claimed[s.ID] = true
			spans = append(spans, span)
		}
	}

	// A row naming no span the run log holds still exports, as a root. These
	// are rows written before span ids existed; dropping them would lose spend
	// a collector used to see, which is a worse trade than a flat span.
	for _, r := range rows {
		if r.SpanID != "" && claimed[r.SpanID] {
			continue
		}
		span, err := spanFor(r)
		if err != nil {
			return Payload{}, err
		}
		spans = append(spans, span)
	}

	return Payload{ResourceSpans: []ResourceSpans{{
		Resource: Resource{Attributes: []KeyValue{
			str("service.name", serviceName),
			str("service.version", version),
		}},
		ScopeSpans: []ScopeSpans{{
			Scope: Scope{Name: "github.com/ankit373/hydra", Version: version},
			Spans: spans,
		}},
		SchemaURL: SchemaURL,
	}}}, nil
}

// flatten walks the span tree depth-first. Order is the tree's, so a parent is
// always emitted before its children.
func flatten(roots []*waterfall.Span) []*waterfall.Span {
	var out []*waterfall.Span
	var walk func([]*waterfall.Span)
	walk = func(ss []*waterfall.Span) {
		for _, s := range ss {
			out = append(out, s)
			walk(s.Children)
		}
	}
	walk(roots)
	return out
}

// otlpSpanID is the id this span exports under. A run-log span id is already an
// OTLP one; anything else is derived, since an unusable id is a dropped span.
func otlpSpanID(s *waterfall.Span) (string, error) {
	if validSpanID(s.ID) {
		return s.ID, nil
	}
	return idHex(8, s.ID+s.TaskID)
}

// emitted maps each waterfall span id to the id it exports under, so a parent
// resolves only if it is in this payload. spendRow is the cost row that named
// this span, zero if none did.
func spanForWaterfall(s *waterfall.Span, traceID string, emitted map[string]string, spendRow cost.Row) (Span, error) {
	spanID, err := otlpSpanID(s)
	if err != nil {
		return Span{}, err
	}
	// A parent the export does not carry is no parent. Pointing a collector at
	// a span that is not in the payload is worse than the root it would
	// otherwise be: the trace renders as broken rather than as flat.
	parent := emitted[s.ParentID]
	if !validSpanID(parent) {
		parent = ""
	}

	attrs := []KeyValue{
		str("gen_ai.operation.name", "chat"),
		str("hydra.span.kind", string(s.Kind)),
		str("hydra.level", string(s.Level)),
	}
	if s.Model != "" {
		attrs = append(attrs, str("gen_ai.request.model", s.Model))
	}
	if s.Head != "" {
		attrs = append(attrs, str("gen_ai.system", s.Head), str("hydra.head", s.Head))
	}
	if s.InputTokens > 0 {
		attrs = append(attrs, num("gen_ai.usage.input_tokens", int64(s.InputTokens)))
	}
	if s.OutputTokens > 0 {
		attrs = append(attrs, num("gen_ai.usage.output_tokens", int64(s.OutputTokens)))
	}
	// Zero means the provider never reported it, not instant, so it is absent
	// rather than exported as a measured zero.
	if s.TTFTMs > 0 {
		attrs = append(attrs, num("hydra.ttft_ms", s.TTFTMs))
	}
	if s.Tier > 0 {
		attrs = append(attrs, num("hydra.tier", int64(s.Tier)))
	}
	if s.Status != "" {
		attrs = append(attrs, str("hydra.status", s.Status))
	}
	if s.Confidence > 0 {
		attrs = append(attrs, flt("hydra.confidence", s.Confidence))
	}
	if s.CostUSD > 0 {
		attrs = append(attrs, flt("hydra.cost.est_usd", s.CostUSD))
	}
	if s.InputRef != "" {
		attrs = append(attrs, str("hydra.payload.input_ref", s.InputRef))
	}
	if s.OutputRef != "" {
		attrs = append(attrs, str("hydra.payload.output_ref", s.OutputRef))
	}
	attrs = append(attrs, metaAttrs(s.Meta)...)
	attrs = append(attrs, spendAttrs(spendRow)...)

	name := string(s.Kind)
	if s.Model != "" {
		name += " " + s.Model
	}
	// A verdict is what the run concluded, and outranks the span's own level:
	// a dispatch that returned cleanly and then failed its tests is an error.
	code := 1
	if s.Level == runlog.LevelError {
		code = 2
	}
	if passed, known := s.Verdict(); known && !passed {
		code = 2
	}
	return Span{
		TraceID:           traceID,
		SpanID:            spanID,
		ParentSpanID:      parent,
		Name:              name,
		Kind:              3, // SPAN_KIND_CLIENT
		StartTimeUnixNano: strconv.FormatInt(s.Start.UnixNano(), 10),
		EndTimeUnixNano:   strconv.FormatInt(s.End.UnixNano(), 10),
		Attributes:        attrs,
		Status:            Status{Code: code},
	}, nil
}

// metaAttrs renders the open Meta map under hydra.meta.*. Keys are sorted so
// one run exports byte-identically twice, which is what makes a diff of two
// exports mean something.
func metaAttrs(meta map[string]any) []KeyValue {
	if len(meta) == 0 {
		return nil
	}
	keys := make([]string, 0, len(meta))
	for k := range meta {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]KeyValue, 0, len(keys))
	for _, k := range keys {
		switch v := meta[k].(type) {
		case string:
			out = append(out, str("hydra.meta."+k, v))
		case bool:
			b := v
			out = append(out, KeyValue{Key: "hydra.meta." + k, Value: Value{BoolValue: &b}})
		case float64: // every JSON number decodes as float64
			out = append(out, flt("hydra.meta."+k, v))
		case int:
			out = append(out, num("hydra.meta."+k, int64(v)))
		case int64:
			out = append(out, num("hydra.meta."+k, v))
		default:
			out = append(out, str("hydra.meta."+k, fmt.Sprint(v)))
		}
	}
	return out
}

// spendAttrs carries what only the cost log knows. A zero row contributes
// nothing rather than a row of zeroes that reads as a free dispatch.
func spendAttrs(r cost.Row) []KeyValue {
	if r.SpanID == "" {
		return nil
	}
	attrs := []KeyValue{
		flt("hydra.cost.est_usd", r.EstCostUSD),
		str("hydra.cost.tokens_source", r.TokensSource),
		flt("hydra.routing.act_prob", r.ActProb),
		flt("hydra.routing.keep_prob", r.KeepProb),
	}
	if r.Enum != "" {
		attrs = append(attrs, str("hydra.enum", r.Enum))
	}
	if r.Pool != "" {
		attrs = append(attrs, str("hydra.pool", r.Pool))
	}
	if r.SwarmMode != "" {
		winner := r.SwarmWinner
		attrs = append(attrs, str("hydra.swarm.mode", r.SwarmMode),
			KeyValue{Key: "hydra.swarm.winner", Value: Value{BoolValue: &winner}})
	}
	if r.Config != "" {
		attrs = append(attrs, str("hydra.config.breadcrumb", r.Config))
	}
	return attrs
}

func spanFor(r cost.Row) (Span, error) {
	start, err := time.Parse(time.RFC3339, r.TS)
	if err != nil {
		// A row whose timestamp will not parse cannot be placed on a timeline.
		// Guessing "now" would put a months-old dispatch in today's trace.
		return Span{}, fmt.Errorf("otlp: row has an unparseable timestamp %q: %w", r.TS, err)
	}
	end := start.Add(time.Duration(r.WallMS) * time.Millisecond)

	traceID, err := idHex(16, r.RunID)
	if err != nil {
		return Span{}, err
	}
	// The run log's span id is already 8 bytes of hex, an OTLP span id exactly,
	// so a row that carries one exports under the same identity the trace uses
	// instead of a second one derived from the task.
	spanID := r.SpanID
	if !validSpanID(spanID) {
		spanID, err = idHex(8, r.TaskID)
		if err != nil {
			return Span{}, err
		}
	}

	attrs := []KeyValue{
		// gen_ai.* where it genuinely corresponds. Still Development-stage
		// upstream, so nothing here depends on these names being stable.
		str("gen_ai.system", r.Executor),
		str("gen_ai.request.model", r.Model),
		str("gen_ai.operation.name", "chat"),
		num("gen_ai.usage.input_tokens", int64(r.PromptTokens)),
		num("gen_ai.usage.output_tokens", int64(r.ResponseTokens)),

		// Hydra's own, which OTel has no place for. These are the fields that
		// make the log worth exporting, so they are carried, not dropped.
		num("hydra.tier", int64(r.Tier)),
		str("hydra.enum", r.Enum),
		str("hydra.pool", r.Pool),
		flt("hydra.cost.est_usd", r.EstCostUSD),
		str("hydra.cost.tokens_source", r.TokensSource),
		flt("hydra.routing.act_prob", r.ActProb),
		flt("hydra.routing.keep_prob", r.KeepProb),
	}
	if r.SwarmMode != "" {
		attrs = append(attrs, str("hydra.swarm.mode", r.SwarmMode),
			KeyValue{Key: "hydra.swarm.winner", Value: Value{BoolValue: &r.SwarmWinner}})
	}
	if r.Config != "" {
		attrs = append(attrs, str("hydra.config.breadcrumb", r.Config))
	}

	name := "dispatch " + r.Model
	if r.Model == "" {
		name = "dispatch"
	}
	return Span{
		TraceID:           traceID,
		SpanID:            spanID,
		Name:              name,
		Kind:              3, // SPAN_KIND_CLIENT
		StartTimeUnixNano: strconv.FormatInt(start.UnixNano(), 10),
		EndTimeUnixNano:   strconv.FormatInt(end.UnixNano(), 10),
		Attributes:        attrs,
		Status:            Status{Code: 1},
	}, nil
}

// validSpanID reports whether s is a well-formed, non-zero OTLP span id. An
// all-zero id is invalid per the spec and collectors drop the span, so a
// malformed one falls back rather than exporting something that vanishes.
func validSpanID(s string) bool {
	if len(s) != 16 {
		return false
	}
	raw, err := hex.DecodeString(s)
	if err != nil {
		return false
	}
	for _, b := range raw {
		if b != 0 {
			return true
		}
	}
	return false
}

// idHex renders a stable id of exactly n bytes as hex.
//
// A trace or span id of all zeroes is invalid per the spec and collectors drop
// the span, so a row with no run or task id gets a random one rather than a
// zero one, an unlinked span is still data; a dropped span is not.
func idHex(n int, seed string) (string, error) {
	buf := make([]byte, n)
	if seed == "" {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		return hex.EncodeToString(buf), nil
	}
	// Repeat the seed's bytes to fill n. Deterministic, so every span from one
	// run shares a trace id across exports.
	for i := 0; i < n; i++ {
		buf[i] = seed[i%len(seed)]
	}
	allZero := true
	for _, b := range buf {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(buf), nil
}

// Marshal renders the payload as the JSON body an OTLP/HTTP endpoint expects.
func Marshal(p Payload) ([]byte, error) { return json.Marshal(p) }
