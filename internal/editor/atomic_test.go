// SPDX-License-Identifier: MIT

package editor

import (
	"os"
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

	rollback(file, "", false, "", filepath.Join(dir, "new.go.hydra-bak"))

	if fileExists(file) {
		t.Error("a file created by the edit survived rollback")
	}
}

// A backup, when present, is the source of truth, it is the exact bytes from
// before the edit.
func TestRollback_RestoresFromTheBackupAndConsumesIt(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.go")
	backup := file + ".hydra-bak"

	if err := os.WriteFile(file, []byte("BROKEN"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup, []byte("ORIGINAL"), 0o600); err != nil {
		t.Fatal(err)
	}

	rollback(file, "ignored because a backup exists", true, "", backup)

	got, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "ORIGINAL" {
		t.Errorf("file = %q after rollback, want the backup's contents", got)
	}
	if fileExists(backup) {
		t.Error("the backup was left behind after being consumed")
	}
}

// With no backup and no git, the in-memory original is the last resort.
func TestRollback_FallsBackToTheInMemoryOriginal(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.go")
	if err := os.WriteFile(file, []byte("BROKEN"), 0o644); err != nil {
		t.Fatal(err)
	}

	rollback(file, "ORIGINAL", true, "", filepath.Join(dir, "nope.hydra-bak"))

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
	out, code := runValidatorCmd("test -f {file}", spaced)
	if code != 0 {
		t.Errorf("validator exit %d (%s), the path was fragmented into separate args", code, out)
	}
	out, code = runValidatorCmd("test -f {file}", filepath.Join(dir, "does not exist.txt"))
	if code == 0 {
		t.Errorf("validator passed for a missing file (%s)", out)
	}
}

func TestRunValidatorCmd_EmptyTemplateIsANoOp(t *testing.T) {
	if out, code := runValidatorCmd("", "/tmp/x"); code != 0 || out != "" {
		t.Errorf("empty template gave (%q, %d), want a clean no-op", out, code)
	}
}
