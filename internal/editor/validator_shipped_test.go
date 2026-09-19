// SPDX-License-Identifier: MIT

//go:build !windows

package editor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The shipped registry, not an override: every other validator test here writes
// its own workspace.yaml, so none of them could tell that `go` had no entry in
// the embedded one. The fake stands in for gofmt because CI's toolchain is not
// what is under test; what is under test is that the shipped template is the
// thing invoked, with the flag it declares, and that a non-zero exit rolls back
// (#998).
func TestEdit_AGoFileIsValidatedByTheShippedRegistry(t *testing.T) {
	repo := editSandbox(t, marked("package main\n\nfunc Broken( {\n"))
	args := filepath.Join(t.TempDir(), "args")
	fakeOnPath(t, "gofmt", "#!/bin/sh\nprintf '%s ' \"$@\" > "+args+"\nexit 2\n")

	file := filepath.Join(repo, "a.go")
	original := "package main\n"
	if err := os.WriteFile(file, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Edit(context.Background(), Request{
		File: file, Enum: "MODERATE", Prompt: "x", Validate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "fail" || !res.RolledBack {
		t.Fatalf("result = %+v, want a rolled-back failure: the shipped go validator did not run", res)
	}
	if res.ValidatorPassed == nil || *res.ValidatorPassed {
		t.Errorf("ValidatorPassed = %v, want a recorded false", res.ValidatorPassed)
	}
	if raw, _ := os.ReadFile(file); string(raw) != original {
		t.Errorf("the file was left in its failed state: %q", raw)
	}
	got, _ := os.ReadFile(args)
	if !strings.Contains(string(got), "-l") || !strings.Contains(string(got), file) {
		t.Errorf("gofmt was called with %q, want the shipped template's -l and the file", got)
	}
}

// fakeOnPath writes an executable into the sandbox's bin directory, which is
// the whole of $PATH there, so a validator template resolves to it.
func fakeOnPath(t *testing.T, name, script string) {
	t.Helper()
	path := filepath.Join(os.Getenv("PATH"), name)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
}
