// SPDX-License-Identifier: MIT

package executor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/testutil"
)

func anthropicEnv(t *testing.T) {
	t.Helper()
	s := testutil.NewSandbox(t)
	s.SetKey(t, "ANTHROPIC_API_KEY", "sk-ant-test")
	t.Setenv("ANTHROPIC_MODEL", "claude-test-1")
}

// readFile and its result: the shape an agent loop actually sends back, which
// anthropicBody used to throw away in favour of req.Prompt alone.
var agentTurns = []Message{
	{Role: "system", Content: "be terse"},
	{Role: "user", Content: "read a.go"},
	{Role: "assistant", Content: "looking", ToolCalls: []ToolCall{
		{ID: "toolu_1", Type: "function", Function: ToolCallFunction{Name: "file_read", Arguments: `{"path":"a.go"}`}},
	}},
	{Role: "tool", ToolCallID: "toolu_1", Content: "package main"},
}

func TestAnthropicConversation_CarriesEveryTurn(t *testing.T) {
	system, msgs, err := anthropicConversation(agentTurns)
	if err != nil {
		t.Fatal(err)
	}
	if system != "be terse" {
		t.Errorf("system = %q, want it lifted out as the top-level field", system)
	}
	if len(msgs) != 3 {
		t.Fatalf("got %d messages, want user, assistant, user: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != "user" || msgs[0].Content[0].Text != "read a.go" {
		t.Errorf("first turn is %+v", msgs[0])
	}

	a := msgs[1]
	if a.Role != "assistant" || len(a.Content) != 2 {
		t.Fatalf("assistant turn is %+v, want its text and its tool_use", a)
	}
	if a.Content[1].Type != "tool_use" || a.Content[1].ID != "toolu_1" || a.Content[1].Name != "file_read" {
		t.Errorf("tool_use block is %+v", a.Content[1])
	}
	// OpenAI's arguments are a JSON string, Anthropic's input is an object.
	var input map[string]string
	if err := json.Unmarshal(a.Content[1].Input, &input); err != nil {
		t.Fatalf("input is not an object: %s", a.Content[1].Input)
	}
	if input["path"] != "a.go" {
		t.Errorf("input = %s", a.Content[1].Input)
	}

	r := msgs[2]
	if r.Role != "user" || r.Content[0].Type != "tool_result" || r.Content[0].ToolUseID != "toolu_1" {
		t.Errorf("the result did not come back as a tool_result in a user turn: %+v", r)
	}
}

// Anthropic refuses a request whose roles do not alternate, and two tool
// results in a row are two user turns unless they are merged.
func TestAnthropicConversation_MergesConsecutiveTurnsOfOneRole(t *testing.T) {
	_, msgs, err := anthropicConversation([]Message{
		{Role: "user", Content: "one"},
		{Role: "assistant", ToolCalls: []ToolCall{
			{ID: "a", Function: ToolCallFunction{Name: "x", Arguments: "{}"}},
			{ID: "b", Function: ToolCallFunction{Name: "y", Arguments: "{}"}},
		}},
		{Role: "tool", ToolCallID: "a", Content: "first"},
		{Role: "tool", ToolCallID: "b", Content: "second"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 {
		t.Fatalf("got %d messages, want the two results merged into one turn: %+v", len(msgs), msgs)
	}
	if last := msgs[2]; len(last.Content) != 2 || last.Content[1].ToolUseID != "b" {
		t.Errorf("the second result is missing or split: %+v", last)
	}
	for i := 1; i < len(msgs); i++ {
		if msgs[i].Role == msgs[i-1].Role {
			t.Fatalf("roles do not alternate at %d: %+v", i, msgs)
		}
	}
}

// Sending {} would be a call the model reads as "no arguments", which is a
// wrong answer rather than a degraded one.
func TestAnthropicConversation_RefusesArgumentsThatAreNotAnObject(t *testing.T) {
	_, _, err := anthropicConversation([]Message{
		{Role: "assistant", ToolCalls: []ToolCall{
			{ID: "toolu_1", Function: ToolCallFunction{Name: "file_read", Arguments: "not json"}},
		}},
	})
	if err == nil {
		t.Fatal("malformed arguments were accepted, so the head is sent a call it will misread")
	}
	for _, want := range []string{"toolu_1", "file_read"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
}

func TestAnthropicToolChoice_TranslatesEverySpelling(t *testing.T) {
	cases := []struct {
		raw       string
		want      map[string]any
		sendTools bool
	}{
		{``, nil, true},
		{`"auto"`, map[string]any{"type": "auto"}, true},
		{`"required"`, map[string]any{"type": "any"}, true},
		{`"none"`, nil, false},
		{`{"type":"function","function":{"name":"file_read"}}`,
			map[string]any{"type": "tool", "name": "file_read"}, true},
	}
	for _, c := range cases {
		got, send, err := anthropicToolChoice(json.RawMessage(c.raw))
		if err != nil {
			t.Errorf("%s: %v", c.raw, err)
			continue
		}
		if send != c.sendTools {
			t.Errorf("%s: sendTools = %v, want %v", c.raw, send, c.sendTools)
		}
		if len(got) != len(c.want) {
			t.Errorf("%s: got %v, want %v", c.raw, got, c.want)
			continue
		}
		for k, v := range c.want {
			if got[k] != v {
				t.Errorf("%s: %s = %v, want %v", c.raw, k, got[k], v)
			}
		}
	}

	// A constraint that cannot be expressed is refused. Falling back to auto
	// would answer a question the caller did not ask, and a tool-using client
	// cannot tell the difference.
	if _, _, err := anthropicToolChoice(json.RawMessage(`"whatever"`)); err == nil {
		t.Error("an unmappable tool_choice was quietly dropped")
	}
}

func TestExecuteAnthropic_SendsToolsAndReadsTheCallBack(t *testing.T) {
	anthropicEnv(t)
	srv, got := serve(t, 200, `{"model":"claude-test-1","stop_reason":"tool_use","content":[
		{"type":"text","text":"looking"},
		{"type":"tool_use","id":"toolu_1","name":"file_read","input":{"path":"a.go"}}],
		"usage":{"input_tokens":31,"output_tokens":12}}`)

	resp, err := redirect(srv).Execute(context.Background(), Request{
		Messages: agentTurns, MaxTokens: 256, Head: head("anthropic"),
		Tools: []ToolDef{{Type: "function", Function: ToolFunction{
			Name: "file_read", Description: "read a file",
			Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
		}}},
		ToolChoice: json.RawMessage(`"auto"`),
	})
	if err != nil {
		t.Fatal(err)
	}

	tools, ok := got.body["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("no tools on the wire: %v", got.body)
	}
	tool := tools[0].(map[string]any)
	if tool["name"] != "file_read" {
		t.Errorf("tool name = %v", tool["name"])
	}
	// Anthropic names it input_schema, and requires it.
	if _, ok := tool["input_schema"].(map[string]any); !ok {
		t.Errorf("no input_schema, which the API rejects: %v", tool)
	}
	if choice, _ := got.body["tool_choice"].(map[string]any); choice["type"] != "auto" {
		t.Errorf("tool_choice = %v, want {type: auto}", got.body["tool_choice"])
	}
	if msgs, _ := got.body["messages"].([]any); len(msgs) != 3 {
		t.Errorf("the conversation arrived as %d messages, want 3", len(msgs))
	}

	if len(resp.ToolCalls) != 1 {
		t.Fatalf("the tool_use block did not come back as a call: %+v", resp)
	}
	c := resp.ToolCalls[0]
	if c.ID != "toolu_1" || c.Function.Name != "file_read" || c.Function.Arguments != `{"path":"a.go"}` {
		t.Errorf("call read as %+v", c)
	}
	// stop_reason is Anthropic's word; the client branches on OpenAI's.
	if resp.FinishReason != "tool_calls" {
		t.Errorf("finish reason = %q, want tool_calls", resp.FinishReason)
	}
	if resp.Output != "looking" {
		t.Errorf("text blocks read as %q", resp.Output)
	}
}

// tool_choice "none" is expressed by withholding the tools, which every
// version of the API honours.
func TestExecuteAnthropic_NoneWithholdsTheToolsEntirely(t *testing.T) {
	anthropicEnv(t)
	srv, got := serve(t, 200, `{"content":[{"type":"text","text":"ok"}]}`)

	if _, err := redirect(srv).Execute(context.Background(), Request{
		Prompt: "hi", Head: head("anthropic"),
		Tools:      []ToolDef{{Function: ToolFunction{Name: "file_read"}}},
		ToolChoice: json.RawMessage(`"none"`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, present := got.body["tools"]; present {
		t.Errorf("tools were sent although the caller asked for none: %v", got.body)
	}
}

func TestExecuteAnthropic_ADeclaredToolWithNoParametersStillHasASchema(t *testing.T) {
	anthropicEnv(t)
	srv, got := serve(t, 200, `{"content":[{"type":"text","text":"ok"}]}`)

	if _, err := redirect(srv).Execute(context.Background(), Request{
		Prompt: "hi", Head: head("anthropic"),
		Tools: []ToolDef{{Function: ToolFunction{Name: "task_done"}}},
	}); err != nil {
		t.Fatal(err)
	}
	tool := got.body["tools"].([]any)[0].(map[string]any)
	schema, ok := tool["input_schema"].(map[string]any)
	if !ok || schema["type"] != "object" {
		t.Errorf("input_schema = %v, want an empty object schema rather than nothing", tool["input_schema"])
	}
}

// The documented stream: the call's identity arrives on content_block_start
// with an empty input, and the arguments follow as partial JSON strings.
func anthropicToolStream() []string {
	return []string{
		sse("message_start", `{"type":"message_start","message":{"model":"claude-test-1","usage":{"input_tokens":31}}}`),
		sse("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		sse("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"looking"}}`),
		sse("content_block_stop", `{"type":"content_block_stop","index":0}`),
		sse("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"file_read","input":{}}}`),
		sse("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":""}}`),
		sse("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`),
		sse("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":" \"a.go\"}"}}`),
		sse("content_block_stop", `{"type":"content_block_stop","index":1}`),
		sse("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":89}}`),
	}
}

func TestStreamAnthropic_AssemblesAToolCallFromPartialJSON(t *testing.T) {
	newAnthropicStub(t, anthropicToolStream())

	resp, err := (&HTTPExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "read a.go", Head: anthropicHead(),
			Tools: []ToolDef{{Function: ToolFunction{Name: "file_read"}}}}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}

	if len(resp.ToolCalls) != 1 {
		t.Fatalf("got %d calls: %+v", len(resp.ToolCalls), resp.ToolCalls)
	}
	c := resp.ToolCalls[0]
	if c.ID != "toolu_1" || c.Function.Name != "file_read" {
		t.Errorf("identity lost between the start event and the deltas: %+v", c)
	}
	var args map[string]string
	if err := json.Unmarshal([]byte(c.Function.Arguments), &args); err != nil {
		t.Fatalf("the partial JSON did not reassemble: %q", c.Function.Arguments)
	}
	if args["path"] != "a.go" {
		t.Errorf("arguments = %q", c.Function.Arguments)
	}
	if resp.FinishReason != "tool_calls" {
		t.Errorf("finish reason = %q, want tool_calls", resp.FinishReason)
	}
	// The text block is the answer; the tool call is not text.
	if resp.Output != "looking" {
		t.Errorf("output = %q, want only the text block", resp.Output)
	}
}

// Anthropic numbers content blocks, so a call that follows a text block is
// index 1. Renumbering by position is what keeps one answer reading the same
// whichever dialect spoke it.
func TestStreamAnthropic_NumbersCallsByPositionNotByContentBlock(t *testing.T) {
	newAnthropicStub(t, anthropicToolStream())

	resp, err := (&HTTPExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "p", Head: anthropicHead()}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ToolCalls[0].Index != 0 {
		t.Errorf("index = %d, want 0: it is the first call, whatever block it arrived in",
			resp.ToolCalls[0].Index)
	}
}

// An answer that is only a tool call has no text at all, and the shared
// transport used to fail a stream that produced none.
func TestStreamAnthropic_AToolOnlyAnswerIsNotEmpty(t *testing.T) {
	newAnthropicStub(t, []string{
		sse("message_start", `{"type":"message_start","message":{"model":"claude-test-1","usage":{"input_tokens":9}}}`),
		sse("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_9","name":"task_done","input":{}}}`),
		sse("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}`),
		sse("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":4}}`),
	})

	resp, err := (&HTTPExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "p", Head: anthropicHead()}, func(string) {})
	if err != nil {
		t.Fatalf("a tool-only answer was reported as a failure: %v", err)
	}
	if len(resp.ToolCalls) != 1 || resp.Output != "" {
		t.Errorf("read as output %q with %d calls", resp.Output, len(resp.ToolCalls))
	}
}

// Two readings of one answer must not disagree.
func TestAnthropic_StreamedAndBufferedAgreeOnTheSameCall(t *testing.T) {
	newAnthropicStub(t, anthropicToolStream())
	streamed, err := (&HTTPExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "p", Head: anthropicHead()}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}

	anthropicEnv(t)
	srv, _ := serve(t, 200, `{"model":"claude-test-1","stop_reason":"tool_use","content":[
		{"type":"text","text":"looking"},
		{"type":"tool_use","id":"toolu_1","name":"file_read","input":{"path": "a.go"}}]}`)
	buffered, err := redirect(srv).Execute(context.Background(),
		Request{Prompt: "p", Head: head("anthropic")})
	if err != nil {
		t.Fatal(err)
	}

	if len(streamed.ToolCalls) != len(buffered.ToolCalls) {
		t.Fatalf("streamed %d calls, buffered %d", len(streamed.ToolCalls), len(buffered.ToolCalls))
	}
	s, b := streamed.ToolCalls[0], buffered.ToolCalls[0]
	if s.ID != b.ID || s.Function.Name != b.Function.Name || s.Index != b.Index {
		t.Errorf("identity differs:\n streamed %+v\n buffered %+v", s, b)
	}
	// The buffered path re-encodes the object and the streamed one concatenates
	// fragments, so compare the parsed value rather than the bytes.
	var sv, bv map[string]any
	_ = json.Unmarshal([]byte(s.Function.Arguments), &sv)
	_ = json.Unmarshal([]byte(b.Function.Arguments), &bv)
	if len(sv) != len(bv) || sv["path"] != bv["path"] {
		t.Errorf("arguments differ: streamed %q, buffered %q", s.Function.Arguments, b.Function.Arguments)
	}
	if streamed.FinishReason != buffered.FinishReason {
		t.Errorf("finish reason differs: %q vs %q", streamed.FinishReason, buffered.FinishReason)
	}
}

// dispatch skips a head this is false for when the request carries tools, so
// this predicate is what routed every agent loop away from the Claude heads.
func TestCanUseTools_NowIncludesAnthropic(t *testing.T) {
	if !CanUseTools(anthropicHead()) {
		t.Error("Anthropic heads are still skipped for a request that carries tools")
	}
	// google moved to the other side of this list in #963. The list is what
	// keeps a dialect from reporting tool support before anything maps it.
	for _, p := range []string{"cohere", "bedrock", "replicate"} {
		if CanUseTools(head(p)) {
			t.Errorf("%s reports it can carry tools, but nothing maps them yet", p)
		}
	}
}
