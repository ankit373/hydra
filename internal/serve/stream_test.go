// SPDX-License-Identifier: MIT

package serve

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/executor"
)

var errRouterDown = errors.New("every head refused")

// streamStub replays a fixed sequence of deltas, then answers. refused records
// that the consumer asked it to stop, which is what a real router turns into a
// cancelled run.
type streamStub struct {
	deltas  []Delta
	answer  Answer
	err     error
	refused bool
	sent    int
}

func (s *streamStub) Chat(_ context.Context, _ Request) (Answer, error) { return s.answer, s.err }
func (s *streamStub) Models() []Model                                   { return nil }

func (s *streamStub) ChatStream(_ context.Context, _ Request, onDelta OnDelta) (Answer, error) {
	for _, d := range s.deltas {
		if err := onDelta(d); err != nil {
			s.refused = true
			break
		}
		s.sent++
	}
	return s.answer, s.err
}

func postStream(t *testing.T, r Router, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	w := httptest.NewRecorder()
	Handler(r, "").ServeHTTP(w, req)
	return w
}

const streamTurn = `{"model":"hydra","stream":true,"messages":[{"role":"user","content":"hi"}]}`

func TestStream_SendsChunksThenDone(t *testing.T) {
	s := &streamStub{
		deltas: []Delta{{Text: "one ", Head: "a", Model: "A"}, {Text: "two", Head: "a", Model: "A"}},
		answer: Answer{Output: "one two", Head: "a", Model: "A"},
	}
	w := postStream(t, s, streamTurn)

	if got := w.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("content type %q, want text/event-stream", got)
	}
	body := w.Body.String()
	for _, want := range []string{
		`"role":"assistant"`, `"content":"one "`, `"content":"two"`,
		`"finish_reason":"stop"`, "data: [DONE]",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the stream is missing %s:\n%s", want, body)
		}
	}
	// The deltas already carried the whole answer. Sending Output as well is
	// how a surface prints the answer twice.
	if strings.Count(body, `"content":"one two"`) != 0 {
		t.Errorf("the whole output was sent as well as the deltas:\n%s", body)
	}
	// Only the last chunk may name a reason; a client reads a non-null one as
	// the answer being over.
	if n := strings.Count(body, `"finish_reason":null`); n != 2 {
		t.Errorf("%d chunks carried an explicit null reason, want 2:\n%s", n, body)
	}
}

// The model field is a routing instruction, so the chunks have to name the head
// that answered rather than echo "hydra" back.
func TestStream_ChunksNameTheHeadNotTheRoutingKey(t *testing.T) {
	s := &streamStub{
		deltas: []Delta{{Text: "x", Head: "ollama/qwen3:4b", Model: "Qwen3 4B"}},
		answer: Answer{Output: "x", Head: "ollama/qwen3:4b", Model: "Qwen3 4B"},
	}
	body := postStream(t, s, streamTurn).Body.String()
	if !strings.Contains(body, `"model":"Qwen3 4B"`) {
		t.Errorf("the chunks do not name the answering head:\n%s", body)
	}
}

// A head that fails before producing anything never reaches the wire, so the
// chain advances and the client only ever sees the head that answered.
func TestStream_FallbackBeforeTheFirstByteIsInvisible(t *testing.T) {
	s := &streamStub{
		deltas: []Delta{{Text: "hello", Head: "b", Model: "B"}},
		answer: Answer{Output: "hello", Head: "b", Model: "B"},
	}
	body := postStream(t, s, streamTurn).Body.String()
	if strings.Contains(body, "error") {
		t.Errorf("a clean fallback reported an error:\n%s", body)
	}
	if !strings.Contains(body, `"model":"B"`) || !strings.Contains(body, "data: [DONE]") {
		t.Errorf("the second head's answer did not stream cleanly:\n%s", body)
	}
}

// Bytes on the wire cannot be retracted, so a second head's output would arrive
// appended to the first one's and read as one answer.
func TestStream_HeadChangeAfterOutputEndsTheStream(t *testing.T) {
	s := &streamStub{
		deltas: []Delta{
			{Text: "from a", Head: "a", Model: "A"},
			{Text: "from b", Head: "b", Model: "B"},
		},
		answer: Answer{Output: "from b", Head: "b", Model: "B"},
	}
	w := postStream(t, s, streamTurn)
	body := w.Body.String()

	if w.Code != http.StatusOK {
		t.Fatalf("status %d: the stream had already opened, so it cannot change", w.Code)
	}
	if strings.Contains(body, "from b") {
		t.Errorf("the second head's output was appended to the first's:\n%s", body)
	}
	if !strings.Contains(body, `"error"`) || !strings.Contains(body, "took over from") {
		t.Errorf("the stream ended without saying what happened:\n%s", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Errorf("the stream was left open:\n%s", body)
	}
	if !s.refused {
		t.Error("the router was not told to stop, so it would finish and be charged for an answer nobody gets")
	}
}

// An empty delta is accepted, so one arriving after the refusal must not clear
// it and hand the stream back to a head it had already been taken from.
func TestStream_AnEmptyDeltaDoesNotClearTheRefusal(t *testing.T) {
	s := &streamStub{
		deltas: []Delta{
			{Text: "from a", Head: "a", Model: "A"},
			{Text: "", Head: "b", Model: "B"},
			{Text: "from b", Head: "b", Model: "B"},
		},
		answer: Answer{Output: "from b", Head: "b", Model: "B"},
	}
	body := postStream(t, s, streamTurn).Body.String()
	if strings.Contains(body, "from b") {
		t.Errorf("an empty delta reopened the stream to the second head:\n%s", body)
	}
}

// Until something has been written this is still an ordinary HTTP exchange, so
// the client gets a status it can branch on.
func TestStream_ErrorBeforeAnyOutputKeepsTheStatusLine(t *testing.T) {
	s := &streamStub{err: ErrBadRequest}
	w := postStream(t, s, streamTurn)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", w.Code, w.Body)
	}
	if got := w.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Errorf("content type %q: a refused request is not a stream", got)
	}

	bad := &streamStub{err: errRouterDown}
	if w := postStream(t, bad, streamTurn); w.Code != http.StatusBadGateway {
		t.Errorf("status %d, want 502: %s", w.Code, w.Body)
	}
}

// After the first byte the status line is gone, so the error has to travel in
// the stream, which is where a client parsing SSE will see it.
func TestStream_ErrorAfterOutputTravelsInTheStream(t *testing.T) {
	s := &streamStub{
		deltas: []Delta{{Text: "partial", Head: "a", Model: "A"}},
		err:    errRouterDown,
	}
	w := postStream(t, s, streamTurn)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: the headers were already sent", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "partial") || !strings.Contains(body, `"error"`) {
		t.Errorf("the partial answer or the error is missing:\n%s", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Errorf("the stream was left open:\n%s", body)
	}
}

// A cache hit runs no head and so emits no deltas. The answer is still the
// whole answer, and a client that receives nothing has been given a wrong one.
func TestStream_AnAnswerWithNoDeltasIsStillSent(t *testing.T) {
	s := &streamStub{answer: Answer{Output: "remembered", Head: "a", Model: "A"}}
	body := postStream(t, s, streamTurn).Body.String()
	if !strings.Contains(body, `"content":"remembered"`) {
		t.Errorf("an answer that emitted no deltas was dropped:\n%s", body)
	}
	if !strings.Contains(body, `"finish_reason":"stop"`) {
		t.Errorf("the stream did not finish:\n%s", body)
	}
}

func TestStream_ToolCallsArriveBeforeTheFinishChunk(t *testing.T) {
	call := executor.ToolCall{ID: "call_1", Type: "function"}
	call.Function.Name = "read_file"
	call.Function.Arguments = `{"path":"main.go"}`
	s := &streamStub{
		deltas: []Delta{{Text: "looking", Head: "a", Model: "A"}},
		answer: Answer{Output: "looking", ToolCalls: []executor.ToolCall{call}, Head: "a", Model: "A"},
	}
	body := postStream(t, s, streamTurn).Body.String()

	tools := strings.Index(body, `"tool_calls"`)
	finish := strings.Index(body, `"finish_reason":"tool_calls"`)
	if tools < 0 || finish < 0 {
		t.Fatalf("the call or the reason is missing:\n%s", body)
	}
	if tools > finish {
		t.Errorf("the call arrived after the finish chunk, which a client has already stopped reading for:\n%s", body)
	}
	if !strings.Contains(body, "read_file") {
		t.Errorf("the call carries no function name:\n%s", body)
	}
}

// Sending usage unasked is what the spec says not to do, and a client summing
// what it receives would count it twice.
func TestStream_UsageOnlyWhenTheClientAsks(t *testing.T) {
	answer := Answer{Output: "x", Head: "a", Model: "A", InputTokens: 5, OutputTokens: 8}

	plain := postStream(t, &streamStub{deltas: []Delta{{Text: "x", Head: "a"}}, answer: answer}, streamTurn)
	if strings.Contains(plain.Body.String(), `"usage"`) {
		t.Errorf("usage was sent unasked:\n%s", plain.Body)
	}

	asked := postStream(t, &streamStub{deltas: []Delta{{Text: "x", Head: "a"}}, answer: answer},
		`{"model":"hydra","stream":true,"stream_options":{"include_usage":true},`+
			`"messages":[{"role":"user","content":"hi"}]}`)
	body := asked.Body.String()
	if !strings.Contains(body, `"total_tokens":13`) {
		t.Errorf("the usage chunk is missing or wrong:\n%s", body)
	}
}

// One request must not report two different reasons depending on which way the
// client asked for it.
func TestStream_FinishReasonMatchesTheWholeBodyPath(t *testing.T) {
	answer := Answer{ToolCalls: []executor.ToolCall{{ID: "c"}}, Head: "a", Model: "A"}

	whole := postStream(t, &streamStub{answer: answer},
		`{"model":"hydra","messages":[{"role":"user","content":"hi"}]}`).Body.String()
	streamed := postStream(t, &streamStub{answer: answer}, streamTurn).Body.String()

	if !strings.Contains(whole, `"finish_reason":"tool_calls"`) ||
		!strings.Contains(streamed, `"finish_reason":"tool_calls"`) {
		t.Errorf("the two paths disagree:\nwhole:    %s\nstreamed: %s", whole, streamed)
	}
}
