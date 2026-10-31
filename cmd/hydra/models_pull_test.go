// SPDX-License-Identifier: MIT

package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/modelpull"
	"github.com/ankit373/hydra/internal/sysinfo"
)

// Hardware that could not be read must disable the guard, not refuse
// everything: the absence of a reading is not a verdict about the machine
// (#258, where unknown hardware ranked every local model as insufficient).
func TestUsableBytesForPull_UnknownHardwareDisablesTheGuard(t *testing.T) {
	if got := usableBytesForPull(&sysinfo.Specs{}); got != 0 {
		t.Errorf("got %d, want 0 so nothing is refused on a reading that does not exist", got)
	}
	known := &sysinfo.Specs{TotalRAMGB: 32, FreeRAMGB: 16}
	if got := usableBytesForPull(known); got <= 0 {
		t.Errorf("got %d for readable hardware, want a real budget", got)
	}
}

// A server that is not running and a model that does not exist are different
// problems, and sending someone to fix the wrong one wastes their time (#248).
func TestPullServerError_NamesTheFix(t *testing.T) {
	err := pullServerError(fmt.Errorf("%w: dial tcp: refused", modelpull.ErrServerDown), "http://localhost:11434")
	for _, want := range []string{"ollama serve", "OLLAMA_HOST", "http://localhost:11434"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not carry %q", err, want)
		}
	}

	// Anything else passes through untouched rather than being relabelled.
	other := errors.New("some other failure")
	if got := pullServerError(other, "http://x"); got != other {
		t.Errorf("got %v, want the original error unchanged", got)
	}
}

// A refusal on size has to say what to do instead, or it is a dead end.
func TestPullError_TooLargeSuggestsASmallerQuant(t *testing.T) {
	err := pullError(fmt.Errorf("%w: 42.5 GB to download against 3.0 GB usable", modelpull.ErrTooLarge), false)
	for _, want := range []string{"Q4_K_M", "--force", "42.5 GB"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not carry %q", err, want)
		}
	}
	// With --force already given, repeating the suggestion would be noise.
	forced := pullError(fmt.Errorf("%w: too big", modelpull.ErrTooLarge), true)
	if strings.Contains(forced.Error(), "--force") {
		t.Errorf("error %q suggests --force to someone who passed it", forced)
	}
}

// A re-pull is not a failure, and must not claim a head appeared that did not.
func TestReportPulled_AlreadyInstalledClaimsNothingNew(t *testing.T) {
	out := captureStdout(t, func() {
		if err := reportPulled("hf.co/a/b", nil, false); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "already installed") {
		t.Errorf("output %q does not say the model was already there", out)
	}
	if strings.Contains(out, "routable as") {
		t.Errorf("output %q names a head that did not appear", out)
	}
}

// The head id comes from what the server reports, because Ollama rewrites a
// HuggingFace ref into a name of its own: deriving it from the ref would print
// an id the router does not have.
func TestReportPulled_NamesTheHeadTheRouterWillUse(t *testing.T) {
	out := captureStdout(t, func() {
		if err := reportPulled("hf.co/Qwen/Qwen2.5-0.5B-Instruct-GGUF",
			[]string{"hf.co/Qwen/Qwen2.5-0.5B-Instruct-GGUF:latest"}, false); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "ollama/hf.co/Qwen/Qwen2.5-0.5B-Instruct-GGUF:latest") {
		t.Errorf("output %q does not name the head id the port provider mints", out)
	}
}

// Progress with no known total must not render a percentage, or an unmeasured
// download reads as one stalled at zero.
func TestPullLine(t *testing.T) {
	cases := []struct {
		name string
		in   modelpull.Progress
		want string
	}{
		{"no total yet", modelpull.Progress{Status: "pulling manifest"}, "pulling manifest"},
		{"with total", modelpull.Progress{Status: "pulling abc", Total: 1000, Completed: 250}, "250 B / 1.0 kB (25%)"},
		{"complete", modelpull.Progress{Status: "pulling abc", Total: 1000, Completed: 1000}, "1.0 kB / 1.0 kB (100%)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pullLine(tc.in); !strings.Contains(got, tc.want) {
				t.Errorf("got %q, want it to carry %q", got, tc.want)
			}
		})
	}
}

// The flag is a boolean. A backtick in a usage string makes cobra read the
// word after it as an argument placeholder, which is how --verify once
// rendered as `--verify hyctl trust reliability`.
func TestCLI_ModelsPullFlagsAreBooleans(t *testing.T) {
	cmd := cmdModelsPull()
	for _, name := range []string{"force", "json"} {
		f := cmd.Flags().Lookup(name)
		if f == nil {
			t.Fatalf("no --%s flag", name)
		}
		if f.Value.Type() != "bool" {
			t.Errorf("--%s is %s, want bool", name, f.Value.Type())
		}
		if strings.Contains(f.Usage, "`") {
			t.Errorf("--%s usage has a backtick, which cobra reads as an argument placeholder: %q", name, f.Usage)
		}
	}
}
