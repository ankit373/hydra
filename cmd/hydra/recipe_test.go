// SPDX-License-Identifier: MIT

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/runlog"
)

func TestRecipeList_PrintsMenuForKind(t *testing.T) {
	cliSandbox(t)
	out, _, err := run(t, "recipe", "list", "--kind", "extraction")
	if err != nil {
		t.Fatal(err)
	}
	out = stripANSICodes(out)
	for _, want := range []string{"extraction.general", "default single", "single", "cheap_k_schema", "up to 3 answers", "strong_once"} {
		if !strings.Contains(out, want) {
			t.Errorf("recipe list --kind extraction lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "cheap_k_tests") {
		t.Errorf("the extraction menu shows code's recipe:\n%s", out)
	}
}

func overrideRecipes(t *testing.T, home, body string) {
	t.Helper()
	dir := filepath.Join(home, "registry")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "recipes.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A bad override fails loudly with its line, in the listing and before a
// --kind dispatch routes, rather than being skipped.
func TestRecipeList_BadFileNamesTheLine(t *testing.T) {
	s := cliSandbox(t)
	overrideRecipes(t, s.HydraHome, "recipes:\n  - name: vote\n    samples: 5\n    stop: all\n")
	for _, args := range [][]string{
		{"recipe", "list"},
		{"dispatch", "--dry-run", "--kind", "code", "q"},
	} {
		_, _, err := run(t, args...)
		if err == nil || !strings.Contains(err.Error(), "recipes.yaml line 2") {
			t.Errorf("%v: err = %v, want the bad recipe at line 2", args, err)
		}
	}
}

// A label-vote recipe cannot run without a label check, and the task span says so.
func TestDispatch_KindLogsTheRecipesItSkipped(t *testing.T) {
	dispatchable(t, "billing")
	if _, cobraOut, err := run(t, "dispatch", "--no-stream", "--kind", "classification", "--tier", "6",
		"which team owns this ticket?"); err != nil {
		t.Fatalf("dispatch failed: %v (%s)", err, cobraOut)
	}
	logs, err := filepath.Glob(filepath.Join(runlog.Dir(), "*.jsonl"))
	if err != nil || len(logs) != 1 {
		t.Fatalf("run logs %v (%v), want one", logs, err)
	}
	raw, err := os.ReadFile(logs[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"recipes_skipped"`, `"cheap_k_vote_label"`, `needs a match check`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the run log lacks %s:\n%s", want, raw)
		}
	}
}
