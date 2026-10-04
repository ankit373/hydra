// SPDX-License-Identifier: MIT

package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ankit373/hydra/internal/testutil"
)

func execAgainst(t *testing.T, body string, tools []ToolDef) *Response {
	t.Helper()
	testutil.NewSandbox(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	resp, err := (&HTTPExecutor{}).Execute(context.Background(), Request{
		Prompt: "weather in Paris?", Head: stubHead(srv.URL), MaxTokens: 64, Tools: tools,
	})
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// The whole point, end to end through the executor: a server that reports the
// call as message text still produces a usable tool call, so an agent loop can
// continue instead of receiving a JSON blob it cannot act on.
func TestExecute_RecoversAToolCallWrittenAsText(t *testing.T) {
	body := `{"model":"m","choices":[{"finish_reason":"stop","message":{"content":
	  "{\"name\": \"get_weather\", \"arguments\": {\"city\": \"Paris\"}}"}}],"usage":{}}`

	resp := execAgainst(t, body, []ToolDef{weatherTool})

	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Function.Name != "get_weather" {
		t.Fatalf("no usable tool call: %+v", resp.ToolCalls)
	}
	if !resp.ToolCallsRecovered {
		t.Error("the call is Hydra's reading of the text and must be recorded as such")
	}
	// An OpenAI client branches on this, and "stop" is what stops the loop.
	if resp.FinishReason != "tool_calls" {
		t.Errorf("FinishReason = %q, want tool_calls", resp.FinishReason)
	}
	// The text was the call, so leaving it as content shows the client a JSON
	// blob beside the call it already has.
	if resp.Output != "" {
		t.Errorf("Output = %q, want empty once the text has been read as a call", resp.Output)
	}
}

// A server that structures its calls is never second-guessed. qwen3:0.6b does
// this on the same Ollama install that Qwen2.5-Coder does not, so both paths
// are live on one machine.
func TestExecute_DoesNotSecondGuessAStructuredCall(t *testing.T) {
	body := `{"model":"m","choices":[{"finish_reason":"tool_calls","message":{"content":"",
	  "tool_calls":[{"id":"srv-1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}]}}],"usage":{}}`

	resp := execAgainst(t, body, []ToolDef{weatherTool})

	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "srv-1" {
		t.Fatalf("the server's own call was replaced: %+v", resp.ToolCalls)
	}
	if resp.ToolCallsRecovered {
		t.Error("ToolCallsRecovered is set for a call the server reported itself")
	}
}

// And with no tools offered the identical content is ordinary text. Measured
// through the real endpoint too: asked to print that exact JSON with no tools
// in the request, the answer came back as content with finish_reason stop.
func TestExecute_LeavesTheSameTextAloneWhenNoToolsWereOffered(t *testing.T) {
	body := `{"model":"m","choices":[{"finish_reason":"stop","message":{"content":
	  "{\"name\": \"get_weather\", \"arguments\": {\"city\": \"Paris\"}}"}}],"usage":{}}`

	resp := execAgainst(t, body, nil)

	if len(resp.ToolCalls) != 0 {
		t.Errorf("invented %d tool call(s) for a request carrying no tools", len(resp.ToolCalls))
	}
	if resp.Output == "" {
		t.Error("the model's text was swallowed")
	}
	if resp.FinishReason != "stop" {
		t.Errorf("FinishReason = %q, want stop", resp.FinishReason)
	}
}
