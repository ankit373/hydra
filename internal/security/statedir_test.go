// SPDX-License-Identifier: MIT

//go:build !windows

package security

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dirAt creates a directory and forces its mode, since umask silently narrows
// whatever MkdirAll is given and would make these tests measure the umask.
func dirAt(t *testing.T, path string, mode os.FileMode) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o700) })
	return path
}

// The finding: a directory another user can write is one in which the integrity
// baseline and the chain anchor can be replaced.
func TestStateDirCheck_AWritableDirectoryIsTheFinding(t *testing.T) {
	root := dirAt(t, filepath.Join(t.TempDir(), "hydra"), 0o777)

	c := stateDirCheckOn(root, "darwin")
	if c.Status != "1 writable by others" {
		t.Fatalf("Status = %q, want 1 writable by others", c.Status)
	}
	if !strings.Contains(c.Detail, "head_binaries.json") {
		t.Errorf("Detail does not say what a writer could replace: %s", c.Detail)
	}
	if !strings.Contains(c.Detail, "0777") {
		t.Errorf("Detail does not name the mode: %s", c.Detail)
	}
}

// Readable but not writable is the weaker finding and must not be reported as
// the stronger one: it leaks the listing, not the files.
func TestStateDirCheck_ReadableIsNotReportedAsWritable(t *testing.T) {
	root := dirAt(t, filepath.Join(t.TempDir(), "hydra"), 0o755)

	c := stateDirCheckOn(root, "darwin")
	if c.Status != "1 readable by others" {
		t.Fatalf("Status = %q, want 1 readable by others", c.Status)
	}
	if strings.Contains(c.Detail, "head_binaries.json") {
		t.Errorf("Detail claims the baseline is replaceable on a directory nobody can write: %s", c.Detail)
	}
	if !strings.Contains(c.Detail, "listing") {
		t.Errorf("Detail does not say what is actually exposed: %s", c.Detail)
	}
}

func TestStateDirCheck_OwnerOnlyIsClean(t *testing.T) {
	root := dirAt(t, filepath.Join(t.TempDir(), "hydra"), 0o700)

	if c := stateDirCheckOn(root, "darwin"); c.Status != "owner only" {
		t.Errorf("Status = %q, want owner only", c.Status)
	}
}

// Every answer has to say what it did not look at, the same discipline as the
// exposure check's firewall caveat.
func TestStateDirCheck_EveryOutcomeStatesWhatItDidNotSee(t *testing.T) {
	base := t.TempDir()
	for _, mode := range []os.FileMode{0o700, 0o755, 0o777} {
		root := dirAt(t, filepath.Join(base, mode.String()), mode)
		c := stateDirCheckOn(root, "darwin")
		if !strings.Contains(c.Detail, "ACL") {
			t.Errorf("mode %v: detail %q does not say an ACL is outside what it read", mode, c.Detail)
		}
	}
}

// A nested directory is where the ledger and the run logs actually live, so a
// check that only looked at the root would miss the interesting case.
func TestStateDirCheck_WalksNestedDirectories(t *testing.T) {
	root := dirAt(t, filepath.Join(t.TempDir(), "hydra"), 0o700)
	dirAt(t, filepath.Join(root, "logs"), 0o700)
	dirAt(t, filepath.Join(root, "logs", "runs"), 0o777)

	c := stateDirCheckOn(root, "darwin")
	if c.Status != "1 writable by others" {
		t.Fatalf("Status = %q, want the nested directory found", c.Status)
	}
	if !strings.Contains(c.Detail, "runs") {
		t.Errorf("Detail does not name the nested directory: %s", c.Detail)
	}
}

// Files are not examined: a directory's own mode is what decides whether
// another user can replace what is inside it.
func TestStateDirCheck_AFileModeIsNotTheFinding(t *testing.T) {
	root := dirAt(t, filepath.Join(t.TempDir(), "hydra"), 0o700)
	if err := os.WriteFile(filepath.Join(root, "loose.json"), []byte("{}"), 0o666); err != nil {
		t.Fatal(err)
	}

	if c := stateDirCheckOn(root, "darwin"); c.Status != "owner only" {
		t.Errorf("Status = %q, want owner only: a file's mode is not a directory's reach", c.Status)
	}
}

// Windows has no Unix mode bits, so the question is not answerable this way.
// Not evaluated, never a pass, the same as a machine with nowhere to probe from.
func TestStateDirCheck_WindowsIsNotEvaluated(t *testing.T) {
	root := dirAt(t, filepath.Join(t.TempDir(), "hydra"), 0o777)

	c := stateDirCheckOn(root, "windows")
	if c.Status != "not evaluated" {
		t.Errorf("Status = %q, want not evaluated", c.Status)
	}
	if strings.Contains(c.Status, "owner only") {
		t.Errorf("Status = %q reads as a pass on a platform this cannot answer for", c.Status)
	}
}

func TestStateDirCheck_AbsentRootIsNotEvaluated(t *testing.T) {
	c := stateDirCheckOn(filepath.Join(t.TempDir(), "never-created"), "darwin")
	if c.Status != "not evaluated" {
		t.Errorf("Status = %q, want not evaluated", c.Status)
	}
	if !strings.Contains(c.Detail, "does not exist") {
		t.Errorf("Detail %q does not say why", c.Detail)
	}
}

func TestStateDirCheck_EmptyRootIsNotEvaluated(t *testing.T) {
	if c := stateDirCheckOn("", "darwin"); c.Status != "not evaluated" {
		t.Errorf("Status = %q, want not evaluated", c.Status)
	}
}

// A root that is a file is a misconfiguration, and unchecked is the honest
// answer rather than a clean one.
func TestStateDirCheck_ARootThatIsAFileIsNotEvaluated(t *testing.T) {
	f := filepath.Join(t.TempDir(), "hydra")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	c := stateDirCheckOn(f, "darwin")
	if c.Status != "not evaluated" {
		t.Errorf("Status = %q, want not evaluated", c.Status)
	}
}

// Writable and readable together: writable is the one that gets reported, since
// it is strictly worse and the count must not double.
func TestStateDirCheck_WritableTakesPrecedenceOverReadable(t *testing.T) {
	root := dirAt(t, filepath.Join(t.TempDir(), "hydra"), 0o755)
	dirAt(t, filepath.Join(root, "logs"), 0o777)

	c := stateDirCheckOn(root, "darwin")
	if c.Status != "1 writable by others" {
		t.Errorf("Status = %q, want the writable one to lead and count once", c.Status)
	}
}
