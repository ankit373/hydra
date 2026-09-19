// SPDX-License-Identifier: MIT

package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var geminiWeatherTool = ToolDef{
	Type: "function",
	Function: ToolFunction{
		Name:        "get_weather",
		Description: "Current weather",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`),
	},
}

// A Gemini head could not carry tools at all, so dispatch skipped it whenever
// a request had any. It is named in the switch rather than left to fall
// through, because the OpenAI-compat check below it would refuse it.
func TestCanUseTools_GeminiCarriesThem(t *testing.T) {
	if !CanUseTools(geminiHead()) {
		t.Error("a Gemini head still reads as unable to carry tools, so dispatch skips it")
	}
}

// Gemini calls the assistant role "model", and a tool result is a user turn.
// A history sent with OpenAI's own role names is refused by the API outright.
func TestGeminiConversation_MapsRolesAndToolTurns(t *testing.T) {
	call := ToolCall{ID: "call_1", Type: "function"}
	call.Function.Name = "get_weather"
	call.Function.Arguments = `{"city":"Paris"}`

	system, contents, err := geminiConversation([]Message{
		{Role: "system", Content: "be terse"},
		{Role: "user", Content: "weather in Paris?"},
		{Role: "assistant", ToolCalls: []ToolCall{call}},
		{Role: "tool", ToolCallID: "call_1", Content: `{"tempC":14}`},
	})
	if err != nil {
		t.Fatal(err)
	}
	if system != "be terse" {
		t.Errorf("system = %q, want it lifted out of the turns", system)
	}
	if len(contents) != 3 {
		t.Fatalf("got %d turns, want user/model/user: %+v", len(contents), contents)
	}
	if contents[0].Role != "user" || contents[1].Role != "model" || contents[2].Role != "user" {
		t.Errorf("roles are %s/%s/%s, want user/model/user",
			contents[0].Role, contents[1].Role, contents[2].Role)
	}
	fc := contents[1].Parts[0].FunctionCall
	if fc == nil || fc.Name != "get_weather" {
		t.Fatalf("the call did not survive: %+v", contents[1].Parts[0])
	}
	// Arguments are a JSON string on the way in and an object on the way out.
	// Passing the string through sends the model a quoted blob.
	if string(fc.Args) != `{"city":"Paris"}` {
		t.Errorf("args = %s, want the object", fc.Args)
	}
	fr := contents[2].Parts[0].FunctionResponse
	if fr == nil || fr.Name != "get_weather" {
		t.Fatalf("the result did not resolve to its call's name: %+v", contents[2].Parts[0])
	}
}

// Two results in a row would otherwise be two user turns with no model turn
// between them, which Gemini refuses.
func TestGeminiConversation_MergesConsecutiveToolResults(t *testing.T) {
	a := ToolCall{ID: "a", Type: "function"}
	a.Function.Name = "one"
	b := ToolCall{ID: "b", Type: "function"}
	b.Function.Name = "two"

	_, contents, err := geminiConversation([]Message{
		{Role: "user", Content: "go"},
		{Role: "assistant", ToolCalls: []ToolCall{a, b}},
		{Role: "tool", ToolCallID: "a", Content: "1"},
		{Role: "tool", ToolCallID: "b", Content: "2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(contents) != 3 {
		t.Fatalf("got %d turns, want the two results merged into one: %+v", len(contents), contents)
	}
	if len(contents[2].Parts) != 2 {
		t.Errorf("the merged turn carries %d parts, want 2", len(contents[2].Parts))
	}
}

// Gemini identifies a result by function name and has no id, so an id naming
// no call in this conversation cannot be expressed. Guessing a name would
// answer the wrong call.
func TestGeminiConversation_RefusesAResultForAnUnknownCall(t *testing.T) {
	_, _, err := geminiConversation([]Message{
		{Role: "user", Content: "go"},
		{Role: "tool", ToolCallID: "never_called", Content: "1"},
	})
	if err == nil {
		t.Fatal("a result for a call nobody made was accepted")
	}
	if !strings.Contains(err.Error(), "never_called") {
		t.Errorf("the refusal does not name the call: %v", err)
	}
}

// Sending {} is a call the model reads as "no arguments", which is a wrong
// answer rather than a degraded one.
func TestGeminiConversation_RefusesArgumentsThatAreNotAnObject(t *testing.T) {
	bad := ToolCall{ID: "c", Type: "function"}
	bad.Function.Name = "get_weather"
	bad.Function.Arguments = `not json`

	_, _, err := geminiConversation([]Message{{Role: "assistant", ToolCalls: []ToolCall{bad}}})
	if err == nil {
		t.Fatal("unparseable arguments were sent anyway")
	}
	if !strings.Contains(err.Error(), "get_weather") {
		t.Errorf("the refusal does not name the call: %v", err)
	}
}

// A tool whose reply is already structured must reach the model as that
// structure, not as a string of it.
func TestGeminiResponseObject_PassesStructureThroughAndWrapsText(t *testing.T) {
	if got := string(geminiResponseObject(`{"tempC":14}`)); got != `{"tempC":14}` {
		t.Errorf("a JSON object reply was rewrapped: %s", got)
	}
	got := string(geminiResponseObject("it is mild"))
	if !strings.Contains(got, `"result"`) || !strings.Contains(got, "it is mild") {
		t.Errorf("a plain-text reply was not carried: %s", got)
	}
}

// `tools` is a list of groupings, not a list of functions. A bare list is
// rejected by the API.
func TestGeminiToolDeclarations_WrapsThemInOneGrouping(t *testing.T) {
	out := geminiToolDeclarations([]ToolDef{geminiWeatherTool})
	if len(out) != 1 {
		t.Fatalf("got %d groupings, want 1: %+v", len(out), out)
	}
	decls, ok := out[0]["functionDeclarations"].([]geminiFunctionDeclaration)
	if !ok || len(decls) != 1 || decls[0].Name != "get_weather" {
		t.Fatalf("the declaration did not survive: %+v", out[0])
	}
	if string(decls[0].Parameters) == "" {
		t.Error("the schema was dropped, so the model cannot know the shape to call with")
	}
	if geminiToolDeclarations(nil) != nil {
		t.Error("an empty tool list produced a grouping, which asks the model to call nothing")
	}
}

// geminiBody must carry both, or the head is asked the question with its tools
// removed and can only answer in prose.
func TestGeminiBody_CarriesTheConversationAndTheTools(t *testing.T) {
	body, err := geminiBody(Request{
		Messages: []Message{{Role: "user", Content: "hi"}},
		Tools:    []ToolDef{geminiWeatherTool},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"contents"`, `"functionDeclarations"`, `"get_weather"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the body is missing %s: %s", want, raw)
		}
	}
}

// A caller with no conversation still has a prompt, which is every path but
// hyctl serve.
func TestGeminiBody_FallsBackToThePromptWhenThereIsNoConversation(t *testing.T) {
	body, err := geminiBody(Request{Prompt: "just a prompt"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(body)
	if !strings.Contains(string(raw), "just a prompt") {
		t.Errorf("the prompt was dropped: %s", raw)
	}
}

// newGeminiJSONStub answers the buffered path, which returns one JSON body
// rather than events.
func newGeminiJSONStub(t *testing.T, payload string) *geminiStub {
	t.Helper()
	s := &geminiStub{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.body, _ = io.ReadAll(r.Body)
		s.header = r.Header.Clone()
		s.targets = append(s.targets, r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(s.Server.Close)
	t.Setenv("GEMINI_BASE_URL", s.Server.URL)
	t.Setenv("GEMINI_API_KEY", "test-key")
	t.Setenv("GEMINI_MODEL", "gemini-test")
	return s
}

func TestGemini_ReadsAFunctionCallBackOffTheBufferedPath(t *testing.T) {
	stub := newGeminiJSONStub(t, `{
      "candidates":[{"content":{"parts":[
        {"text":"checking "},
        {"functionCall":{"name":"get_weather","args":{"city":"Paris"}}}
      ]},"finishReason":"STOP"}],
      "usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":7},
      "modelVersion":"gemini-test"}`)

	resp, err := (&HTTPExecutor{}).Execute(context.Background(), Request{
		Prompt: "weather?", Head: geminiHead(), Tools: []ToolDef{geminiWeatherTool},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("got %d calls, want 1: %+v", len(resp.ToolCalls), resp.ToolCalls)
	}
	got := resp.ToolCalls[0]
	if got.Function.Name != "get_weather" || got.Function.Arguments != `{"city":"Paris"}` {
		t.Errorf("the call did not survive: %+v", got)
	}
	// Gemini sends no id. An empty one leaves the client nothing to key its
	// result on, and geminiCallName resolves this one back to its name.
	if got.ID == "" {
		t.Error("the call carries no id, so a client cannot answer it")
	}
	if resp.FinishReason != "tool_calls" {
		t.Errorf("finish reason %q: a client branches on this, not on the content", resp.FinishReason)
	}
	if !strings.Contains(resp.Output, "checking") {
		t.Errorf("the text part was dropped: %q", resp.Output)
	}
	if !strings.Contains(string(stub.body), "functionDeclarations") {
		t.Errorf("the tools never reached the head: %s", stub.body)
	}
}

func TestGemini_ReadsAFunctionCallBackOffTheStream(t *testing.T) {
	stub := newGeminiStub(t, []string{
		`{"candidates":[{"content":{"parts":[{"text":"checking "}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"get_weather","args":{"city":"Paris"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":7}}`,
	})

	var streamed strings.Builder
	resp, err := (&HTTPExecutor{}).ExecuteStream(context.Background(), Request{
		Prompt: "weather?", Head: geminiHead(), Tools: []ToolDef{geminiWeatherTool},
	}, func(d string) { streamed.WriteString(d) })
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Function.Name != "get_weather" {
		t.Fatalf("the streamed call did not survive: %+v", resp.ToolCalls)
	}
	if resp.FinishReason != "tool_calls" {
		t.Errorf("finish reason %q, want tool_calls", resp.FinishReason)
	}
	if !strings.Contains(string(stub.body), "get_weather") {
		t.Errorf("the tools never reached the head: %s", stub.body)
	}
	if streamed.String() != "checking " {
		t.Errorf("the text deltas are %q, want the text only", streamed.String())
	}
}

// An answer made only of calls carries no text, and an emptiness check on text
// alone reads that complete reply as a stream that failed.
func TestGemini_AStreamOfOnlyCallsIsNotEmpty(t *testing.T) {
	newGeminiStub(t, []string{
		`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"get_weather","args":{}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2}}`,
	})
	resp, err := (&HTTPExecutor{}).ExecuteStream(context.Background(), Request{
		Prompt: "x", Head: geminiHead(), Tools: []ToolDef{geminiWeatherTool},
	}, func(string) {})
	if err != nil {
		t.Fatalf("an answer of only calls was rejected as empty: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("got %d calls, want 1", len(resp.ToolCalls))
	}
}

// Two chunks carrying a call each would both claim index 0, and a client
// reassembling by index would fold them into one.
func TestGemini_StreamedCallsAreRenumberedAcrossChunks(t *testing.T) {
	newGeminiStub(t, []string{
		`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"one","args":{}}}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"two","args":{}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2}}`,
	})
	resp, err := (&HTTPExecutor{}).ExecuteStream(context.Background(), Request{
		Prompt: "x", Head: geminiHead(), Tools: []ToolDef{geminiWeatherTool},
	}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("got %d calls, want 2: %+v", len(resp.ToolCalls), resp.ToolCalls)
	}
	if resp.ToolCalls[0].Index == resp.ToolCalls[1].Index {
		t.Errorf("both calls claim index %d, so a client folds them into one", resp.ToolCalls[0].Index)
	}
	if resp.ToolCalls[0].ID == resp.ToolCalls[1].ID {
		t.Errorf("both calls share id %q, so a result cannot say which it answers", resp.ToolCalls[0].ID)
	}
}

// The round trip: the call comes back, its result goes in as a turn, and the
// second request carries both.
func TestGemini_ToolResultGoesBackAsAFunctionResponse(t *testing.T) {
	stub := newGeminiJSONStub(t, `{"candidates":[{"content":{"parts":[{"text":"14C in Paris"}]},"finishReason":"STOP"}],
      "usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":5}}`)

	call := ToolCall{ID: "call_0_get_weather", Type: "function"}
	call.Function.Name = "get_weather"
	call.Function.Arguments = `{"city":"Paris"}`

	resp, err := (&HTTPExecutor{}).Execute(context.Background(), Request{
		Head:  geminiHead(),
		Tools: []ToolDef{geminiWeatherTool},
		Messages: []Message{
			{Role: "user", Content: "weather in Paris?"},
			{Role: "assistant", ToolCalls: []ToolCall{call}},
			{Role: "tool", ToolCallID: "call_0_get_weather", Content: `{"tempC":14}`},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Output, "14C") {
		t.Errorf("the second answer did not come back: %q", resp.Output)
	}
	sent := string(stub.body)
	if !strings.Contains(sent, "functionResponse") || !strings.Contains(sent, `"tempC":14`) {
		t.Errorf("the tool result never reached the head: %s", sent)
	}
	if strings.Contains(sent, `"role":"assistant"`) {
		t.Errorf("the assistant role was sent unmapped, which Gemini refuses: %s", sent)
	}
}
