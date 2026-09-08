// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/payload"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/waterfall"
)

// seedRun writes a fallback chain: one head fails, the next answers.
func seedRun(t *testing.T, id string) (failSpan, okSpan string) {
	t.Helper()
	failSpan, okSpan = runlog.NewSpanID(), runlog.NewSpanID()
	l := runlog.New(id)
	base := time.Date(2026, 9, 8, 6, 0, 0, 0, time.UTC)
	ts := func(sec int) string { return base.Add(time.Duration(sec) * time.Second).Format(time.RFC3339Nano) }

	for _, e := range []runlog.Event{
		{Kind: runlog.KindRunStarted, TaskID: "t1", TS: ts(0), Detail: "add pagination"},
		{Kind: runlog.KindHeadSelected, TaskID: "t1", TS: ts(1), SpanID: failSpan,
			Head: "h1", Model: "Expensive Model", Tier: 2, Detail: "candidate 1 of 2"},
		{Kind: runlog.KindError, TaskID: "t1", TS: ts(1), SpanID: failSpan, Level: runlog.LevelError,
			Head: "h1", Model: "Expensive Model", Tier: 2, Status: "failed", Detail: "no such binary"},
		{Kind: runlog.KindHeadSelected, TaskID: "t1", TS: ts(2), SpanID: okSpan,
			Head: "h2", Model: "Local Model", Tier: 10, Detail: "candidate 2 of 2"},
		{Kind: runlog.KindDispatchFinished, TaskID: "t1", TS: ts(8), SpanID: okSpan,
			Head: "h2", Model: "Local Model", Tier: 10, Status: "ok",
			InputTokens: 120, OutputTokens: 40, CostUSD: 0.00012, DurationMS: 6000, TTFTMs: 250},
		{Kind: runlog.KindRunFinished, TaskID: "t1", TS: ts(9)},
	} {
		if err := l.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	return failSpan, okSpan
}

// The waterfall has to say which head ran, how the chain moved, and what it
// cost. A view that renders without those is decorative.
func TestCLI_TraceViewRendersTheFallbackChain(t *testing.T) {
	cliSandbox(t)
	seedRun(t, "20260908T060000Z-aaaaaaaaaaaaaaaa")

	out := captureStdout(t, func() {
		cmd := cmdTraceView()
		cmd.SetArgs([]string{"20260908T060000Z-aaaaaaaaaaaaaaaa"})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
	})

	for _, want := range []string{
		"Expensive Model", "Local Model", // both attempts, not just the winner
		"add pagination", // the run's own ask
		"120", "40",      // tokens on the span
		"t2", "t10", // the tiers it moved between
		"ttft 250ms",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the waterfall never shows %q:\n%s", want, out)
		}
	}
}

// No argument means the newest run. That is what someone wants nearly always,
// and making them paste an id first would be friction for nothing.
func TestCLI_TraceViewDefaultsToTheNewestRun(t *testing.T) {
	cliSandbox(t)
	seedRun(t, "20260908T060000Z-0000000000000001")
	seedRun(t, "20260908T070000Z-0000000000000002") // later id sorts newer

	out := captureStdout(t, func() {
		cmd := cmdTraceView()
		cmd.SetArgs(nil)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "20260908T070000Z-0000000000000002") {
		t.Errorf("the default is not the newest run:\n%s", out)
	}
}

func TestCLI_TraceViewJSONCarriesTheNesting(t *testing.T) {
	cliSandbox(t)
	_, okSpan := seedRun(t, "20260908T060000Z-bbbbbbbbbbbbbbbb")

	out := captureStdout(t, func() {
		cmd := cmdTraceView()
		cmd.SetArgs([]string{"20260908T060000Z-bbbbbbbbbbbbbbbb", "--json"})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
	})
	var tr waterfall.Trace
	if err := json.Unmarshal([]byte(out), &tr); err != nil {
		t.Fatalf("--json is not valid JSON: %v\n%s", err, out)
	}
	if len(tr.Roots) != 2 {
		t.Fatalf("got %d roots, want the two attempts", len(tr.Roots))
	}
	var found bool
	for _, s := range tr.Roots {
		if s.ID == okSpan && s.InputTokens == 120 {
			found = true
		}
	}
	if !found {
		t.Errorf("the successful span is not in the JSON with its tokens:\n%s", out)
	}
}

// A bad id must name what to run instead. An observability tool that answers
// "not found" and stops is one someone gives up on.
func TestCLI_TraceViewUnknownSpanSaysHowToListThem(t *testing.T) {
	cliSandbox(t)
	seedRun(t, "20260908T060000Z-cccccccccccccccc")

	cmd := cmdTraceView()
	cmd.SetArgs([]string{"20260908T060000Z-cccccccccccccccc", "--span", "nosuchspan"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("an unknown span id was accepted")
	}
	if !strings.Contains(err.Error(), "hyctl trace view") {
		t.Errorf("the error does not say how to list spans: %v", err)
	}
}

func TestCLI_TraceViewEmptyRunIsAnError(t *testing.T) {
	cliSandbox(t)
	cmd := cmdTraceView()
	cmd.SetArgs([]string{"20260908T060000Z-dddddddddddddddd"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("a run with no events rendered as though it had some")
	}
}

// "Capture is off", "not stored" and "evicted" are three different facts. One
// empty box for all three is what makes a viewer untrustworthy.
func TestTraceView_DistinguishesTheThreeWaysTextIsMissing(t *testing.T) {
	s := cliSandbox(t)
	_ = s

	// Capture off: no ref, and the config says why.
	if got := unavailableReason(""); !strings.Contains(got, "capture is off") {
		t.Errorf("with capture off the reason is %q", got)
	}

	// Capture on but this span stored nothing.
	if err := config.Save(&config.Config{Cortex: "none", CapturePayloads: true}); err != nil {
		t.Fatal(err)
	}
	if got := unavailableReason(""); !strings.Contains(got, "not stored") {
		t.Errorf("with capture on and no ref the reason is %q", got)
	}

	// A ref is present, so nothing is unavailable at this stage.
	if got := unavailableReason("deadbeefdeadbeef"); got != "" {
		t.Errorf("a present ref reported %q", got)
	}

	// Evicted: a ref the store has never heard of resolves to the eviction
	// message rather than to a bare error.
	segs, why := loadSegments("deadbeefdeadbeef")
	if segs != nil {
		t.Error("a missing ref returned content")
	}
	if !strings.Contains(why, "evicted") {
		t.Errorf("a missing ref reads as %q, want the eviction explanation", why)
	}
}

// The point of --span is reading the text, with the system prompt kept apart
// from the task.
func TestCLI_TraceViewSpanShowsThePromptAndResponse(t *testing.T) {
	cliSandbox(t)
	if err := config.Save(&config.Config{Cortex: "none", CapturePayloads: true}); err != nil {
		t.Fatal(err)
	}
	store, err := payload.Open(payload.Dir())
	if err != nil {
		t.Fatal(err)
	}
	inRef, err := store.PutSegments([]payload.Segment{
		{Label: "system", Content: "SYSTEM-MARKER"},
		{Label: "prompt", Content: "PROMPT-MARKER"},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	outRef, err := store.PutSegments([]payload.Segment{{Label: "response", Content: "RESPONSE-MARKER"}}, 1)
	if err != nil {
		t.Fatal(err)
	}

	span := runlog.NewSpanID()
	if err := runlog.New("20260908T060000Z-eeeeeeeeeeeeeeee").Append(runlog.Event{
		Kind: runlog.KindDispatchFinished, TaskID: "t1", SpanID: span,
		Head: "h1", Model: "M", Tier: 8, Status: "ok",
		InputRef: inRef, OutputRef: outRef,
	}); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		cmd := cmdTraceView()
		cmd.SetArgs([]string{"20260908T060000Z-eeeeeeeeeeeeeeee", "--span", span})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
	})
	for _, want := range []string{"SYSTEM-MARKER", "PROMPT-MARKER", "RESPONSE-MARKER",
		"[system]", "[prompt]", "asked", "answered"} {
		if !strings.Contains(out, want) {
			t.Errorf("the span detail never shows %q:\n%s", want, out)
		}
	}
}

func TestHumanDuration_ScalesToWhatMatters(t *testing.T) {
	for _, c := range []struct {
		in   time.Duration
		want string
	}{
		{0, "0ms"},
		{-time.Second, "0ms"},
		{380 * time.Millisecond, "380ms"},
		{15600 * time.Millisecond, "15.6s"},
		{95 * time.Second, "1m35s"},
	} {
		if got := humanDuration(c.in); got != c.want {
			t.Errorf("humanDuration(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The bar must stay exactly barWidth cells whatever the timings, or rows stop
// lining up and the chart is unreadable.
func TestBar_IsAlwaysExactlyOneWidth(t *testing.T) {
	base := time.Date(2026, 9, 8, 6, 0, 0, 0, time.UTC)
	tr := &waterfall.Trace{Start: base, End: base.Add(10 * time.Second)}
	cases := map[string]*waterfall.Span{
		"whole timeline":   {Start: base, End: base.Add(10 * time.Second)},
		"instant at start": {Start: base, End: base},
		"instant at end":   {Start: base.Add(10 * time.Second), End: base.Add(10 * time.Second)},
		"past the end":     {Start: base.Add(9 * time.Second), End: base.Add(30 * time.Second)},
		"before the start": {Start: base.Add(-5 * time.Second), End: base.Add(time.Second)},
		"no timestamps":    {},
		"end before start": {Start: base.Add(5 * time.Second), End: base.Add(time.Second)},
	}
	for name, s := range cases {
		cells := len([]rune(stripANSI(bar(s, tr))))
		if cells != barWidth {
			t.Errorf("%s: bar is %d cells, want %d", name, cells, barWidth)
		}
	}
	// A zero-length trace cannot be scaled against; it must still draw a row.
	flat := &waterfall.Trace{Start: base, End: base}
	if cells := len([]rune(stripANSI(bar(&waterfall.Span{Start: base, End: base}, flat)))); cells != barWidth {
		t.Errorf("zero-length trace: bar is %d cells, want %d", cells, barWidth)
	}
}

// stripANSI removes the colour escapes lipgloss adds, so a width assertion
// counts cells rather than bytes.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
