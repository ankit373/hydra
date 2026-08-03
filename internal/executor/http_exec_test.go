// SPDX-License-Identifier: MIT

package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/testutil"
)

// A head with an Endpoint short-circuits provider config resolution, so the
// whole HTTP execution path can be driven against a stub server instead of a
// real API. Everything here runs inside a sandbox, so no developer's API key
// can turn one of these into a live, billable call.
func stubHead(url string) provider.Head {
	return provider.Head{ID: "stub-model", Provider: "openai", Source: "env", Endpoint: url, AuthReady: true}
}

const okChatBody = `{"model":"gpt-test","choices":[{"message":{"content":"hello from the stub"}}],
  "usage":{"prompt_tokens":11,"completion_tokens":7}}`

func TestExecute_HappyPathParsesOutputAndUsage(t *testing.T) {
	testutil.NewSandbox(t)

	var gotPath, gotMethod, gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod, gotCT = r.URL.Path, r.Method, r.Header.Get("Content-Type")
		_, _ = w.Write([]byte(okChatBody))
	}))
	defer srv.Close()

	resp, err := (&HTTPExecutor{}).Execute(context.Background(), Request{
		Prompt: "hi", Head: stubHead(srv.URL), MaxTokens: 64,
	})
	if err != nil {
		t.Fatal(err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("path = %s, want /v1/chat/completions", gotPath)
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type = %q", gotCT)
	}
	if resp.Output != "hello from the stub" {
		t.Errorf("Output = %q", resp.Output)
	}
	// Token counts must come from the provider, not be estimated — cost
	// reporting labels these as actual spend.
	if resp.InputTokens != 11 || resp.OutputTokens != 7 {
		t.Errorf("tokens = %d/%d, want 11/7", resp.InputTokens, resp.OutputTokens)
	}
	if resp.TokensEstimated {
		t.Error("TokensEstimated = true, but the provider reported real usage — " +
			"estimated tokens must never be presented as measured spend")
	}
	if resp.Model != "gpt-test" {
		t.Errorf("Model = %q, want the provider's reported model", resp.Model)
	}
	if resp.Duration <= 0 {
		t.Error("Duration was not measured")
	}
}

// The system prompt must actually reach the provider. Dropping it silently
// changes the model's behaviour with no error anywhere.
func TestExecute_SendsSystemPromptAndUserPrompt(t *testing.T) {
	testutil.NewSandbox(t)

	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(b)
		body = string(b)
		_, _ = w.Write([]byte(okChatBody))
	}))
	defer srv.Close()

	_, err := (&HTTPExecutor{}).Execute(context.Background(), Request{
		Prompt: "USER-MARKER", System: "SYSTEM-MARKER", Head: stubHead(srv.URL),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"USER-MARKER", "SYSTEM-MARKER", `"role":"system"`, `"role":"user"`} {
		if !strings.Contains(body, want) {
			t.Errorf("request body missing %q:\n%s", want, body)
		}
	}
	if strings.Index(body, "SYSTEM-MARKER") > strings.Index(body, "USER-MARKER") {
		t.Error("system message is ordered after the user message")
	}
}

// Fault injection: every one of these must be an error, never a Response that
// a caller would treat as a successful (and billable) completion.
func TestExecute_FaultsAreErrorsNotEmptySuccesses(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		wantIn  string
	}{
		{"500", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("upstream exploded"))
		}, "status 500"},
		{"401 unauthorized", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"bad key"}`))
		}, "status 401"},
		{"429 rate limited", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
		}, "status 429"},
		{"malformed json", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("{not json"))
		}, "decode"},
		{"no choices", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"model":"m","choices":[]}`))
		}, "empty response"},
		{"truncated body", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "500")
			_, _ = w.Write([]byte(`{"choices":[{"messa`))
		}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			testutil.NewSandbox(t)
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()

			resp, err := (&HTTPExecutor{}).Execute(context.Background(), Request{
				Prompt: "x", Head: stubHead(srv.URL),
			})
			if err == nil {
				t.Fatalf("got a Response (%+v) instead of an error — a caller would bill this "+
					"as a successful completion", resp)
			}
			if resp != nil {
				t.Errorf("both a Response and an error were returned: %+v", resp)
			}
			if tc.wantIn != "" && !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("error %q does not mention %q", err, tc.wantIn)
			}
			// The head ID must be in the error or an operator cannot tell which
			// head failed in a fallback chain.
			if !strings.Contains(err.Error(), "stub-model") {
				t.Errorf("error %q does not name the head", err)
			}
		})
	}
}

// A cancelled context must abort promptly rather than block until the client
// timeout — dispatch relies on this to enforce its own deadlines.
func TestExecute_HonoursContextCancellation(t *testing.T) {
	testutil.NewSandbox(t)

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		_, _ = w.Write([]byte(okChatBody))
	}))
	defer srv.Close()
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()

	start := time.Now()
	_, err := (&HTTPExecutor{}).Execute(ctx, Request{Prompt: "x", Head: stubHead(srv.URL)})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a cancelled request returned success")
	}
	if elapsed > 5*time.Second {
		t.Errorf("took %v to notice cancellation", elapsed)
	}
}

// Concurrent execution must be race-free: dispatch fans out across heads and
// swarm fans out further still.
func TestExecute_IsSafeUnderConcurrency(t *testing.T) {
	testutil.NewSandbox(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(okChatBody))
	}))
	defer srv.Close()

	e := &HTTPExecutor{}
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		go func() {
			_, err := e.Execute(context.Background(), Request{Prompt: "x", Head: stubHead(srv.URL)})
			errs <- err
		}()
	}
	for i := 0; i < 16; i++ {
		if err := <-errs; err != nil {
			t.Errorf("concurrent execute %d: %v", i, err)
		}
	}
}

// An unknown provider with no endpoint must be refused before any network call.
func TestExecute_UnsupportedProviderIsRefused(t *testing.T) {
	testutil.NewSandbox(t)

	_, err := (&HTTPExecutor{}).Execute(context.Background(), Request{
		Prompt: "x", Head: provider.Head{ID: "who", Provider: "nobody", Source: "env"},
	})
	if err == nil {
		t.Fatal("an unknown provider was accepted")
	}
	if !strings.Contains(err.Error(), "not executable") {
		t.Errorf("error %q does not explain that the provider is unsupported", err)
	}
}

// ── pure helpers ──────────────────────────────────────────────────────────────

func TestBuildMessages(t *testing.T) {
	if got := buildMessages(Request{Prompt: "p"}); len(got) != 1 || got[0].Role != "user" || got[0].Content != "p" {
		t.Errorf("without a system prompt: %+v", got)
	}
	got := buildMessages(Request{Prompt: "p", System: "s"})
	if len(got) != 2 || got[0].Role != "system" || got[1].Role != "user" {
		t.Errorf("with a system prompt: %+v", got)
	}
	// An empty system prompt must not become an empty system message, which
	// some providers reject outright.
	if got := buildMessages(Request{Prompt: "p", System: ""}); len(got) != 1 {
		t.Errorf("empty system prompt produced %d messages", len(got))
	}
}

func TestDefaultMaxTokens(t *testing.T) {
	for _, tc := range []struct{ v, fallback, want int }{
		{0, 1024, 1024}, {512, 1024, 512}, {-1, 1024, 1024}, {1, 1024, 1},
	} {
		if got := defaultMaxTokens(tc.v, tc.fallback); got != tc.want {
			t.Errorf("defaultMaxTokens(%d, %d) = %d, want %d", tc.v, tc.fallback, got, tc.want)
		}
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "  ", "x", "y"); got != "x" {
		t.Errorf("got %q, want x — whitespace-only is not a value", got)
	}
	if got := firstNonEmpty("", ""); got != "" {
		t.Errorf("got %q, want empty", got)
	}
	if got := firstNonEmpty(); got != "" {
		t.Errorf("no args returned %q", got)
	}
}

func TestTrimLocalModelID(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"ollama/qwen3:8b", "qwen3:8b"},
		{"lmstudio/some-model", "some-model"},
		{"gpt-4o", "gpt-4o"},
		{"", ""},
		// Only the leading prefix is stripped, never an embedded occurrence.
		{"x/ollama/y", "x/ollama/y"},
	} {
		if got := trimLocalModelID(tc.in); got != tc.want {
			t.Errorf("trimLocalModelID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestJoinTextBlocks_SkipsEmptyAndTrims(t *testing.T) {
	got := joinTextBlocks([]textBlock{{"  a  "}, {"   "}, {""}, {"b"}})
	if got != "a\nb" {
		t.Errorf("got %q, want %q", got, "a\nb")
	}
	if got := joinTextBlocks(nil); got != "" {
		t.Errorf("nil blocks gave %q", got)
	}
}

func TestStringifyAny(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   any
		want string
	}{
		{"nil", nil, ""},
		{"string", "hi", "hi"},
		{"slice of strings", []any{"a", "b"}, "a\nb"},
		{"slice with empties", []any{"a", "", "b"}, "a\nb"},
		{"empty slice", []any{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := stringifyAny(tc.in); got != tc.want {
				t.Errorf("stringifyAny(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The error must carry the status and a bounded slice of the body — enough to
// diagnose, not so much that a huge error page floods the log.
func TestHTTPStatusError_IsInformativeAndBounded(t *testing.T) {
	huge := strings.Repeat("E", 100_000)
	resp := &http.Response{StatusCode: 503, Body: http.NoBody}
	resp.Body = httptest.NewRecorder().Result().Body
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(503)
		_, _ = w.Write([]byte(huge))
	}))
	defer srv.Close()

	r, err := http.Get(srv.URL) //nolint:noctx // test-local
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()

	e := httpStatusError("head-x", r)
	msg := e.Error()
	if !strings.Contains(msg, "503") || !strings.Contains(msg, "head-x") {
		t.Errorf("error %q lacks the status or the head ID", msg[:min(200, len(msg))])
	}
	if len(msg) > 8192 {
		t.Errorf("error is %d bytes — an upstream error page should be truncated, not logged whole", len(msg))
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
