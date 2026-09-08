// SPDX-License-Identifier: MIT

package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/provider"
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

// ── Ollama ────────────────────────────────────────────────────────────────────

// ndjsonOllama serves an /api/generate stream: one object per fragment, then a
// final one with done and the counts, which is the real wire shape.
func ndjsonOllama(t *testing.T, fragments []string, finalCounts bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.Path != "/api/generate" {
			http.NotFound(w, r)
			return
		}
		fl, _ := w.(http.Flusher)
		for _, f := range fragments {
			fmt.Fprintf(w, `{"model":"stub","response":%q,"done":false}`+"\n", f)
			if fl != nil {
				fl.Flush()
			}
		}
		if finalCounts {
			fmt.Fprint(w, `{"model":"stub","response":"","done":true,`+
				`"prompt_eval_count":11,"eval_count":22,"prompt_eval_duration":5000000}`+"\n")
		} else {
			fmt.Fprint(w, `{"model":"stub","response":"","done":true}`+"\n")
		}
		if fl != nil {
			fl.Flush()
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OLLAMA_HOST", srv.URL)
	return srv
}

func ollamaReq() Request {
	return Request{
		Prompt: "a prompt long enough to estimate from",
		Head: provider.Head{
			ID: "ollama/stub", Name: "stub", Provider: "local", Source: "port",
			LocalOnly: true, Meta: map[string]string{"model_flag": "stub"},
		},
	}
}

func TestOllamaStream_DeliversFragmentsInOrderAndAssemblesTheWhole(t *testing.T) {
	ndjsonOllama(t, []string{"Hello", ", ", "world"}, true)

	var mu sync.Mutex
	var got []string
	resp, err := (&OllamaExecutor{}).ExecuteStream(context.Background(), ollamaReq(),
		func(d string) { mu.Lock(); got = append(got, d); mu.Unlock() })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, "") != "Hello, world" {
		t.Errorf("deltas %q do not reassemble to the answer", got)
	}
	if len(got) != 3 {
		t.Errorf("got %d deltas, want 3: fragments must arrive as they come, not batched", len(got))
	}
	if resp.Output != "Hello, world" {
		t.Errorf("Output %q, want the assembled answer", resp.Output)
	}
}

// The trap this issue exists to avoid: token counts live only on the final
// `done` object, so a reader that stops at the last fragment logs the dispatch
// as free.
func TestOllamaStream_RealTokenCountsSurviveTheStream(t *testing.T) {
	ndjsonOllama(t, []string{"a", "b"}, true)

	resp, err := (&OllamaExecutor{}).ExecuteStream(context.Background(), ollamaReq(), func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if resp.InputTokens != 11 || resp.OutputTokens != 22 {
		t.Errorf("tokens %d/%d, want the reported 11/22", resp.InputTokens, resp.OutputTokens)
	}
	if resp.TokensEstimated {
		t.Error("provider-reported counts are labelled estimated")
	}
	if resp.TTFT == 0 {
		t.Error("TTFT is zero on a stream we watched arrive")
	}
}

// A stream that ends without counts must estimate and say so, rather than
// report zero tokens as though the call were free.
func TestOllamaStream_MissingCountsAreEstimatedAndLabelled(t *testing.T) {
	ndjsonOllama(t, []string{"some answer text"}, false)

	resp, err := (&OllamaExecutor{}).ExecuteStream(context.Background(), ollamaReq(), func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.TokensEstimated {
		t.Error("estimated counts are not labelled, so they would read as measured")
	}
	if resp.InputTokens == 0 || resp.OutputTokens == 0 {
		t.Errorf("tokens %d/%d: a call that answered must not report zero",
			resp.InputTokens, resp.OutputTokens)
	}
}

// Cancelling mid-stream is the user pressing ctrl-c. What arrived is the
// answer; it must not be thrown away, and it must not hang.
func TestOllamaStream_CancellationKeepsWhatArrived(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.WriteHeader(http.StatusOK)
			return
		}
		fl, _ := w.(http.Flusher)
		fmt.Fprint(w, `{"model":"stub","response":"partial","done":false}`+"\n")
		if fl != nil {
			fl.Flush()
		}
		<-r.Context().Done() // never sends done
	}))
	defer srv.Close()
	t.Setenv("OLLAMA_HOST", srv.URL)

	done := make(chan struct{})
	var resp *Response
	var err error
	go func() {
		resp, err = (&OllamaExecutor{}).ExecuteStream(ctx, ollamaReq(),
			func(string) { cancel() }) // cancel the moment the first delta lands
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("ExecuteStream did not return after its context was cancelled")
	}
	if err != nil {
		t.Fatalf("cancellation reported as an error: %v", err)
	}
	if resp.Output != "partial" {
		t.Errorf("Output %q, want the partial that had already arrived", resp.Output)
	}
	if !resp.TokensEstimated {
		t.Error("a cancelled stream has no provider counts, so they must be labelled estimated")
	}
}

// A body that is not NDJSON is a real failure, not an empty answer.
func TestOllamaStream_GarbageBodyIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.WriteHeader(http.StatusOK)
			return
		}
		fmt.Fprint(w, "not json at all\n")
	}))
	defer srv.Close()
	t.Setenv("OLLAMA_HOST", srv.URL)

	if _, err := (&OllamaExecutor{}).ExecuteStream(context.Background(), ollamaReq(), func(string) {}); err == nil {
		t.Fatal("a non-NDJSON body was accepted")
	}
}

// Stream must pick the streaming path when the executor has one.
func TestStream_PrefersTheStreamingPath(t *testing.T) {
	ndjsonOllama(t, []string{"x", "y", "z"}, true)

	ex := &OllamaExecutor{}
	if !CanStream(ex) {
		t.Fatal("CanStream is false for the Ollama executor")
	}
	var n int
	if _, err := Stream(context.Background(), ex, ollamaReq(), func(string) { n++ }); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("got %d deltas, want 3: Stream fell back to Execute", n)
	}
}
