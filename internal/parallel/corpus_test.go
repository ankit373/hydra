// SPDX-License-Identifier: MIT

//go:build !windows

// Needs a validator script whose exit code depends on the file, so a POSIX
// command line, like the validation tests beside it.

package parallel

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ankit373/hydra/internal/evalset"
)

// The validator exits on the file's own content, so the verdict provably
// depends on what this task wrote. A validator that ignored the file would pass
// a naive test while proving nothing, which is how the dispatch path went
// wrong (#982).
func contentValidator(t *testing.T, want string) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "v.sh")
	// Shell built-ins only: the sandbox empties PATH.
	body := "#!/bin/sh\n" +
		"while IFS= read -r line; do\n" +
		"  case \"$line\" in *" + want + "*) exit 0;; esac\n" +
		"done < \"$1\"\n" +
		"exit 1\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script + " {file}"
}

func batchCorpus(t *testing.T) []evalset.Example {
	t.Helper()
	all, err := evalset.Load(evalset.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	return all
}

// hyctl parallel is the fan-out command, so it is where corpus volume comes
// from. It validated the file it wrote and then recorded nothing at all, not
// even the calibration outcome hyctl edit records (#999).
func TestEdit_BatchFilesTheVerifiedExample(t *testing.T) {
	repo := editSandbox(t, marked("package main // GOOD\n"))
	writeWorkspaceYAML(t, repo, contentValidator(t, "GOOD"))

	file := filepath.Join(repo, "a.go")
	if err := os.WriteFile(file, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := runEdit(t, Task{Label: "ok", Enum: "MODERATE", File: file, Prompt: "mark it good"})
	if got.Status != "ok" {
		t.Fatalf("result = %+v, want ok", got)
	}

	all := batchCorpus(t)
	if len(all) != 1 {
		t.Fatalf("corpus holds %d examples, want 1", len(all))
	}
	e := all[0]
	if !e.Passed {
		t.Error("Passed = false, want true")
	}
	if e.Head == "" {
		t.Error("Head is empty, so the example can never count toward readiness")
	}
	if e.Enum != "MODERATE" {
		t.Errorf("Enum = %q, want MODERATE", e.Enum)
	}
	if e.TaskHash != evalset.TaskHashFor("mark it good") {
		t.Error("TaskHash is not the task's")
	}
}

// The rejection is the more useful record, and it has to be filed before the
// rollback restores content the head never wrote.
func TestEdit_BatchFilesTheRejectionBeforeRollingBack(t *testing.T) {
	repo := editSandbox(t, marked("package main // BAD\n"))
	writeWorkspaceYAML(t, repo, contentValidator(t, "GOOD"))

	file := filepath.Join(repo, "a.go")
	original := "package main\n"
	if err := os.WriteFile(file, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	got := runEdit(t, Task{Label: "bad", Enum: "MODERATE", File: file, Prompt: "mark it good"})
	if got.Status != "fail" || !got.RolledBack {
		t.Fatalf("result = %+v, want a rolled-back failure", got)
	}

	all := batchCorpus(t)
	if len(all) != 1 {
		t.Fatalf("corpus holds %d examples, want 1", len(all))
	}
	if all[0].Passed {
		t.Error("Passed = true, want false")
	}
	if all[0].Candidate == original {
		t.Error("Candidate is the restored original, not what the head wrote")
	}
}

// No validator configured means nothing checked it, and recording that as a
// pass is the mislabelling #982 removed. hyctl edit already refuses to; the two
// paths must not disagree about what counts as evidence.
func TestEdit_BatchWithNoValidatorFilesNothing(t *testing.T) {
	repo := editSandbox(t, marked("package main\n"))
	writeWorkspaceYAML(t, repo, "")

	file := filepath.Join(repo, "a.go")
	if err := os.WriteFile(file, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := runEdit(t, Task{Label: "novalidator", Enum: "MODERATE", File: file, Prompt: "x"})
	if got.Status != "ok" {
		t.Fatalf("result = %+v, want ok", got)
	}
	if n := len(batchCorpus(t)); n != 0 {
		t.Fatalf("corpus holds %d examples, want 0: nothing judged this edit", n)
	}
}

// validate:false is the same argument by a different route.
func TestEdit_BatchWithValidationDisabledFilesNothing(t *testing.T) {
	repo := editSandbox(t, marked("package main // GOOD\n"))
	writeWorkspaceYAML(t, repo, contentValidator(t, "GOOD"))

	file := filepath.Join(repo, "a.go")
	if err := os.WriteFile(file, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	no := false
	runEdit(t, Task{Label: "off", Enum: "MODERATE", File: file, Prompt: "x", Validate: &no})
	if n := len(batchCorpus(t)); n != 0 {
		t.Fatalf("corpus holds %d examples, want 0", n)
	}
}
