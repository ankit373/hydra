// SPDX-License-Identifier: MIT

package executor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/testutil"
)

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
// instantly outside a real terminal, every dispatch to codex failed this
// way and silently fell back to a lower-scoring head (#491). The exec
// subcommand is the non-interactive entry point.
func TestCLIBuildArgs_OpenAIUsesTheExecSubcommand(t *testing.T) {
	got := cliTemplates["openai"].buildArgs("the prompt")
	want := []string{"exec", "the prompt"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("openai template args = %q, want %q, bare codex launches its interactive TUI", got, want)
	}
}

// Bare `agy "<prompt>"` launches agy's interactive TUI too, but, unlike
// codex, still exits 0, with the TUI error text on stdout. CLIExecutor only
// checks the exit code, so this silently reports a false success carrying an
// error message as if it were the model's real answer (#492).
func TestCLIBuildArgs_AntigravityUsesPrintFlag(t *testing.T) {
	got := cliTemplates["antigravity"].buildArgs("the prompt")
	want := []string{"--print", "the prompt"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("antigravity template args = %q, want %q, bare agy launches its interactive TUI", got, want)
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

// A tool that exits non-zero must be an error carrying its stderr, that text
// is the only diagnostic the user gets, and dropping it turns a login prompt
// into a blank failure.
func TestCLIExecute_NonZeroExitCarriesStderr(t *testing.T) {
	s := testutil.NewSandbox(t)

	body := "#!/bin/sh\necho 'not logged in, run: claude login' >&2\nexit 1\n"
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
		t.Errorf("error = %v, want the tool's stderr, it is the only diagnostic "+
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
				t.Errorf("a sidecar was written to %q, outside the temp directory, "+
					"the executor is steerable into writing arbitrary files", clean)
			}
		})
	}

	// Unset is a no-op, not a write to "".
	t.Setenv("HYDRA_TOKEN_SIDECAR", "")
	writeTokenSidecar("m", "e", "real", 1, 1)
}
