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

type cohereStub struct {
	*httptest.Server
	body   []byte
	header http.Header
	paths  []string
}

func newCohereStub(t *testing.T, events []string) *cohereStub {
	t.Helper()
	s := &cohereStub{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.body, _ = io.ReadAll(r.Body)
		s.header = r.Header.Clone()
		s.paths = append(s.paths, r.URL.Path)
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for _, e := range events {
			fmt.Fprintf(w, "data: %s\n\n", e)
			if fl != nil {
				fl.Flush()
			}
		}
	}))
	t.Cleanup(s.Server.Close)
	t.Setenv("CO_API_URL", s.Server.URL)
	t.Setenv("COHERE_API_KEY", "test-key")
	t.Setenv("COHERE_MODEL", "command-test")
	return s
}

func cohereHead() provider.Head {
	return provider.Head{
		ID: "cohere", Name: "Cohere", Provider: "cohere",
		Source: "env", AuthReady: true, Meta: map[string]string{},
	}
}

func cohereText(text string) string {
	b, _ := json.Marshal(text)
	return fmt.Sprintf(`{"type":"content-delta","index":0,"delta":{"message":{"content":{"text":%s}}}}`, b)
}

// usage.tokens and usage.billed_units are different numbers for the same call.
// The buffered path reports tokens, so this one has to as well.
const cohereEnd = `{"type":"message-end","delta":{"finish_reason":"COMPLETE",` +
	`"usage":{"billed_units":{"input_tokens":4,"output_tokens":9},` +
	`"tokens":{"input_tokens":72,"output_tokens":21}}}}`

func TestCohereStream_ReassemblesDeltasAndReportsTheTokenCounts(t *testing.T) {
	newCohereStub(t, []string{
		`{"type":"message-start","id":"abc","delta":{"message":{"role":"assistant"}}}`,
		`{"type":"content-start","index":0,"delta":{"message":{"content":{"type":"text","text":""}}}}`,
		cohereText("One "),
		cohereText("answer."),
		`{"type":"content-end","index":0}`,
		cohereEnd,
	})

	var got []string
	resp, err := (&HTTPExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "ask", Head: cohereHead()}, func(d string) { got = append(got, d) })
	if err != nil {
		t.Fatal(err)
	}

	if resp.Output != "One answer." {
		t.Errorf("Output = %q, want %q", resp.Output, "One answer.")
	}
	if strings.Join(got, "") != resp.Output {
		t.Errorf("deltas %q do not reassemble to Output %q", got, resp.Output)
	}
	if resp.InputTokens != 72 || resp.OutputTokens != 21 {
		t.Errorf("counts = %d/%d, want 72/21 from usage.tokens, not 4/9 from billed_units",
			resp.InputTokens, resp.OutputTokens)
	}
	if resp.TokensEstimated {
		t.Error("TokensEstimated = true, but message-end reported both counts")
	}
	if resp.TTFT <= 0 {
		t.Error("TTFT = 0, want the measured time to the first delta")
	}
}

func TestCohereStream_NoUsageIsEstimatedAndLabelled(t *testing.T) {
	newCohereStub(t, []string{cohereText("an answer with no counts")})

	resp, err := (&HTTPExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "ask", Head: cohereHead()}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.TokensEstimated {
		t.Error("TokensEstimated = false, so a call with no reported usage would log as free")
	}
	if resp.InputTokens == 0 || resp.OutputTokens == 0 {
		t.Errorf("counts = %d/%d, want estimates rather than zero", resp.InputTokens, resp.OutputTokens)
	}
}

func TestCohereStream_AsksToStreamAndAuthenticates(t *testing.T) {
	stub := newCohereStub(t, []string{cohereText("hi"), cohereEnd})

	if _, err := (&HTTPExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "p", System: "be brief", Head: cohereHead()}, func(string) {}); err != nil {
		t.Fatal(err)
	}

	var sent map[string]any
	if err := json.Unmarshal(stub.body, &sent); err != nil {
		t.Fatalf("request body did not parse: %v (%s)", err, stub.body)
	}
	if sent["stream"] != true {
		t.Errorf("stream = %v, want true: without it the server answers in one block", sent["stream"])
	}
	if got := stub.header.Get("Authorization"); got != "Bearer test-key" {
		t.Errorf("Authorization = %q, want the configured key", got)
	}
}

// Streaming and buffered must resolve the same endpoint, or a gateway serves
// one of them and the other quietly goes to Cohere.
func TestCohere_BothPathsResolveTheSameEndpoint(t *testing.T) {
	stub := newCohereStub(t, []string{cohereText("hi"), cohereEnd})

	ex := &HTTPExecutor{}
	req := Request{Prompt: "p", Head: cohereHead()}
	if _, err := ex.ExecuteStream(context.Background(), req, func(string) {}); err != nil {
		t.Fatal(err)
	}
	// The buffered path decodes JSON, so the event text it gets back is not a
	// valid body; the path it asked for is what this asserts.
	_, _ = ex.Execute(context.Background(), req)

	if len(stub.paths) != 2 {
		t.Fatalf("stub saw %d requests, want both paths to reach it: %v", len(stub.paths), stub.paths)
	}
	if stub.paths[0] != "/v2/chat" || stub.paths[1] != stub.paths[0] {
		t.Errorf("paths = %v, want both at /v2/chat", stub.paths)
	}
}
