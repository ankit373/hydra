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

// sseServer serves an OpenAI-compatible chat stream and records the request
// body, so a test can assert what Hydra asked for as well as what it parsed.
type sseServer struct {
	*httptest.Server
	body []byte
}

func newSSEServer(t *testing.T, fragments []string, withUsage bool) *sseServer {
	t.Helper()
	s := &sseServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for _, f := range fragments {
			fmt.Fprintf(w, "data: {\"model\":\"stub-1\",\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", f)
			if fl != nil {
				fl.Flush()
			}
		}
		if withUsage {
			// The real shape: a final chunk with an EMPTY choices array whose
			// only payload is usage. A reader that requires a choice per chunk
			// drops the counts here.
			fmt.Fprint(w, "data: {\"model\":\"stub-1\",\"choices\":[],"+
				"\"usage\":{\"prompt_tokens\":31,\"completion_tokens\":64}}\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		if fl != nil {
			fl.Flush()
		}
	}))
	t.Cleanup(s.Server.Close)
	return s
}

// compatHead points the OpenAI-compatible path at a stub. Head.Endpoint takes
// precedence in openAICompatConfigFor, which is how a self-hosted server is
// addressed, so it needs no credentials and reaches no real provider.
func compatHead(t *testing.T, base string) provider.Head {
	t.Helper()
	return provider.Head{
		ID: "stub-1", Name: "stub", Provider: "openai", Source: "env",
		Endpoint: base, AuthReady: true, Meta: map[string]string{},
	}
}

// The whole reason this issue was filed with a warning: a streamed OpenAI
// response carries no usage unless the request asks for it.
func TestHTTPStream_AsksForUsageOnTheStream(t *testing.T) {
	srv := newSSEServer(t, []string{"hi"}, true)
	head := compatHead(t, srv.URL)

	if _, err := (&HTTPExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "p", Head: head}, func(string) {}); err != nil {
		t.Fatal(err)
	}

	var sent map[string]any
	if err := json.Unmarshal(srv.body, &sent); err != nil {
		t.Fatalf("request body did not parse: %v (%s)", err, srv.body)
	}
	if sent["stream"] != true {
		t.Error("stream was not requested")
	}
	opts, ok := sent["stream_options"].(map[string]any)
	if !ok {
		t.Fatalf("no stream_options in the request, so usage will not be reported: %s", srv.body)
	}
	if opts["include_usage"] != true {
		t.Errorf("stream_options.include_usage is %v, want true", opts["include_usage"])
	}
}

func TestHTTPStream_DeliversDeltasAndKeepsRealUsage(t *testing.T) {
	srv := newSSEServer(t, []string{"Hel", "lo ", "there"}, true)
	head := compatHead(t, srv.URL)

	var got []string
	resp, err := (&HTTPExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "p", Head: head}, func(d string) { got = append(got, d) })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || strings.Join(got, "") != "Hello there" {
		t.Errorf("deltas %q, want three fragments reassembling to the answer", got)
	}
	if resp.Output != "Hello there" {
		t.Errorf("Output %q", resp.Output)
	}
	if resp.InputTokens != 31 || resp.OutputTokens != 64 {
		t.Errorf("tokens %d/%d, want the reported 31/64 from the usage-only final chunk",
			resp.InputTokens, resp.OutputTokens)
	}
	if resp.TokensEstimated {
		t.Error("reported counts are labelled estimated")
	}
	if resp.Model != "stub-1" {
		t.Errorf("Model %q, want the model the chunks named", resp.Model)
	}
}

// A server that ignores stream_options must not produce a free dispatch.
func TestHTTPStream_NoUsageIsEstimatedAndLabelled(t *testing.T) {
	srv := newSSEServer(t, []string{"an answer"}, false)
	head := compatHead(t, srv.URL)

	resp, err := (&HTTPExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "a prompt", Head: head}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.TokensEstimated {
		t.Error("estimated counts are not labelled, so they read as measured spend")
	}
	if resp.InputTokens == 0 || resp.OutputTokens == 0 {
		t.Errorf("tokens %d/%d: a call that answered must not report zero",
			resp.InputTokens, resp.OutputTokens)
	}
}

// SSE carries comment keep-alives and event/id lines. Rendering them as
// content would put protocol noise in the answer.
func TestHTTPStream_IgnoresKeepAlivesAndNonDataLines(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		fmt.Fprint(w, ": keep-alive\n\n")
		fmt.Fprint(w, "event: message\nid: 1\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"only this\"}}]}\n\n")
		fmt.Fprint(w, ": another keep-alive\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		if fl != nil {
			fl.Flush()
		}
	}))
	defer srv.Close()
	head := compatHead(t, srv.URL)

	var got []string
	resp, err := (&HTTPExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "p", Head: head}, func(d string) { got = append(got, d) })
	if err != nil {
		t.Fatal(err)
	}
	if resp.Output != "only this" {
		t.Errorf("Output %q: protocol lines leaked into the answer", resp.Output)
	}
	if len(got) != 1 {
		t.Errorf("got %d deltas, want 1: a keep-alive fired a delta", len(got))
	}
}
