// SPDX-License-Identifier: MIT

//go:build !windows

package parallel

import (
	"os"
	"path/filepath"
	"testing"
)

// `hyctl parallel` keeps its own edit mechanics, so it needs the same rule
// `hyctl edit` follows: act on the file the scope check authorised, because
// os.Rename replaces a symlink rather than following it (#1023). Tagged rather
// than skipped, creating a symlink on Windows needs a privilege a runner does
// not hold.
func TestEdit_WritesThroughASymlinkToTheFileItNames(t *testing.T) {
	repo := editSandbox(t, marked("package main\n\nfunc greet() string { return \"hello\" }"))

	if err := os.MkdirAll(filepath.Join(repo, "shared"), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(repo, "shared", "real.go")
	original := "package main\n"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(repo, "alias.go")
	if err := os.Symlink(filepath.Join("shared", "real.go"), link); err != nil {
		t.Fatal(err)
	}

	got := runEdit(t, Task{
		Label: "edit through a link", Enum: "MODERATE", File: link,
		Prompt: "add greet", Validate: boolPtr(false),
	})
	if got.Status != "ok" {
		t.Fatalf("status = %q, error %q", got.Status, got.Error)
	}

	if raw, _ := os.ReadFile(target); string(raw) == original {
		t.Error("the file the symlink names was not edited, so the work went nowhere")
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("the symlink was replaced by a regular file")
	}
	real, err := filepath.EvalSymlinks(got.File)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if real != want {
		t.Errorf("result names %q, want the file actually written %q", got.File, target)
	}
}
