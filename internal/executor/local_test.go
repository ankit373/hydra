// SPDX-License-Identifier: MIT

package executor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/testutil"
)

// ── Ollama ────────────────────────────────────────────────────────────────────

// The local head is the terminal fallback: when everything paid is exhausted or
// a PII policy forces local-only, this is what answers. A silent failure here
// is a dispatch chain with nothing at the end of it.

func ollamaExecutorFor(srv *httptest.Server) *OllamaExecutor {
	return &OllamaExecutor{client: srv.Client()}
}

func TestOllamaExecute_HappyPathParsesOutputAndUsage(t *testing.T) {
	testutil.NewSandbox(t)

	var gotPath string
	var gotBody ollamaGenerateRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" { // the health check
			return
		}
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"response":"local answer","model":"qwen2.5-coder:7b",
			"done":true,"prompt_eval_count":21,"eval_count":9}`))
	}))
	defer srv.Close()
	t.Setenv("OLLAMA_HOST", srv.URL)

	resp, err := ollamaExecutorFor(srv).Execute(context.Background(), Request{
		Prompt: "hello",
		Head: provider.Head{
			ID: "qwen2.5-coder:7b", Provider: "ollama", Source: "port", LocalOnly: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if gotPath != "/api/generate" {
		t.Errorf("path = %q, want /api/generate", gotPath)
	}
	if gotBody.Stream {
		t.Error("stream = true; the executor parses a single JSON object, not a stream")
	}
	if gotBody.Prompt != "hello" || gotBody.Model != "qwen2.5-coder:7b" {
		t.Errorf("body = %+v, want the prompt and the head's model", gotBody)
	}
	if resp.Output != "local answer" {
		t.Errorf("Output = %q", resp.Output)
	}
	// Ollama reports real counts, so they must not be labelled as estimated —
	// local heads are free, but the budget governor still books their usage.
	if resp.InputTokens != 21 || resp.OutputTokens != 9 {
		t.Errorf("tokens = %d/%d, want 21/9", resp.InputTokens, resp.OutputTokens)
	}
	if resp.TokensEstimated {
		t.Error("TokensEstimated = true though Ollama reported real counts")
	}
}

// A head discovered by the port provider carries the server's own model name in
// Meta, which can differ from the head ID. Sending the ID instead is a 404 from
// Ollama that reads as "the model is not installed".
func TestOllamaExecute_PrefersTheModelFlagFromMeta(t *testing.T) {
	testutil.NewSandbox(t)

	var gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			return
		}
		var body ollamaGenerateRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotModel = body.Model
		_, _ = w.Write([]byte(`{"response":"ok","done":true}`))
	}))
	defer srv.Close()
	t.Setenv("OLLAMA_HOST", srv.URL)

	if _, err := ollamaExecutorFor(srv).Execute(context.Background(), Request{
		Prompt: "hi",
		Head: provider.Head{
			ID: "ollama-qwen", Provider: "ollama", Source: "port",
			Meta: map[string]string{"model_flag": "qwen2.5-coder:7b"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if gotModel != "qwen2.5-coder:7b" {
		t.Errorf("model = %q, want the model_flag from Meta, not the head ID", gotModel)
	}
}

// An empty response with done:true is Ollama saying it produced nothing. That
// is not a successful answer.
func TestOllamaExecute_EmptyResponseIsAnError(t *testing.T) {
	testutil.NewSandbox(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			return
		}
		_, _ = w.Write([]byte(`{"response":"","done":true}`))
	}))
	defer srv.Close()
	t.Setenv("OLLAMA_HOST", srv.URL)

	if resp, err := ollamaExecutorFor(srv).Execute(context.Background(), Request{
		Prompt: "hi", Head: provider.Head{ID: "m", Provider: "ollama", Source: "port"},
	}); err == nil {
		t.Errorf("an empty generation was reported as success: %+v", resp)
	}
}

func TestOllamaExecute_ServerErrorsAndGarbageAreErrors(t *testing.T) {
	testutil.NewSandbox(t)

	t.Run("http error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/" {
				return
			}
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"model 'nope' not found"}`))
		}))
		defer srv.Close()
		t.Setenv("OLLAMA_HOST", srv.URL)

		_, err := ollamaExecutorFor(srv).Execute(context.Background(), Request{
			Prompt: "hi", Head: provider.Head{ID: "nope", Provider: "ollama", Source: "port"},
		})
		if err == nil {
			t.Fatal("a 404 was reported as success")
		}
		if !strings.Contains(err.Error(), "404") {
			t.Errorf("error = %v, want the status code", err)
		}
	})

	t.Run("unparsable body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/" {
				return
			}
			_, _ = w.Write([]byte(`{"response": truncated`))
		}))
		defer srv.Close()
		t.Setenv("OLLAMA_HOST", srv.URL)

		if _, err := ollamaExecutorFor(srv).Execute(context.Background(), Request{
			Prompt: "hi", Head: provider.Head{ID: "m", Provider: "ollama", Source: "port"},
		}); err == nil {
			t.Fatal("an unparsable body was reported as success")
		}
	})
}

// isHealthy is the gate that decides whether to try starting a server. It must
// distinguish "answering" from "the port is open but erroring".
func TestOllamaIsHealthy(t *testing.T) {
	testutil.NewSandbox(t)

	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("Ollama is running"))
	}))
	defer ok.Close()
	if !(&OllamaExecutor{client: ok.Client()}).isHealthy(ok.URL) {
		t.Error("a responding server was reported unhealthy")
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()
	if (&OllamaExecutor{client: bad.Client()}).isHealthy(bad.URL) {
		t.Error("a 500 was reported as healthy")
	}

	// Nothing listening, and a host that is not a URL at all — both must be
	// "not healthy" rather than a panic.
	if (&OllamaExecutor{}).isHealthy("http://127.0.0.1:1") {
		t.Error("a dead port was reported as healthy")
	}
	if (&OllamaExecutor{}).isHealthy("://not a url") {
		t.Error("an unparsable host was reported as healthy")
	}
}

// ensureRunning must not be reached when the server is already up — the
// auto-start path spawns a subprocess and waits three seconds.
func TestOllamaEnsureRunning_NoOpWhenAlreadyHealthy(t *testing.T) {
	testutil.NewSandbox(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()

	start := time.Now()
	if err := (&OllamaExecutor{client: srv.Client()}).ensureRunning(srv.URL); err != nil {
		t.Fatalf("a healthy server was reported as needing a start: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("took %v against a healthy server — it tried to start one", elapsed)
	}
}

// ollamaHost must resolve through the provider so discovery and execution can
// never disagree about the address (#282).
func TestOllamaHost_MatchesDiscovery(t *testing.T) {
	testutil.NewSandbox(t)

	if got, want := ollamaHost(), provider.OllamaHost(); got != want {
		t.Errorf("ollamaHost() = %q but discovery uses %q — a dispatch would go to a "+
			"different server than the one probe reported", got, want)
	}
	t.Setenv("OLLAMA_HOST", "http://192.0.2.10:11434")
	if got, want := ollamaHost(), provider.OllamaHost(); got != want {
		t.Errorf("with OLLAMA_HOST set, ollamaHost() = %q but discovery uses %q", got, want)
	}
}

// ── CLI ───────────────────────────────────────────────────────────────────────

// The CLI executor drives installed agent binaries. Its whole job is building
// an argv, and a wrong one is a subprocess that either does nothing or does
// something else.

func TestCLIBuildArgs_SubstitutesThePromptPlaceholder(t *testing.T) {
	tests := []struct {
		name string
		tmpl cliTemplate
		want []string
	}{
		{"prompt only", cliTemplate{args: []string{""}}, []string{"the prompt"}},
		{"flag then prompt", cliTemplate{args: []string{"--print", ""}}, []string{"--print", "the prompt"}},
		{"prompt in the middle", cliTemplate{args: []string{"ask", "", "--json"}},
			[]string{"ask", "the prompt", "--json"}},
		{"stdin templates pass no prompt argument", cliTemplate{stdinPrompt: true}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.tmpl.buildArgs("the prompt")
			if len(got) != len(tt.want) {
				t.Fatalf("buildArgs = %q, want %q", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("buildArgs = %q, want %q", got, tt.want)
					break
				}
			}
		})
	}

	// A prompt containing spaces, quotes or newlines must stay one argument.
	// Splitting it would silently send the tool a different instruction.
	tricky := "write a func; echo \"hi\" && rm -rf /\nsecond line"
	got := cliTemplate{args: []string{"--print", ""}}.buildArgs(tricky)
	if len(got) != 2 || got[1] != tricky {
		t.Errorf("buildArgs fragmented a prompt with shell metacharacters: %q", got)
	}
}

// Bare `codex "<prompt>"` launches codex's interactive TUI, which fails
// instantly outside a real terminal — every dispatch to codex failed this
// way and silently fell back to a lower-scoring head (#491). The exec
// subcommand is the non-interactive entry point.
func TestCLIBuildArgs_OpenAIUsesTheExecSubcommand(t *testing.T) {
	got := cliTemplates["openai"].buildArgs("the prompt")
	want := []string{"exec", "the prompt"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("openai template args = %q, want %q — bare codex launches its interactive TUI", got, want)
	}
}

// Bare `agy "<prompt>"` launches agy's interactive TUI too, but — unlike
// codex — still exits 0, with the TUI error text on stdout. CLIExecutor only
// checks the exit code, so this silently reports a false success carrying an
// error message as if it were the model's real answer (#492).
func TestCLIBuildArgs_AntigravityUsesPrintFlag(t *testing.T) {
	got := cliTemplates["antigravity"].buildArgs("the prompt")
	want := []string{"--print", "the prompt"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("antigravity template args = %q, want %q — bare agy launches its interactive TUI", got, want)
	}
}

func TestCLIExecute_RunsTheBinaryAndReturnsItsOutput(t *testing.T) {
	s := testutil.NewSandbox(t)

	body := "#!/bin/sh\necho \"got: $2\"\n"
	if runtime.GOOS == "windows" {
		body = "@echo off\r\necho got: %2\r\n"
	}
	bin := s.FakeBinary(t, "fake-claude", body)

	resp, err := (&CLIExecutor{}).Execute(context.Background(), Request{
		Prompt: "hello",
		Head: provider.Head{
			ID: "claude", Provider: "anthropic", Source: "cli", Executable: bin,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Output, "hello") {
		t.Errorf("Output = %q, want the prompt echoed back through the argv", resp.Output)
	}
	if resp.Model != "claude" {
		t.Errorf("Model = %q, want the head ID", resp.Model)
	}
	if resp.Duration <= 0 {
		t.Error("Duration was not measured")
	}
}

// A template can send the prompt on stdin instead of argv (cursor, continue).
func TestCLIExecute_StdinTemplatesSendThePromptOnStdin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the .bat stdin plumbing differs enough that it proves nothing here")
	}
	s := testutil.NewSandbox(t)
	bin := s.FakeBinary(t, "fake-cursor", "#!/bin/sh\nexec /bin/cat\n")

	resp, err := (&CLIExecutor{}).Execute(context.Background(), Request{
		Prompt: "from stdin",
		Head: provider.Head{
			ID: "cursor-agent", Provider: "cursor", Source: "cli", Executable: bin,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Output != "from stdin" {
		t.Errorf("Output = %q, want the prompt read from stdin", resp.Output)
	}
}

// A tool that exits non-zero must be an error carrying its stderr — that text
// is the only diagnostic the user gets, and dropping it turns a login prompt
// into a blank failure.
func TestCLIExecute_NonZeroExitCarriesStderr(t *testing.T) {
	s := testutil.NewSandbox(t)

	body := "#!/bin/sh\necho 'not logged in — run: claude login' >&2\nexit 1\n"
	if runtime.GOOS == "windows" {
		body = "@echo off\r\necho not logged in 1>&2\r\nexit /b 1\r\n"
	}
	bin := s.FakeBinary(t, "fake-failing", body)

	_, err := (&CLIExecutor{}).Execute(context.Background(), Request{
		Prompt: "hi",
		Head:   provider.Head{ID: "claude", Provider: "anthropic", Source: "cli", Executable: bin},
	})
	if err == nil {
		t.Fatal("a non-zero exit was reported as success")
	}
	if !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("error = %v, want the tool's stderr — it is the only diagnostic "+
			"the user gets", err)
	}
}

func TestCLIExecute_UnknownProviderIsRefusedBeforeSpawning(t *testing.T) {
	testutil.NewSandbox(t)

	_, err := (&CLIExecutor{}).Execute(context.Background(), Request{
		Prompt: "hi",
		Head: provider.Head{
			ID: "mystery", Provider: "some-vendor", Source: "cli",
			Executable: "/definitely/not/here",
		},
	})
	if err == nil {
		t.Fatal("a head with no CLI template was executed anyway")
	}
	if !strings.Contains(err.Error(), "no CLI template") {
		t.Errorf("error = %v, want it to name the missing template", err)
	}
}

// The template can be keyed by head ID as well as provider, which is how a
// single vendor with several tools is driven.
func TestCLIExecute_TemplateMayBeKeyedByHeadID(t *testing.T) {
	s := testutil.NewSandbox(t)

	var id string
	for k := range cliTemplates {
		if _, isProvider := cliTemplates[k]; isProvider && k == "cursor" {
			id = k
		}
	}
	if id == "" {
		t.Skip("no head-ID-keyed template to exercise")
	}
	body := "#!/bin/sh\necho ok\n"
	if runtime.GOOS == "windows" {
		body = "@echo off\r\necho ok\r\n"
	}
	bin := s.FakeBinary(t, "fake-byid", body)

	if _, err := (&CLIExecutor{}).Execute(context.Background(), Request{
		Prompt: "hi",
		Head:   provider.Head{ID: id, Provider: "unregistered-vendor", Source: "cli", Executable: bin},
	}); err != nil {
		t.Fatalf("a template keyed by head ID was not found: %v", err)
	}
}

// A context deadline must kill the subprocess rather than leaving it running.
func TestCLIExecute_ContextCancellationKillsTheSubprocess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no portable sleeping .bat")
	}
	s := testutil.NewSandbox(t)
	bin := s.FakeBinary(t, "fake-slow", "#!/bin/sh\nexec /bin/sleep 30\n")

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := (&CLIExecutor{}).Execute(ctx, Request{
		Prompt: "hi",
		Head:   provider.Head{ID: "claude", Provider: "anthropic", Source: "cli", Executable: bin},
	})
	if err == nil {
		t.Fatal("a cancelled subprocess returned success")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("took %v to kill the subprocess", elapsed)
	}
}

// ── token sidecar ─────────────────────────────────────────────────────────────

// The sidecar path comes from the environment and is written to. It is
// deliberately confined to the temp directory: an executor must never be
// steerable into writing over an arbitrary file.

func TestWriteTokenSidecar_WritesInsideTempDir(t *testing.T) {
	testutil.NewSandbox(t)

	path := filepath.Join(os.TempDir(), "hydra-sidecar-test.json")
	t.Cleanup(func() { _ = os.Remove(path) })
	t.Setenv("HYDRA_TOKEN_SIDECAR", path)

	writeTokenSidecar("qwen:7b", "ollama", "real", 100, 50)

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no sidecar was written: %v", err)
	}
	var got tokenSidecar
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Model != "qwen:7b" || got.PromptTokens != 100 || got.ResponseTokens != 50 {
		t.Errorf("sidecar = %+v", got)
	}
	// The source label is what stops an estimate being read as a measurement.
	if got.Source != "real" {
		t.Errorf("Source = %q, want real", got.Source)
	}

	if info, err := os.Stat(path); err == nil && runtime.GOOS != "windows" {
		if info.Mode().Perm()&0o077 != 0 {
			t.Errorf("sidecar mode %v is group/other readable", info.Mode().Perm())
		}
	}
}

func TestWriteTokenSidecar_RefusesToWriteOutsideTempDir(t *testing.T) {
	testutil.NewSandbox(t)

	tmp := filepath.Clean(os.TempDir())
	cases := []struct {
		name   string
		target string
	}{
		{
			// Cleans to the parent of the temp directory.
			name:   "traversal out of the temp directory",
			target: filepath.Join(tmp, "..", "hydra-escaped-sidecar.json"),
		},
		{
			// A sibling whose path merely starts with the temp directory's. A
			// plain HasPrefix without the separator would accept this.
			name:   "sibling directory sharing the prefix",
			target: tmp + "-evil" + string(filepath.Separator) + "sidecar.json",
		},
		{
			name:   "relative path",
			target: filepath.Join("relative", "sidecar.json"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clean := filepath.Clean(tc.target)
			t.Setenv("HYDRA_TOKEN_SIDECAR", tc.target)
			writeTokenSidecar("m", "e", "real", 1, 1)

			if _, err := os.Stat(clean); err == nil {
				_ = os.Remove(clean)
				t.Errorf("a sidecar was written to %q, outside the temp directory — "+
					"the executor is steerable into writing arbitrary files", clean)
			}
		})
	}

	// Unset is a no-op, not a write to "".
	t.Setenv("HYDRA_TOKEN_SIDECAR", "")
	writeTokenSidecar("m", "e", "real", 1, 1)
}

// With no server answering, ensureRunning tries to start one. On a machine with
// no ollama installed that must be a clear error naming the cause, not a silent
// three-second stall followed by a generic failure.
//
// The auto-start attempt is guarded by a package-level sync.Once, so it happens
// at most once per process and every later caller falls through to the
// three-second wait loop instead. Resetting it here is what makes this test
// independent of which other test ran first — without that it passes alone and
// fails under -count=2.
func TestOllamaEnsureRunning_ReportsWhyItCouldNotStart(t *testing.T) {
	testutil.NewSandbox(t) // empty PATH: there is no ollama to start

	ollamaServeOnce = sync.Once{}
	t.Cleanup(func() { ollamaServeOnce = sync.Once{} })

	start := time.Now()
	err := (&OllamaExecutor{}).ensureRunning("http://127.0.0.1:1")
	if err == nil {
		t.Fatal("ensureRunning reported success with nothing listening and no binary")
	}
	if !strings.Contains(err.Error(), "could not start") {
		t.Errorf("error = %v, want it to say the server could not be started", err)
	}
	// It must not sit through the three-second readiness wait when the start
	// itself failed — there is nothing to wait for.
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("took %v to report a failed start", elapsed)
	}
}
