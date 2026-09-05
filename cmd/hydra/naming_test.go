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
