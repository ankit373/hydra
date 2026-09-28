// SPDX-License-Identifier: MIT

package executor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// The turns an agent loop sends back, with the id Hydra minted for a call
// Gemini issued without one.
func geminiTurns() []Message {
	return []Message{
		{Role: "system", Content: "be terse"},
		{Role: "user", Content: "read a.go"},
		{Role: "assistant", ToolCalls: []ToolCall{{
			ID: "call_file_read_0", Type: "function",
			Function: ToolCallFunction{Name: "file_read", Arguments: `{"path":"a.go"}`},
		}}},
		{Role: "tool", ToolCallID: "call_file_read_0", Content: "package main"},
	}
}

func TestGeminiConversation_CarriesEveryTurnWithGeminiRoles(t *testing.T) {
	system, contents, err := geminiConversation(geminiTurns())
	if err != nil {
		t.Fatal(err)
	}
	if system != "be terse" {
		t.Errorf("system = %q, want it lifted into system_instruction", system)
	}
	if len(contents) != 3 {
		t.Fatalf("got %d contents, want user, model, user: %+v", len(contents), contents)
	}
	// Gemini says model, not assistant, and rejects the role it does not know.
	if contents[1].Role != "model" {
		t.Errorf("the answer turn is role %q, want model", contents[1].Role)
	}
	call := contents[1].Parts[0].FunctionCall
	if call == nil || call.Name != "file_read" {
		t.Fatalf("no functionCall part: %+v", contents[1].Parts)
	}
	var args map[string]string
	if err := json.Unmarshal(call.Args, &args); err != nil || args["path"] != "a.go" {
		t.Errorf("args = %s", call.Args)
	}

	res := contents[2].Parts[0].FunctionResponse
	if res == nil {
		t.Fatalf("no functionResponse part: %+v", contents[2].Parts)
	}
	// The dialect matches a result to its call by name. The id is Hydra's.
	if res.Name != "file_read" {
		t.Errorf("functionResponse names %q, want the function the id resolves to", res.Name)
	}
	if contents[2].Role != "user" {
		t.Errorf("the result turn is role %q, want user", contents[2].Role)
	}
}

// Guessing a name would send the result to whichever function the model
// happened to pick, which reads as an answer rather than as a mistake.
func TestGeminiConversation_RefusesAResultItCannotNameAFunctionFor(t *testing.T) {
	_, _, err := geminiConversation([]Message{
		{Role: "user", Content: "hi"},
		{Role: "tool", ToolCallID: "call_unknown", Content: "result"},
	})
	if err == nil {
		t.Fatal("a result with no matching call was accepted")
	}
	if !strings.Contains(err.Error(), "call_unknown") {
		t.Errorf("the refusal does not name the id: %v", err)
	}
}

// OpenAI's tool message may carry the function name itself, which is enough
// even when the call that produced it is not in the window being sent.
func TestGeminiConversation_FallsBackToTheNameOnTheToolMessage(t *testing.T) {
	_, contents, err := geminiConversation([]Message{
		{Role: "user", Content: "hi"},
		{Role: "tool", ToolCallID: "call_x", Name: "file_read", Content: "package main"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got *geminiFunctionResponse
	for _, p := range contents[len(contents)-1].Parts {
		if p.FunctionResponse != nil {
			got = p.FunctionResponse
		}
	}
	if got == nil || got.Name != "file_read" {
		t.Errorf("did not use the name on the message: %+v", got)
	}
}

func TestToolResultObject_IsAlwaysAnObject(t *testing.T) {
	// functionResponse.response is an object, and a tool result is text.
	plain := toolResultObject("package main")
	var m map[string]any
	if err := json.Unmarshal(plain, &m); err != nil {
		t.Fatalf("plain text did not wrap into an object: %s", plain)
	}
	if m["result"] != "package main" {
		t.Errorf("wrapped as %s", plain)
	}
	// One that is already an object is sent as itself rather than double-wrapped.
	structured := toolResultObject(`{"lines": 12}`)
	var s map[string]any
	if err := json.Unmarshal(structured, &s); err != nil {
		t.Fatal(err)
	}
	if _, wrapped := s["result"]; wrapped {
		t.Errorf("an object result was wrapped again: %s", structured)
	}
}

func TestGeminiToolConfig_TranslatesEverySpelling(t *testing.T) {
	mode := func(t *testing.T, raw string) map[string]any {
		t.Helper()
		got, err := geminiToolConfig(json.RawMessage(raw))
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if got == nil {
			return nil
		}
		return got["functionCallingConfig"].(map[string]any)
	}

	if mode(t, "") != nil {
		t.Error("no tool_choice should send no toolConfig; AUTO is the API's own default")
	}
	if m := mode(t, `"auto"`); m["mode"] != "AUTO" {
		t.Errorf("auto -> %v", m)
	}
	if m := mode(t, `"required"`); m["mode"] != "ANY" {
		t.Errorf("required -> %v", m)
	}
	// Unlike Anthropic, Gemini has a NONE mode, so this is said rather than
	// expressed by withholding the tools.
	if m := mode(t, `"none"`); m["mode"] != "NONE" {
		t.Errorf("none -> %v", m)
	}
	m := mode(t, `{"type":"function","function":{"name":"file_read"}}`)
	if m["mode"] != "ANY" {
		t.Errorf("a named function -> %v", m)
	}
	if names, _ := m["allowedFunctionNames"].([]string); len(names) != 1 || names[0] != "file_read" {
		t.Errorf("allowedFunctionNames = %v", m["allowedFunctionNames"])
	}

	if _, err := geminiToolConfig(json.RawMessage(`"whatever"`)); err == nil {
		t.Error("an unmappable tool_choice was quietly dropped")
	}
}

func geminiEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GEMINI_API_KEY", "test-key")
	t.Setenv("GEMINI_MODEL", "gemini-test")
}

func TestExecuteGemini_SendsToolsAndReadsTheCallBack(t *testing.T) {
	geminiEnv(t)
	srv, got := serve(t, 200, `{"modelVersion":"gemini-test","candidates":[{"finishReason":"STOP","content":{"parts":[
		{"text":"looking"},
		{"functionCall":{"name":"file_read","args":{"path":"a.go"}}}]}}],
		"usageMetadata":{"promptTokenCount":31,"candidatesTokenCount":12}}`)
	t.Setenv("GEMINI_BASE_URL", srv.URL)

	resp, err := (&HTTPExecutor{}).Execute(context.Background(), Request{
		Messages: geminiTurns(), Head: head("google"),
		Tools: []ToolDef{
			{Type: "function", Function: ToolFunction{
				Name: "file_read", Description: "read a file",
				Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
			}},
			{Type: "function", Function: ToolFunction{Name: "task_done"}},
		},
		ToolChoice: json.RawMessage(`"auto"`),
	})
	if err != nil {
		t.Fatal(err)
	}

	// One tools entry holding every declaration, not one entry per tool.
	tools, ok := got.body["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools on the wire: %v", got.body["tools"])
	}
	decls, ok := tools[0].(map[string]any)["functionDeclarations"].([]any)
	if !ok || len(decls) != 2 {
		t.Fatalf("want one tools entry holding both declarations, got %v", tools)
	}
	if decls[0].(map[string]any)["name"] != "file_read" {
		t.Errorf("declaration = %v", decls[0])
	}
	if len(got.body["contents"].([]any)) != 3 {
		t.Errorf("the conversation arrived as %d contents, want 3", len(got.body["contents"].([]any)))
	}

	if len(resp.ToolCalls) != 1 {
		t.Fatalf("the functionCall did not come back: %+v", resp)
	}
	c := resp.ToolCalls[0]
	if c.Function.Name != "file_read" || c.Function.Arguments != `{"path":"a.go"}` {
		t.Errorf("call read as %+v", c)
	}
	// Gemini issues no id, and a client needs one to send the result back.
	if c.ID == "" {
		t.Error("no id was minted, so a client cannot correlate its result")
	}
	// Gemini says STOP whether or not it called anything, so the call itself is
	// the evidence.
	if resp.FinishReason != "tool_calls" {
		t.Errorf("finish reason = %q, want tool_calls despite finishReason STOP", resp.FinishReason)
	}
	if resp.Output != "looking" {
		t.Errorf("text read as %q", resp.Output)
	}
}

func TestStreamGemini_ReadsCallsOutOfChunks(t *testing.T) {
	newGeminiStub(t, []string{
		`{"candidates":[{"content":{"parts":[{"text":"looking"}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"file_read","args":{"path":"a.go"}}}]}}]}`,
		`{"candidates":[{"finishReason":"STOP","content":{"parts":[]}}],` +
			`"usageMetadata":{"promptTokenCount":31,"candidatesTokenCount":12}}`,
	})

	resp, err := (&HTTPExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "read a.go", Head: geminiHead(),
			Tools: []ToolDef{{Function: ToolFunction{Name: "file_read"}}}}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("got %d calls: %+v", len(resp.ToolCalls), resp.ToolCalls)
	}
	if resp.ToolCalls[0].Function.Arguments != `{"path":"a.go"}` {
		t.Errorf("args = %q", resp.ToolCalls[0].Function.Arguments)
	}
	if resp.FinishReason != "tool_calls" {
		t.Errorf("finish reason = %q", resp.FinishReason)
	}
	if resp.Output != "looking" {
		t.Errorf("output = %q, want only the text part", resp.Output)
	}
}

// Two calls in one answer, which Gemini sends as two parts with no index of
// any kind: folding on a missing index would make them one call.
func TestStreamGemini_KeepsTwoCallsApartWithNoIndexOnTheWire(t *testing.T) {
	newGeminiStub(t, []string{
		`{"candidates":[{"content":{"parts":[` +
			`{"functionCall":{"name":"file_read","args":{"path":"a.go"}}},` +
			`{"functionCall":{"name":"file_find","args":{"glob":"*.go"}}}]}}]}`,
	})

	resp, err := (&HTTPExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "p", Head: geminiHead()}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("got %d calls, want 2: %+v", len(resp.ToolCalls), resp.ToolCalls)
	}
	if resp.ToolCalls[0].ID == resp.ToolCalls[1].ID {
		t.Errorf("both calls were given the same id: %+v", resp.ToolCalls)
	}
	if resp.ToolCalls[0].Function.Name == resp.ToolCalls[1].Function.Name {
		t.Errorf("the two calls merged: %+v", resp.ToolCalls)
	}
}

func TestStreamGemini_AToolOnlyAnswerIsNotEmpty(t *testing.T) {
	newGeminiStub(t, []string{
		`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"task_done","args":{}}}]}}]}`,
	})

	resp, err := (&HTTPExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "p", Head: geminiHead()}, func(string) {})
	if err != nil {
		t.Fatalf("a tool-only answer was reported as a failure: %v", err)
	}
	if len(resp.ToolCalls) != 1 || resp.Output != "" {
		t.Errorf("read as output %q with %d calls", resp.Output, len(resp.ToolCalls))
	}
}

func TestGemini_StreamedAndBufferedAgreeOnTheSameCall(t *testing.T) {
	newGeminiStub(t, []string{
		`{"candidates":[{"content":{"parts":[{"text":"looking"},` +
			`{"functionCall":{"name":"file_read","args":{"path":"a.go"}}}]}}]}`,
	})
	streamed, err := (&HTTPExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "p", Head: geminiHead()}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}

	geminiEnv(t)
	srv, _ := serve(t, 200, `{"candidates":[{"finishReason":"STOP","content":{"parts":[
		{"text":"looking"},{"functionCall":{"name":"file_read","args":{"path":"a.go"}}}]}}]}`)
	t.Setenv("GEMINI_BASE_URL", srv.URL)
	buffered, err := (&HTTPExecutor{}).Execute(context.Background(),
		Request{Prompt: "p", Head: head("google")})
	if err != nil {
		t.Fatal(err)
	}

	if len(streamed.ToolCalls) != len(buffered.ToolCalls) {
		t.Fatalf("streamed %d calls, buffered %d", len(streamed.ToolCalls), len(buffered.ToolCalls))
	}
	s, b := streamed.ToolCalls[0], buffered.ToolCalls[0]
	// The minted id has to match too: a client that retried on the other path
	// would otherwise send back an id the next turn cannot resolve.
	if s.ID != b.ID || s.Function.Name != b.Function.Name || s.Index != b.Index {
		t.Errorf("identity differs:\n streamed %+v\n buffered %+v", s, b)
	}
	if streamed.FinishReason != buffered.FinishReason || streamed.Output != buffered.Output {
		t.Errorf("streamed %q/%q, buffered %q/%q",
			streamed.FinishReason, streamed.Output, buffered.FinishReason, buffered.Output)
	}
}
