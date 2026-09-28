// SPDX-License-Identifier: MIT

package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/provider"
)

// anthropicStub serves a Messages stream and records what was asked for, so a
// test can assert the request as well as what Hydra made of the response.
type anthropicStub struct {
	*httptest.Server
	body   []byte
	header http.Header
	paths  []string
}

// newAnthropicStub points both Anthropic paths at itself through
// ANTHROPIC_BASE_URL, which is also how a gateway is addressed in production.
func newAnthropicStub(t *testing.T, events []string) *anthropicStub {
	t.Helper()
	s := &anthropicStub{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.body, _ = io.ReadAll(r.Body)
		s.header = r.Header.Clone()
		s.paths = append(s.paths, r.URL.Path)
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for _, e := range events {
			fmt.Fprint(w, e)
			if fl != nil {
				fl.Flush()
			}
		}
	}))
	t.Cleanup(s.Server.Close)
	t.Setenv("ANTHROPIC_BASE_URL", s.Server.URL)
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("ANTHROPIC_MODEL", "claude-test-1")
	return s
}

func anthropicHead() provider.Head {
	return provider.Head{
		ID: "anthropic", Name: "Claude", Provider: "anthropic",
		Source: "env", AuthReady: true, Meta: map[string]string{},
	}
}

func sse(eventType, data string) string {
	return fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, data)
}

// The whole answer plus both counts, each from the event that carries it.
const (
	anthropicStart  = `{"type":"message_start","message":{"model":"claude-test-1-20260101","usage":{"input_tokens":31,"output_tokens":1}}}`
	anthropicStop   = `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":64}}`
	anthropicClosed = "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
)

func anthropicText(s string) string {
	b, _ := json.Marshal(s)
	return sse("content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%s}}`, b))
}

func TestAnthropicStream_ReassemblesDeltasAndReportsBothCounts(t *testing.T) {
	newAnthropicStub(t, []string{
		sse("message_start", anthropicStart),
		sse("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		anthropicText("Hello, "),
		anthropicText("world"),
		sse("ping", `{"type":"ping"}`),
		sse("content_block_stop", `{"type":"content_block_stop","index":0}`),
		sse("message_delta", anthropicStop),
		anthropicClosed,
	})

	var got []string
	resp, err := (&HTTPExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "say hello", Head: anthropicHead()},
		func(d string) { got = append(got, d) })
	if err != nil {
		t.Fatal(err)
	}

	if resp.Output != "Hello, world" {
		t.Errorf("Output = %q, want %q", resp.Output, "Hello, world")
	}
	if strings.Join(got, "") != resp.Output {
		t.Errorf("deltas %q do not reassemble to Output %q", got, resp.Output)
	}
	if len(got) != 2 {
		t.Errorf("got %d deltas, want 2: a ping or a stop event was rendered as content", len(got))
	}
	// input_tokens is on message_start and output_tokens on message_delta, so
	// either count alone means one of the two events was thrown away.
	if resp.InputTokens != 31 {
		t.Errorf("InputTokens = %d, want 31 (message_start's count)", resp.InputTokens)
	}
	if resp.OutputTokens != 64 {
		t.Errorf("OutputTokens = %d, want 64 (message_delta's final count, not message_start's 1)", resp.OutputTokens)
	}
	if resp.TokensEstimated {
		t.Error("TokensEstimated = true, but the provider reported both counts")
	}
	if resp.Model != "claude-test-1-20260101" {
		t.Errorf("Model = %q, want the id message_start reported", resp.Model)
	}
	if resp.TTFT <= 0 {
		t.Error("TTFT = 0, want the measured time to the first text delta")
	}
}

func TestAnthropicStream_NoUsageIsEstimatedAndLabelled(t *testing.T) {
	newAnthropicStub(t, []string{
		sse("message_start", `{"type":"message_start","message":{"model":"claude-test-1"}}`),
		anthropicText("an answer with no counts attached"),
		anthropicClosed,
	})

	resp, err := (&HTTPExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "ask", Head: anthropicHead()}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.TokensEstimated {
		t.Error("TokensEstimated = false, so a call with no reported usage would log as free")
	}
	if resp.InputTokens == 0 || resp.OutputTokens == 0 {
		t.Errorf("counts = %d/%d, want estimates rather than zero", resp.InputTokens, resp.OutputTokens)
	}
}

func TestAnthropicStream_MidStreamErrorFailsTheDispatch(t *testing.T) {
	newAnthropicStub(t, []string{
		sse("message_start", anthropicStart),
		anthropicText("this much arrived, then "),
		sse("error", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`),
	})

	resp, err := (&HTTPExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "ask", Head: anthropicHead()}, func(string) {})
	if err == nil {
		t.Fatalf("a mid-stream error returned a partial as the answer: %q", resp.Output)
	}
	if !strings.Contains(err.Error(), "overloaded_error") {
		t.Errorf("error = %v, want the provider's own error type in it", err)
	}
}

func TestAnthropicStream_ThinkingIsNotTheAnswer(t *testing.T) {
	newAnthropicStub(t, []string{
		sse("message_start", anthropicStart),
		sse("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"let me work through this"}}`),
		anthropicText("42"),
		sse("message_delta", anthropicStop),
		anthropicClosed,
	})

	resp, err := (&HTTPExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "ask", Head: anthropicHead()}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Output != "42" {
		t.Errorf("Output = %q, want only the text delta: reasoning is not the answer", resp.Output)
	}
}

func TestAnthropicStream_AsksToStreamAndPinsTheAPIVersion(t *testing.T) {
	stub := newAnthropicStub(t, []string{
		sse("message_start", anthropicStart),
		anthropicText("hi"),
		sse("message_delta", anthropicStop),
		anthropicClosed,
	})

	if _, err := (&HTTPExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "p", System: "be brief", Head: anthropicHead()}, func(string) {}); err != nil {
		t.Fatal(err)
	}

	var sent map[string]any
	if err := json.Unmarshal(stub.body, &sent); err != nil {
		t.Fatalf("request body did not parse: %v (%s)", err, stub.body)
	}
	if sent["stream"] != true {
		t.Errorf("stream = %v, want true: without it the server answers in one buffered block", sent["stream"])
	}
	if sent["system"] != "be brief" {
		t.Errorf("system = %v, want the request's system prompt", sent["system"])
	}
	if got := stub.header.Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("anthropic-version = %q, want the same version the buffered path pins", got)
	}
	if got := stub.header.Get("x-api-key"); got != "test-key" {
		t.Errorf("x-api-key = %q, want the configured key", got)
	}
	if got := stub.header.Get("Accept"); got != "text/event-stream" {
		t.Errorf("Accept = %q, want text/event-stream", got)
	}
}

// Streaming and buffered must resolve the same endpoint, or a gateway serves
// one of them and the other quietly goes to Anthropic itself.
func TestAnthropic_BothPathsResolveTheSameEndpoint(t *testing.T) {
	stub := newAnthropicStub(t, []string{
		sse("message_start", anthropicStart),
		anthropicText("hi"),
		sse("message_delta", anthropicStop),
		anthropicClosed,
	})

	ex := &HTTPExecutor{}
	req := Request{Prompt: "p", Head: anthropicHead()}
	if _, err := ex.ExecuteStream(context.Background(), req, func(string) {}); err != nil {
		t.Fatal(err)
	}
	// The buffered path decodes JSON, so the stream text it gets back is not a
	// valid body; the path it asked for is what this asserts.
	_, _ = ex.Execute(context.Background(), req)

	if len(stub.paths) != 2 {
		t.Fatalf("stub saw %d requests, want both paths to reach it: %v", len(stub.paths), stub.paths)
	}
	if stub.paths[0] != "/v1/messages" || stub.paths[1] != stub.paths[0] {
		t.Errorf("paths = %v, want both at /v1/messages", stub.paths)
	}
}
