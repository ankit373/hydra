// SPDX-License-Identifier: MIT

//go:build !windows

package editor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// An edit acts on the file the scope check authorised. os.Rename replaces a
// symlink rather than following it, so editing one deleted the link, wrote a
// copy of its target under the link's name and left the named file untouched,
// all reported as a clean success (#1023). Tagged rather than skipped: creating
// a symlink on Windows needs a privilege a runner does not hold.

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

	res, err := Edit(context.Background(), Request{
		File: link, Enum: "MODERATE", Prompt: "add greet", Validate: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "ok" {
		t.Fatalf("result = %+v, want ok", res)
	}

	raw, rerr := os.ReadFile(target)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(raw) == original {
		t.Error("the file the symlink names was not edited, so the work went nowhere")
	}

	// The link itself must survive: replacing it leaves a duplicate of its
	// target under its name, which git records as a type change.
	info, lerr := os.Lstat(link)
	if lerr != nil {
		t.Fatal(lerr)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("the symlink was replaced by a regular file")
	}

	// And the result must name what was written, not what was typed, or the
	// caller is told the edit landed somewhere it did not.
	got, err := filepath.EvalSymlinks(res.File)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("result names %q, want the file actually written %q", res.File, target)
	}
}

func TestEdit_ARolledBackEditLeavesTheSymlinkAlone(t *testing.T) {
	repo := editSandbox(t, marked("package main\n\nfunc broken( {"))

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
	fakeOnPath(t, "gofmt", "#!/bin/sh\nexit 2\n")

	res, err := Edit(context.Background(), Request{
		File: link, Enum: "MODERATE", Prompt: "x", Validate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "fail" || !res.RolledBack {
		t.Fatalf("result = %+v, want a rolled-back failure", res)
	}
	// A refused edit must leave the tree exactly as it found it. Writing to the
	// link instead of its target destroys the link on the way in, and the
	// rollback restores content, not the link.
	info, lerr := os.Lstat(link)
	if lerr != nil {
		t.Fatal(lerr)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("a rejected edit left the symlink replaced by a regular file")
	}
	if raw, _ := os.ReadFile(target); string(raw) != original {
		t.Errorf("the file the symlink names was left edited: %q", raw)
	}
}
