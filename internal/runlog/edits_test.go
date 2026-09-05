// SPDX-License-Identifier: MIT

package runlog

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func editSandbox(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func TestSaveLoadEdit_RoundTrips(t *testing.T) {
	editSandbox(t)

	before := []byte("package main\n\nfunc main() {}\n")
	after := []byte("package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"hi\") }\n")
	if err := SaveEdit("run-1", "001", before, after); err != nil {
		t.Fatal(err)
	}

	gotBefore, gotAfter, err := LoadEdit("run-1", "001")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotBefore, before) {
		t.Errorf("before = %q, want %q", gotBefore, before)
	}
	if !bytes.Equal(gotAfter, after) {
		t.Errorf("after = %q, want %q", gotAfter, after)
	}
}

// A ref comes from event data, and a run log can be written by anything sharing
// the run id. It must never be able to address a file outside the run.
func TestSaveEdit_RejectsTraversalRefs(t *testing.T) {
	editSandbox(t)

	for _, ref := range []string{"../escape", "a/b", `a\b`, "..", "", "sub/../../x"} {
		t.Run(ref, func(t *testing.T) {
			if err := SaveEdit("run-1", ref, []byte("x"), []byte("y")); err == nil {
				t.Errorf("SaveEdit accepted ref %q", ref)
			}
			if _, _, err := LoadEdit("run-1", ref); err == nil {
				t.Errorf("LoadEdit accepted ref %q", ref)
			}
		})
	}
}

// The store must not be able to fill a disk because a model was pointed at a
// huge file. Losing the event would hide that a change happened, so only the
// content is refused.
func TestSaveEdit_RefusesOversizedContent(t *testing.T) {
	editSandbox(t)

	big := bytes.Repeat([]byte("x"), MaxSnapshotBytes+1)
	err := SaveEdit("run-1", "001", []byte("small"), big)
	if !errors.Is(err, ErrSnapshotTooLarge) {
		t.Fatalf("err = %v, want ErrSnapshotTooLarge", err)
	}
	// Nothing partial left behind that a reader would mistake for a real snapshot.
	if _, _, err := LoadEdit("run-1", "001"); !errors.Is(err, ErrNoSnapshot) {
		t.Errorf("after a refused save, LoadEdit err = %v, want ErrNoSnapshot", err)
	}
}

// Exactly at the cap must still store, an off-by-one here silently drops
// legitimate snapshots.
func TestSaveEdit_AcceptsExactlyTheCap(t *testing.T) {
	editSandbox(t)

	atCap := bytes.Repeat([]byte("x"), MaxSnapshotBytes)
	if err := SaveEdit("run-1", "001", nil, atCap); err != nil {
		t.Fatalf("content exactly at the cap was refused: %v", err)
	}
}

// A pruned or never-written ref is not a read failure. The view says "diff
// unavailable" for one and surfaces an error for the other.
func TestLoadEdit_MissingIsItsOwnError(t *testing.T) {
	editSandbox(t)

	_, _, err := LoadEdit("run-1", "999")
	if !errors.Is(err, ErrNoSnapshot) {
		t.Errorf("err = %v, want ErrNoSnapshot", err)
	}
}

// A half-written pair, after was lost, must not read as an empty file, which
// would render as "everything deleted".
func TestLoadEdit_PartialPairIsMissing(t *testing.T) {
	home := editSandbox(t)

	if err := SaveEdit("run-1", "001", []byte("a"), []byte("b")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(home, ".hydra", "logs", "runs", "run-1.edits", "001.after")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadEdit("run-1", "001"); !errors.Is(err, ErrNoSnapshot) {
		t.Errorf("err = %v, want ErrNoSnapshot, a lost half must not read as an empty file", err)
	}
}

// Snapshots are verbatim copies of the user's source, so they must not be
// readable by other users.
//
// The mechanism differs by platform and the test follows it rather than
// skipping (#273). On Unix the guarantee is the 0600 mode SaveEdit sets. On
// Windows a FileMode only toggles the read-only attribute, 0600 becomes 0666,
// so the mode proves nothing there, and the actual protection is the ACL on the
// user's profile directory. Asserting containment under the home directory is
// what makes that guarantee non-vacuous: it is exactly the property that would
// break if snapshots ever moved to a shared or temp location.
func TestSaveEdit_SnapshotsAreNotWorldReadable(t *testing.T) {
	home := editSandbox(t)

	if err := SaveEdit("run-1", "001", []byte("secret"), []byte("secret2")); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".hydra", "logs", "runs", "run-1.edits")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("%d files, want 2", len(entries))
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS == "windows" {
			// Mode bits carry no access information here; assert the mechanism
			// that does. filepath.EvalSymlinks resolves the 8.3/symlinked temp
			// paths Windows hands out, which would otherwise defeat the compare.
			got, err := filepath.EvalSymlinks(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			want, err := filepath.EvalSymlinks(home)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(got, want) {
				t.Errorf("%s resolved to %s, outside the user profile %s; on Windows that ACL is "+
					"the only thing protecting a verbatim copy of the user's source", e.Name(), got, want)
			}
			continue
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s has mode %v; snapshots hold the user's source verbatim", e.Name(), info.Mode().Perm())
		}
	}
}

// Edits live beside the run's event log so retention is deleting a directory.
func TestEditsDir_IsBesideTheRunLog(t *testing.T) {
	editSandbox(t)

	if got, want := EditsDir("run-1"), Path("run-1"); !strings.HasPrefix(got, strings.TrimSuffix(want, ".jsonl")) {
		t.Errorf("EditsDir = %q, want it beside %q", got, want)
	}
}
