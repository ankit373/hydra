// SPDX-License-Identifier: MIT

package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func contentChunk(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return toolChunk(`{"content":` + string(b) + `}`)
}

const stopChunk = `{"model":"stub-1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`

// streamText streams fragments as content and returns the response and the
// deltas a client was shown.
func streamText(t *testing.T, fragments []string, tools []ToolDef) (*Response, []string) {
	t.Helper()
	chunks := make([]string, 0, len(fragments)+1)
	for _, f := range fragments {
		chunks = append(chunks, contentChunk(t, f))
	}
	srv := newToolSSEServer(t, append(chunks, stopChunk))

	var got []string
	resp, err := (&HTTPExecutor{}).ExecuteStream(context.Background(), Request{
		Prompt: "weather in Paris?", Head: compatHead(t, srv.URL), Tools: tools,
	}, func(d string) { got = append(got, d) })
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	return resp, got
}

// The defect: with stream: true the loop stopped on turn one, because the call
// arrived as content and finish_reason stop. Red before #1223.
func TestHTTPStream_RecoversAToolCallWrittenAsText(t *testing.T) {
	resp, deltas := streamText(t, []string{
		`{"name": "get_`, `weather", "argu`, `ments": {"city": "Paris"}}`,
	}, []ToolDef{weatherTool})

	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Function.Name != "get_weather" {
		t.Fatalf("no usable tool call: %+v", resp.ToolCalls)
	}
	if args := resp.ToolCalls[0].Function.Arguments; args != `{"city": "Paris"}` {
		t.Errorf("arguments %q", args)
	}
	if !resp.ToolCallsRecovered {
		t.Error("the call is Hydra's reading of the text and must be recorded as such")
	}
	if resp.FinishReason != "tool_calls" {
		t.Errorf("FinishReason = %q, want tool_calls", resp.FinishReason)
	}
	if resp.Output != "" || len(deltas) != 0 {
		t.Errorf("the call leaked as text: Output %q, deltas %q", resp.Output, deltas)
	}
	if resp.TTFT == 0 {
		t.Error("an answer made only of a recovered call reports no TTFT")
	}
}

// One byte per delta, in the array form, is the most a server can split it.
func TestHTTPStream_RecoversAnArrayOfCallsSplitByTheByte(t *testing.T) {
	call := ` [ {"name": "get_weather", "arguments": {"city": "Paris"}}, ` +
		`{"name": "get_weather", "arguments": {"city": "Rome"}} ]`
	frags := make([]string, 0, len(call))
	for i := range call {
		frags = append(frags, call[i:i+1])
	}

	resp, deltas := streamText(t, frags, []ToolDef{weatherTool})

	if len(resp.ToolCalls) != 2 || len(deltas) != 0 {
		t.Fatalf("calls %+v, deltas %q", resp.ToolCalls, deltas)
	}
}

// Text that is not a call reaches the client exactly as the server sent it,
// fragment for fragment, whether it gives up the hold early or at the end.
func TestHTTPStream_TextThatIsNotACallIsUnchanged(t *testing.T) {
	cases := map[string][]string{
		"prose":                           {"\n", "  ", "It is ", "sunny."},
		"a bracket, not JSON":             {"[1] ", "see the docs"},
		"an object, not a call":           {"{ ", "x: 1 }"},
		"a call to a tool nobody offered": {`{"name": "rm_rf", `, `"arguments": {}}`},
		"a call with prose after it":      {`{"name": "get_weather", "arguments": {}}`, " done"},
	}
	for name, frags := range cases {
		t.Run(name, func(t *testing.T) {
			_, plain := streamText(t, frags, nil)
			resp, held := streamText(t, frags, []ToolDef{weatherTool})

			if !reflect.DeepEqual(held, plain) {
				t.Errorf("deltas %q, want %q as sent", held, plain)
			}
			if resp.Output != strings.Join(frags, "") {
				t.Errorf("Output %q", resp.Output)
			}
			if len(resp.ToolCalls) != 0 || resp.FinishReason != "stop" {
				t.Errorf("invented a call: %+v, finish %q", resp.ToolCalls, resp.FinishReason)
			}
		})
	}
}

// Prose is released on its first token, not when the stream ends: the server
// will not send the rest until the client has seen the first fragment.
func TestHTTPStream_ProseIsNotHeldPastItsFirstToken(t *testing.T) {
	seen := make(chan struct{})
	var waited atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		fmt.Fprintf(w, "data: %s\n\n", contentChunk(t, "It is"))
		fl.Flush()
		select {
		case <-seen:
		case <-time.After(2 * time.Second):
			waited.Store(true)
		}
		fmt.Fprintf(w, "data: %s\n\ndata: %s\n\ndata: [DONE]\n\n", contentChunk(t, " sunny."), stopChunk)
		fl.Flush()
	}))
	t.Cleanup(srv.Close)

	var once bool
	_, err := (&HTTPExecutor{}).ExecuteStream(context.Background(), Request{
		Prompt: "weather?", Head: compatHead(t, srv.URL), Tools: []ToolDef{weatherTool},
	}, func(string) {
		if !once {
			once = true
			close(seen)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if waited.Load() {
		t.Error("the first prose token was held until the stream went on without it")
	}
}

// A server that structures its call is never second-guessed, even after a
// whitespace delta the hold had taken.
func TestHTTPStream_AStructuredCallReleasesTheHold(t *testing.T) {
	srv := newToolSSEServer(t, []string{
		contentChunk(t, "\n"),
		toolChunk(`{"tool_calls":[{"index":0,"id":"srv-1","type":"function",` +
			`"function":{"name":"get_weather","arguments":"{}"}}]}`),
		`{"model":"stub-1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	})
	resp := streamOnce(t, srv, Request{Prompt: "p", Tools: []ToolDef{weatherTool}})

	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "srv-1" || resp.ToolCallsRecovered {
		t.Fatalf("the server's own call was replaced: %+v recovered=%v", resp.ToolCalls, resp.ToolCallsRecovered)
	}
	if resp.Output != "\n" {
		t.Errorf("held text was lost: Output %q", resp.Output)
	}
}

func TestOpensCall(t *testing.T) {
	cases := []struct {
		in                string
		possible, settled bool
	}{
		{"", true, false},
		{"  \n", true, false},
		{"{", true, false},
		{"[ ", true, false},
		{"[{", true, false},
		{`{"`, true, true},
		{` [ { "n`, true, true},
		{"{}", false, false},
		{"[]", false, false},
		{"Hello", false, false},
		{"{ x", false, false},
	}
	for _, c := range cases {
		possible, settled := opensCall(c.in)
		if possible != c.possible || settled != c.settled {
			t.Errorf("opensCall(%q) = %v, %v, want %v, %v", c.in, possible, settled, c.possible, c.settled)
		}
	}
}
