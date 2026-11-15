// SPDX-License-Identifier: MIT

package sandbox

import (
	"strings"
	"testing"
)

func has(env []string, key string) (string, bool) {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v, true
		}
	}
	return "", false
}

func TestBaseEnv_CarriesNoCredential(t *testing.T) {
	for _, k := range []string{
		"AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY",
		"SSH_AUTH_SOCK", "GITHUB_TOKEN", "NPM_TOKEN",
	} {
		t.Setenv(k, "should-not-cross")
	}

	env := BaseEnv()
	for _, kv := range env {
		if strings.Contains(kv, "should-not-cross") {
			t.Errorf("a credential crossed into BaseEnv: %s", kv)
		}
	}
}

func TestBaseEnv_KeepsWhatAProcessNeedsToRun(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("HOME", "/home/tester")

	env := BaseEnv()
	for _, k := range []string{"PATH", "HOME"} {
		if _, ok := has(env, k); !ok {
			t.Errorf("%s is missing, nothing would start", k)
		}
	}
}

func TestWithVars_AddsOnlyWhatItIsAskedFor(t *testing.T) {
	t.Setenv("WANTED", "yes")
	t.Setenv("UNWANTED", "no")

	env := WithVars("WANTED")
	if v, ok := has(env, "WANTED"); !ok || v != "yes" {
		t.Errorf("WANTED = %q, present=%v; want \"yes\", true", v, ok)
	}
	if _, ok := has(env, "UNWANTED"); ok {
		t.Error("WithVars exported a variable it was not asked for")
	}
}

// An unset or empty name is skipped rather than exported as an empty string:
// a tool checking LookupEnv sees configured-but-broken otherwise.
func TestWithVars_SkipsUnsetAndEmpty(t *testing.T) {
	t.Setenv("BLANK", "")

	env := WithVars("BLANK", "NEVER_SET_ANYWHERE", "")
	for _, k := range []string{"BLANK", "NEVER_SET_ANYWHERE", ""} {
		if _, ok := has(env, k); ok {
			t.Errorf("%q was exported despite having no value", k)
		}
	}
}

func TestIsCredentialName(t *testing.T) {
	for _, k := range []string{
		"OPENAI_API_KEY", "GITHUB_TOKEN", "SSH_AUTH_SOCK", "AWS_PROFILE",
		"aws_region", "DB_PASSWORD", "GOOGLE_APPLICATION_CREDENTIALS", "ARM_CLIENT_ID",
	} {
		if !IsCredentialName(k) {
			t.Errorf("%s is not flagged as a credential", k)
		}
	}
	for _, k := range append(append([]string{}, passthrough...), toolchainVars...) {
		if IsCredentialName(k) {
			t.Errorf("default name %s is flagged as a credential, so ASI03 would never pass", k)
		}
	}
}

// Validators get the base set plus toolchain paths, configured extras, and
// never a credential, even one the config names.
func TestValidatorEnv_ToolchainAndExtrasButNoCredential(t *testing.T) {
	t.Setenv("GOCACHE", "/cache/go")
	t.Setenv("PLAYWRIGHT_BROWSERS_PATH", "/pw")
	t.Setenv("OPENAI_API_KEY", "should-not-cross")
	t.Setenv("AWS_PROFILE", "should-not-cross")
	t.Setenv("UNNAMED", "should-not-cross")

	env, refused := ValidatorEnv([]string{" PLAYWRIGHT_BROWSERS_PATH ", "AWS_PROFILE", ""})
	for k, want := range map[string]string{"GOCACHE": "/cache/go", "PLAYWRIGHT_BROWSERS_PATH": "/pw"} {
		if v, ok := has(env, k); !ok || v != want {
			t.Errorf("%s = %q, present=%v; want %q", k, v, ok, want)
		}
	}
	for _, kv := range env {
		if strings.Contains(kv, "should-not-cross") {
			t.Errorf("%s crossed into a validator's environment", kv)
		}
	}
	if len(refused) != 1 || refused[0] != "AWS_PROFILE" {
		t.Errorf("refused = %v, want [AWS_PROFILE]", refused)
	}
}

func TestHeadExtra_TrimsAndDropsBlanks(t *testing.T) {
	t.Setenv(HeadEnvVar, " A_URL , ,B_DIR,")
	got := strings.Join(HeadExtra(), ",")
	if got != "A_URL,B_DIR" {
		t.Errorf("HeadExtra() = %q, want A_URL,B_DIR", got)
	}
}
