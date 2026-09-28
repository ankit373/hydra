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
