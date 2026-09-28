// SPDX-License-Identifier: MIT

package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/executor"
)

// streamRouter replays a fixed event sequence, which is how a fallback chain is
// reproduced without a model: what matters here is the order deltas and
// failures arrive in, not what produced them.
type streamRouter struct {
	events    []Event
	answer    Answer
	err       error
	got       Request
	cancelled bool
}

func (s *streamRouter) Chat(ctx context.Context, r Request) (Answer, error) {
	s.got = r
	for _, e := range s.events {
		if r.OnEvent != nil {
			r.OnEvent(e)
		}
	}
	select {
	case <-ctx.Done():
		s.cancelled = true
	default:
	}
	return s.answer, s.err
}

func (s *streamRouter) Models() []Model { return nil }

// flushRecorder counts flushes, because a frame written but not flushed is a
// frame the client has not got.
type flushRecorder struct {
	*httptest.ResponseRecorder
	flushes int
}

func (f *flushRecorder) Flush() { f.flushes++; f.ResponseRecorder.Flush() }

func stream(t *testing.T, r Router, body string) *flushRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	w := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	Handler(r, "").ServeHTTP(w, req)
	return w
}

const streamTurn = `{"model":"hydra","stream":true,"messages":[{"role":"user","content":"hi"}]}`

// frames parses the SSE body into its chunks, and reports whether the stream
// was terminated with [DONE].
func frames(t *testing.T, body string) ([]map[string]any, bool) {
	t.Helper()
	var out []map[string]any
	done := false
	for _, block := range strings.Split(body, "\n\n") {
		line := strings.TrimSpace(block)
		if line == "" {
			continue
		}
		payload, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			t.Fatalf("a frame is not an SSE data line: %q", line)
		}
		if payload == "[DONE]" {
			done = true
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(payload), &m); err != nil {
			t.Fatalf("frame is not JSON: %v (%s)", err, payload)
		}
		out = append(out, m)
	}
	return out, done
}

// choice reads a frame's one choice, failing rather than panicking on a frame
// that has none: a panic takes the whole test binary down with it and hides
// every test that had not run yet.
func choice(t *testing.T, f map[string]any) map[string]any {
	t.Helper()
	choices, ok := f["choices"].([]any)
	if !ok || len(choices) == 0 {
		t.Fatalf("frame carries no choice: %v", f)
	}
	c, _ := choices[0].(map[string]any)
	return c
}

func deltaOf(t *testing.T, f map[string]any) map[string]any {
	t.Helper()
	choices, ok := f["choices"].([]any)
	if !ok || len(choices) == 0 {
		return nil
	}
	d, _ := choices[0].(map[string]any)["delta"].(map[string]any)
	return d
}

func TestStream_SpeaksTheChunkProtocol(t *testing.T) {
	r := &streamRouter{
		events: []Event{
			{Kind: EventDelta, Text: "Hel", Head: "h1", Model: "m1"},
			{Kind: EventDelta, Text: "lo", Head: "h1", Model: "m1"},
		},
		answer: Answer{Output: "Hello", Head: "h1", Model: "m1"},
	}
	w := stream(t, r, streamTurn)

	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q, which no SSE client will read", ct)
	}
	fs, done := frames(t, w.Body.String())
	if !done {
		t.Error("the stream never sent [DONE], so a client waits for ever")
	}
	if len(fs) < 4 {
		t.Fatalf("got %d frames, want role, two deltas and a finish: %s", len(fs), w.Body)
	}
	if fs[0]["object"] != "chat.completion.chunk" {
		t.Errorf("object = %v, which no OpenAI client expects", fs[0]["object"])
	}
	if deltaOf(t, fs[0])["role"] != "assistant" {
		t.Errorf("the first chunk does not carry the role: %v", fs[0])
	}
	var text string
	for _, f := range fs[1:] {
		if c, ok := deltaOf(t, f)["content"].(string); ok {
			text += c
		}
	}
	if text != "Hello" {
		t.Errorf("the deltas spell %q, want Hello", text)
	}
	last := choice(t, fs[len(fs)-1])
	if last["finish_reason"] != "stop" {
		t.Errorf("the last chunk's finish_reason is %v, want stop", last["finish_reason"])
	}
	// Every chunk of one answer carries one id, which is how a client groups
	// them, and the head that answered rather than the key the client asked for.
	for _, f := range fs {
		if f["id"] != fs[0]["id"] {
			t.Fatalf("the id changed mid-stream: %v then %v", fs[0]["id"], f["id"])
		}
		if f["model"] != "m1" {
			t.Errorf("model = %v, want the head that answered", f["model"])
		}
	}
}

// A router that ignores OnEvent still answers. That is what lets a surface be
// written once, and the property the whole optional-callback shape rests on.
func TestStream_ARouterThatDoesNotStreamStillAnswers(t *testing.T) {
	w := stream(t, &stubRouter{answer: Answer{Output: "whole answer", Model: "m"}}, streamTurn)
	fs, done := frames(t, w.Body.String())
	if !done {
		t.Fatal("no [DONE]")
	}
	var text string
	for _, f := range fs {
		if c, ok := deltaOf(t, f)["content"].(string); ok {
			text += c
		}
	}
	if text != "whole answer" {
		t.Errorf("the buffered answer arrived as %q", text)
	}
}

// The other half of that: an answer already streamed must not be sent again
// when Chat returns it whole.
func TestStream_DoesNotRepeatWhatItAlreadyStreamed(t *testing.T) {
	r := &streamRouter{
		events: []Event{{Kind: EventDelta, Text: "Hello", Head: "h1"}},
		answer: Answer{Output: "Hello", Head: "h1"},
	}
	w := stream(t, r, streamTurn)
	if n := strings.Count(w.Body.String(), "Hello"); n != 1 {
		t.Errorf("Hello appears %d times, so the client renders the answer twice:\n%s", n, w.Body)
	}
}

func TestStream_UsageOnlyWhenTheClientAsks(t *testing.T) {
	answer := Answer{Output: "x", InputTokens: 31, OutputTokens: 64}

	w := stream(t, &stubRouter{answer: answer}, streamTurn)
	if strings.Contains(w.Body.String(), "usage") {
		t.Errorf("usage was sent unasked, which is not the documented shape: %s", w.Body)
	}

	w = stream(t, &stubRouter{answer: answer},
		`{"model":"hydra","stream":true,"stream_options":{"include_usage":true},`+
			`"messages":[{"role":"user","content":"hi"}]}`)
	fs, _ := frames(t, w.Body.String())
	last := fs[len(fs)-1]
	usage, ok := last["usage"].(map[string]any)
	if !ok {
		t.Fatalf("no usage chunk: %s", w.Body)
	}
	if usage["total_tokens"] != float64(95) {
		t.Errorf("total_tokens = %v, want 95", usage["total_tokens"])
	}
	if choices, _ := last["choices"].([]any); len(choices) != 0 {
		t.Errorf("the usage chunk must carry an empty choices array, got %v", choices)
	}
}

func TestStream_ToolCallsCarryAnIndexEvenAtZero(t *testing.T) {
	r := &stubRouter{answer: Answer{
		ToolCalls: []executor.ToolCall{{
			ID: "call_1", Type: "function",
			Function: executor.ToolCallFunction{Name: "file_read", Arguments: `{"path":"a.go"}`},
		}},
		FinishReason: "tool_calls",
	}}
	w := stream(t, r, streamTurn)

	fs, done := frames(t, w.Body.String())
	if !done {
		t.Fatal("no [DONE]")
	}
	var call map[string]any
	for _, f := range fs {
		if tc, ok := deltaOf(t, f)["tool_calls"].([]any); ok && len(tc) > 0 {
			call = tc[0].(map[string]any)
		}
	}
	if call == nil {
		t.Fatalf("the tool call never reached the stream: %s", w.Body)
	}
	// omitempty on the executor's own Index would drop this, and a client
	// reassembling by index cannot tell a missing zero from no index at all.
	if _, ok := call["index"]; !ok {
		t.Error("the tool call frame has no index")
	}
	fn := call["function"].(map[string]any)
	if fn["name"] != "file_read" || fn["arguments"] != `{"path":"a.go"}` {
		t.Errorf("the call was mangled: %v", call)
	}
	last := choice(t, fs[len(fs)-1])
	if last["finish_reason"] != "tool_calls" {
		t.Errorf("finish_reason = %v, want tool_calls", last["finish_reason"])
	}
}

// The common fallback: a head refused before saying anything. Nothing has
// reached the client, so the next head's answer is the only one it sees.
func TestStream_ASilentFallbackIsInvisible(t *testing.T) {
	r := &streamRouter{
		events: []Event{
			{Kind: EventAttemptFailed, Head: "dead", Reason: "connection refused"},
			{Kind: EventDelta, Text: "answer", Head: "live"},
		},
		answer: Answer{Output: "answer", Head: "live"},
	}
	w := stream(t, r, streamTurn)

	body := w.Body.String()
	if strings.Contains(body, "error") || strings.Contains(body, "connection refused") {
		t.Errorf("a silent fallback leaked into the stream:\n%s", body)
	}
	if _, done := frames(t, body); !done {
		t.Errorf("the stream did not complete: %s", body)
	}
}

// A head that failed after emitting cannot be taken back. Appending the next
// head's answer would compose a reply no head ever gave.
func TestStream_AFallbackAfterOutputEndsTheStream(t *testing.T) {
	r := &streamRouter{
		events: []Event{
			{Kind: EventDelta, Text: "half an ans", Head: "flaky"},
			{Kind: EventAttemptFailed, Head: "flaky", Reason: "EOF", SpanID: "abc123"},
			{Kind: EventDelta, Text: "a whole different answer", Head: "next"},
		},
		answer: Answer{Output: "a whole different answer", Head: "next"},
	}
	w := stream(t, r, streamTurn)

	body := w.Body.String()
	if strings.Contains(body, "a whole different answer") {
		t.Errorf("the next head's answer was appended to an abandoned partial:\n%s", body)
	}
	fs, done := frames(t, body)
	if done {
		t.Error("[DONE] means the answer completed, and this one did not")
	}
	last := fs[len(fs)-1]
	e, ok := last["error"].(map[string]any)
	if !ok {
		t.Fatalf("the stream ended with no error frame: %s", body)
	}
	msg, _ := e["message"].(string)
	for _, want := range []string{"flaky", "EOF", "abc123"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the error does not mention %q: %q", want, msg)
		}
	}
	if !r.cancelled {
		t.Error("the dispatch carried on after the stream ended, so it is spending on an answer nobody reads")
	}
}

// Nothing has been written yet, so this is still a real status code. A 200 with
// an empty stream reads as the model saying nothing.
func TestStream_AnErrorBeforeAnyFrameIsAStatusCode(t *testing.T) {
	w := stream(t, &streamRouter{err: ErrBadRequest}, streamTurn)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", w.Code, w.Body)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content type %q, want application/json for a refusal", ct)
	}
}

// An error after the answer started can only be in-band, and must not end with
// [DONE], which would tell the client the partial was the whole answer.
func TestStream_AnErrorAfterOutputIsAnInBandFrame(t *testing.T) {
	r := &streamRouter{
		events: []Event{{Kind: EventDelta, Text: "partial", Head: "h"}},
		err:    context.DeadlineExceeded,
	}
	w := stream(t, r, streamTurn)
	fs, done := frames(t, w.Body.String())
	if done {
		t.Error("[DONE] after a failure tells the client the partial was the answer")
	}
	if _, ok := fs[len(fs)-1]["error"]; !ok {
		t.Errorf("no error frame: %s", w.Body)
	}
}

func TestStream_FlushesEveryFrame(t *testing.T) {
	r := &streamRouter{
		events: []Event{
			{Kind: EventDelta, Text: "a", Head: "h"},
			{Kind: EventDelta, Text: "b", Head: "h"},
			{Kind: EventDelta, Text: "c", Head: "h"},
		},
		answer: Answer{Output: "abc", Head: "h"},
	}
	w := stream(t, r, streamTurn)
	fs, _ := frames(t, w.Body.String())
	// One flush per frame, plus the [DONE] line.
	if w.flushes < len(fs)+1 {
		t.Errorf("%d flushes for %d frames: a frame held back is a frame the client has not got",
			w.flushes, len(fs))
	}
}

// A client that hung up must not go on being paid for.
func TestStream_ADisconnectedClientCancelsTheDispatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(streamTurn)).WithContext(ctx)
	r := &streamRouter{answer: Answer{Output: "x"}}
	Handler(r, "").ServeHTTP(&flushRecorder{ResponseRecorder: httptest.NewRecorder()}, req)

	if !r.cancelled {
		t.Error("the router's context outlived the client")
	}
}

// The streamed and buffered paths must ask the router for the same thing, or
// one of them is quietly a different request.
func TestStream_AsksTheRouterForTheSameThingTheBufferedPathDoes(t *testing.T) {
	const body = `{"model":"hydra/hard","max_completion_tokens":128,
	 "messages":[{"role":"user","content":"hi"}],
	 "tools":[{"type":"function","function":{"name":"file_read"}}]}`

	buffered := &stubRouter{answer: Answer{Output: "x"}}
	post(t, buffered, "", "", body)

	streamed := &streamRouter{answer: Answer{Output: "x"}}
	stream(t, streamed, strings.Replace(body, `"model":"hydra/hard",`, `"model":"hydra/hard","stream":true,`, 1))

	got, want := streamed.got, buffered.got
	got.OnEvent, want.OnEvent = nil, nil
	if got.Route != want.Route || got.MaxTokens != want.MaxTokens || len(got.Tools) != len(want.Tools) {
		t.Errorf("the two paths asked for different things:\n streamed %+v\n buffered %+v", got, want)
	}
}
