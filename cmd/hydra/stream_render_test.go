// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/provider"
)

func started(name string) dispatch.StreamEvent {
	return dispatch.StreamEvent{
		Kind: dispatch.StreamAttemptStarted,
		Head: provider.Head{ID: name, Name: name},
	}
}

func delta(s string) dispatch.StreamEvent {
	return dispatch.StreamEvent{Kind: dispatch.StreamDelta, Text: s}
}

func failed(reason, span string) dispatch.StreamEvent {
	return dispatch.StreamEvent{
		Kind: dispatch.StreamAttemptFailed, Reason: reason, SpanID: span,
		Head: provider.Head{ID: "h", Name: "h"},
	}
}

// The whole point: the answer is on screen before the call has returned.
func TestStreamRender_DrawsOutputBeforeTheCallFinishes(t *testing.T) {
	var buf bytes.Buffer
	r := newStreamRenderer(&buf, "run-1", 80, 40, true)

	r.Handle(started("qwen"))
	r.Handle(delta("Hello "))
	r.Handle(delta("there"))

	if got := buf.String(); !strings.Contains(got, "Hello there") {
		t.Errorf("output not drawn yet, so nothing was streamed:\n%q", got)
	}
	if !r.Streamed() {
		t.Error("Streamed() is false though deltas were drawn, so the caller would print the answer a second time")
	}
}

// An abandoned partial must be retracted, not left above the real answer.
func TestStreamRender_CollapsesAnAbandonedPartial(t *testing.T) {
	var buf bytes.Buffer
	r := newStreamRenderer(&buf, "run-7", 80, 40, true)

	r.Handle(started("flaky"))
	r.Handle(delta("half an ans"))
	r.Handle(failed("context deadline exceeded", "a1b2c3d4"))

	got := buf.String()
	// blank + header + rule + blank = 4 rows, and the partial fits on one.
	if want := "\r\033[4A\033[J"; !strings.Contains(got, want) {
		t.Errorf("no erase for the drawn rows (want %q):\n%q", want, got)
	}
	if !strings.Contains(got, "abandoned") {
		t.Error("the partial was erased with nothing said about it")
	}
	// Erasing it is only acceptable because it stays reachable.
	if !strings.Contains(got, "hyctl trace view run-7 --span a1b2c3d4") {
		t.Errorf("no way to read the erased partial back:\n%q", got)
	}
	if !strings.Contains(got, "context deadline exceeded") {
		t.Error("the reason the attempt was abandoned is not shown")
	}
}

// The erase count has to match the rows actually drawn, wrapping included.
// Computed here independently of the renderer's own arithmetic, so an
// off-by-one in it cannot also move the expectation.
func TestStreamRender_EraseCountMatchesWrappedRows(t *testing.T) {
	// 60 is the narrowest width that still clears the header-wrap guard, so
	// this is the smallest terminal where wrapping is measured rather than
	// given up on.
	const width = 60
	for _, tc := range []struct {
		name string
		text string
		rows int // 4 header rows + rows the text advances
	}{
		{"one short line", "abc", 4},
		{"exactly one wrap", strings.Repeat("a", 60), 5},
		{"wrap and a newline", strings.Repeat("a", 90) + "\n", 6},
		{"three newlines", "a\nb\nc\n", 7},
		{"two wraps", strings.Repeat("a", 121), 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			r := newStreamRenderer(&buf, "run", width, 100, true)
			r.Handle(started("h"))
			r.Handle(delta(tc.text))
			r.Handle(failed("nope", "sp"))

			want := fmt.Sprintf("\r\033[%dA\033[J", tc.rows)
			if !strings.Contains(buf.String(), want) {
				t.Errorf("erase is not %d rows (want %q) for %d chars at width %d",
					tc.rows, want, len(tc.text), width)
			}
		})
	}
}

// Erasing rows that have scrolled off would eat whatever the user was reading
// above, so a partial taller than the screen is left where it is.
func TestStreamRender_WillNotEraseWhatHasScrolled(t *testing.T) {
	var buf bytes.Buffer
	r := newStreamRenderer(&buf, "run", 80, 10, true)

	r.Handle(started("h"))
	r.Handle(delta(strings.Repeat("line\n", 20)))
	r.Handle(failed("boom", "sp"))

	got := buf.String()
	if strings.Contains(got, "\033[J") {
		t.Errorf("erased a partial that has scrolled past a 10-row terminal:\n%q", got)
	}
	if !strings.Contains(got, "abandoned") {
		t.Error("nothing marks the partial as abandoned, so it reads as the answer")
	}
}

// A rune that may render wider than one cell makes the row count a guess, and
// erasing on a guess is what corrupts a scrollback.
func TestStreamRender_WillNotEraseOnAGuessedRowCount(t *testing.T) {
	for _, tc := range []struct{ name, text string }{
		{"wide runes", "日本語のテキスト"},
		{"an escape sequence", "plain \x1b[31mred\x1b[0m"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			r := newStreamRenderer(&buf, "run", 80, 40, true)
			r.Handle(started("h"))
			r.Handle(delta(tc.text))
			r.Handle(failed("boom", "sp"))

			if got := buf.String(); strings.Contains(got, "\033[J") {
				t.Errorf("erased rows whose width is unknown:\n%q", got)
			}
		})
	}
}

// A narrow terminal wraps the header rows themselves, so the row model is
// already wrong before any delta arrives.
func TestStreamRender_WillNotEraseOnANarrowTerminal(t *testing.T) {
	var buf bytes.Buffer
	r := newStreamRenderer(&buf, "run", 40, 40, true)

	r.Handle(started("h"))
	r.Handle(delta("short"))
	r.Handle(failed("boom", "sp"))

	if got := buf.String(); strings.Contains(got, "\033[J") {
		t.Errorf("erased on a terminal too narrow for the 58-column rule:\n%q", got)
	}
}

// A failure with nothing produced has no partial to go and read, so offering
// a trace command would point at an empty span.
func TestStreamRender_NoPartialOffersNoTraceCommand(t *testing.T) {
	var buf bytes.Buffer
	r := newStreamRenderer(&buf, "run", 80, 40, true)

	r.Handle(started("h"))
	r.Handle(failed("no such binary", "sp"))

	got := buf.String()
	if strings.Contains(got, "trace view") {
		t.Errorf("offered to show a partial that was never produced:\n%q", got)
	}
	if !strings.Contains(got, "no such binary") {
		t.Error("the failure reason is not reported")
	}
}

// With payload capture off the span opens but holds no text, so the command
// would send someone to a page reading "payload capture is off". Verified
// against a real run before this guard existed.
func TestStreamRender_OffersNoTraceCommandWhenNothingWasStored(t *testing.T) {
	var buf bytes.Buffer
	r := newStreamRenderer(&buf, "run", 80, 40, false)

	r.Handle(started("h"))
	r.Handle(delta("a partial that is not stored anywhere"))
	r.Handle(failed("boom", "sp"))

	got := buf.String()
	if strings.Contains(got, "trace view") {
		t.Errorf("offered to read back a partial that payload capture never stored:\n%q", got)
	}
	// The collapse itself still has to happen and still has to be explained.
	if !strings.Contains(got, "abandoned") || !strings.Contains(got, "\033[J") {
		t.Errorf("the partial was neither collapsed nor explained:\n%q", got)
	}
}

func TestStreamRender_FinishReportsTheNumbersAndOmitsAnUnknownTTFT(t *testing.T) {
	var buf bytes.Buffer
	r := newStreamRenderer(&buf, "run", 80, 40, true)
	r.Handle(started("h"))
	r.Handle(delta("hi"))
	r.Finish(31, 64, 2*time.Second, 0)

	got := buf.String()
	if !strings.Contains(got, "31→64 tokens") {
		t.Errorf("token counts missing:\n%q", got)
	}
	// Zero TTFT means unknown, and "ttft 0s" reads as instant.
	if strings.Contains(got, "ttft") {
		t.Errorf("reported an unmeasured TTFT:\n%q", got)
	}

	buf.Reset()
	r2 := newStreamRenderer(&buf, "run", 80, 40, true)
	r2.Handle(started("h"))
	r2.Handle(delta("hi"))
	r2.Finish(31, 64, 2*time.Second, 1200*time.Millisecond)
	if !strings.Contains(buf.String(), "ttft 1.2s") {
		t.Errorf("measured TTFT not reported:\n%q", buf.String())
	}
}

// A second attempt starts from a clean row model, or the first attempt's rows
// are counted into the second one's erase.
func TestStreamRender_ResetsBetweenAttempts(t *testing.T) {
	var buf bytes.Buffer
	r := newStreamRenderer(&buf, "run", 80, 40, true)

	r.Handle(started("first"))
	r.Handle(delta(strings.Repeat("x\n", 5)))
	r.Handle(failed("boom", "sp1"))
	if r.Streamed() {
		t.Error("still open after a failed attempt, so the next answer appends to a dead block")
	}

	buf.Reset()
	r.Handle(started("second"))
	r.Handle(delta("ok"))
	r.Handle(failed("boom", "sp2"))
	if want := "\r\033[4A\033[J"; !strings.Contains(buf.String(), want) {
		t.Errorf("second attempt erased the wrong row count (want %q):\n%q", want, buf.String())
	}
}

// A head can answer with nothing to stream, an empty response from a CLI head
// is the real case, and the spinner then has to be taken down before the
// buffered rendering prints over the row it is still using.
func TestStreamRender_StopGivesTheLineBackWhenNothingStreamed(t *testing.T) {
	var buf bytes.Buffer
	r := newStreamRenderer(&buf, "run", 80, 40, true)

	r.Handle(started("silent"))
	r.Stop()

	if r.Streamed() {
		t.Error("Streamed() is true though no delta arrived")
	}
	// The spinner's row is cleared, so whatever prints next starts clean.
	if got := buf.String(); !strings.HasSuffix(got, "\r\033[K") {
		t.Errorf("spinner line not cleared:\n%q", got)
	}
	// Idempotent: the caller cannot know whether a failure already stopped it.
	r.Stop()
}

// The spinner is the answer to "is anything happening" during prefill, which
// was measured at 4.2s, so it has to actually animate.
func TestStreamRender_SpinnerAnimatesWhileWaiting(t *testing.T) {
	var buf lockedBuffer
	r := newStreamRenderer(&buf, "run", 80, 40, true)
	r.Handle(started("slow"))

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(buf.String(), spinFrames[1]) {
		time.Sleep(10 * time.Millisecond)
	}
	got := buf.String()
	r.Stop()
	if !strings.Contains(got, spinFrames[0]) || !strings.Contains(got, spinFrames[1]) {
		t.Errorf("spinner did not advance past its first frame:\n%q", got)
	}
	if !strings.Contains(got, "slow") {
		t.Error("the spinner line does not name the head being waited on")
	}
}

// lockedBuffer lets the test read while the spinner goroutine writes.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestStreamRender_FirstLineTrimsAMultiLineReason(t *testing.T) {
	long := strings.Repeat("x", 200)
	for _, tc := range []struct{ name, in, want string }{
		{"single line kept", "boom", "boom"},
		{"only the first line", "boom\nstack frame\nmore", "boom"},
		{"trimmed to a line's worth", long, strings.Repeat("x", 100) + "…"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstLine(tc.in); got != tc.want {
				t.Errorf("firstLine(%d chars) = %q, want %q", len(tc.in), got, tc.want)
			}
		})
	}
}
