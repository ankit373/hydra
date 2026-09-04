// SPDX-License-Identifier: MIT

package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/testutil"
)

// reviewRepo is the desktop-side equivalent of internal/review's repoSandbox:
// a workspace with a .git directory that is not a repository, so git commands
// fail and the backup path is the one exercised. The chdir matters —
// workspace resolution roots itself at GitRoot(os.Getwd()), and the OS's
// spelling of a temp dir differs from the one handed to us (macOS /var
// symlinks, Windows casing).
func reviewRepo(t *testing.T) string {
	t.Helper()
	testutil.NewSandbox(t)

	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	cwd, err := os.Getwd()
	if err != nil {
		return repo
	}
	return cwd
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Both edit paths refuse a relative path, so a stored one is absolute by
// construction. Anything else came from somewhere Hydra did not write, and
// resolving it against a guessed workspace root could accept a file outside
// the intended scope.
func TestApproveEdit_RefusesAPathItCannotResolve(t *testing.T) {
	reviewRepo(t)

	for _, bad := range []string{"", "src/a.go", "./a.go", "../a.go"} {
		got := New().ApproveEdit(bad)
		if got.Error == "" {
			t.Errorf("ApproveEdit(%q) should refuse, got %+v", bad, got)
		}
		if got.Status != "" {
			t.Errorf("ApproveEdit(%q) reported status %q while refusing", bad, got.Status)
		}
	}
}

func TestRejectEdit_RefusesAPathItCannotResolve(t *testing.T) {
	reviewRepo(t)

	for _, bad := range []string{"", "src/a.go"} {
		got := New().RejectEdit(bad)
		if got.Error == "" {
			t.Errorf("RejectEdit(%q) should refuse, got %+v", bad, got)
		}
	}
}

func TestApproveEdit_AcceptsAChangeAndDropsTheBackup(t *testing.T) {
	repo := reviewRepo(t)
	file := filepath.Join(repo, "src", "a.go")
	backup := file + ".hydra-bak"
	writeFile(t, file, "NEW")
	writeFile(t, backup, "ORIGINAL")

	got := New().ApproveEdit(file)
	if got.Error != "" {
		t.Fatalf("ApproveEdit: %s", got.Error)
	}
	if got.Status != "approved" {
		t.Errorf("Status = %q, want approved", got.Status)
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Error("the backup survived an approval, so the change is still undoable")
	}
	body, _ := os.ReadFile(file)
	if string(body) != "NEW" {
		t.Errorf("approving changed the file: %q", body)
	}
}

// Approving a file that never existed must surface the error rather than
// reporting success for an accountability trail nobody can trust (#449).
func TestApproveEdit_SurfacesAMissingFile(t *testing.T) {
	repo := reviewRepo(t)

	got := New().ApproveEdit(filepath.Join(repo, "src", "never-existed.go"))
	if got.Error == "" {
		t.Fatal("approving a nonexistent file reported success")
	}
	if got.Status == "approved" {
		t.Error("a failed approval still reported approved")
	}
}

func TestRejectEdit_RestoresAndNamesHow(t *testing.T) {
	repo := reviewRepo(t)
	file := filepath.Join(repo, "src", "a.go")
	backup := file + ".hydra-bak"
	writeFile(t, file, "BROKEN")
	writeFile(t, backup, "ORIGINAL")

	got := New().RejectEdit(file)
	if got.Error != "" {
		t.Fatalf("RejectEdit: %s", got.Error)
	}
	if got.Status != "rejected" {
		t.Errorf("Status = %q, want rejected", got.Status)
	}
	// The three rollback methods are not equally recoverable, so which one ran
	// is part of the answer, not an implementation detail.
	if got.Method != "backup_restore" {
		t.Errorf("Method = %q, want backup_restore", got.Method)
	}
	body, _ := os.ReadFile(file)
	if string(body) != "ORIGINAL" {
		t.Errorf("file = %q after reject, want the pre-edit content", body)
	}
}

// A button that silently does nothing is worse than one that says why it
// cannot. review.Reject already fails loudly; the API must not swallow it.
func TestRejectEdit_SaysWhenThereIsNothingToRollBack(t *testing.T) {
	repo := reviewRepo(t)
	file := filepath.Join(repo, "src", "a.go")
	writeFile(t, file, "EDITED")

	got := New().RejectEdit(file)
	if got.Error == "" {
		t.Fatal("rejecting with no backup and no git reported success")
	}
	if !strings.Contains(got.Error, "nothing to roll back") {
		t.Errorf("error should say what is missing, got %q", got.Error)
	}
	if got.Status == "rejected" {
		t.Error("a failed rollback still reported rejected")
	}
	// And it must not have touched the file on its way to failing.
	body, _ := os.ReadFile(file)
	if string(body) != "EDITED" {
		t.Errorf("a failed rollback modified the file: %q", body)
	}
}

// scopeCheck is review's guard, not this layer's, but the desktop must not be
// a way around it.
func TestApproveEdit_RefusesOutsideEveryWorkspace(t *testing.T) {
	reviewRepo(t)
	outside := filepath.Join(t.TempDir(), "elsewhere.go")
	writeFile(t, outside, "x")

	if got := New().ApproveEdit(outside); got.Error == "" {
		t.Errorf("a path outside every workspace was approved: %+v", got)
	}
}
