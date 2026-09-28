// SPDX-License-Identifier: MIT

package executor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func cohereEnv(t *testing.T, url string) {
	t.Helper()
	t.Setenv("CO_API_URL", url)
	t.Setenv("COHERE_API_KEY", "test-key")
	t.Setenv("COHERE_MODEL", "command-test")
}

func TestCohereToolChoice_TranslatesWhatItCanAndRefusesTheRest(t *testing.T) {
	for raw, want := range map[string]string{
		``:           "",
		`"auto"`:     "", // omitted: leaving it out is how the model decides
		`"required"`: "REQUIRED",
		`"none"`:     "NONE",
	} {
		got, err := cohereToolChoice(json.RawMessage(raw))
		if err != nil {
			t.Errorf("%s: %v", raw, err)
			continue
		}
		if got != want {
			t.Errorf("%s -> %q, want %q", raw, got, want)
		}
	}

	// Cohere cannot force a named tool. Downgrading to REQUIRED would call
	// something the caller did not ask for, which is the wrong kind of wrong.
	_, err := cohereToolChoice(json.RawMessage(`{"type":"function","function":{"name":"file_read"}}`))
	if err == nil {
		t.Fatal("naming a tool was accepted, so the constraint was silently changed")
	}
	if !strings.Contains(err.Error(), "file_read") {
		t.Errorf("the refusal does not name the tool: %v", err)
	}
}

func TestCohereFinish_TranslatesEveryReason(t *testing.T) {
	for reason, want := range map[string]string{
		"":              "",
		"COMPLETE":      "stop",
		"STOP_SEQUENCE": "stop",
		"MAX_TOKENS":    "length",
		"TOOL_CALL":     "tool_calls",
	} {
		got, err := cohereFinish(reason)
		if err != nil {
			t.Errorf("%s: %v", reason, err)
		}
		if got != want {
			t.Errorf("%s -> %q, want %q", reason, got, want)
		}
	}

	// A run the provider says errored is not an answer, and reporting it as
	// "stop" hands the caller a truncated one as a whole one.
	for _, reason := range []string{"ERROR", "TIMEOUT"} {
		if _, err := cohereFinish(reason); err == nil {
			t.Errorf("%s was reported as a completed answer", reason)
		}
	}
}

func TestExecuteCohere_SendsToolsUnchangedAndReadsTheCallsBack(t *testing.T) {
	srv, got := serve(t, 200, `{"finish_reason":"TOOL_CALL","message":{
		"tool_plan":"I will read the file.",
		"tool_calls":[{"id":"call_1","type":"function",
			"function":{"name":"file_read","arguments":"{\"path\":\"a.go\"}"}}]},
		"usage":{"tokens":{"input_tokens":31,"output_tokens":12}}}`)
	cohereEnv(t, srv.URL)

	resp, err := (&HTTPExecutor{}).Execute(context.Background(), Request{
		Prompt: "read a.go", Head: cohereHead(),
		Tools: []ToolDef{{Type: "function", Function: ToolFunction{
			Name: "file_read", Description: "read a file",
			Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
		}}},
		ToolChoice: json.RawMessage(`"required"`),
	})
	if err != nil {
		t.Fatal(err)
	}

	// A v2 tool definition is shaped exactly like OpenAI's, so what goes on the
	// wire is the caller's own object.
	tools, ok := got.body["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools on the wire: %v", got.body["tools"])
	}
	fn := tools[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "file_read" {
		t.Errorf("tool = %v", fn)
	}
	if got.body["tool_choice"] != "REQUIRED" {
		t.Errorf("tool_choice = %v, want REQUIRED", got.body["tool_choice"])
	}

	if len(resp.ToolCalls) != 1 {
		t.Fatalf("the call did not come back: %+v", resp)
	}
	if c := resp.ToolCalls[0]; c.ID != "call_1" || c.Function.Arguments != `{"path":"a.go"}` {
		t.Errorf("call read as %+v", c)
	}
	if resp.FinishReason != "tool_calls" {
		t.Errorf("finish reason = %q, want tool_calls", resp.FinishReason)
	}
	// tool_plan is the model's reasoning about what it will call, not its
	// answer, the same call this executor makes for a thinking delta.
	if resp.Output != "" {
		t.Errorf("output = %q, want empty: the plan is not the answer", resp.Output)
	}
}

func TestExecuteCohere_AnErroredRunIsNotAnAnswer(t *testing.T) {
	srv, _ := serve(t, 200, `{"finish_reason":"ERROR","message":{"content":[{"type":"text","text":"half an ans"}]}}`)
	cohereEnv(t, srv.URL)

	if _, err := (&HTTPExecutor{}).Execute(context.Background(),
		Request{Prompt: "p", Head: cohereHead()}); err == nil {
		t.Fatal("a run the provider said errored was returned as a complete answer")
	}
}

// Cohere documents these events as Python reprs, so whether tool_calls is an
// object or an array is not stated. Both shapes are accepted, and both are
// tested, rather than a guess being encoded once in the code and again in a
// stub that agrees with it.
func cohereToolEvents(asArray bool) []string {
	call := `{"id":"call_1","type":"function","function":{"name":"file_read","arguments":""}}`
	frag := `{"function":{"arguments":"{\"path\":\"a.go\"}"}}`
	if asArray {
		call, frag = "["+call+"]", "["+frag+"]"
	}
	return []string{
		`{"type":"tool-plan-delta","delta":{"message":{"tool_plan":"I will read it."}}}`,
		`{"type":"tool-call-start","index":0,"delta":{"message":{"tool_calls":` + call + `}}}`,
		`{"type":"tool-call-delta","index":0,"delta":{"message":{"tool_calls":` + frag + `}}}`,
		`{"type":"tool-call-end","index":0}`,
		`{"type":"message-end","delta":{"finish_reason":"TOOL_CALL",` +
			`"usage":{"tokens":{"input_tokens":31,"output_tokens":12}}}}`,
	}
}

func TestStreamCohere_AssemblesCallsWhicheverShapeTheWireUses(t *testing.T) {
	for _, asArray := range []bool{false, true} {
		shape := "object"
		if asArray {
			shape = "array"
		}
		t.Run(shape, func(t *testing.T) {
			newCohereStub(t, cohereToolEvents(asArray))

			resp, err := (&HTTPExecutor{}).ExecuteStream(context.Background(),
				Request{Prompt: "read a.go", Head: cohereHead(),
					Tools: []ToolDef{{Function: ToolFunction{Name: "file_read"}}}}, func(string) {})
			if err != nil {
				t.Fatal(err)
			}
			if len(resp.ToolCalls) != 1 {
				t.Fatalf("got %d calls: %+v", len(resp.ToolCalls), resp.ToolCalls)
			}
			c := resp.ToolCalls[0]
			if c.ID != "call_1" || c.Function.Name != "file_read" {
				t.Errorf("identity lost: %+v", c)
			}
			var args map[string]string
			if err := json.Unmarshal([]byte(c.Function.Arguments), &args); err != nil {
				t.Fatalf("arguments did not reassemble: %q", c.Function.Arguments)
			}
			if args["path"] != "a.go" {
				t.Errorf("arguments = %q", c.Function.Arguments)
			}
			if resp.FinishReason != "tool_calls" {
				t.Errorf("finish reason = %q", resp.FinishReason)
			}
			// The plan streamed past and is not the answer.
			if resp.Output != "" {
				t.Errorf("output = %q, want empty", resp.Output)
			}
			if resp.InputTokens != 31 || resp.OutputTokens != 12 {
				t.Errorf("counts = %d/%d, want 31/12", resp.InputTokens, resp.OutputTokens)
			}
		})
	}
}

func TestStreamCohere_AnErroredStreamIsNotAnAnswer(t *testing.T) {
	newCohereStub(t, []string{
		cohereText("half an ans"),
		`{"type":"message-end","delta":{"finish_reason":"TIMEOUT"}}`,
	})

	if _, err := (&HTTPExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "p", Head: cohereHead()}, func(string) {}); err == nil {
		t.Fatal("a stream that timed out was returned as a complete answer")
	}
}

func TestCohere_StreamedAndBufferedAgreeOnTheSameCall(t *testing.T) {
	newCohereStub(t, cohereToolEvents(false))
	streamed, err := (&HTTPExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "p", Head: cohereHead()}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}

	srv, _ := serve(t, 200, `{"finish_reason":"TOOL_CALL","message":{"tool_calls":[
		{"id":"call_1","type":"function","function":{"name":"file_read","arguments":"{\"path\":\"a.go\"}"}}]}}`)
	cohereEnv(t, srv.URL)
	buffered, err := (&HTTPExecutor{}).Execute(context.Background(),
		Request{Prompt: "p", Head: cohereHead()})
	if err != nil {
		t.Fatal(err)
	}

	if len(streamed.ToolCalls) != len(buffered.ToolCalls) {
		t.Fatalf("streamed %d calls, buffered %d", len(streamed.ToolCalls), len(buffered.ToolCalls))
	}
	s, b := streamed.ToolCalls[0], buffered.ToolCalls[0]
	if s.ID != b.ID || s.Function.Name != b.Function.Name ||
		s.Function.Arguments != b.Function.Arguments || s.Index != b.Index {
		t.Errorf("the two paths read one answer differently:\n streamed %+v\n buffered %+v", s, b)
	}
	if streamed.FinishReason != buffered.FinishReason {
		t.Errorf("finish reason differs: %q vs %q", streamed.FinishReason, buffered.FinishReason)
	}
}
