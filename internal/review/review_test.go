// SPDX-License-Identifier: MIT

package review

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/testutil"
	"github.com/ankit373/hydra/internal/trust"
)

// internal/review was at 0%. Approve and Reject are the other half of the
// rollback story, Reject is the only user-facing way to undo an edit, and it
// runs `git checkout --` on whatever it resolves.
//
// Every test below chdirs into its own temp repository first. That is not
// tidiness: without it, a Reject test resolving the real workspace would run
// `git checkout --` against this repo and discard uncommitted work.

// repoSandbox gives a hermetic environment whose only workspace is a fresh git
// repository in a temp dir. workspace.Load finds no configured registry and
// falls back to the repo the process is standing in (#297), so this is also
// what a real first-run install does.
func repoSandbox(t *testing.T, gitInit bool) string {
	t.Helper()
	s := testutil.NewSandbox(t)

	repo := t.TempDir()
	if gitInit {
		// Admit the real git rather than skipping. These are the paths that
		// delete files; a skip here means they are covered on no machine at all.
		if !s.AllowHostBinary(t, "git") {
			t.Skip("git is not installed on this machine")
		}
		for _, args := range [][]string{
			{"init", "-q"},
			{"config", "user.email", "t@example.com"},
			{"config", "user.name", "t"},
			// Windows git defaults core.autocrlf=true, so a checkout rewrites
			// LF to CRLF and the restored bytes differ from the committed ones.
			// Pinned off so the test asserts what git restored rather than what
			// the platform's line-ending policy happens to be.
			{"config", "core.autocrlf", "false"},
		} {
			cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v failed: %v (%s)", args, err, out)
			}
		}
	} else {
		// A .git directory that is not a repository: workspace resolution still
		// treats this as the root, but git commands fail, so the backup path is
		// exercised.
		if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	// Return the working directory as the OS reports it, not the temp path we
	// were handed. workspace's fallback roots itself at GitRoot(os.Getwd()), and
	// the two spellings differ in practice: macOS gives /var symlinks for temp
	// dirs, and Windows normalises casing and short (8.3) components. Comparing
	// against anything else makes every containment check fail with
	// "no workspace contains …", which is exactly what the Windows leg caught.
	cwd, err := os.Getwd()
	if err != nil {
		return repo
	}
	return cwd
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeLastEditFixture stands in for editor.writeLastEdit without importing
// the editor package, same file, same shape.
func writeLastEditFixture(t *testing.T, file, headID string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"file": file, "head_id": headID})
	if err != nil {
		t.Fatal(err)
	}
	logDir := filepath.Join(config.Dir(), "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "last_edit.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// calibrationObservation reports N for (source, domain), or -1 if absent.
func calibrationObservation(t *testing.T, source, domain string) float64 {
	t.Helper()
	cal, err := trust.New(trust.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range cal.Report() {
		if s.Source == source && s.Domain == domain {
			return s.N
		}
	}
	return -1
}

// ── Approve ───────────────────────────────────────────────────────────────────

// Approving a non-git edit consumes the backup. Leaving it behind means the
// next edit sees a stale baseline and a later Reject restores the wrong bytes.
func TestApprove_ConsumesTheBackup(t *testing.T) {
	repo := repoSandbox(t, false)
	file := filepath.Join(repo, "src", "a.go")
	backup := file + ".hydra-bak"
	write(t, file, "new")
	write(t, backup, "old")

	got, err := Approve(file)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "approved" || got.File != file {
		t.Errorf("Approve = %+v", got)
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Error("the backup survived approval, a later Reject would restore stale bytes")
	}
	// The edited content is untouched: approving is not a write.
	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "new" {
		t.Errorf("file = %q after approve, want the edited content", body)
	}
}

// Approve is a real ground-truth signal for calibration, the head that
// produced the edit gets a "correct" observation, without a human ever
// running `hyctl trust record`.
func TestApprove_RecordsACalibrationObservation(t *testing.T) {
	repo := repoSandbox(t, false)
	file := filepath.Join(repo, "src", "a.go")
	write(t, file, "new")
	writeLastEditFixture(t, file, "cody")

	if _, err := Approve(file); err != nil {
		t.Fatal(err)
	}
	if n := calibrationObservation(t, "cody", "go"); n != 1 {
		t.Errorf("observations for cody/go = %v, want 1", n)
	}
}

func TestApprove_RefusesRelativePaths(t *testing.T) {
	repoSandbox(t, false)
	if _, err := Approve("relative.go"); err == nil {
		t.Error("a relative path was accepted")
	}
}

// Scope is enforced before anything is touched: a path outside the workspace
// must be refused, not approved.
func TestApprove_RefusesAPathOutsideTheWorkspace(t *testing.T) {
	repoSandbox(t, false)
	outside := filepath.Join(t.TempDir(), "x.go")
	write(t, outside, "x")

	if _, err := Approve(outside); err == nil {
		t.Error("a path outside every workspace was approved")
	}
}

// scopeCheck only validates the path against workspace globs, it never stats
// the file. Approving a file that never existed must error, not report
// "approved" for an accountability trail nobody can trust (#449).
func TestApprove_RefusesANonexistentFile(t *testing.T) {
	repo := repoSandbox(t, false)
	file := filepath.Join(repo, "src", "never-existed.go")

	if _, err := Approve(file); err == nil {
		t.Error("Approve reported success for a file that was never created")
	}
}

// ── Reject ────────────────────────────────────────────────────────────────────

// The backup path: with no usable git, the .hydra-bak bytes are the truth.
func TestReject_RestoresFromTheBackup(t *testing.T) {
	repo := repoSandbox(t, false)
	file := filepath.Join(repo, "src", "a.go")
	backup := file + ".hydra-bak"
	write(t, file, "BROKEN")
	write(t, backup, "ORIGINAL")

	got, err := Reject(file)
	if err != nil {
		t.Fatal(err)
	}
	if got.Method != "backup_restore" {
		t.Errorf("Method = %q, want backup_restore", got.Method)
	}
	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "ORIGINAL" {
		t.Errorf("file = %q after reject, want the pre-edit content", body)
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Error("the backup was left behind after being consumed")
	}
}

// Reject is the negative ground-truth signal: the head's edit was bad enough
// to undo, so it must reach calibration as an incorrect observation.
func TestReject_RecordsACalibrationObservation(t *testing.T) {
	repo := repoSandbox(t, false)
	file := filepath.Join(repo, "src", "a.go")
	backup := file + ".hydra-bak"
	write(t, file, "BROKEN")
	write(t, backup, "ORIGINAL")
	writeLastEditFixture(t, file, "cody")

	if _, err := Reject(file); err != nil {
		t.Fatal(err)
	}
	if n := calibrationObservation(t, "cody", "go"); n != 1 {
		t.Errorf("observations for cody/go = %v, want 1", n)
	}
}

// Nothing to roll back must be an error, not a silent success that tells the
// user their edit was undone when it was not.
func TestReject_NothingToRollBackIsAnError(t *testing.T) {
	repo := repoSandbox(t, false)
	file := filepath.Join(repo, "src", "a.go")
	write(t, file, "content")

	if _, err := Reject(file); err == nil {
		t.Error("Reject reported success with no backup and no git baseline")
	}
	// …and it must not have deleted the file as a consolation.
	if _, err := os.Stat(file); err != nil {
		t.Errorf("the file was removed despite there being nothing to roll back: %v", err)
	}
}

// A tracked file in a real repo is restored with git, and the working tree ends
// up matching HEAD.
func TestReject_TrackedFileIsRestoredFromGit(t *testing.T) {
	repo := repoSandbox(t, true)
	file := filepath.Join(repo, "a.go")
	write(t, file, "COMMITTED\n")

	for _, args := range [][]string{{"add", "a.go"}, {"commit", "-qm", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v (%s)", args, err, out)
		}
	}
	write(t, file, "BROKEN\n")

	got, err := Reject(file)
	if err != nil {
		t.Fatal(err)
	}
	if got.Method != "git_checkout" {
		t.Errorf("Method = %q, want git_checkout", got.Method)
	}
	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "COMMITTED\n" {
		t.Errorf("file = %q, want the committed content restored", body)
	}
}

// An untracked file created by an edit has no baseline to restore, so rejecting
// it means removing it.
func TestReject_UntrackedFileIsRemoved(t *testing.T) {
	repo := repoSandbox(t, true)
	file := filepath.Join(repo, "brand-new.go")
	write(t, file, "generated by a model")

	got, err := Reject(file)
	if err != nil {
		t.Fatal(err)
	}
	if got.Method != "rm_untracked" {
		t.Errorf("Method = %q, want rm_untracked", got.Method)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Error("an untracked generated file survived rejection")
	}
}

// A file that was never created and never tracked has nothing to roll back
// to, in a real git repo as much as a backup-only workspace (#449 sibling
// case for Reject, the "nothing to roll back" gate already covers it).
func TestReject_RefusesANonexistentFileInAGitRepo(t *testing.T) {
	repo := repoSandbox(t, true)
	file := filepath.Join(repo, "never-existed.go")

	if _, err := Reject(file); err == nil {
		t.Error("Reject reported success for a file that was never created or tracked")
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Error("Reject conjured a file that never existed")
	}
}

func TestReject_RefusesRelativeAndOutOfScopePaths(t *testing.T) {
	repoSandbox(t, false)

	if _, err := Reject("relative.go"); err == nil {
		t.Error("a relative path was accepted")
	}
	outside := filepath.Join(t.TempDir(), "x.go")
	write(t, outside, "x")
	if _, err := Reject(outside); err == nil {
		t.Error("a path outside every workspace was rejected-through")
	}
	// Critically, the out-of-scope file must still be there.
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("an out-of-scope file was deleted by Reject: %v", err)
	}
}

// ── Summary / numstat ─────────────────────────────────────────────────────────

// numstat drives what `hyctl review` reports. Reporting 0/0 for a modified file
// is the #260 failure mode, it reads as "nothing changed" and gets approved.
func TestSummary_CountsAModifiedFile(t *testing.T) {
	repo := repoSandbox(t, false)
	file := filepath.Join(repo, "src", "a.go")
	write(t, file, "one\ntwo\nthree\n")
	write(t, file+".hydra-bak", "one\n")

	res, err := Summary([]string{file})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 1 {
		t.Fatalf("got %d entries, want 1: %+v", len(res.Files), res.Files)
	}
	e := res.Files[0]
	if e.Status != "modified" {
		t.Errorf("status = %q, want modified", e.Status)
	}
	if e.Added == 0 {
		t.Error("added = 0 for a file with two new lines, this is the #260 symptom")
	}
	if res.Totals.Count != 1 || res.Totals.Added != e.Added {
		t.Errorf("totals %+v do not aggregate the single entry", res.Totals)
	}
}

// A file with no baseline is reported as such, not as unchanged.
func TestSummary_NoBaselineIsItsOwnStatus(t *testing.T) {
	repo := repoSandbox(t, false)
	file := filepath.Join(repo, "src", "fresh.go")
	write(t, file, "content\n")

	res, err := Summary([]string{file})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 1 {
		t.Fatalf("got %d entries", len(res.Files))
	}
	if s := res.Files[0].Status; s == "modified" {
		t.Errorf("status = %q for a file with no baseline, that claims a change nobody measured", s)
	}
}

// Relative paths are skipped rather than guessed at.
func TestSummary_SkipsRelativePaths(t *testing.T) {
	repoSandbox(t, false)

	res, err := Summary([]string{"relative.go", "also/relative.go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 0 {
		t.Errorf("relative paths produced entries: %+v", res.Files)
	}
}

// ── Diff ──────────────────────────────────────────────────────────────────────

// Diff must distinguish "no changes" from "could not produce a diff", the two
// were the same value before #260, so a reviewer approved changes never shown.
func TestDiff_NoBaselineIsAnErrorNotAnEmptyDiff(t *testing.T) {
	repo := repoSandbox(t, false)
	file := filepath.Join(repo, "src", "a.go")
	write(t, file, "content\n")

	out, err := Diff(file)
	if err == nil {
		t.Errorf("Diff returned %q and no error for a file with no baseline", out)
	}
}

func TestDiff_ProducesAUnifiedDiffFromTheBackup(t *testing.T) {
	repo := repoSandbox(t, false)
	file := filepath.Join(repo, "src", "a.go")
	write(t, file, "one\nCHANGED\n")
	write(t, file+".hydra-bak", "one\ntwo\n")

	out, err := Diff(file)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "+CHANGED") || !strings.Contains(out, "-two") {
		t.Errorf("diff does not describe the change:\n%s", out)
	}
}

func TestDiff_RefusesRelativePaths(t *testing.T) {
	repoSandbox(t, false)
	if _, err := Diff("relative.go"); err == nil {
		t.Error("a relative path was accepted")
	}
}

// git diff on a pathspec it doesn't track exits 0 with empty output, exactly
// like a real "no changes" result. Both a file that never existed and one
// that exists but was never git-added hit this, Diff must error on both
// instead of returning the same blank string a genuine no-op diff gives (#459).
func TestDiff_UntrackedOrNonexistentFileInAGitRepoErrorsClearly(t *testing.T) {
	repo := repoSandbox(t, true)

	t.Run("never existed", func(t *testing.T) {
		file := filepath.Join(repo, "never-existed.go")
		out, err := Diff(file)
		if err == nil {
			t.Errorf("Diff returned %q and no error for a file that never existed", out)
		}
	})

	t.Run("exists but was never git-added", func(t *testing.T) {
		file := filepath.Join(repo, "brand-new.go")
		write(t, file, "package main\n")
		out, err := Diff(file)
		if err == nil {
			t.Errorf("Diff returned %q and no error for an untracked file", out)
		}
	})
}

// The real "no changes" case, a tracked, committed, untouched file, must
// stay a non-error empty diff, distinct from the untracked/nonexistent error.
func TestDiff_RealNoChangesIsNotAnError(t *testing.T) {
	repo := repoSandbox(t, true)
	file := filepath.Join(repo, "settled.go")
	write(t, file, "package main\n")

	for _, args := range [][]string{{"add", "settled.go"}, {"commit", "-qm", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v (%s)", args, err, out)
		}
	}

	out, err := Diff(file)
	if err != nil {
		t.Fatalf("Diff errored on a real, unmodified, tracked file: %v", err)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("Diff = %q, want empty for an unmodified tracked file", out)
	}
}
