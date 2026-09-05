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
// actually run accept — Langfuse ingests at /api/public/otel/v1/traces and
// offers no gRPC endpoint at all.
package otlp

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/ankit373/hydra/internal/cost"
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
// values as strings — a JSON number loses precision above 2^53, and unix nanos
// passed that in 1970.
type Span struct {
	TraceID           string     `json:"traceId"`
	SpanID            string     `json:"spanId"`
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

// Build renders dispatch rows as a single OTLP payload.
//
// serviceName names the resource; version is stamped so a collector can tell
// which Hydra produced a span.
func Build(rows []cost.Row, serviceName, version string) (Payload, error) {
	spans := make([]Span, 0, len(rows))
	for _, r := range rows {
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
	spanID, err := idHex(8, r.TaskID)
	if err != nil {
		return Span{}, err
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

// idHex renders a stable id of exactly n bytes as hex.
//
// A trace or span id of all zeroes is invalid per the spec and collectors drop
// the span, so a row with no run or task id gets a random one rather than a
// zero one — an unlinked span is still data; a dropped span is not.
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
