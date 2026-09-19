// SPDX-License-Identifier: MIT

package executor

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/ankit373/hydra/internal/testutil"
)

func bedrockTurns() []Message {
	return []Message{
		{Role: "system", Content: "be terse"},
		{Role: "user", Content: "read a.go"},
		{Role: "assistant", Content: "looking", ToolCalls: []ToolCall{{
			ID: "tooluse_1", Type: "function",
			Function: ToolCallFunction{Name: "file_read", Arguments: `{"path":"a.go"}`},
		}}},
		{Role: "tool", ToolCallID: "tooluse_1", Content: "package main"},
	}
}

// bedrockBuffered points the Converse path at a body-capturing stub.
func bedrockBuffered(t *testing.T, response string) (*httptest.Server, *capture) {
	t.Helper()
	srv, got := serve(t, 200, response)
	sb := testutil.NewSandbox(t)
	sb.SetKey(t, "AWS_ACCESS_KEY_ID", "AKIDEXAMPLE")
	sb.SetKey(t, "AWS_SECRET_ACCESS_KEY", "secret")
	t.Setenv("AWS_REGION", "eu-west-1")
	t.Setenv("AWS_ENDPOINT_URL_BEDROCK_RUNTIME", srv.URL)
	t.Setenv("BEDROCK_MODEL_ID", "anthropic.claude-3-5-sonnet-20241022-v2:0")
	return srv, got
}

func TestBedrockConversation_CarriesEveryTurn(t *testing.T) {
	system, msgs, err := bedrockConversation(bedrockTurns())
	if err != nil {
		t.Fatal(err)
	}
	if len(system) != 1 || system[0] != "be terse" {
		t.Errorf("system = %v, want it lifted into the top-level array", system)
	}
	if len(msgs) != 3 {
		t.Fatalf("got %d messages, want user, assistant, user: %+v", len(msgs), msgs)
	}
	use := msgs[1].Content[1].ToolUse
	if use == nil || use.ToolUseID != "tooluse_1" || use.Name != "file_read" {
		t.Fatalf("toolUse block is %+v", msgs[1].Content)
	}
	var input map[string]string
	if err := json.Unmarshal(use.Input, &input); err != nil || input["path"] != "a.go" {
		t.Errorf("input = %s", use.Input)
	}

	res := msgs[2].Content[0].ToolResult
	if res == nil || res.ToolUseID != "tooluse_1" {
		t.Fatalf("toolResult block is %+v", msgs[2].Content)
	}
	// content is an array of blocks, not a string: a bare string is rejected.
	if len(res.Content) != 1 || res.Content[0].Text != "package main" {
		t.Errorf("result content = %+v", res.Content)
	}
	if msgs[2].Role != "user" {
		t.Errorf("the result turn is role %q, want user", msgs[2].Role)
	}
}

func TestBedrockConversation_MergesConsecutiveResultsIntoOneTurn(t *testing.T) {
	_, msgs, err := bedrockConversation([]Message{
		{Role: "user", Content: "go"},
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
		t.Fatalf("got %d messages, want the results merged: %+v", len(msgs), msgs)
	}
	if last := msgs[2]; len(last.Content) != 2 {
		t.Errorf("the second result is split into its own turn: %+v", last)
	}
}

func TestBedrockToolChoice_TranslatesEverySpelling(t *testing.T) {
	cases := []struct {
		raw    string
		member string
		send   bool
	}{
		{``, "", true},
		{`"auto"`, "auto", true},
		{`"required"`, "any", true},
		{`"none"`, "", false},
		{`{"type":"function","function":{"name":"file_read"}}`, "tool", true},
	}
	for _, c := range cases {
		got, send, err := bedrockToolChoice(json.RawMessage(c.raw))
		if err != nil {
			t.Errorf("%s: %v", c.raw, err)
			continue
		}
		if send != c.send {
			t.Errorf("%s: sendTools = %v, want %v", c.raw, send, c.send)
		}
		if c.member == "" {
			if got != nil {
				t.Errorf("%s: got %v, want no choice", c.raw, got)
			}
			continue
		}
		if _, ok := got[c.member]; !ok || len(got) != 1 {
			t.Errorf("%s: got %v, want the union member %q alone", c.raw, got, c.member)
		}
	}
	if _, _, err := bedrockToolChoice(json.RawMessage(`"whatever"`)); err == nil {
		t.Error("an unmappable tool_choice was quietly dropped")
	}
}

func TestExecuteBedrock_SendsToolsAndReadsTheCallBack(t *testing.T) {
	_, got := bedrockBuffered(t, `{"stopReason":"tool_use","output":{"message":{"role":"assistant","content":[
		{"text":"looking"},
		{"toolUse":{"toolUseId":"tooluse_1","name":"file_read","input":{"path":"a.go"}}}]}},
		"usage":{"inputTokens":31,"outputTokens":12}}`)

	resp, err := (&HTTPExecutor{}).Execute(context.Background(), Request{
		Messages: bedrockTurns(), Head: bedrockHead(),
		Tools: []ToolDef{{Type: "function", Function: ToolFunction{
			Name: "file_read", Description: "read a file",
			Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
		}}},
		ToolChoice: json.RawMessage(`"auto"`),
	})
	if err != nil {
		t.Fatal(err)
	}

	cfg, ok := got.body["toolConfig"].(map[string]any)
	if !ok {
		t.Fatalf("no toolConfig on the wire: %v", got.body)
	}
	tools := cfg["tools"].([]any)
	spec, ok := tools[0].(map[string]any)["toolSpec"].(map[string]any)
	if !ok {
		t.Fatalf("a tool is not wrapped in toolSpec: %v", tools[0])
	}
	if spec["name"] != "file_read" {
		t.Errorf("tool name = %v", spec["name"])
	}
	// inputSchema is a union, and the schema sits under its json member: one
	// level deeper than every other dialect.
	schema, ok := spec["inputSchema"].(map[string]any)["json"].(map[string]any)
	if !ok || schema["type"] != "object" {
		t.Errorf("inputSchema = %v, want the schema under its json member", spec["inputSchema"])
	}
	if _, ok := cfg["toolChoice"].(map[string]any)["auto"]; !ok {
		t.Errorf("toolChoice = %v, want the auto member", cfg["toolChoice"])
	}

	if len(resp.ToolCalls) != 1 {
		t.Fatalf("the toolUse block did not come back: %+v", resp)
	}
	c := resp.ToolCalls[0]
	if c.ID != "tooluse_1" || c.Function.Name != "file_read" || c.Function.Arguments != `{"path":"a.go"}` {
		t.Errorf("call read as %+v", c)
	}
	if resp.FinishReason != "tool_calls" {
		t.Errorf("finish reason = %q, want tool_calls", resp.FinishReason)
	}
	if resp.Output != "looking" {
		t.Errorf("text read as %q", resp.Output)
	}
}

// executeBedrock failed a call whose joined text was empty, which is exactly
// what an answer made only of toolUse blocks looks like.
func TestExecuteBedrock_AnAnswerOfOnlyToolCallsIsNotEmpty(t *testing.T) {
	bedrockBuffered(t, `{"stopReason":"tool_use","output":{"message":{"role":"assistant","content":[
		{"toolUse":{"toolUseId":"tooluse_9","name":"task_done","input":{}}}]}}}`)

	resp, err := (&HTTPExecutor{}).Execute(context.Background(),
		Request{Prompt: "p", Head: bedrockHead()})
	if err != nil {
		t.Fatalf("a tool-calling answer was reported as a failure: %v", err)
	}
	if len(resp.ToolCalls) != 1 || resp.Output != "" {
		t.Errorf("read as output %q with %d calls", resp.Output, len(resp.ToolCalls))
	}
}

// An answer with neither text nor calls is still empty, and must stay an error.
func TestExecuteBedrock_AnAnswerWithNothingInItIsStillEmpty(t *testing.T) {
	bedrockBuffered(t, `{"output":{"message":{"role":"assistant","content":[]}}}`)

	if _, err := (&HTTPExecutor{}).Execute(context.Background(),
		Request{Prompt: "p", Head: bedrockHead()}); err == nil {
		t.Fatal("an answer carrying nothing was accepted")
	}
}

func bedrockToolFrames() [][]byte {
	ev := func(kind, payload string) []byte {
		return encodeEventFrame(
			map[string]string{":message-type": "event", ":event-type": kind}, []byte(payload))
	}
	return [][]byte{
		ev("messageStart", `{"role":"assistant"}`),
		bedrockDelta("looking"),
		ev("contentBlockStart",
			`{"contentBlockIndex":1,"start":{"toolUse":{"toolUseId":"tooluse_1","name":"file_read"}}}`),
		ev("contentBlockDelta", `{"contentBlockIndex":1,"delta":{"toolUse":{"input":"{\"path\":"}}}`),
		ev("contentBlockDelta", `{"contentBlockIndex":1,"delta":{"toolUse":{"input":"\"a.go\"}"}}}`),
		ev("contentBlockStop", `{"contentBlockIndex":1}`),
		ev("messageStop", `{"stopReason":"tool_use"}`),
		bedrockMetadata,
	}
}

func TestBedrockStream_AssemblesAToolCallFromPartialInput(t *testing.T) {
	newBedrockStub(t, bedrockToolFrames())

	resp, err := (&HTTPExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "read a.go", Head: bedrockHead(),
			Tools: []ToolDef{{Function: ToolFunction{Name: "file_read"}}}}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("got %d calls: %+v", len(resp.ToolCalls), resp.ToolCalls)
	}
	c := resp.ToolCalls[0]
	if c.ID != "tooluse_1" || c.Function.Name != "file_read" {
		t.Errorf("identity lost between the start frame and the deltas: %+v", c)
	}
	var args map[string]string
	if err := json.Unmarshal([]byte(c.Function.Arguments), &args); err != nil {
		t.Fatalf("the partial input did not reassemble: %q", c.Function.Arguments)
	}
	if args["path"] != "a.go" {
		t.Errorf("arguments = %q", c.Function.Arguments)
	}
	if resp.FinishReason != "tool_calls" {
		t.Errorf("finish reason = %q, want tool_calls", resp.FinishReason)
	}
	// The tool input rides the same event as the answer text and is not it.
	if resp.Output != "looking" {
		t.Errorf("output = %q, want only the text delta", resp.Output)
	}
}

func TestBedrock_StreamedAndBufferedAgreeOnTheSameCall(t *testing.T) {
	newBedrockStub(t, bedrockToolFrames())
	streamed, err := (&HTTPExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "p", Head: bedrockHead()}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}

	bedrockBuffered(t, `{"stopReason":"tool_use","output":{"message":{"content":[
		{"text":"looking"},
		{"toolUse":{"toolUseId":"tooluse_1","name":"file_read","input":{"path":"a.go"}}}]}}}`)
	buffered, err := (&HTTPExecutor{}).Execute(context.Background(),
		Request{Prompt: "p", Head: bedrockHead()})
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
	var sv, bv map[string]any
	_ = json.Unmarshal([]byte(s.Function.Arguments), &sv)
	_ = json.Unmarshal([]byte(b.Function.Arguments), &bv)
	if len(sv) != len(bv) || sv["path"] != bv["path"] {
		t.Errorf("arguments differ: streamed %q, buffered %q", s.Function.Arguments, b.Function.Arguments)
	}
	if streamed.FinishReason != buffered.FinishReason || streamed.Output != buffered.Output {
		t.Errorf("streamed %q/%q, buffered %q/%q",
			streamed.FinishReason, streamed.Output, buffered.FinishReason, buffered.Output)
	}
}

// inputSchema is required by the API, so a tool declared with no parameters
// still needs an empty object schema under it.
func TestExecuteBedrock_ADeclaredToolWithNoParametersStillHasASchema(t *testing.T) {
	_, got := bedrockBuffered(t, `{"output":{"message":{"content":[{"text":"ok"}]}}}`)

	if _, err := (&HTTPExecutor{}).Execute(context.Background(), Request{
		Prompt: "hi", Head: bedrockHead(),
		Tools: []ToolDef{{Function: ToolFunction{Name: "task_done"}}},
	}); err != nil {
		t.Fatal(err)
	}
	tools := got.body["toolConfig"].(map[string]any)["tools"].([]any)
	spec := tools[0].(map[string]any)["toolSpec"].(map[string]any)
	schema, ok := spec["inputSchema"].(map[string]any)["json"].(map[string]any)
	if !ok || schema["type"] != "object" {
		t.Errorf("inputSchema.json = %v, want an empty object schema rather than nothing",
			spec["inputSchema"])
	}
}
