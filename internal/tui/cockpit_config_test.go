// SPDX-License-Identifier: MIT

package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/config"
)

func cockpitHome(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HYDRA_HOME", dir)
	if body != "" {
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func startupLog(t *testing.T) string {
	t.Helper()
	m := NewCockpit()
	return strings.Join(m.th().log, "\n")
}

// The cockpit drew "routing engine ready" on a machine where dispatch.New
// fails outright, so the first sign of a broken setup was a prompt that went
// nowhere (#1112).
func TestCockpit_SaysWhenNothingCanBeDispatched(t *testing.T) {
	t.Run("no config at all", func(t *testing.T) {
		cockpitHome(t, "")
		log := startupLog(t)

		if !strings.Contains(log, "No config yet") {
			t.Errorf("startup log does not mention the missing config:\n%s", log)
		}
		// The wizard is the remedy for this one specifically.
		if !strings.Contains(log, "hyctl init") {
			t.Errorf("want the wizard named:\n%s", log)
		}
	})

	t.Run("a config that will not parse", func(t *testing.T) {
		cockpitHome(t, "cortex = \"c\"\ncache_threshold = oops\n")
		log := startupLog(t)

		if !strings.Contains(log, "cannot read its config") {
			t.Errorf("startup log does not mention the unreadable config:\n%s", log)
		}
		// The parse failure is the thing that says what to fix.
		if !strings.Contains(log, "cache_threshold") {
			t.Errorf("want the parse error carried through:\n%s", log)
		}
		// And the wizard is exactly the wrong advice here: it would overwrite
		// the file holding the answer. #1030, pointed the other way.
		if !strings.Contains(log, "Do not run `hyctl init`") {
			t.Errorf("want the wizard warned against, not offered:\n%s", log)
		}
	})

	t.Run("a working config says nothing extra", func(t *testing.T) {
		cockpitHome(t, "cortex = \"c\"\nskills = [\"go\"]\n")
		log := startupLog(t)

		for _, unwanted := range []string{"No config yet", "cannot read its config"} {
			if strings.Contains(log, unwanted) {
				t.Errorf("warned about a config that is fine (%q):\n%s", unwanted, log)
			}
		}
	})
}

// The two failures have opposite remedies, which is the whole reason
// config.Load keeps them apart. Asserted on the renderer directly, so the
// distinction is guarded without a cockpit in the way.
func TestCkConfigLines(t *testing.T) {
	if got := ckConfigLines(nil); got != nil {
		t.Errorf("a readable config produced %v, want nothing", got)
	}

	absent := strings.Join(ckConfigLines(errors.New("no hydra config at /x, run: hyctl init")), " ")
	if strings.Contains(absent, "No config yet") {
		t.Error("matched on the message text; it must test the error, not the words in it")
	}

	wrapped := ckConfigLines(config.ErrNotFound)
	if !strings.Contains(strings.Join(wrapped, " "), "No config yet") {
		t.Errorf("ErrNotFound rendered as %v", wrapped)
	}

	broken := strings.Join(ckConfigLines(errors.New("config /x is not readable: toml: line 5")), " ")
	if !strings.Contains(broken, "cannot read its config") || !strings.Contains(broken, "line 5") {
		t.Errorf("a parse error rendered as %q", broken)
	}
}
