// SPDX-License-Identifier: MIT

//go:build !windows

// Needs a validator script with a controllable exit code, like
// validator_cancel_test.go beside it.

package editor

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ankit373/hydra/internal/evalset"
)

// validatorCheckingContent exits 0 only when the file on disk contains want, so
// its verdict depends on what the edit actually wrote. That dependence is the
// whole difference from a dispatch's verifier, which never sees the candidate
// and judges the tree instead (#982).
func validatorCheckingContent(t *testing.T, want string) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "validate.sh")
	// Shell built-ins only: the sandbox empties PATH, so grep is not there.
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

func editCorpus(t *testing.T) []evalset.Example {
	t.Helper()
	all, err := evalset.Load(evalset.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	return all
}

// The validator ran against the file this edit had just written, so its verdict
// is ground truth about the candidate. That is the corpus entry the router is
// fitted on, and nothing was filing it (#986).
func TestEdit_FilesTheVerifiedExample(t *testing.T) {
	repo := editSandbox(t, marked("package main // GOOD\n"))
	writeWorkspaceYAML(t, repo, validatorCheckingContent(t, "GOOD"))

	file := filepath.Join(repo, "a.go")
	if err := os.WriteFile(file, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Edit(context.Background(), Request{
		File: file, Enum: "MODERATE", Prompt: "mark it good", Validate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "ok" {
		t.Fatalf("status = %q, error %q", res.Status, res.Error)
	}

	all := editCorpus(t)
	if len(all) != 1 {
		t.Fatalf("corpus holds %d examples, want 1: a verified edit is ground truth", len(all))
	}
	e := all[0]
	if !e.Passed {
		t.Errorf("Passed = false, want true: the validator accepted the file")
	}
	if e.Head == "" {
		t.Error("Head is empty, so the example can never count toward readiness")
	}
	if e.Enum != "MODERATE" {
		t.Errorf("Enum = %q, want MODERATE", e.Enum)
	}
	if e.TaskHash != evalset.TaskHashFor("mark it good") {
		t.Error("TaskHash is not the task's, so two edits would collide on it")
	}
	if e.Domain == "" {
		t.Error("Domain is empty")
	}
}

// A rejected edit is the more valuable record: a corpus of only passes cannot
// rank one head against another. It has to be filed before the rollback, which
// is what discards the content the verdict is about.
func TestEdit_FilesTheRejectedExampleBeforeRollingBack(t *testing.T) {
	repo := editSandbox(t, marked("package main // BAD\n"))
	writeWorkspaceYAML(t, repo, validatorCheckingContent(t, "GOOD"))

	file := filepath.Join(repo, "a.go")
	original := "package main\n"
	if err := os.WriteFile(file, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Edit(context.Background(), Request{
		File: file, Enum: "MODERATE", Prompt: "mark it good", Validate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "fail" || !res.RolledBack {
		t.Fatalf("status = %q rolledBack = %v, want a rolled-back failure", res.Status, res.RolledBack)
	}
	if raw, _ := os.ReadFile(file); string(raw) != original {
		t.Fatalf("file = %q, want the original restored", raw)
	}

	all := editCorpus(t)
	if len(all) != 1 {
		t.Fatalf("corpus holds %d examples, want 1: the rejection is the evidence", len(all))
	}
	if all[0].Passed {
		t.Error("Passed = true, want false: the validator rejected this content")
	}
	// The rollback restored the file, but the example must still hold what the
	// head actually produced, or the corpus records the old content as the answer.
	if all[0].Candidate == original {
		t.Error("Candidate is the restored original, not what the head wrote")
	}
}

// Nothing ran, so nothing was learned. validatorPassed is initialised true and
// stays true here, which is exactly the mislabelling #982 removed elsewhere.
func TestEdit_NoConfiguredValidatorFilesNothing(t *testing.T) {
	repo := editSandbox(t, marked("package main\n"))
	writeWorkspaceYAML(t, repo, "") // a workspace, but no validator for .go

	file := filepath.Join(repo, "a.go")
	if err := os.WriteFile(file, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Edit(context.Background(), Request{
		File: file, Enum: "MODERATE", Prompt: "x", Validate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "ok" {
		t.Fatalf("status = %q, error %q", res.Status, res.Error)
	}
	if n := len(editCorpus(t)); n != 0 {
		t.Fatalf("corpus holds %d examples, want 0: no validator ran, so nothing was verified", n)
	}
}

// --validate=false is the same argument by a different route.
func TestEdit_ValidationDisabledFilesNothing(t *testing.T) {
	repo := editSandbox(t, marked("package main // GOOD\n"))
	writeWorkspaceYAML(t, repo, validatorCheckingContent(t, "GOOD"))

	file := filepath.Join(repo, "a.go")
	if err := os.WriteFile(file, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Edit(context.Background(), Request{
		File: file, Enum: "MODERATE", Prompt: "x", Validate: false,
	}); err != nil {
		t.Fatal(err)
	}
	if n := len(editCorpus(t)); n != 0 {
		t.Fatalf("corpus holds %d examples, want 0: nothing judged this edit", n)
	}
}
