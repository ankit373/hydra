// SPDX-License-Identifier: MIT

package review

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/config"
)

// Summary with no files named is what `hyctl review` does by default: it reads
// what Hydra last edited out of logs/. Getting this wrong means the user
// reviews nothing and is told everything is clean.

func writeLog(t *testing.T, name string, v any) {
	t.Helper()
	dir := filepath.Join(config.Dir(), "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSummary_WithNoArgsReadsTheLastParallelRun(t *testing.T) {
	repo := repoSandbox(t, false)
	edited := filepath.Join(repo, "edited.go")
	write(t, edited, "one\ntwo\nthree\n")
	write(t, edited+".hydra-bak", "one\n")

	writeLog(t, "last_parallel.json", []map[string]string{
		{"mode": "edit", "file": edited},
		{"mode": "read", "file": filepath.Join(repo, "ignored.go")}, // not an edit
		{"mode": "edit", "file": ""},                                // no path
	})

	res, err := Summary(nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Totals.Count != 1 {
		t.Fatalf("Summary(nil) reported %d files, want only the one edit: %+v",
			res.Totals.Count, res.Files)
	}
	if res.Files[0].File != edited {
		t.Errorf("File = %q, want %q", res.Files[0].File, edited)
	}
	if res.Files[0].Added != 2 || res.Files[0].Status != "modified" {
		t.Errorf("entry = %+v, want 2 lines added and status modified", res.Files[0])
	}
}

// last_edit.json is the single-file fallback. It must only be consulted when
// the parallel log yielded nothing, or a stale single edit would shadow a
// fresh batch.
func TestSummary_FallsBackToTheLastSingleEditOnlyWhenNeeded(t *testing.T) {
	repo := repoSandbox(t, false)
	fromBatch := filepath.Join(repo, "batch.go")
	fromSingle := filepath.Join(repo, "single.go")
	for _, f := range []string{fromBatch, fromSingle} {
		write(t, f, "a\nb\n")
		write(t, f+".hydra-bak", "a\n")
	}

	// Only last_edit.json exists: it is used.
	writeLog(t, "last_edit.json", map[string]string{"file": fromSingle})
	res, err := Summary(nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Totals.Count != 1 || res.Files[0].File != fromSingle {
		t.Fatalf("Summary(nil) = %+v, want the single edit", res.Files)
	}

	// Now a batch exists too: it wins.
	writeLog(t, "last_parallel.json", []map[string]string{{"mode": "edit", "file": fromBatch}})
	res, err = Summary(nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Totals.Count != 1 || res.Files[0].File != fromBatch {
		t.Errorf("Summary(nil) = %+v, want the batch to shadow the stale single edit", res.Files)
	}
}

// A corrupt or absent log must produce an empty review, not an error and not a
// panic, the logs are written by another process and can be truncated.
func TestSummary_CorruptOrAbsentLogsReviewNothing(t *testing.T) {
	repoSandbox(t, false)

	res, err := Summary(nil)
	if err != nil {
		t.Fatalf("Summary(nil) with no logs at all: %v", err)
	}
	if res.Totals.Count != 0 {
		t.Errorf("Summary(nil) found %d files with no logs", res.Totals.Count)
	}

	dir := filepath.Join(config.Dir(), "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"last_parallel.json", "last_edit.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{truncated"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	res, err = Summary(nil)
	if err != nil {
		t.Fatalf("Summary(nil) with corrupt logs: %v", err)
	}
	if res.Totals.Count != 0 {
		t.Errorf("Summary(nil) found %d files in a corrupt log", res.Totals.Count)
	}
}

// numstat's git branches: tracked-and-modified, tracked-and-unchanged,
// untracked, and missing. Each maps to a different status the reviewer reads.
func TestNumstat_GitStatuses(t *testing.T) {
	repo := repoSandbox(t, true)

	tracked := filepath.Join(repo, "tracked.go")
	write(t, tracked, "one\ntwo\n")
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	run("add", "tracked.go")
	run("commit", "-qm", "initial")

	if added, removed, status := numstat(tracked, repo); status != "unchanged" || added != 0 || removed != 0 {
		t.Errorf("committed file = (%d, %d, %q), want (0, 0, unchanged)", added, removed, status)
	}

	write(t, tracked, "one\ntwo\nthree\n")
	added, removed, status := numstat(tracked, repo)
	if status != "modified" || added != 1 || removed != 0 {
		t.Errorf("modified file = (%d, %d, %q), want (1, 0, modified)", added, removed, status)
	}

	untracked := filepath.Join(repo, "new.go")
	write(t, untracked, "a\nb\nc\n")
	added, removed, status = numstat(untracked, repo)
	if status != "new" || added != 3 {
		t.Errorf("untracked file = (%d, %d, %q), want (3, 0, new)", added, removed, status)
	}

	gone := filepath.Join(repo, "never-existed.go")
	if added, removed, status := numstat(gone, repo); status != "missing" {
		t.Errorf("absent file = (%d, %d, %q), want missing", added, removed, status)
	}
}

// Without a usable git root the backup is the baseline. An unreadable backup
// must be reported as no_baseline rather than as an unchanged file, the whole
// point of #260 is that a silent 0/0 reads as "nothing to review".
func TestNumstat_BackupPathAndUnreadableBaseline(t *testing.T) {
	repo := repoSandbox(t, false)

	file := filepath.Join(repo, "a.go")
	write(t, file, "one\ntwo\nthree\n")
	write(t, file+".hydra-bak", "one\n")

	added, removed, status := numstat(file, "")
	if status != "modified" || added != 2 || removed != 0 {
		t.Errorf("backup baseline = (%d, %d, %q), want (2, 0, modified)", added, removed, status)
	}

	// A file with no backup at all: nothing to compare against.
	noBaseline := filepath.Join(repo, "b.go")
	write(t, noBaseline, "x\n")
	if _, _, status := numstat(noBaseline, ""); status != "no_baseline" {
		t.Errorf("file with no backup = %q, want no_baseline", status)
	}

	// A backup that exists but cannot be read is not a clean file.
	if _, _, status := numstat(filepath.Join(repo, "gone.go"), ""); status != "missing" {
		t.Errorf("absent file with no git = %q, want missing", status)
	}
}

// gitUsable is the guard that stops a git failure being read as a fact about
// the file, in Reject that meant deleting it.
//
// One sandbox per test: NewSandbox empties $PATH, so a second one in the same
// test can no longer resolve git and AllowHostBinary skips.
func TestGitUsable_RejectsAMarkerThatIsNotARepository(t *testing.T) {
	if gitUsable("") {
		t.Error("gitUsable(\"\") = true; there is no root to run in")
	}
	fake := repoSandbox(t, false) // .git exists but is not a repository
	if gitUsable(fake) {
		t.Error("gitUsable() accepted a bare .git directory that is not a repository")
	}
}

func TestGitUsable_AcceptsARealRepository(t *testing.T) {
	repo := repoSandbox(t, true)
	if !gitUsable(repo) {
		t.Error("gitUsable() rejected a real repository")
	}
}

// Diff against a real repository goes through git; the error path must say so
// rather than returning a blank diff (#260).
func TestDiff_UsesGitInARepository(t *testing.T) {
	repo := repoSandbox(t, true)

	file := filepath.Join(repo, "a.go")
	write(t, file, "one\n")
	for _, args := range [][]string{{"add", "a.go"}, {"commit", "-qm", "init"}} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	write(t, file, "one\ntwo\n")

	got, err := Diff(file)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "+two") {
		t.Errorf("git diff did not show the added line:\n%s", got)
	}

	// An unchanged file has an empty diff, which is correct here, unlike the
	// backup path where empty meant the diff had failed.
	write(t, file, "one\n")
	if got, err := Diff(file); err != nil || strings.TrimSpace(got) != "" {
		t.Errorf("Diff of an unchanged tracked file = (%q, %v), want empty", got, err)
	}
}

// QA refuses before it dispatches: a relative path or a file outside the
// workspace must never reach a paid head.
func TestQA_RefusesBeforeDispatching(t *testing.T) {
	repo := repoSandbox(t, false)

	if _, err := QA(context.Background(), "relative.go", 4); err == nil {
		t.Error("QA accepted a relative path")
	} else if !strings.Contains(err.Error(), "absolute") {
		t.Errorf("error = %v, want it to name the problem", err)
	}

	outside := filepath.Join(t.TempDir(), "elsewhere.go")
	write(t, outside, "x\n")
	if _, err := QA(context.Background(), outside, 4); err == nil {
		t.Error("QA accepted a file outside every workspace")
	} else if !strings.Contains(err.Error(), "scope_rejected") {
		t.Errorf("error = %v, want a scope rejection", err)
	}

	// In scope, but nothing has changed: there is no diff to pay a head to read.
	clean := filepath.Join(repo, "clean.go")
	write(t, clean, "x\n")
	if _, err := QA(context.Background(), clean, 4); err == nil {
		t.Error("QA dispatched a review for a file with no diff")
	} else if !strings.Contains(err.Error(), "no diff") {
		t.Errorf("error = %v, want it to say there is no diff", err)
	}
}

// With a real diff and no head available, QA must fail at the dispatch step
// rather than returning an empty verdict that reads as an approval.
func TestQA_NoHeadAvailableIsAnErrorNotAnEmptyVerdict(t *testing.T) {
	repo := repoSandbox(t, false)

	file := filepath.Join(repo, "a.go")
	write(t, file, "one\ntwo\n")
	write(t, file+".hydra-bak", "one\n")

	res, err := QA(context.Background(), file, 4)
	if err == nil {
		t.Fatalf("QA succeeded with no heads discoverable: %+v", res)
	}
	if res != nil {
		t.Errorf("QA returned %+v alongside an error", res)
	}
}
