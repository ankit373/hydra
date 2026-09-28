// SPDX-License-Identifier: MIT

package runlog

import (
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

func TestNewSpanID_IsAnOTLPSpanID(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		id := NewSpanID()
		if len(id) != 16 {
			t.Fatalf("span id %q is %d chars, want 16 (8 bytes of hex)", id, len(id))
		}
		raw, err := hex.DecodeString(id)
		if err != nil {
			t.Fatalf("span id %q is not hex: %v", id, err)
		}
		// An all-zero span id is invalid per the OTLP spec and collectors
		// drop the span silently.
		if raw[0] == 0 && raw[7] == 0 && id == strings.Repeat("0", 16) {
			t.Fatal("span id is all zeroes, collectors drop it")
		}
		if seen[id] {
			t.Fatalf("span id %q repeated after %d draws", id, i)
		}
		seen[id] = true
	}
}

func TestSpanIDFor_IsStableAcrossProcesses(t *testing.T) {
	a := SpanIDFor("task-42")
	if got := SpanIDFor("task-42"); got != a {
		t.Fatalf("derived span id is not stable: %q then %q", a, got)
	}
	if SpanIDFor("task-43") == a {
		t.Fatal("two different tasks derived the same span id")
	}
	if len(a) != 16 {
		t.Fatalf("derived span id %q is %d chars, want 16", a, len(a))
	}
}

func TestSeverity_DerivesFromKindWhenUnset(t *testing.T) {
	cases := []struct {
		name string
		e    Event
		want Level
	}{
		{"unset defaults to info", Event{Kind: KindAttempt}, LevelInfo},
		{"error kind reads as error", Event{Kind: KindError}, LevelError},
		{"explicit level wins", Event{Kind: KindError, Level: LevelWarn}, LevelWarn},
		{"explicit debug on a normal kind", Event{Kind: KindAttempt, Level: LevelDebug}, LevelDebug},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.e.Severity(); got != c.want {
				t.Fatalf("Severity() = %q, want %q", got, c.want)
			}
		})
	}
}

// A v1 line has none of the v2 fields. It must still load, because sealed
// segments written before this change are not rewritten.
func TestLoad_V1EventsStillParse(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())
	const v1 = `{"v":1,"seq":1,"ts":"2026-09-07T19:08:23.038093Z","run_id":"r1",` +
		`"task_id":"t1","kind":"dispatch_finished","head":"flash-med",` +
		`"model":"flash-med","tier":8,"status":"ok","cost_usd":0.000224,"duration_ms":21052}`

	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path("r1"), []byte(v1+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	events, err := Load("r1")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("loaded %d events, want 1", len(events))
	}
	e := events[0]
	if e.V != 1 || e.Head != "flash-med" || e.DurationMS != 21052 {
		t.Fatalf("v1 event lost fields on load: %+v", e)
	}
	// The v2 fields are absent, not wrong.
	if e.SpanID != "" || e.InputTokens != 0 {
		t.Fatalf("v1 event invented v2 values: %+v", e)
	}
	if got := e.Severity(); got != LevelInfo {
		t.Fatalf("v1 event severity = %q, want %q", got, LevelInfo)
	}
}

// Meta is the only unbounded field, so it is what gets shed. Losing metadata is
// recoverable; a line long enough to tear breaks the log's ordering guarantee.
func TestAppend_ShedsOversizedMetaRatherThanTheEvent(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())
	l := New("r-big")
	if err := l.Append(Event{
		Kind: KindAttempt, Head: "h1",
		Meta: map[string]any{"blob": strings.Repeat("x", MaxEventBytes*2)},
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(Path("r-big"))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > MaxEventBytes+64 {
		t.Fatalf("line is %d bytes, cap is %d: the bound did not hold", len(raw), MaxEventBytes)
	}
	events, err := Load("r-big")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("loaded %d events, want 1: the event itself must survive", len(events))
	}
	if events[0].Head != "h1" {
		t.Fatalf("the event lost its own fields: %+v", events[0])
	}
	if _, ok := events[0].Meta["meta_dropped"]; !ok {
		t.Fatalf("shedding Meta was not recorded: %+v", events[0].Meta)
	}
}

func TestAppend_KeepsMetaThatFits(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())
	l := New("r-ok")
	if err := l.Append(Event{
		Kind: KindDispatchFinished, Head: "h1",
		InputTokens: 1843, OutputTokens: 412, TTFTMs: 380,
		SpanID: NewSpanID(), ParentSpanID: SpanIDFor("t1"),
		Meta: map[string]any{"max_tokens": 4096, "enum": "SIMPLE"},
	}); err != nil {
		t.Fatal(err)
	}
	events, err := Load("r-ok")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("loaded %d events, want 1", len(events))
	}
	e := events[0]
	if e.V != SchemaVersion {
		t.Fatalf("stamped v%d, want v%d", e.V, SchemaVersion)
	}
	if e.InputTokens != 1843 || e.OutputTokens != 412 || e.TTFTMs != 380 {
		t.Fatalf("token/latency fields lost: %+v", e)
	}
	if e.Meta["enum"] != "SIMPLE" {
		t.Fatalf("meta lost: %+v", e.Meta)
	}
	if _, dropped := e.Meta["meta_dropped"]; dropped {
		t.Fatal("meta that fits was shed anyway")
	}
}

// The premise of the v2 schema is that a fully detailed span stays cheap once
// sealed. Measured: 35.1 B/event on 460 real events from this machine, 41.3 on
// the varied corpus below. The guard is an absolute 56 B/event.
//
// Absolute, not a ratio against v1: how much better a baseline happens to
// compress is an accident of the corpus, and an earlier ratio guard read
// anywhere from +61% to +1814% for the same schema depending on how repetitive
// the sample was. Bytes per span is the number a disk actually charges.
func TestV2SpanCostStaysUnderBudget(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())
	const (
		events        = 400
		budgetPerSpan = 56.0
	)
	heads := []string{"flash-med", "qwen3-coder", "sonnet-5", "gpt-5-mini"}

	var wire []byte
	for i := 0; i < events; i++ {
		taskID := fmt.Sprintf("20260907T19%04dZ-cc90a86d67e49a%02d", i, i%97)
		e := Event{
			Seq:    uint64(i%8) + 1,
			TS:     time.Unix(1757270881+int64(i)*37, int64(i)*104729).UTC().Format(time.RFC3339Nano),
			TaskID: taskID, Kind: KindDispatchFinished,
			Head: heads[i%len(heads)], Model: heads[i%len(heads)],
			Tier: 4 + i%6, Status: "ok",
			CostUSD: float64(i%997) / 1e6, DurationMS: int64(800 + i*13%40000),

			SpanID:       NewSpanID(),
			ParentSpanID: SpanIDFor(taskID), // elided by Append
			Level:        LevelInfo,
			InputTokens:  400 + i*7%9000,
			OutputTokens: 100 + i*3%4000,
			TTFTMs:       int64(120 + i*11%2000),
			Meta:         map[string]any{"max_tokens": 4096, "enum": "SIMPLE", "attempt": 1 + i%3},
		}
		wire = append(append(wire, marshalAsAppended(t, e)...), '\n')
	}

	sealed := len(sealBytes(t, wire))
	perSpan := float64(sealed) / events
	t.Logf("fully detailed span: %.1f B sealed (%d B loose)", perSpan, len(wire)/events)
	if perSpan > budgetPerSpan {
		t.Fatalf("a span costs %.1f B sealed, budget is %.0f B", perSpan, budgetPerSpan)
	}
}

// The parent is a hash of the task id, which is already on the line. Writing it
// costs 16 incompressible bytes to restate something derivable, so Append
// elides it and ParentSpan puts it back. This is the guard on that round trip.
func TestAppend_ElidesTheDerivableParent(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())
	l := New("r-elide")
	taskSpan := SpanIDFor("t-1")
	if err := l.Append(Event{
		Kind: KindDispatchFinished, TaskID: "t-1",
		SpanID: NewSpanID(), ParentSpanID: taskSpan,
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(Path("r-elide"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"psid"`) {
		t.Fatalf("the derivable parent was written to the wire: %s", raw)
	}
	events, err := Load("r-elide")
	if err != nil {
		t.Fatal(err)
	}
	if got := events[0].ParentSpan(); got != taskSpan {
		t.Fatalf("ParentSpan() = %q, want the derived task span %q", got, taskSpan)
	}
}

// A parent that is not the task's own span carries real information and must
// survive, or nesting deeper than one level is silently flattened.
func TestAppend_KeepsANonDerivableParent(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())
	l := New("r-keep")
	swarmRoot := SpanIDFor("t-1/swarm")
	if err := l.Append(Event{
		Kind: KindAttempt, TaskID: "t-1",
		SpanID: SpanIDFor("t-1/swarm/head-a"), ParentSpanID: swarmRoot,
	}); err != nil {
		t.Fatal(err)
	}
	events, err := Load("r-keep")
	if err != nil {
		t.Fatal(err)
	}
	if got := events[0].ParentSpan(); got != swarmRoot {
		t.Fatalf("ParentSpan() = %q, want the swarm root %q", got, swarmRoot)
	}
}

// marshalAsAppended renders an event the way Append writes it, so the budget is
// measured against the wire format rather than the in-memory struct.
func marshalAsAppended(t *testing.T, e Event) []byte {
	t.Helper()
	l := New("budget-" + e.TaskID)
	if err := l.Append(e); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(Path("budget-" + e.TaskID))
	if err != nil {
		t.Fatal(err)
	}
	return raw[:len(raw)-1] // drop the newline; the caller adds one
}

// sealBytes compresses with the same settings Seal uses, so the budget above is
// measured against what actually lands on disk.
func sealBytes(t *testing.T, raw []byte) []byte {
	t.Helper()
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	return enc.EncodeAll(raw, nil)
}
