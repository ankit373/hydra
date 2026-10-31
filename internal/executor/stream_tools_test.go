// SPDX-License-Identifier: MIT

package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// toolSSEServer emits whatever chunks a test names, because the shapes that
// matter here are the ones a content-only stub cannot produce: a delta with no
// content, a call split down the middle, two calls interleaved by index.
type toolSSEServer struct {
	*httptest.Server
	body []byte
}

func newToolSSEServer(t *testing.T, chunks []string) *toolSSEServer {
	t.Helper()
	s := &toolSSEServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
			if fl != nil {
				fl.Flush()
			}
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		if fl != nil {
			fl.Flush()
		}
	}))
	t.Cleanup(s.Server.Close)
	return s
}

func toolChunk(delta string) string {
	return `{"model":"stub-1","choices":[{"index":0,"delta":` + delta + `}]}`
}

var weatherTool = ToolDef{
	Type: "function",
	Function: ToolFunction{
		Name:        "get_weather",
		Description: "look up the weather",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`),
	},
}

func streamOnce(t *testing.T, srv *toolSSEServer, req Request) *Response {
	t.Helper()
	req.Head = compatHead(t, srv.URL)
	resp, err := (&HTTPExecutor{}).ExecuteStream(context.Background(), req, func(string) {})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	return resp
}

// The defect itself: the tools were built into the request struct and then not
// set, so omitempty dropped them and the head answered a different question.
func TestHTTPStream_SendsTheToolsItWasGiven(t *testing.T) {
	srv := newToolSSEServer(t, []string{toolChunk(`{"content":"hi"}`)})
	streamOnce(t, srv, Request{
		Prompt:     "what is the weather",
		Tools:      []ToolDef{weatherTool},
		ToolChoice: json.RawMessage(`"auto"`),
	})

	var sent map[string]any
	if err := json.Unmarshal(srv.body, &sent); err != nil {
		t.Fatalf("request body did not parse: %v (%s)", err, srv.body)
	}
	tools, ok := sent["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("the streamed request carries no tools, so the head cannot call one: %s", srv.body)
	}
	fn := tools[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Errorf("tool name on the wire is %v, want get_weather", fn["name"])
	}
	if sent["tool_choice"] != "auto" {
		t.Errorf("tool_choice on the wire is %v, want auto", sent["tool_choice"])
	}
}

func TestHTTPStream_ReassemblesArgumentsSplitAcrossChunks(t *testing.T) {
	srv := newToolSSEServer(t, []string{
		toolChunk(`{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":""}}]}`),
		toolChunk(`{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":"}}]}`),
		toolChunk(`{"tool_calls":[{"index":0,"function":{"arguments":"\"Berlin\"}"}}]}`),
	})
	resp := streamOnce(t, srv, Request{Prompt: "p", Tools: []ToolDef{weatherTool}})

	if len(resp.ToolCalls) != 1 {
		t.Fatalf("got %d calls, want the fragments folded into 1: %+v", len(resp.ToolCalls), resp.ToolCalls)
	}
	got := resp.ToolCalls[0]
	if got.ID != "call_1" || got.Function.Name != "get_weather" {
		t.Errorf("identity lost across fragments: %+v", got)
	}
	var args map[string]string
	if err := json.Unmarshal([]byte(got.Function.Arguments), &args); err != nil {
		t.Fatalf("arguments did not reassemble into JSON: %q", got.Function.Arguments)
	}
	if args["city"] != "Berlin" {
		t.Errorf("arguments are %q, want city Berlin", got.Function.Arguments)
	}
}

func TestHTTPStream_KeepsParallelCallsApartByIndex(t *testing.T) {
	srv := newToolSSEServer(t, []string{
		toolChunk(`{"tool_calls":[{"index":0,"id":"a","function":{"name":"get_weather","arguments":"{\"city\":"}}]}`),
		toolChunk(`{"tool_calls":[{"index":1,"id":"b","function":{"name":"get_time","arguments":"{\"zone\":"}}]}`),
		toolChunk(`{"tool_calls":[{"index":0,"function":{"arguments":"\"Berlin\"}"}}]}`),
		toolChunk(`{"tool_calls":[{"index":1,"function":{"arguments":"\"CET\"}"}}]}`),
	})
	resp := streamOnce(t, srv, Request{Prompt: "p", Tools: []ToolDef{weatherTool}})

	if len(resp.ToolCalls) != 2 {
		t.Fatalf("got %d calls, want 2 kept apart: %+v", len(resp.ToolCalls), resp.ToolCalls)
	}
	want := map[string]string{"a": `{"city":"Berlin"}`, "b": `{"zone":"CET"}`}
	for _, c := range resp.ToolCalls {
		if want[c.ID] != c.Function.Arguments {
			t.Errorf("call %s reassembled as %q, want %q", c.ID, c.Function.Arguments, want[c.ID])
		}
	}
}

// A server with nothing to interleave sends no index at all, so both calls
// arrive as index 0. Folding on the index alone concatenates them into one call
// whose arguments parse as nothing.
func TestHTTPStream_KeepsWholeCallsApartWhenTheServerSendsNoIndex(t *testing.T) {
	srv := newToolSSEServer(t, []string{
		toolChunk(`{"tool_calls":[{"id":"a","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Berlin\"}"}}]}`),
		toolChunk(`{"tool_calls":[{"id":"b","type":"function","function":{"name":"get_time","arguments":"{\"zone\":\"CET\"}"}}]}`),
	})
	resp := streamOnce(t, srv, Request{Prompt: "p", Tools: []ToolDef{weatherTool}})

	if len(resp.ToolCalls) != 2 {
		t.Fatalf("got %d calls, want 2: %+v", len(resp.ToolCalls), resp.ToolCalls)
	}
	for _, c := range resp.ToolCalls {
		var args map[string]string
		if err := json.Unmarshal([]byte(c.Function.Arguments), &args); err != nil {
			t.Errorf("call %s has unparseable arguments %q, which is what concatenation looks like",
				c.ID, c.Function.Arguments)
		}
	}
}

func TestHTTPStream_CarriesTheFinishReason(t *testing.T) {
	srv := newToolSSEServer(t, []string{
		toolChunk(`{"tool_calls":[{"index":0,"id":"a","function":{"name":"get_weather","arguments":"{}"}}]}`),
		`{"model":"stub-1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	})
	resp := streamOnce(t, srv, Request{Prompt: "p", Tools: []ToolDef{weatherTool}})

	if resp.FinishReason != "tool_calls" {
		t.Errorf("finish_reason is %q, want tool_calls; a client branches on this, not on the text",
			resp.FinishReason)
	}
}

// An answer made only of tool calls carries no content, and the shared
// transport treated a stream with no text as one that produced nothing.
func TestHTTPStream_AnAnswerOfOnlyToolCallsIsNotEmpty(t *testing.T) {
	srv := newToolSSEServer(t, []string{
		toolChunk(`{"tool_calls":[{"index":0,"id":"a","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Berlin\"}"}}]}`),
		`{"model":"stub-1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	})
	head := compatHead(t, srv.URL)

	resp, err := (&HTTPExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "p", Head: head, Tools: []ToolDef{weatherTool}}, func(string) {})
	if err != nil {
		t.Fatalf("a tool-calling answer was reported as a failure: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("got %d calls, want 1: %+v", len(resp.ToolCalls), resp.ToolCalls)
	}
	if resp.Output != "" {
		t.Errorf("output is %q, want empty: the call is the answer", resp.Output)
	}
	if resp.TTFT == 0 {
		t.Error("TTFT is unmeasured, but the head produced a tool call at a measurable time")
	}
}

// Two readings of one answer must not disagree: a client cannot tell which path
// it took, and a head that streams today buffers tomorrow behind a proxy.
func TestHTTPStream_AgreesWithTheBufferedPathOnTheSameCall(t *testing.T) {
	const id, name, args = "call_1", "get_weather", `{"city":"Berlin"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var in map[string]any
		_ = json.Unmarshal(raw, &in)
		if in["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: %s\n\n", toolChunk(fmt.Sprintf(
				`{"tool_calls":[{"index":0,"id":%q,"type":"function","function":{"name":%q,"arguments":%q}}]}`,
				id, name, args)))
			fmt.Fprint(w, "data: {\"model\":\"stub-1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		fmt.Fprintf(w, `{"model":"stub-1","choices":[{"message":{"content":"","tool_calls":`+
			`[{"id":%q,"type":"function","function":{"name":%q,"arguments":%q}}]},`+
			`"finish_reason":"tool_calls"}]}`, id, name, args)
	}))
	t.Cleanup(srv.Close)

	head := compatHead(t, srv.URL)
	req := Request{Prompt: "p", Head: head, Tools: []ToolDef{weatherTool}}
	ex := &HTTPExecutor{}

	buffered, err := ex.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("buffered: %v", err)
	}
	streamed, err := ex.ExecuteStream(context.Background(), req, func(string) {})
	if err != nil {
		t.Fatalf("streamed: %v", err)
	}

	if fmt.Sprint(buffered.ToolCalls) != fmt.Sprint(streamed.ToolCalls) {
		t.Errorf("the two paths read one answer differently:\n buffered %+v\n streamed %+v",
			buffered.ToolCalls, streamed.ToolCalls)
	}
	if buffered.FinishReason != streamed.FinishReason {
		t.Errorf("finish reason differs: buffered %q, streamed %q",
			buffered.FinishReason, streamed.FinishReason)
	}
}

// A name split the way the arguments beside it are split has to fold, or the
// agent asks for a tool whose name is only its own last fragment.
func TestHTTPStream_ReassemblesANameSplitAcrossChunks(t *testing.T) {
	srv := newToolSSEServer(t, []string{
		toolChunk(`{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_wea","arguments":"{\"ci"}}]}`),
		toolChunk(`{"tool_calls":[{"index":0,"function":{"name":"ther","arguments":"ty\":\"Paris\"}"}}]}`),
	})
	resp := streamOnce(t, srv, Request{Prompt: "p", Tools: []ToolDef{weatherTool}})

	if len(resp.ToolCalls) != 1 {
		t.Fatalf("got %d calls, want 1: %+v", len(resp.ToolCalls), resp.ToolCalls)
	}
	if got := resp.ToolCalls[0].Function.Name; got != "get_weather" {
		t.Errorf("name folded to %q, want get_weather", got)
	}
}

// A server that restates the whole name on every fragment must not have it
// doubled. Its arguments are what say the two fragments are one call.
func TestHTTPStream_ARestatedNameIsNotDoubled(t *testing.T) {
	srv := newToolSSEServer(t, []string{
		toolChunk(`{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":"}}]}`),
		toolChunk(`{"tool_calls":[{"index":0,"function":{"name":"get_weather","arguments":"\"Paris\"}"}}]}`),
	})
	resp := streamOnce(t, srv, Request{Prompt: "p", Tools: []ToolDef{weatherTool}})

	if len(resp.ToolCalls) != 1 {
		t.Fatalf("got %d calls, want 1: %+v", len(resp.ToolCalls), resp.ToolCalls)
	}
	if got := resp.ToolCalls[0].Function.Name; got != "get_weather" {
		t.Errorf("name folded to %q, want get_weather", got)
	}
}

// The shape OpenAI itself sends: the name once, complete, then arguments only.
func TestHTTPStream_ANameSentOnceIsKept(t *testing.T) {
	srv := newToolSSEServer(t, []string{
		toolChunk(`{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":""}}]}`),
		toolChunk(`{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":\"Paris\"}"}}]}`),
	})
	resp := streamOnce(t, srv, Request{Prompt: "p", Tools: []ToolDef{weatherTool}})

	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Function.Name != "get_weather" {
		t.Errorf("the ordinary shape did not survive: %+v", resp.ToolCalls)
	}
}
