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

// geminiStub serves a streamGenerateContent response and records what was
// asked for, so a test can assert the request as well as the parse.
type geminiStub struct {
	*httptest.Server
	body   []byte
	header http.Header
	paths  []string
	query  []string
}

// newGeminiStub points both Gemini paths at itself through GEMINI_BASE_URL,
// which is also how a gateway is addressed in production.
func newGeminiStub(t *testing.T, chunks []string) *geminiStub {
	t.Helper()
	s := &geminiStub{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.body, _ = io.ReadAll(r.Body)
		s.header = r.Header.Clone()
		s.paths = append(s.paths, r.URL.Path)
		s.query = append(s.query, r.URL.RawQuery)
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
	t.Setenv("GEMINI_MODEL", "gemini-test-1")
	return s
}

func geminiHead() provider.Head {
	return provider.Head{
		ID: "google", Name: "Gemini", Provider: "google",
		Source: "env", AuthReady: true, Meta: map[string]string{},
	}
}

// geminiChunkJSON builds one chunk carrying text and, when running > 0, the
// usage totals as they stand at that point.
func geminiChunkJSON(text string, promptTok, candidatesTok int) string {
	b, _ := json.Marshal(text)
	usage := ""
	if promptTok > 0 || candidatesTok > 0 {
		usage = fmt.Sprintf(`,"usageMetadata":{"promptTokenCount":%d,"candidatesTokenCount":%d}`,
			promptTok, candidatesTok)
	}
	return fmt.Sprintf(`{"candidates":[{"content":{"parts":[{"text":%s}],"role":"model"}}]`+
		`%s,"modelVersion":"gemini-test-1-001"}`, b, usage)
}

func TestGeminiStream_ReassemblesDeltasAndReportsCounts(t *testing.T) {
	newGeminiStub(t, []string{
		geminiChunkJSON("Hello, ", 31, 2),
		geminiChunkJSON("world", 31, 64),
	})

	var got []string
	resp, err := (&HTTPExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "say hello", Head: geminiHead()},
		func(d string) { got = append(got, d) })
	if err != nil {
		t.Fatal(err)
	}

	if resp.Output != "Hello, world" {
		t.Errorf("Output = %q, want %q", resp.Output, "Hello, world")
	}
	if strings.Join(got, "") != resp.Output {
		t.Errorf("deltas %q do not reassemble to Output %q", got, resp.Output)
	}
	if resp.TokensEstimated {
		t.Error("TokensEstimated = true, but the provider reported both counts")
	}
	if resp.Model != "gemini-test-1-001" {
		t.Errorf("Model = %q, want the id modelVersion reported", resp.Model)
	}
	if resp.TTFT <= 0 {
		t.Error("TTFT = 0, want the measured time to the first delta")
	}
}

// usageMetadata repeats on every chunk carrying running totals, so taking the
// first one bills a whole call at the length of its opening fragment. 2 against
// 64 is the difference, and both are non-zero, so a guard that only checks
// "counts survived" passes on the wrong one.
func TestGeminiStream_LastUsageWinsNotTheFirst(t *testing.T) {
	newGeminiStub(t, []string{
		geminiChunkJSON("Hello, ", 31, 2),
		geminiChunkJSON("world", 31, 64),
	})

	resp, err := (&HTTPExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "say hello", Head: geminiHead()}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OutputTokens != 64 {
		t.Errorf("OutputTokens = %d, want 64 (the last chunk's running total, not the first chunk's 2)",
			resp.OutputTokens)
	}
	if resp.InputTokens != 31 {
		t.Errorf("InputTokens = %d, want 31", resp.InputTokens)
	}
}

// A chunk with no usageMetadata at all must not zero what the last one
// reported: Gemini omits the block on some chunks, and overwriting
// unconditionally logs the call as free.
func TestGeminiStream_AChunkWithoutUsageDoesNotEraseIt(t *testing.T) {
	newGeminiStub(t, []string{
		geminiChunkJSON("Hello, ", 31, 64),
		geminiChunkJSON("world", 0, 0), // no usageMetadata block
	})

	resp, err := (&HTTPExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "say hello", Head: geminiHead()}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if resp.TokensEstimated {
		t.Error("TokensEstimated = true: the reported counts were erased by a chunk that carried none")
	}
	if resp.InputTokens != 31 || resp.OutputTokens != 64 {
		t.Errorf("counts = %d/%d, want 31/64 to survive a chunk with no usage block",
			resp.InputTokens, resp.OutputTokens)
	}
}

func TestGeminiStream_NoUsageIsEstimatedAndLabelled(t *testing.T) {
	newGeminiStub(t, []string{geminiChunkJSON("an answer with no counts attached", 0, 0)})

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

func TestGeminiStream_MidStreamErrorFailsTheDispatch(t *testing.T) {
	newGeminiStub(t, []string{
		geminiChunkJSON("this much arrived, then ", 31, 2),
		`{"error":{"status":"RESOURCE_EXHAUSTED","message":"Quota exceeded"}}`,
	})

	resp, err := (&HTTPExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "ask", Head: geminiHead()}, func(string) {})
	if err == nil {
		t.Fatalf("a mid-stream error returned a partial as the answer: %q", resp.Output)
	}
	if !strings.Contains(err.Error(), "RESOURCE_EXHAUSTED") {
		t.Errorf("error = %v, want the provider's own status in it", err)
	}
}

// Gemini's default framing is a JSON array that arrives whole, which would
// deliver the answer in one delta and defeat the point of streaming.
func TestGeminiStream_AsksForSSEFramingOnTheStreamingMethod(t *testing.T) {
	s := newGeminiStub(t, []string{geminiChunkJSON("hi", 1, 1)})

	if _, err := (&HTTPExecutor{}).ExecuteStream(context.Background(),
		Request{Prompt: "p", Head: geminiHead()}, func(string) {}); err != nil {
		t.Fatal(err)
	}

	if len(s.paths) != 1 {
		t.Fatalf("stub saw %d requests, want 1", len(s.paths))
	}
	if !strings.HasSuffix(s.paths[0], ":streamGenerateContent") {
		t.Errorf("path = %q, want the streamGenerateContent method", s.paths[0])
	}
	if !strings.Contains(s.query[0], "alt=sse") {
		t.Errorf("query = %q, want alt=sse; without it the response is one JSON array", s.query[0])
	}
	if got := s.header.Get("x-goog-api-key"); got != "test-key" {
		t.Errorf("x-goog-api-key = %q, want the configured key", got)
	}
}

// The buffered path must keep working, and must keep asking for the
// non-streaming method, or the two would drift apart.
func TestGemini_BufferedPathStillUsesGenerateContent(t *testing.T) {
	s := newGeminiStub(t, nil)
	s.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.paths = append(s.paths, r.URL.Path)
		s.query = append(s.query, r.URL.RawQuery)
		fmt.Fprint(w, `{"candidates":[{"content":{"parts":[{"text":"buffered"}]}}],`+
			`"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":7},"modelVersion":"gemini-test-1-001"}`)
	})

	resp, err := (&HTTPExecutor{}).Execute(context.Background(),
		Request{Prompt: "p", Head: geminiHead()})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Output != "buffered" {
		t.Errorf("Output = %q, want %q", resp.Output, "buffered")
	}
	if !strings.HasSuffix(s.paths[0], ":generateContent") {
		t.Errorf("path = %q, want the non-streaming generateContent method", s.paths[0])
	}
	if strings.Contains(s.query[0], "alt=sse") {
		t.Errorf("query = %q, the buffered path must not ask for SSE framing", s.query[0])
	}
}
