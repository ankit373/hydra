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

type geminiStub struct {
	*httptest.Server
	body    []byte
	header  http.Header
	targets []string
}

// newGeminiStub points both Gemini paths at itself through GEMINI_BASE_URL,
// which is also how a gateway is addressed in production.
func newGeminiStub(t *testing.T, chunks []string) *geminiStub {
	t.Helper()
	s := &geminiStub{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.body, _ = io.ReadAll(r.Body)
		s.header = r.Header.Clone()
		s.targets = append(s.targets, r.URL.RequestURI())
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
			if fl != nil {
				fl.Flush()
			}
		}
	}))
	t.Cleanup(s.Server.Close)
	t.Setenv("GEMINI_BASE_URL", s.Server.URL)
	t.Setenv("GEMINI_API_KEY", "test-key")
	t.Setenv("GEMINI_MODEL", "gemini-test")
	return s
}

func geminiHead() provider.Head {
	return provider.Head{
		ID: "google", Name: "Gemini", Provider: "google",
		Source: "env", AuthReady: true, Meta: map[string]string{},
	}
}

// geminiText is one chunk: some answer text and the running usage, which is
// cumulative and repeated on every chunk.
func geminiText(text string, prompt, candidates int) string {
	b, _ := json.Marshal(text)
	return fmt.Sprintf(`{"candidates":[{"content":{"parts":[{"text":%s}],"role":"model"},"index":0}],`+
		`"usageMetadata":{"promptTokenCount":%d,"candidatesTokenCount":%d,"totalTokenCount":%d},`+
		`"modelVersion":"gemini-test-002"}`, b, prompt, candidates, prompt+candidates)
}

func TestGeminiStream_ReassemblesDeltasAndKeepsTheLastUsage(t *testing.T) {
	newGeminiStub(t, []string{
		geminiText("A ", 17, 1),
		geminiText("routed ", 17, 5),
		geminiText("answer.", 17, 12),
	})

	var got []string
	resp, err := (&HTTPExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "ask", Head: geminiHead()}, func(d string) { got = append(got, d) })
	if err != nil {
		t.Fatal(err)
	}

	if resp.Output != "A routed answer." {
		t.Errorf("Output = %q, want %q", resp.Output, "A routed answer.")
	}
	if strings.Join(got, "") != resp.Output {
		t.Errorf("deltas %q do not reassemble to Output %q", got, resp.Output)
	}
	// usageMetadata is cumulative and repeats, so the first chunk's count is
	// the count of a few tokens, not of the answer.
	if resp.OutputTokens != 12 {
		t.Errorf("OutputTokens = %d, want 12: the last usageMetadata, not the first", resp.OutputTokens)
	}
	if resp.InputTokens != 17 {
		t.Errorf("InputTokens = %d, want 17", resp.InputTokens)
	}
	if resp.TokensEstimated {
		t.Error("TokensEstimated = true, but the provider reported both counts")
	}
	if resp.Model != "gemini-test-002" {
		t.Errorf("Model = %q, want the version the chunks reported", resp.Model)
	}
	if resp.TTFT <= 0 {
		t.Error("TTFT = 0, want the measured time to the first delta")
	}
}

// Without alt=sse the same endpoint answers with a streamed JSON array, which
// an SSE reader gets nothing at all from.
func TestGeminiStream_AsksForSSEAndStreamGenerate(t *testing.T) {
	stub := newGeminiStub(t, []string{geminiText("hi", 3, 1)})

	if _, err := (&HTTPExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "p", System: "be brief", Head: geminiHead()}, func(string) {}); err != nil {
		t.Fatal(err)
	}

	if len(stub.targets) != 1 {
		t.Fatalf("stub saw %d requests, want 1", len(stub.targets))
	}
	if !strings.Contains(stub.targets[0], ":streamGenerateContent") {
		t.Errorf("target = %q, want :streamGenerateContent", stub.targets[0])
	}
	if !strings.Contains(stub.targets[0], "alt=sse") {
		t.Errorf("target = %q, want alt=sse: without it the response is a JSON array", stub.targets[0])
	}
	if got := stub.header.Get("x-goog-api-key"); got != "test-key" {
		t.Errorf("x-goog-api-key = %q, want the configured key", got)
	}

	var sent map[string]any
	if err := json.Unmarshal(stub.body, &sent); err != nil {
		t.Fatalf("request body did not parse: %v (%s)", err, stub.body)
	}
	if sent["system_instruction"] == nil {
		t.Error("system_instruction missing, so the system prompt was dropped on the streamed path")
	}
}

func TestGeminiStream_NoUsageIsEstimatedAndLabelled(t *testing.T) {
	newGeminiStub(t, []string{
		`{"candidates":[{"content":{"parts":[{"text":"an answer with no counts"}],"role":"model"}}]}`,
	})

	resp, err := (&HTTPExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "ask", Head: geminiHead()}, func(string) {})
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

func TestGeminiStream_BlockedPromptIsARefusalNotAnEmptyAnswer(t *testing.T) {
	newGeminiStub(t, []string{
		`{"promptFeedback":{"blockReason":"SAFETY","safetyRatings":[{"category":"HARM_CATEGORY_DANGEROUS","probability":"HIGH"}]}}`,
	})

	resp, err := (&HTTPExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "ask", Head: geminiHead()}, func(string) {})
	if err == nil {
		t.Fatalf("a blocked prompt returned %q instead of failing", resp.Output)
	}
	if !strings.Contains(err.Error(), "SAFETY") {
		t.Errorf("error = %v, want the block reason in it", err)
	}
}

// Streaming and buffered must resolve the same host, or a gateway serves one of
// them and the other quietly goes to Google.
func TestGemini_BothPathsResolveTheSameHost(t *testing.T) {
	stub := newGeminiStub(t, []string{geminiText("hi", 3, 1)})

	ex := &HTTPExecutor{}
	req := Request{Prompt: "p", Head: geminiHead()}
	if _, err := ex.ExecuteStream(context.Background(), req, func(string) {}); err != nil {
		t.Fatal(err)
	}
	// The buffered path decodes JSON, so the event text it gets back is not a
	// valid body; what it asked for is what this asserts.
	_, _ = ex.Execute(context.Background(), req)

	if len(stub.targets) != 2 {
		t.Fatalf("stub saw %d requests, want both paths to reach it: %v", len(stub.targets), stub.targets)
	}
	if !strings.HasPrefix(stub.targets[1], "/v1beta/models/gemini-test:generateContent") {
		t.Errorf("buffered target = %q, want the same host and model, without the stream method", stub.targets[1])
	}
}
