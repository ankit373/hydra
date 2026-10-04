// SPDX-License-Identifier: MIT

package editor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// These cover the code that mutates the user's files. A bug here does not
// produce a wrong answer, it destroys work, or quietly changes a file's
// permissions on the way past.

func writeFileMode(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	// WriteFile only applies mode on create; force it so the test's premise holds.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func modeOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

// The bug: os.CreateTemp makes the temp file 0600 and the rename carries that
// mode onto the target, so every edit silently reset the file it touched.
// Editing a shell script made it non-executable.
func TestAtomicWrite_PreservesTheExistingFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		// A FileMode here only toggles the read-only attribute, so there is no
		// executable bit to preserve. Asserted on the platforms where the mode
		// is real, rather than skipped silently.
		t.Log("windows: FileMode carries no executable bit; mode preservation is a no-op")
		return
	}
	dir := t.TempDir()

	for _, mode := range []os.FileMode{0o755, 0o600, 0o644, 0o700} {
		path := filepath.Join(dir, "script")
		writeFileMode(t, path, "#!/bin/sh\necho old\n", mode)

		if err := atomicWrite(path, "#!/bin/sh\necho new\n"); err != nil {
			t.Fatal(err)
		}
		if got := modeOf(t, path); got != mode {
			t.Errorf("mode %v became %v after an edit, an executable script would "+
				"stop being executable", mode, got)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "echo new") {
			t.Errorf("content was not replaced: %q", body)
		}
		_ = os.Remove(path)
	}
}

func TestAtomicWrite_CreatesANewFileAt0644(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Log("windows: FileMode is not meaningful for a new file here")
		return
	}
	path := filepath.Join(t.TempDir(), "new.go")
	if err := atomicWrite(path, "package x\n"); err != nil {
		t.Fatal(err)
	}
	if got := modeOf(t, path); got != 0o644 {
		t.Errorf("new file mode = %v, want 0644", got)
	}
}

// The whole point of the temp-file dance: a failure must never leave a
// half-written source file, and must never leave litter behind.
func TestAtomicWrite_LeavesNoTempFilesBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.go")

	if err := atomicWrite(path, "package x\n"); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".hydra-tmp") {
			t.Errorf("temp file %s was left behind", e.Name())
		}
	}
	if len(entries) != 1 {
		t.Errorf("directory holds %d entries, want just the written file", len(entries))
	}
}

// An unwritable directory must be an error, not a silent no-op that reports
// success while the file on disk is unchanged.
func TestAtomicWrite_UnwritableDirectoryIsAnError(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Log("skipping: directory permissions are not enforced for this user/platform")
		return
	}
	dir := t.TempDir()
	sub := filepath.Join(dir, "ro")
	if err := os.Mkdir(sub, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o700) })

	if err := atomicWrite(filepath.Join(sub, "f.go"), "package x\n"); err == nil {
		t.Error("writing into a read-only directory reported success")
	}
}

// The backup holds a verbatim copy of the user's source until the edit is
// approved. It was created 0644, the same defect #273 fixed for runlog's edit
// snapshots, still present here.
func TestBackup_IsNotWorldReadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Log("windows: mode bits carry no access information; the profile ACL protects these")
		return
	}
	dir := t.TempDir()
	backup := filepath.Join(dir, "f.go.hydra-bak")

	// Mirrors what Edit does when it takes a baseline.
	if err := os.WriteFile(backup, []byte("secret source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := modeOf(t, backup); got&0o077 != 0 {
		t.Errorf(".hydra-bak mode %v is group/other readable; it is a verbatim copy "+
			"of the user's source", got)
	}
}

// ── rollback ─────────────────────────────────────────────────────────────────

// A file that did not exist before the edit must be removed, not left behind as
// an empty or partial artifact.
func TestRollback_RemovesAFileThatDidNotExistBefore(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "new.go")
	if err := os.WriteFile(file, []byte("generated"), 0o644); err != nil {
		t.Fatal(err)
	}

	rollback(file, "", false)

	if fileExists(file) {
		t.Error("a file created by the edit survived rollback")
	}
}

// This edit's own snapshot is the source of truth, never the .hydra-bak beside
// it. The backup is written on the FIRST edit only, so once a second edit has
// been accepted it describes the file from before the first, and restoring it
// would throw the accepted one away. It belongs to internal/review, which wants
// exactly that older baseline to diff against (#1150).
func TestRollback_PrefersThisEditsSnapshotOverAStaleBackup(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.go")
	backup := file + ".hydra-bak"

	if err := os.WriteFile(file, []byte("BROKEN"), 0o644); err != nil {
		t.Fatal(err)
	}
	// What the file looked like before the FIRST edit, two edits ago.
	if err := os.WriteFile(backup, []byte("TWO EDITS AGO"), 0o600); err != nil {
		t.Fatal(err)
	}

	rollback(file, "ACCEPTED BY THE PREVIOUS EDIT", true)

	got, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "ACCEPTED BY THE PREVIOUS EDIT" {
		t.Errorf("file = %q after rollback, want this edit's snapshot; a stale "+
			"backup discards every edit accepted since it was written", got)
	}
	if !fileExists(backup) {
		t.Error("rollback consumed internal/review's diff baseline")
	}
}

// The snapshot is the only source, so this is the path, not a last resort.
func TestRollback_RestoresTheInMemoryOriginal(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.go")
	if err := os.WriteFile(file, []byte("BROKEN"), 0o644); err != nil {
		t.Fatal(err)
	}

	rollback(file, "ORIGINAL", true)

	got, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "ORIGINAL" {
		t.Errorf("file = %q, want the in-memory original restored", got)
	}
}

// ── marker extraction ────────────────────────────────────────────────────────

// A model's output is untrusted text. Extraction must never return a fragment
// that would then be written over the user's file.
func TestExtractContent_MarkerHandling(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"both markers", markerStart + "\nhello\n" + markerEnd, "hello"},
		{"with surrounding prose", "sure!\n" + markerStart + "\nhello\n" + markerEnd + "\ndone", "hello"},
		{"empty body", markerStart + "\n" + markerEnd, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := strings.TrimSpace(extractContent(tc.in)); got != tc.want {
				t.Errorf("extractContent(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The validator template is split around {file} so a path containing spaces is
// passed as one argument, splitting on whitespace would fragment it and the
// validator would check the wrong (or no) file.
func TestRunValidatorCmd_PathWithSpacesStaysOneArgument(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Log("windows: no /bin/sh to exercise this against")
		return
	}
	dir := t.TempDir()
	spaced := filepath.Join(dir, "a file with spaces.txt")
	if err := os.WriteFile(spaced, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	// `test -f {file}` exits 0 only if the path arrived intact.
	out, code, err := runValidatorCmd(context.Background(), "test -f {file}", spaced)
	if code != 0 || err != nil {
		t.Errorf("validator exit %d (%s, err %v), the path was fragmented into separate args", code, out, err)
	}
	out, code, err = runValidatorCmd(context.Background(), "test -f {file}", filepath.Join(dir, "does not exist.txt"))
	if code == 0 || err != nil {
		t.Errorf("validator passed for a missing file (%s, err %v)", out, err)
	}
}

func TestRunValidatorCmd_EmptyTemplateIsANoOp(t *testing.T) {
	if out, code, err := runValidatorCmd(context.Background(), "", "/tmp/x"); code != 0 || out != "" || err != nil {
		t.Errorf("empty template gave (%q, %d, %v), want a clean no-op", out, code, err)
	}
}

// The defect this contract exists for. `git checkout -- <file>` restores HEAD,
// which is a different question from "what did this edit overwrite": a
// developer's uncommitted work in that file was destroyed and the edit still
// reported "rolled_back": true. The correct bytes were a parameter the whole
// time (#1150).
func TestRollback_DoesNotDiscardUncommittedWork(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", repo}, args...)...)
		c.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	git("init", "-q")
	git("config", "core.autocrlf", "false")

	file := filepath.Join(repo, "a.go")
	if err := os.WriteFile(file, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "a.go")
	git("commit", "-qm", "init")

	// What the developer had on disk when the edit began: committed content
	// plus work they had not committed yet.
	precious := "package main\n\nfunc NotCommittedYet() string { return \"hours of work\" }\n"
	if err := os.WriteFile(file, []byte(precious), 0o644); err != nil {
		t.Fatal(err)
	}

	// The edit overwrites it, then fails validation and rolls back.
	if err := os.WriteFile(file, []byte("this does not compile\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rollback(file, precious, true)

	got, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != precious {
		t.Errorf("rollback destroyed uncommitted work.\n got: %q\nwant: %q", got, precious)
	}
}
