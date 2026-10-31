// SPDX-License-Identifier: MIT

package main

import (
	"os"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/testutil"
)

// A config that is opted in and has one type error on an unrelated line. The
// settings below are what every one of these commands reports on.
const brokenButOptedIn = `cortex = "claude"
skills = ["go"]
capture_payloads = true
capture_embeddings = true
cache_answers = true
cache_threshold = oops
`

// Driven through `hyctl trace <sub>`, the way a user reaches them: `payloads`
// is declared inline inside cmdTrace rather than by its own builder.
func runTrace(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var err error
	out := captureStdout(t, func() {
		c := cmdTrace()
		c.SetArgs(args)
		c.SilenceUsage, c.SilenceErrors = true, true
		err = c.Execute()
	})
	return out, err
}

func writeConfig(t *testing.T, body string) {
	t.Helper()
	if err := os.WriteFile(config.Path(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Each of these answers "is this setting on". With an unreadable config none of
// them can know, and all five said "off" and told the reader to set a key the
// file already carried (#1106).
func TestCLI_AnUnreadableConfigIsNotAnAnsweredQuestion(t *testing.T) {
	for _, name := range []string{"cache", "payloads", "embeddings", "search"} {
		t.Run(name, func(t *testing.T) {
			testutil.NewSandbox(t)
			writeConfig(t, brokenButOptedIn)

			args := []string{name}
			if name == "search" {
				args = append(args, "a prompt")
			}
			out, err := runTrace(t, args...)

			if err == nil {
				t.Fatalf("reported an answer it could not read; stdout was:\n%s", out)
			}
			// It must name the file and the parse failure, the way `hyctl
			// status` already does, not merely fail.
			if !strings.Contains(err.Error(), "not readable") || !strings.Contains(err.Error(), "config.toml") {
				t.Errorf("error = %q, want it to name the file and say it is unreadable", err)
			}
			// And it must not also have printed the wrong answer first.
			if strings.Contains(out, "is off") {
				t.Errorf("printed a setting it could not read:\n%s", out)
			}
		})
	}
}

// The other half, and the reason the swallow was written: a machine that never
// ran `hyctl init` has no config, and "never opted in" is knowable without one.
// Turning that into an error would be an answer about Hydra's plumbing instead
// of the question asked.
func TestCLI_NoConfigStillAnswers(t *testing.T) {
	for _, name := range []string{"cache", "payloads", "embeddings"} {
		t.Run(name, func(t *testing.T) {
			testutil.NewSandbox(t)
			os.Remove(config.Path())

			out, err := runTrace(t, name)
			if err != nil {
				t.Fatalf("a machine with no config got an error instead of the answer: %v", err)
			}
			if !strings.Contains(out, "off") {
				t.Errorf("want the answer, got:\n%s", out)
			}
		})
	}
}

// reportConfig is the one place the distinction is made, so it is also where it
// can be asserted without a command in the way.
func TestReportConfig(t *testing.T) {
	t.Run("absent is the zero config", func(t *testing.T) {
		testutil.NewSandbox(t)
		os.Remove(config.Path())
		cfg, err := reportConfig()
		if err != nil || cfg == nil {
			t.Fatalf("got %v, %v; want the zero config and no error", cfg, err)
		}
		if cfg.CapturePayloads || cfg.CacheAnswers {
			t.Error("the zero config must not claim anything is on")
		}
	})

	t.Run("unreadable is an error, not a default", func(t *testing.T) {
		testutil.NewSandbox(t)
		writeConfig(t, brokenButOptedIn)
		cfg, err := reportConfig()
		if err == nil {
			t.Fatalf("got %+v; want the parse failure", cfg)
		}
		if cfg != nil {
			t.Error("returned a config beside the error, which a caller may then read")
		}
	})

	t.Run("a readable config is returned as written", func(t *testing.T) {
		testutil.NewSandbox(t)
		writeConfig(t, "cortex = \"c\"\ncache_answers = true\n")
		cfg, err := reportConfig()
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.CacheAnswers {
			t.Error("lost a setting that was there")
		}
	})
}

// `hyctl trace view` renders a reason where text would be. Its own comment says
// "nothing here" and "you turned this off" are different answers; so is "the
// config that would say is unreadable", which it gave as the second.
func TestUnavailableReasonSeparatesOffFromUnreadable(t *testing.T) {
	t.Run("off", func(t *testing.T) {
		testutil.NewSandbox(t)
		writeConfig(t, "cortex = \"c\"\n")
		if got := unavailableReason(""); !strings.Contains(got, "capture is off") {
			t.Errorf("got %q, want it to say capture is off", got)
		}
	})

	t.Run("unreadable", func(t *testing.T) {
		testutil.NewSandbox(t)
		writeConfig(t, brokenButOptedIn)
		got := unavailableReason("")
		if strings.Contains(got, "capture is off") {
			t.Errorf("got %q, which states a setting it could not read", got)
		}
		if !strings.Contains(got, "unknown") {
			t.Errorf("got %q, want it to say the setting is unknown", got)
		}
	})

	t.Run("stored", func(t *testing.T) {
		testutil.NewSandbox(t)
		writeConfig(t, "cortex = \"c\"\ncapture_payloads = true\n")
		if got := unavailableReason(""); !strings.Contains(got, "not stored") {
			t.Errorf("got %q, want the third answer", got)
		}
	})

	t.Run("a ref present means nothing is missing", func(t *testing.T) {
		testutil.NewSandbox(t)
		if got := unavailableReason("abc"); got != "" {
			t.Errorf("got %q, want no reason at all", got)
		}
	})
}
