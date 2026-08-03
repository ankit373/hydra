// SPDX-License-Identifier: MIT

package testutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// APIKeyVars is every environment variable the env provider treats as evidence
// of a usable head (internal/provider/env's knownKeys table). A test that does
// not clear these discovers whatever the developer happens to have exported,
// so the same test passes on a laptop with no keys and fails on one with them —
// or worse, quietly routes a test dispatch to a real paid provider.
//
// Kept in sync with knownKeys by TestAPIKeyVars_CoversEnvProvider.
var APIKeyVars = []string{
	"ANTHROPIC_API_KEY",
	"AWS_ACCESS_KEY_ID",
	"AWS_SECRET_ACCESS_KEY",
	"AZURE_OPENAI_API_KEY",
	"AZURE_OPENAI_ENDPOINT",
	"COHERE_API_KEY",
	"DEEPSEEK_API_KEY",
	"FIREWORKS_API_KEY",
	"GEMINI_API_KEY",
	"GOOGLE_API_KEY",
	"GROQ_API_KEY",
	"MISTRAL_API_KEY",
	"OPENAI_API_KEY",
	"OPENROUTER_API_KEY",
	"PERPLEXITY_API_KEY",
	"REPLICATE_API_TOKEN",
	"TOGETHER_API_KEY",
	"XAI_API_KEY",
}

// tuningVars are Hydra's own knobs. A developer with HYDRA_HOME exported for
// day-to-day use would otherwise have every test read their real registry.
var tuningVars = []string{
	"HYDRA_HOME",
	"HYDRA_MAX_OUTPUT_BYTES",
	"HYDRA_NO_UPDATE_CHECK",
	"HYDRA_PRICING_TTL_HOURS",
	"HYDRA_TOKEN_SIDECAR",
	"HYDRA_WORKSPACE",
	"AGY_TIMEOUT",
	"OLLAMA_HOST",
}

// Sandbox is a hermetic environment for one test: a private home directory, a
// private $HYDRA_HOME, an empty $PATH, and no provider credentials.
//
// The point is that a contributor with no Ollama, no API keys and no agy on
// their machine gets byte-identical results to CI, and to a maintainer whose
// laptop has all three. Discovery is the layer where that matters most — it
// exists to report what is installed, so it is the layer most likely to report
// the *developer's* machine instead of the test's fixture.
//
// It is also what makes a test cross-platform for free: nothing here reads a
// path, a binary or a variable that differs between Linux, macOS and Windows.
type Sandbox struct {
	// Home is the fake user home. Both $HOME and %USERPROFILE% point here, so
	// os.UserHomeDir resolves to it on every OS.
	Home string
	// HydraHome is $HYDRA_HOME: where an on-disk registry/ override and the
	// logs are read from. config.ScriptHome() resolves to this.
	HydraHome string
	// BinDir is the only directory on $PATH. Empty until FakeBinary is called,
	// so exec.LookPath finds nothing by default.
	BinDir string
}

// NewSandbox installs a hermetic environment for the duration of t. Every
// variable is restored by t.Setenv's own cleanup, so nothing leaks between
// tests even when they run in the same process.
func NewSandbox(t *testing.T) *Sandbox {
	t.Helper()

	root := t.TempDir()
	s := &Sandbox{
		Home:      filepath.Join(root, "home"),
		HydraHome: filepath.Join(root, "hydra"),
		BinDir:    filepath.Join(root, "bin"),
	}
	for _, d := range []string{s.Home, s.HydraHome, s.BinDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	// os.UserHomeDir reads $HOME on unix and %USERPROFILE% on Windows. Set both
	// rather than branching, so the sandbox has one behaviour everywhere.
	t.Setenv("HOME", s.Home)
	t.Setenv("USERPROFILE", s.Home)
	t.Setenv("HYDRA_HOME", s.HydraHome)

	// An empty PATH would make exec.LookPath fail for a different reason on
	// each OS; one empty directory makes "not found" mean exactly that.
	t.Setenv("PATH", s.BinDir)

	for _, v := range APIKeyVars {
		t.Setenv(v, "")
	}
	for _, v := range tuningVars {
		if v == "HYDRA_HOME" {
			continue // set above
		}
		t.Setenv(v, "")
	}

	// Best-effort outbound-HTTP block. This stops any client using
	// http.DefaultTransport (which honours the proxy environment) from reaching
	// the network — that covers the pricing fetcher and the update checker. It
	// does NOT stop a transport constructed with Proxy: nil, so it is a
	// backstop against accidental egress, not a sandbox boundary. Tests that
	// must not hit the network should inject a fake client rather than rely on
	// this alone.
	t.Setenv("HTTP_PROXY", "127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "127.0.0.1:1")
	t.Setenv("NO_PROXY", "")

	return s
}

// FakeBinary puts an executable named name on the sandbox's $PATH, so a
// discovery test can assert "this CLI agent is installed" without installing
// one. The script exits 0 and echoes nothing; pass a body to make it behave.
//
// On Windows the file needs a .bat extension for exec.LookPath to consider it
// executable, which is exactly the sort of difference a discovery contract
// should be exercising rather than skipping.
func (s *Sandbox) FakeBinary(t *testing.T, name string, body ...string) string {
	t.Helper()

	script, path := "#!/bin/sh\nexit 0\n", filepath.Join(s.BinDir, name)
	if runtime.GOOS == "windows" {
		script, path = "@echo off\r\nexit /b 0\r\n", filepath.Join(s.BinDir, name+".bat")
	}
	if len(body) > 0 {
		script = body[0]
	}
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// SetKey re-introduces one provider credential that NewSandbox cleared, for a
// test that needs exactly one head to be discoverable.
func (s *Sandbox) SetKey(t *testing.T, envVar, value string) {
	t.Helper()
	t.Setenv(envVar, value)
}
