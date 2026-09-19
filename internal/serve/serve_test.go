// SPDX-License-Identifier: MIT

package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/executor"
)

// ── the bind refusal ──────────────────────────────────────────────────────────

// A refusal, not a warning. hyctl security reports when another local model
// server answers off-loopback; Hydra serving that way by default would make
// that check describe a risk Hydra itself created.
func TestListen_RefusesAnUnauthenticatedPortOffLoopback(t *testing.T) {
	ln, err := Listen("0.0.0.0:0", "")
	if err == nil {
		_ = ln.Close()
		t.Fatal("bound every interface with no token")
	}
	if !errors.Is(err, ErrExposed) {
		t.Fatalf("err = %v, want ErrExposed", err)
	}
	for _, want := range []string{"--token", "127.0.0.1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

func TestListen_AllowsLoopbackAndAuthenticatedExposure(t *testing.T) {
	ln, err := Listen("127.0.0.1:0", "")
	if err != nil {
		t.Fatalf("loopback with no token was refused: %v", err)
	}
	_ = ln.Close()

	ln, err = Listen("127.0.0.1:0", "tok")
	if err != nil {
		t.Fatalf("loopback with a token was refused: %v", err)
	}
	_ = ln.Close()
}

// An address it cannot parse is treated as exposed, the safe reading of
// something unverifiable.
func TestLoopback_ClassifiesEveryShape(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:8787": true,
		"localhost:8787": true,
		"[::1]:8787":     true,
		"0.0.0.0:8787":   false,
		":8787":          false,
		"192.168.1.5:80": false,
		"garbage":        false,
		"":               false,
	}
	for addr, want := range cases {
		if got := loopback(addr); got != want {
			t.Errorf("loopback(%q) = %v, want %v", addr, got, want)
		}
	}
}

// ── routing ───────────────────────────────────────────────────────────────────

func TestParseRoute_ReadsTheModelFieldAsARoutingInstruction(t *testing.T) {
	cases := []struct {
		model string
		want  Route
	}{
		{"", Route{}},
		{"hydra", Route{}},
		{"HYDRA", Route{}},
		{"hydra/hard", Route{Enum: "HARD"}},
		{"hydra/t4", Route{Tier: "4"}},
		{"ollama/qwen3:0.6b", Route{Head: "ollama/qwen3:0.6b"}},
		// Not a tier: "trivial" also starts with t, and reading it as tier
		// "rivial" would route somewhere nobody asked for.
		{"hydra/trivial", Route{Enum: "TRIVIAL"}},
	}
	for _, c := range cases {
		got, err := parseRoute(c.model)
		if err != nil {
			t.Errorf("parseRoute(%q): %v", c.model, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseRoute(%q) = %+v, want %+v", c.model, got, c.want)
		}
	}
	if _, err := parseRoute("hydra/"); !errors.Is(err, ErrBadRequest) {
		t.Errorf("a trailing slash names no key and must be refused: %v", err)
	}
}

// ── the endpoint ──────────────────────────────────────────────────────────────

type stubRouter struct {
	got    Request
	answer Answer
	err    error
}

func (s *stubRouter) Chat(_ context.Context, r Request) (Answer, error) {
	s.got = r
	return s.answer, s.err
}
func (s *stubRouter) Models() []Model {
	return []Model{{ID: "hydra", Object: "model", OwnedBy: "hydra"}}
}

func post(t *testing.T, r Router, token, auth, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	w := httptest.NewRecorder()
	Handler(r, token).ServeHTTP(w, req)
	return w
}

const oneTurn = `{"model":"hydra","messages":[{"role":"user","content":"hi"}]}`

// An agent loop sends its tool definitions and reads back structured calls.
// Anything lost in between is a loop that never terminates.
func TestChat_ToolsGoDownAndCallsComeBack(t *testing.T) {
	s := &stubRouter{answer: Answer{
		ToolCalls: []executor.ToolCall{{
			ID: "call_1", Type: "function",
			Function: executor.ToolCallFunction{Name: "file_read", Arguments: `{"path":"a.go"}`},
		}},
		FinishReason: "tool_calls", Model: "m", InputTokens: 10, OutputTokens: 3,
	}}
	w := post(t, s, "", "", `{"model":"hydra","messages":[{"role":"user","content":"hi"}],
	 "tools":[{"type":"function","function":{"name":"file_read","parameters":{"type":"object"}}}]}`)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if len(s.got.Tools) != 1 || s.got.Tools[0].Function.Name != "file_read" {
		t.Fatalf("the tools never reached the router: %+v", s.got.Tools)
	}
	// The schema is the caller's own JSON Schema and must survive untouched.
	if !strings.Contains(string(s.got.Tools[0].Function.Parameters), `"type":"object"`) {
		t.Errorf("the parameter schema was mangled: %s", s.got.Tools[0].Function.Parameters)
	}

	var out struct {
		Object  string `json:"object"`
		Choices []struct {
			Message      executor.Message `json:"message"`
			FinishReason string           `json:"finish_reason"`
		} `json:"choices"`
		Usage map[string]int `json:"usage"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("the reply is not valid JSON: %v", err)
	}
	if out.Object != "chat.completion" {
		t.Errorf("object = %q, which no OpenAI client expects", out.Object)
	}
	if len(out.Choices) != 1 || len(out.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("the tool call did not come back: %s", w.Body)
	}
	if out.Choices[0].FinishReason != "tool_calls" {
		t.Errorf("finish_reason = %q; a client branches on this, not on the content",
			out.Choices[0].FinishReason)
	}
	if out.Usage["total_tokens"] != 13 {
		t.Errorf("total_tokens = %d, want 13", out.Usage["total_tokens"])
	}
}

// A head that reported no reason but asked for tools still stopped for one.
func TestChat_FinishReasonIsDerivedWhenTheHeadOmitsIt(t *testing.T) {
	s := &stubRouter{answer: Answer{ToolCalls: []executor.ToolCall{{ID: "c"}}}}
	if w := post(t, s, "", "", oneTurn); !strings.Contains(w.Body.String(), `"finish_reason":"tool_calls"`) {
		t.Fatalf("a tool-calling answer reported no reason: %s", w.Body)
	}
	plain := &stubRouter{answer: Answer{Output: "hello"}}
	if w := post(t, plain, "", "", oneTurn); !strings.Contains(w.Body.String(), `"finish_reason":"stop"`) {
		t.Fatalf("a plain answer did not report stop: %s", w.Body)
	}
}

// A router that cannot stream refuses. Answering with a whole body would be
// worse: the client is parsing SSE and would see a malformed stream rather than
// a message it can act on.
func TestChat_StreamIsRefusedNotQuietlyAnswered(t *testing.T) {
	s := &stubRouter{answer: Answer{Output: "hi"}}
	w := post(t, s, "", "", `{"model":"hydra","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), "cannot stream") {
		t.Errorf("the refusal does not say why: %s", w.Body)
	}
	if s.got.Messages != nil {
		t.Error("the request was dispatched anyway, so it was paid for and discarded")
	}
}

// The newer spelling is what open-code-review sends; reading only the old one
// silently ignores the caller's ceiling.
func TestChat_HonoursBothMaxTokenSpellings(t *testing.T) {
	s := &stubRouter{answer: Answer{Output: "x"}}
	post(t, s, "", "", `{"model":"hydra","max_completion_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)
	if s.got.MaxTokens != 64 {
		t.Errorf("max_completion_tokens ignored: got %d", s.got.MaxTokens)
	}
	post(t, s, "", "", `{"model":"hydra","max_tokens":32,"messages":[{"role":"user","content":"hi"}]}`)
	if s.got.MaxTokens != 32 {
		t.Errorf("max_tokens ignored: got %d", s.got.MaxTokens)
	}
}

func TestChat_RefusesWhatItCannotAct(t *testing.T) {
	s := &stubRouter{answer: Answer{Output: "x"}}
	if w := post(t, s, "", "", `{"model":"hydra","messages":[]}`); w.Code != http.StatusBadRequest {
		t.Errorf("empty messages: status %d, want 400", w.Code)
	}
	if w := post(t, s, "", "", `not json`); w.Code != http.StatusBadRequest {
		t.Errorf("malformed body: status %d, want 400", w.Code)
	}
	if w := post(t, s, "", "", `{"model":"hydra/","messages":[{"role":"user","content":"hi"}]}`); w.Code != http.StatusBadRequest {
		t.Errorf("unusable routing key: status %d, want 400", w.Code)
	}
}

// A caller's mistake must not read as Hydra having failed, and the reverse.
func TestChat_SeparatesTheCallersFaultFromTheRouters(t *testing.T) {
	bad := &stubRouter{err: fmt.Errorf("%w: unknown routing key", ErrBadRequest)}
	if w := post(t, bad, "", "", oneTurn); w.Code != http.StatusBadRequest {
		t.Errorf("a caller mistake answered %d, want 400", w.Code)
	}
	broke := &stubRouter{err: errors.New("every head refused")}
	w := post(t, broke, "", "", oneTurn)
	if w.Code != http.StatusBadGateway {
		t.Errorf("a routing failure answered %d, want 502", w.Code)
	}
	if !strings.Contains(w.Body.String(), "every head refused") {
		t.Errorf("the reason was swallowed: %s", w.Body)
	}
}

func TestHandler_TokenGuardsEveryEndpoint(t *testing.T) {
	s := &stubRouter{answer: Answer{Output: "x"}}
	if w := post(t, s, "secret", "", oneTurn); w.Code != http.StatusUnauthorized {
		t.Errorf("no token: status %d, want 401", w.Code)
	}
	if w := post(t, s, "secret", "Bearer wrong", oneTurn); w.Code != http.StatusUnauthorized {
		t.Errorf("wrong token: status %d, want 401", w.Code)
	}
	if w := post(t, s, "secret", "Bearer secret", oneTurn); w.Code != http.StatusOK {
		t.Errorf("right token: status %d, want 200: %s", w.Code, w.Body)
	}
	// The listing is as gated as the completion: it enumerates this machine.
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	w := httptest.NewRecorder()
	Handler(s, "secret").ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("/v1/models unauthenticated: status %d, want 401", w.Code)
	}
}

func TestModels_ListsInTheShapeAClientReads(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	w := httptest.NewRecorder()
	Handler(&stubRouter{}, "").ServeHTTP(w, req)

	var out struct {
		Object string  `json:"object"`
		Data   []Model `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if out.Object != "list" || len(out.Data) != 1 || out.Data[0].ID != "hydra" {
		t.Fatalf("listing is not the shape a client reads: %s", w.Body)
	}
}

func TestHandler_WrongMethodsAreRefused(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	w := httptest.NewRecorder()
	Handler(&stubRouter{}, "").ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET on completions: status %d, want 405", w.Code)
	}
}

// An agent loop's prompt grows with every tool result, so the cap is generous,
// but unbounded is a memory exhaustion away.
func TestChat_OversizeBodyIsRefusedNotBuffered(t *testing.T) {
	body := `{"model":"hydra","messages":[{"role":"user","content":"` +
		strings.Repeat("a", maxBody) + `"}]}`
	if w := post(t, &stubRouter{}, "", "", body); w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status %d, want 413", w.Code)
	}
}

func TestServe_StopsWhenTheContextIsDone(t *testing.T) {
	ln, err := Listen("127.0.0.1:0", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, ln, Handler(&stubRouter{}, "")) }()
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("shutdown returned %v", err)
	}
}
