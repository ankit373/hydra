// SPDX-License-Identifier: MIT

package executor

import (
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/provider"
)

// envMap turns a []string environment into something assertable.
func envMap(t *testing.T, env []string) map[string]string {
	t.Helper()
	out := make(map[string]string, len(env))
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			t.Fatalf("malformed environment entry %q", kv)
		}
		out[k] = v
	}
	return out
}

// The finding this closes: cmd.Env was set in no executor in the tree, so
// every head subprocess inherited the full environment. An agy head held the
// user's AWS credentials and every other provider's API key for the duration
// of the call.
func TestHeadEnv_GivesAHeadOnlyItsOwnProvidersCredential(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-should-reach-claude")
	t.Setenv("OPENAI_API_KEY", "sk-should-not-reach-claude")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "aws-should-reach-nothing")
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIAIOSFODNN7EXAMPLE")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/agent.sock")
	t.Setenv("GITHUB_TOKEN", "ghp_should-not-reach-claude")

	env := envMap(t, headEnv(provider.Head{ID: "claude", Provider: "anthropic"}))

	if got := env["ANTHROPIC_API_KEY"]; got != "sk-ant-should-reach-claude" {
		t.Errorf("the head cannot see its own credential: %q", got)
	}
	for _, k := range []string{
		"OPENAI_API_KEY",
		"AWS_SECRET_ACCESS_KEY",
		"AWS_ACCESS_KEY_ID",
		"SSH_AUTH_SOCK",
		"GITHUB_TOKEN",
	} {
		if v, ok := env[k]; ok {
			t.Errorf("%s reached an anthropic head: %q", k, v)
		}
	}
}

// The mirror of the above: an openai head sees its own key and not Anthropic's.
// Without this, a test that only checked one direction would pass on an
// allowlist that happened to name every key.
func TestHeadEnv_IsPerProviderInBothDirections(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-nope")
	t.Setenv("OPENAI_API_KEY", "sk-yes")

	env := envMap(t, headEnv(provider.Head{ID: "codex", Provider: "openai"}))

	if env["OPENAI_API_KEY"] != "sk-yes" {
		t.Error("an openai head cannot see OPENAI_API_KEY")
	}
	if _, ok := env["ANTHROPIC_API_KEY"]; ok {
		t.Error("ANTHROPIC_API_KEY reached an openai head")
	}
}

// A head still needs to find its binary and read its own config, or the
// hardening breaks every head instead of protecting them.
func TestHeadEnv_KeepsWhatAToolActuallyNeeds(t *testing.T) {
	t.Setenv("ANTHROPIC_BASE_URL", "https://proxy.internal/v1")

	env := envMap(t, headEnv(provider.Head{ID: "claude", Provider: "anthropic"}))

	for _, k := range []string{"PATH", "HOME"} {
		if env[k] == "" {
			t.Errorf("%s is missing, the head cannot run", k)
		}
	}
	if env["ANTHROPIC_BASE_URL"] != "https://proxy.internal/v1" {
		t.Error("a custom base URL did not reach the head that needs it")
	}
}

// The allowlist cannot be complete: every CLI agent reads its own settings and
// they change. Without an escape hatch the fix for one unlisted variable is to
// stop using Hydra.
func TestHeadEnv_OperatorCanRestoreAnUnlistedVariable(t *testing.T) {
	t.Setenv("SOME_NEW_AGENT_TOKEN", "restored")
	t.Setenv("STILL_NOT_PASSED", "nope")
	t.Setenv(extraEnvVar, " SOME_NEW_AGENT_TOKEN , ")

	env := envMap(t, headEnv(provider.Head{ID: "claude", Provider: "anthropic"}))

	if env["SOME_NEW_AGENT_TOKEN"] != "restored" {
		t.Error("HYDRA_HEAD_ENV did not restore the named variable")
	}
	if _, ok := env["STILL_NOT_PASSED"]; ok {
		t.Error("HYDRA_HEAD_ENV passed a variable it did not name")
	}
}

// An unknown provider gets no credential at all rather than everything. A
// default that widens on an unrecognized name is how allowlists rot into
// passthroughs.
func TestHeadEnv_UnknownProviderGetsNoCredential(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-nope")
	t.Setenv("OPENAI_API_KEY", "sk-nope")

	env := envMap(t, headEnv(provider.Head{ID: "mystery", Provider: "not-a-provider"}))

	for k := range env {
		if strings.Contains(k, "API_KEY") || strings.Contains(k, "TOKEN") {
			t.Errorf("%s reached a head with an unrecognized provider", k)
		}
	}
}
