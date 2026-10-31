// SPDX-License-Identifier: MIT

package executor

import (
	"encoding/json"
	"testing"
)

func offered(names ...string) []ToolDef {
	out := make([]ToolDef, 0, len(names))
	for _, n := range names {
		out = append(out, ToolDef{Type: "function", Function: ToolFunction{
			Name:       n,
			Parameters: json.RawMessage(`{"type":"object"}`),
		}})
	}
	return out
}

// The fixtures are what the models on this machine actually returned, captured
// from Ollama rather than invented: Qwen2.5-Coder:7b writes its call as the
// whole message content, every time, across four different prompts.
func TestRecoverToolCalls_ReadsWhatTheModelActuallyWrote(t *testing.T) {
	cases := []struct {
		name     string
		content  string
		tools    []ToolDef
		wantName string
		wantArgs string
	}{
		{
			name:     "measured: Qwen2.5-Coder:7b asked for Paris",
			content:  `{"name": "get_weather", "arguments": {"city": "Paris"}}`,
			tools:    offered("get_weather", "list_files"),
			wantName: "get_weather",
			wantArgs: `{"city": "Paris"}`,
		},
		{
			name:     "measured: the same model asked to list /tmp",
			content:  `{"name": "list_files", "arguments": {"path": "/tmp"}}`,
			tools:    offered("get_weather", "list_files"),
			wantName: "list_files",
			wantArgs: `{"path": "/tmp"}`,
		},
		{
			name:     "surrounded by whitespace, which a template often adds",
			content:  "\n  {\"name\": \"get_weather\", \"arguments\": {\"city\": \"Oslo\"}}  \n",
			tools:    offered("get_weather"),
			wantName: "get_weather",
			wantArgs: `{"city": "Oslo"}`,
		},
		{
			name:     "the other spelling of the arguments key",
			content:  `{"name": "get_weather", "parameters": {"city": "Rome"}}`,
			tools:    offered("get_weather"),
			wantName: "get_weather",
			wantArgs: `{"city": "Rome"}`,
		},
		{
			name:     "arguments already encoded as the wire format's string",
			content:  `{"name": "get_weather", "arguments": "{\"city\": \"Bern\"}"}`,
			tools:    offered("get_weather"),
			wantName: "get_weather",
			wantArgs: `{"city": "Bern"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls, ok := RecoverToolCalls(tc.content, tc.tools)
			if !ok {
				t.Fatalf("not recovered: %q", tc.content)
			}
			if len(calls) != 1 {
				t.Fatalf("got %d calls, want 1", len(calls))
			}
			if calls[0].Function.Name != tc.wantName {
				t.Errorf("name = %q, want %q", calls[0].Function.Name, tc.wantName)
			}
			if calls[0].Function.Arguments != tc.wantArgs {
				t.Errorf("arguments = %q, want %q", calls[0].Function.Arguments, tc.wantArgs)
			}
			if calls[0].Type != "function" || calls[0].ID == "" {
				t.Errorf("the call is not usable by a client: %+v", calls[0])
			}
		})
	}
}

func TestRecoverToolCalls_ReadsAnArrayOfCalls(t *testing.T) {
	calls, ok := RecoverToolCalls(
		`[{"name":"get_weather","arguments":{"city":"Paris"}},{"name":"list_files","arguments":{"path":"/tmp"}}]`,
		offered("get_weather", "list_files"))
	if !ok || len(calls) != 2 {
		t.Fatalf("ok=%v calls=%d, want 2 recovered", ok, len(calls))
	}
	// The index is what an OpenAI client uses to order them.
	if calls[0].Index != 0 || calls[1].Index != 1 {
		t.Errorf("indices are %d,%d, want 0,1", calls[0].Index, calls[1].Index)
	}
	if calls[0].ID == calls[1].ID {
		t.Errorf("both calls share the id %q, so a result cannot be attributed", calls[0].ID)
	}
}

// Everything this must refuse. The first case is the one that matters most:
// a model naming a tool nobody offered is confabulating, and taking it is
// vLLM #58147, where quoted markup became real calls for tools outside the
// request.
func TestRecoverToolCalls_Refuses(t *testing.T) {
	cases := []struct {
		name    string
		content string
		tools   []ToolDef
	}{
		{"a tool nobody offered", `{"name":"rm_rf","arguments":{"path":"/"}}`, offered("get_weather")},
		{"no tools were offered at all", `{"name":"get_weather","arguments":{"city":"Oslo"}}`, nil},
		{"prose before the call", `Sure, here goes: {"name":"get_weather","arguments":{"city":"Oslo"}}`, offered("get_weather")},
		{"prose after the call", `{"name":"get_weather","arguments":{"city":"Oslo"}} — shall I run it?`, offered("get_weather")},
		{"a fenced example, which is prose around JSON", "```json\n{\"name\":\"get_weather\",\"arguments\":{}}\n```", offered("get_weather")},
		{"measured: the model describing its tools", "I have access to the following tools:\n\n1. **Get Weather**", offered("get_weather")},
		{"an object that is not a call", `{"city":"Oslo"}`, offered("get_weather")},
		{"no name", `{"arguments":{"city":"Oslo"}}`, offered("get_weather")},
		{"no arguments", `{"name":"get_weather"}`, offered("get_weather")},
		{"arguments are not an object", `{"name":"get_weather","arguments":[1,2]}`, offered("get_weather")},
		{"one good call and one bad, so neither", `[{"name":"get_weather","arguments":{}},{"name":"rm_rf","arguments":{}}]`, offered("get_weather")},
		{"an empty array", `[]`, offered("get_weather")},
		{"empty content", ``, offered("get_weather")},
		{"not JSON at all", `the temperature in Paris is 14C`, offered("get_weather")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if calls, ok := RecoverToolCalls(tc.content, tc.tools); ok {
				t.Errorf("recovered %d call(s) from %q", len(calls), tc.content)
			}
		})
	}
}

// The limit, recorded as a test so nobody rediscovers it as a bug. Asked to
// *show* a call without making one, Qwen2.5-Coder answers with exactly the
// bytes it sends to make it. Measured, not supposed. Nothing in the text
// distinguishes the two, so this is recovered, and that is the model's
// ambiguity rather than the parser's. The real answer is to route tool work to
// a head that structures its calls, which is a routing decision.
func TestRecoverToolCalls_CannotTellACallFromAQuotedOne(t *testing.T) {
	// Captured from: "Show me an example of the JSON for calling get_weather.
	// Do not actually call it." The model answered with exactly the bytes it
	// sends to make the call.
	quoted := `{"name": "get_weather", "arguments": {"city": "New York"}}`
	if _, ok := RecoverToolCalls(quoted, offered("get_weather")); !ok {
		t.Error("this input is no longer recovered, so the documented limit has " +
			"moved: update the comment on RecoverToolCalls and this test rather " +
			"than deleting either")
	}
	// Asserted rather than skipped on purpose. A skip hides a limit; pinning it
	// means anyone who does find a way to tell the two apart has to say so.
}
