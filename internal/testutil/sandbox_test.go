// SPDX-License-Identifier: MIT

package testutil

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// The sandbox exists to make a test's result independent of the machine it runs
// on. If a credential the developer exported survives into the test, discovery
// reports their machine instead of the fixture — and the test passes or fails by
// accident.
func TestSandbox_ClearsProviderCredentials(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-should-not-survive")
	t.Setenv("OPENAI_API_KEY", "sk-should-not-survive")

	NewSandbox(t)

	for _, v := range APIKeyVars {
		if got := os.Getenv(v); got != "" {
			t.Errorf("%s = %q inside the sandbox; a test would discover the developer's real head", v, got)
		}
	}
}

func TestSandbox_RedirectsHomeAndHydraHome(t *testing.T) {
	realHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	s := NewSandbox(t)

	got, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != s.Home {
		t.Errorf("os.UserHomeDir() = %q, want the sandbox home %q", got, s.Home)
	}
	if got == realHome {
		t.Error("os.UserHomeDir() still resolves to the developer's real home")
	}
	if os.Getenv("HYDRA_HOME") != s.HydraHome {
		t.Errorf("HYDRA_HOME = %q, want %q", os.Getenv("HYDRA_HOME"), s.HydraHome)
	}
}

// Discovery walks $PATH looking for CLI agents. An unscrubbed PATH means a
// maintainer with claude/codex/ollama installed tests a different code path
// than a contributor without them.
func TestSandbox_PathFindsNothingByDefault(t *testing.T) {
	s := NewSandbox(t)

	for _, bin := range []string{"claude", "codex", "ollama", "agy", "cursor"} {
		if p, err := exec.LookPath(bin); err == nil {
			t.Errorf("LookPath(%q) found %q inside the sandbox", bin, p)
		}
	}

	// …and a deliberately planted one is found, or the check above would pass
	// for the trivial reason that PATH lookup is broken entirely.
	s.FakeBinary(t, "ollama")
	if _, err := exec.LookPath("ollama"); err != nil {
		t.Errorf("planted binary not found: %v", err)
	}
}

func TestSandbox_IsIsolatedBetweenTests(t *testing.T) {
	first := NewSandbox(t)
	t.Run("nested", func(t *testing.T) {
		second := NewSandbox(t)
		if second.Home == first.Home {
			t.Error("two sandboxes share a home directory")
		}
	})
}

// APIKeyVars must list every variable the env provider treats as a usable head.
// A provider added there but missed here leaks that credential into every test.
//
// The env package is read as source rather than imported: its init() registers
// a provider globally, so importing it from testutil would silently change
// discovery in every test binary that uses the sandbox.
func TestAPIKeyVars_CoversEnvProvider(t *testing.T) {
	src := filepath.Join("..", "provider", "env", "provider.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}

	envVar := regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	found := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		isKnownKeys := false
		for _, name := range vs.Names {
			if name.Name == "knownKeys" {
				isKnownKeys = true
			}
		}
		if !isKnownKeys {
			return true
		}
		ast.Inspect(vs, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err == nil && envVar.MatchString(s) {
				found[s] = true
			}
			return true
		})
		return false
	})

	if len(found) == 0 {
		t.Fatalf("parsed no env vars out of %s — this guard has stopped guarding "+
			"(knownKeys renamed or restructured?)", src)
	}

	listed := map[string]bool{}
	for _, v := range APIKeyVars {
		listed[v] = true
	}
	for v := range found {
		if !listed[v] {
			t.Errorf("%s is read by the env provider but missing from APIKeyVars — "+
				"a developer with it exported would have it leak into every sandboxed test", v)
		}
	}
	for v := range listed {
		if !found[v] {
			t.Errorf("APIKeyVars lists %s but the env provider no longer reads it — drop it", v)
		}
	}
}

// FakeBinary must produce something exec.LookPath accepts on the host OS —
// on Windows that means a .bat, which is exactly the kind of difference a
// discovery contract should exercise rather than skip.
func TestFakeBinary_IsExecutableOnThisOS(t *testing.T) {
	s := NewSandbox(t)
	path := s.FakeBinary(t, "probe-me")

	if !strings.HasPrefix(path, s.BinDir) {
		t.Errorf("planted at %q, outside the sandbox bin dir %q", path, s.BinDir)
	}
	resolved, err := exec.LookPath("probe-me")
	if err != nil {
		t.Fatalf("LookPath: %v", err)
	}
	if err := exec.Command(resolved).Run(); err != nil {
		t.Errorf("planted binary is not runnable: %v", err)
	}
}

// The Windows branch of EchoScript is what the marker-based edit tests depend
// on, and it cannot be exercised from a Unix runner — so the escaping it
// applies is tested directly. Getting it wrong is not a rendering nit: an
// unescaped "<" makes cmd.exe fail the whole script with "<< was unexpected at
// this time", and the fake head exits 255 with no output.
func TestEscapeBatch_QuotesEveryCmdMetacharacter(t *testing.T) {
	tests := map[string]string{
		"<<<HYDRA_FILE_START>>>": "^<^<^<HYDRA_FILE_START^>^>^>",
		"a & b":                  "a ^& b",
		"a | b":                  "a ^| b",
		"100%":                   "100%%",
		"plain text":             "plain text",
		"":                       "",
		// The caret is escaped first, so an existing one is not doubled by a
		// later replacement.
		"a ^ b": "a ^^ b",
		"^<":    "^^^<",
	}
	for in, want := range tests {
		if got := escapeBatch(in); got != want {
			t.Errorf("escapeBatch(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEchoScript_ProducesAScriptForThisPlatform(t *testing.T) {
	reply := "<<<HYDRA_FILE_START>>>\npackage main\n<<<HYDRA_FILE_END>>>"
	got := EchoScript(reply)

	if got == "" {
		t.Fatal("EchoScript produced nothing")
	}
	if runtime.GOOS == "windows" {
		if strings.Contains(got, "echo <<<") {
			t.Errorf("the redirection operators were not escaped:\n%s", got)
		}
		if !strings.Contains(got, "@echo off") {
			t.Errorf("not a batch script:\n%s", got)
		}
		return
	}
	if !strings.HasPrefix(got, "#!/bin/sh") {
		t.Errorf("not a shell script:\n%s", got)
	}
	// A quoted heredoc needs no escaping, so the payload must appear verbatim.
	if !strings.Contains(got, reply) {
		t.Errorf("the reply was altered:\n%s", got)
	}
	// /bin/cat by absolute path — NewSandbox empties $PATH, so a bare `cat`
	// exits 127.
	if !strings.Contains(got, "/bin/cat") {
		t.Errorf("cat is not named by absolute path:\n%s", got)
	}
}

// A round trip through FakeBinary: the script must actually print the reply.
func TestEchoScript_RoundTripsThroughAFakeBinary(t *testing.T) {
	s := NewSandbox(t)
	reply := "<<<HYDRA_FILE_START>>>\nline one\n<<<HYDRA_FILE_END>>>"
	path := s.FakeBinary(t, "echo-head", EchoScript(reply))

	out, err := exec.Command(path).Output()
	if err != nil {
		t.Fatalf("the fake head failed to run: %v", err)
	}
	got := strings.ReplaceAll(strings.TrimSpace(string(out)), "\r\n", "\n")
	if got != reply {
		t.Errorf("the fake head printed:\n%q\nwant:\n%q", got, reply)
	}
}
