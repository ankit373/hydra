// SPDX-License-Identifier: MIT

package executor

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/util"
)

// nonStreaming is an Executor with no ExecuteStream, the case Stream has to
// keep working for.
type nonStreaming struct {
	out  string
	err  error
	seen int
}

func (n *nonStreaming) Execute(context.Context, Request) (*Response, error) {
	n.seen++
	if n.err != nil {
		return nil, n.err
	}
	return &Response{Output: n.out, OutputTokens: 3}, nil
}

// A surface must not have to branch on whether its head streams, so a
// non-streaming executor delivers its whole output as one delta.
func TestStream_NonStreamingExecutorStillDeliversOneDelta(t *testing.T) {
	ex := &nonStreaming{out: "the whole answer"}
	var got []string
	resp, err := Stream(context.Background(), ex, Request{}, func(d string) { got = append(got, d) })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "the whole answer" {
		t.Errorf("deltas %q, want one delta with the whole output", got)
	}
	if resp.Output != "the whole answer" {
		t.Errorf("Output %q, want the whole answer", resp.Output)
	}
	if CanStream(ex) {
		t.Error("CanStream is true for an executor with no ExecuteStream")
	}
}

// An error must not be reported as an empty answer, and must not fire a delta.
func TestStream_NonStreamingErrorFiresNoDelta(t *testing.T) {
	ex := &nonStreaming{err: fmt.Errorf("boom")}
	fired := false
	if _, err := Stream(context.Background(), ex, Request{}, func(string) { fired = true }); err == nil {
		t.Fatal("expected the executor error to surface")
	}
	if fired {
		t.Error("a delta fired for a failed execute")
	}
}

// A nil callback means the caller wants no incremental delivery, so the plain
// Execute path is taken rather than streaming into a discarded function.
func TestStream_NilCallbackTakesThePlainPath(t *testing.T) {
	ex := &nonStreaming{out: "x"}
	if _, err := Stream(context.Background(), ex, Request{}, nil); err != nil {
		t.Fatal(err)
	}
	if ex.seen != 1 {
		t.Errorf("Execute called %d times, want 1", ex.seen)
	}
}

// ── the delta sink ────────────────────────────────────────────────────────────

// The cap is a hard invariant (CLAUDE.md: util.Accumulator, never a raw
// buffer). Past it nothing more may be forwarded either: a surface must not
// render text the Response will not contain.
func TestDeltaSink_StopsForwardingAtTheCapAndReportsTruncation(t *testing.T) {
	// The accumulator honours HYDRA_MAX_OUTPUT_BYTES over its argument, so a
	// stray value in the environment would silently change the cap this test
	// is about.
	t.Setenv("HYDRA_MAX_OUTPUT_BYTES", "")

	var forwarded int
	s := newDeltaSink(func(string) { forwarded++ }, time.Now())
	s.acc = util.NewAccumulator(8)

	s.write("12345678") // exactly the cap
	s.write("9")        // past it
	s.write("10")

	if !s.truncated() {
		t.Error("Truncated is false after writing past the cap")
	}
	if forwarded != 1 {
		t.Errorf("forwarded %d deltas, want 1: nothing past the cap may reach a surface", forwarded)
	}
	got := s.output()
	if !strings.HasPrefix(got, "12345678") {
		t.Errorf("output %q lost the content that fit under the cap", got)
	}
	// The accumulator appends its own marker, so the answer is longer than the
	// cap by design; what must not be there is the content it refused.
	if strings.Contains(got, "12345678910") {
		t.Errorf("output %q kept content written past the cap", got)
	}
}

// TTFT is the first delta that carries something. A provider's role preamble
// and keep-alives are empty or whitespace, and counting one as the first token
// would report a TTFT of nearly zero for a head that had not started.
func TestDeltaSink_TTFTIgnoresEmptyAndWhitespaceDeltas(t *testing.T) {
	s := newDeltaSink(nil, time.Now())
	s.write("")
	s.write("   ")
	if s.firstTokenAt() != 0 {
		t.Error("whitespace counted as the first token")
	}
	time.Sleep(2 * time.Millisecond)
	s.write("real")
	first := s.firstTokenAt()
	if first == 0 {
		t.Fatal("TTFT still zero after a real delta")
	}
	s.write("more")
	if s.firstTokenAt() != first {
		t.Error("TTFT moved after a later delta; it is the *first* token")
	}
	// Whitespace is still part of the answer even when it does not start it.
	if got := s.output(); got != "   realmore" {
		t.Errorf("output %q dropped a whitespace delta", got)
	}
}

// Stream must pick the streaming path when the executor has one. Driven
// through the HTTP executor because that is the one every real head resolves
// to, local servers included (#819).
func TestStream_PrefersTheStreamingPath(t *testing.T) {
	srv := newSSEServer(t, []string{"x", "y", "z"}, true)

	ex := &HTTPExecutor{}
	if !CanStream(ex) {
		t.Fatal("CanStream is false for the HTTP executor")
	}
	var n int
	req := Request{Prompt: "p", Head: compatHead(t, srv.URL)}
	if _, err := Stream(context.Background(), ex, req, func(string) { n++ }); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("got %d deltas, want 3: Stream fell back to Execute", n)
	}
}

// #810: zero was both the sentinel and a reachable measurement, so a first
// token that arrived inside the clock's granularity read as "the provider does
// not report TTFT" and the next fragment then recorded its own elapsed time as
// the time to the first. Windows against a local server is where that showed.
func TestDeltaSink_TheFirstTokenKeepsTheTitle(t *testing.T) {
	s := newDeltaSink(func(string) {}, time.Now())

	s.write("a")
	if s.firstTokenAt() == 0 {
		t.Fatal("TTFT is zero for a token that demonstrably arrived")
	}

	// Stand in for a sub-granularity measurement, which is the state the old
	// sentinel could not tell from "never measured". A later fragment must not
	// be able to claim the title from it.
	s.mu.Lock()
	s.ttft = 0
	s.mu.Unlock()

	time.Sleep(2 * time.Millisecond)
	s.write("b")

	if got := s.firstTokenAt(); got != 0 {
		t.Errorf("a later fragment recorded %v as the time to the first token", got)
	}
}

// The floor itself: a reading of zero is a reading, and must not render as the
// absence of one.
func TestMeasured_ZeroIsReportedAsTheFloorNotAsUnknown(t *testing.T) {
	if got := measured(0); got != time.Nanosecond {
		t.Errorf("measured(0) = %v, want the floor: zero reads as 'not reported'", got)
	}
	if got := measured(3 * time.Millisecond); got != 3*time.Millisecond {
		t.Errorf("measured(3ms) = %v, a real reading must pass through untouched", got)
	}
}
