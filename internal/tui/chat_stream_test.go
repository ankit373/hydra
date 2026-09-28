// SPDX-License-Identifier: MIT

package tui

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/testutil"

	// Registered by init(), the same way cmd/hydra and the desktop app
	// link it. Without this the test binary discovers no heads at all.
	_ "github.com/ankit373/hydra/internal/provider/port"
)

func evStart(head string) dispatch.StreamEvent {
	return dispatch.StreamEvent{
		Kind: dispatch.StreamAttemptStarted,
		Head: provider.Head{ID: head, Name: head},
	}
}

func evDelta(s string) dispatch.StreamEvent {
	return dispatch.StreamEvent{Kind: dispatch.StreamDelta, Text: s}
}

func evFail(reason, span string) dispatch.StreamEvent {
	return dispatch.StreamEvent{Kind: dispatch.StreamAttemptFailed, Reason: reason, SpanID: span}
}

// The whole point: the answer is in the log before the task finishes.
func TestCkStream_LiveTextReachesTheLogWhileRunning(t *testing.T) {
	ex := &ckExecState{}
	h := ex.stream.handler()
	h(evStart("qwen"))
	h(evDelta("Hello "))
	h(evDelta("there"))

	th := &ckThread{log: []string{"you: hi"}}
	th.exec = ex
	got := strings.Join(th.displayLog(), "\n")
	if !strings.Contains(got, "Hello there") {
		t.Errorf("live text is not in the log:\n%q", got)
	}
	if !strings.Contains(got, "qwen") {
		t.Errorf("the head producing the stream is not named:\n%q", got)
	}
	if !strings.Contains(got, "you: hi") {
		t.Error("the existing scrollback was dropped")
	}
}

// A failed attempt's partial must never read as part of the answer.
func TestCkStream_FailedAttemptCollapsesAndTheNextIsNotMixedIn(t *testing.T) {
	ex := &ckExecState{}
	h := ex.stream.handler()
	h(evStart("flaky"))
	h(evDelta("half an answer"))
	h(evFail("context deadline exceeded", "sp1"))
	h(evStart("good"))
	h(evDelta("the real answer"))

	th := &ckThread{exec: ex}
	th.gone = ckMergeAbandoned(th.gone, ex.stream.abandoned())
	got := strings.Join(th.displayLog(), "\n")

	if !strings.Contains(got, "abandoned") || !strings.Contains(got, "flaky") {
		t.Errorf("the abandoned attempt is not marked:\n%q", got)
	}
	if !strings.Contains(got, "context deadline exceeded") {
		t.Errorf("the reason is not shown:\n%q", got)
	}
	// Collapsed means the text is gone from view, not that it was forgotten.
	if strings.Contains(got, "half an answer") {
		t.Errorf("the abandoned partial is still rendered, so it reads as the answer:\n%q", got)
	}
	if !strings.Contains(got, "the real answer") {
		t.Errorf("the answering head's text is missing:\n%q", got)
	}
	if len(th.gone) != 1 || th.gone[0].text != "half an answer" {
		t.Errorf("the partial was not retained for expanding: %+v", th.gone)
	}
}

func TestCkStream_ExpandShowsThePartialAgain(t *testing.T) {
	ex := &ckExecState{}
	h := ex.stream.handler()
	h(evStart("flaky"))
	h(evDelta("the partial text"))
	h(evFail("boom", "sp1"))

	th := &ckThread{exec: ex}
	th.gone = ckMergeAbandoned(th.gone, ex.stream.abandoned())
	if strings.Contains(strings.Join(th.displayLog(), "\n"), "the partial text") {
		t.Fatal("collapsed but still showing the text")
	}
	if !th.toggleNewestAbandoned() {
		t.Fatal("toggle reported nothing to expand")
	}
	if !strings.Contains(strings.Join(th.displayLog(), "\n"), "the partial text") {
		t.Error("expanded but the text is not shown")
	}
	// And back, since collapsing again has to work too.
	th.toggleNewestAbandoned()
	if strings.Contains(strings.Join(th.displayLog(), "\n"), "the partial text") {
		t.Error("re-collapse did not hide the text")
	}
}

// A tick copies fresh partials in from the worker, and must not undo an expand
// the user has already done: the worker's copy never carries `open`.
func TestCkMergeAbandoned_KeepsExpandStateAcrossTicks(t *testing.T) {
	have := []ckAbandoned{{head: "a", open: true}, {head: "b", open: false}}
	fresh := []ckAbandoned{{head: "a"}, {head: "b"}, {head: "c"}}

	got := ckMergeAbandoned(have, fresh)
	if len(got) != 3 {
		t.Fatalf("got %d entries, want the 3 the worker reported", len(got))
	}
	if !got[0].open {
		t.Error("an expanded partial was re-collapsed by a tick")
	}
	if got[1].open || got[2].open {
		t.Error("a collapsed partial was expanded by a tick")
	}
	if got[2].head != "c" {
		t.Errorf("the new partial did not arrive: %+v", got)
	}
}

// The retained buffer is bounded, and says so rather than pretending the
// clipped text was all of it.
func TestCkStream_LongStreamIsCappedAndMarked(t *testing.T) {
	ex := &ckExecState{}
	h := ex.stream.handler()
	h(evStart("chatty"))
	for i := 0; i < 40; i++ {
		h(evDelta(strings.Repeat("x", 1024)))
	}
	_, live, clipped := ex.stream.snapshot()
	if len(live) > ckLiveCapBytes {
		t.Errorf("retained %d bytes, cap is %d", len(live), ckLiveCapBytes)
	}
	if !clipped {
		t.Error("clipped is false though the cap was passed, so the view claims it has everything")
	}
	// The tail is kept: a live view that froze at the cap would be worse.
	h(evDelta("THE-NEWEST-TEXT"))
	_, live, _ = ex.stream.snapshot()
	if !strings.HasSuffix(live, "THE-NEWEST-TEXT") {
		t.Error("the newest delta is not at the tail, so the live view stopped updating")
	}
}

// An empty delta is a keep-alive or a role preamble; rendering a caret for it
// would flicker.
func TestCkStream_EmptyDeltaDoesNotOpenTheBlock(t *testing.T) {
	ex := &ckExecState{}
	h := ex.stream.handler()
	h(evStart("quiet"))
	h(evDelta(""))

	th := &ckThread{exec: ex, log: []string{"only this"}}
	if got := strings.Join(th.displayLog(), "\n"); got != "only this" {
		t.Errorf("an empty delta drew a block:\n%q", got)
	}
}

// The worker writes while the render reads, every frame. Run under -race.
func TestCkStream_ConcurrentWriteAndRender(t *testing.T) {
	ex := &ckExecState{}
	h := ex.stream.handler()
	th := &ckThread{exec: ex}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		h(evStart("head"))
		for i := 0; i < 500; i++ {
			h(evDelta("token "))
			if i%100 == 99 {
				h(evFail("boom", "sp"))
				h(evStart("next"))
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			th.gone = ckMergeAbandoned(th.gone, ex.stream.abandoned())
			_ = th.displayLog()
		}
	}()
	wg.Wait()
}

// A nil stream handler means a task simply does not stream, which is what the
// swarm and consensus strategies pass.
func TestCkStream_NilHandlerIsNotAStream(t *testing.T) {
	var s *ckStream
	if s.handler() != nil {
		t.Error("a nil stream produced a handler, which would panic on the first delta")
	}
	th := &ckThread{log: []string{"a"}}
	if got := strings.Join(th.displayLog(), "\n"); got != "a" {
		t.Errorf("a thread with no exec rendered stream furniture: %q", got)
	}
}

// `e` expands the newest collapsed partial, and must not be stolen from the
// input when there is nothing collapsed, "explain this" starts with e.
func TestCkStream_ExpandKeyOnlyActsWhenSomethingIsCollapsed(t *testing.T) {
	m := chatFixture("ask")
	m, _ = keyRune(m, 'e')
	if got := m.th().input; got != "e" {
		t.Errorf("input is %q, want the letter typed when nothing is collapsed", got)
	}

	m = chatFixture("ask")
	m.th().gone = []ckAbandoned{{head: "flaky", text: "a partial", chars: 9, reason: "boom"}}
	m, _ = keyRune(m, 'e')
	if m.th().input != "" {
		t.Errorf("the key typed instead of expanding: input %q", m.th().input)
	}
	if !m.th().gone[0].open {
		t.Error("the partial did not expand")
	}
	if m.th().scroll != 0 {
		t.Errorf("scroll is %d, want 0: the expanded text is at the tail and has to be visible", m.th().scroll)
	}
}

// End to end through the real router, not synthetic events: the stub is
// discovered as an Ollama head via $OLLAMA_HOST, the real executor streams its
// NDJSON, and the text has to arrive in the thread's log.
//
// This is the one line the unit tests above cannot cover, `OnStream: onStream`
// in ckRealDispatchStage. Wire it wrong and every test still passes while the
// TUI shows nothing.
func TestCkRealDispatchStage_FeedsTheThreadLogAsItArrives(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			fmt.Fprint(w, `{"models":[{"name":"qwen2.5-coder:7b","model":"qwen2.5-coder:7b",
			  "details":{"quantization_level":"Q4_K_M","parameter_size":"7B"}}]}`)
		// A port-discovered Ollama head carries Provider "local", so
		// executor.For picks HTTPExecutor and talks to Ollama's
		// OpenAI-compatible endpoint, not /api/generate. The stub serves what
		// the real path asks for.
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			fl, _ := w.(http.Flusher)
			for _, frag := range []string{"streamed ", "through ", "the router"} {
				fmt.Fprintf(w, "data: {\"model\":\"qwen2.5-coder:7b\","+
					"\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", frag)
				if fl != nil {
					fl.Flush()
				}
			}
			fmt.Fprint(w, "data: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	testutil.NewSandbox(t)
	t.Setenv("OLLAMA_HOST", srv.URL)
	if err := config.Save(&config.Config{Cortex: "none"}); err != nil {
		t.Fatal(err)
	}

	ex := &ckExecState{}
	th := &ckThread{exec: ex}
	tk := &ckTask{runID: "r-stream", taskID: "t-stream"}

	out, err := ckRealDispatchStage(context.Background(), tk, "hi", "10", 0, ex.stream.handler())
	if err != nil {
		t.Fatalf("dispatch through the stub failed: %v", err)
	}
	if out.output != "streamed through the router" {
		t.Fatalf("output %q", out.output)
	}
	// The deltas the UI saw must be the answer, not a different rendering of it.
	_, live, _ := ex.stream.snapshot()
	if live != out.output {
		t.Errorf("the log showed %q while the answer is %q", live, out.output)
	}
	if got := strings.Join(th.displayLog(), "\n"); !strings.Contains(got, "streamed through the router") {
		t.Errorf("the streamed text never reached the thread log:\n%q", got)
	}
}

func TestCkStreamReason_OneLineOnly(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"single line", "boom", "boom"},
		{"first line only", "boom\ngoroutine 1 [running]:\nmain.main()", "boom"},
		{"trimmed", "  spaced  ", "spaced"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ckStreamReason(tc.in); got != tc.want {
				t.Errorf("ckStreamReason(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
	if got := ckStreamReason(strings.Repeat("x", 300)); len(got) > 130 {
		t.Errorf("a long reason was not bounded: %d chars", len(got))
	}
}
