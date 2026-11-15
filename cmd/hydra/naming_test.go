// SPDX-License-Identifier: MIT

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ankit373/hydra/internal/testutil"
)

// The binary is hyctl. Cobra prints `Use` correctly on its own, but every
// hand-written error string, help Long and Example is just text nobody
// recompiles against, so the #150 rename left 37 of them across 12 files still
// telling users to run `hydra <something>`. `hyctl status` on a fresh machine
// answered "no config found, run: hydra init", which is a command that does
// not exist. That is the first thing a new user sees.
//
// Command names come from the real tree rather than a hardcoded list, so adding
// a subcommand extends this guard automatically.
func TestUserFacingText_NeverNamesABinaryThatDoesNotExist(t *testing.T) {
	var names []string
	var collect func(*cobra.Command)
	collect = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			names = append(names, regexp.QuoteMeta(sub.Name()))
			collect(sub)
		}
	}
	collect(rootCmd())
	if len(names) == 0 {
		t.Fatal("no subcommands found, the guard would silently pass")
	}

	stale := regexp.MustCompile(`\bhydra (` + strings.Join(names, "|") + `)\b`)

	// Tests run in cmd/hydra; the tree to scan is the module root.
	roots := []string{filepath.Join("..", "..", "cmd"), filepath.Join("..", "..", "internal")}
	self := "naming_test.go"

	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || d.Name() == self {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for i, line := range strings.Split(string(src), "\n") {
				if m := stale.FindString(line); m != "" {
					t.Errorf("%s:%d says %q, the binary is hyctl, so that command does not exist:\n    %s",
						path, i+1, m, strings.TrimSpace(line))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", root, err)
		}
	}
}

// A runtime error (e.g. "no config found") used to dump the full flags block
// for every one of ~25 subcommands, drowning the one line that actually
// mattered. SilenceUsage on the root command must propagate to every
// subcommand, including one deep in a command group (#464).
func TestUserFacingText_RuntimeErrorsDoNotDumpUsage(t *testing.T) {
	testutil.NewSandbox(t) // no config written

	_, cobraOut, err := run(t, "status")
	if err == nil {
		t.Fatal("`hyctl status` on an unconfigured machine did not error")
	}
	if strings.Contains(cobraOut, "Usage:") || strings.Contains(cobraOut, "Flags:") {
		t.Errorf("a runtime error dumped the flags block:\n%s", cobraOut)
	}
}

// The tap formula's desc is typed into the script by hand, so it drifted from
// `hyctl --help` once already. brew search shows it, so it must say the same.
func TestTapFormulaDesc_MatchesRootHelp(t *testing.T) {
	m := regexp.MustCompile(`(?m)^\s*desc "([^"]*)"$`).
		FindStringSubmatch(repoFile(t, "scripts", "update-tap-formula.sh"))
	if m == nil {
		t.Fatal("scripts/update-tap-formula.sh has no desc line")
	}
	if want := rootCmd().Short; m[1] != want {
		t.Errorf("formula desc %q differs from root help %q", m[1], want)
	}
}

// The skills and AGENTS.md are instructions a model follows, so a script they
// name that was removed (dispatch/route.sh, #86) sends it to a dead command.
func TestAgentInstructions_NeverNameAScriptThatDoesNotExist(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "skills", "*.md"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no skills found (%v), the guard would silently pass", err)
	}
	files = append(files, filepath.Join("..", "..", "AGENTS.md"))
	script := regexp.MustCompile(`[\w./-]+\.sh\b`)
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range script.FindAllString(string(src), -1) {
			if _, err := os.Stat(filepath.Join("..", "..", name)); err != nil {
				t.Errorf("%s names %s, which is not in the repository", f, name)
			}
		}
	}
}
