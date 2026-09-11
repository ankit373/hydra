// SPDX-License-Identifier: MIT

package editor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// `hyctl edit` is the single-file command a person reaches for, and it
// consulted registry/policy.yaml nowhere at all, so every cap in that file
// applied to `hyctl parallel` and nothing else. An operator who set
// diff_size_cap_pct to stop an agent rewriting a file wholesale got that
// protection from the batch command and not from the one they were running
// (#769).

// writePolicyApply writes a policy.yaml whose single always-matching rule
// applies whatever fields the caller names, so a test can trip one cap
// without tripping the others.
func writePolicyApply(t *testing.T, apply string) {
	t.Helper()
	dir := filepath.Join(os.Getenv("HYDRA_HOME"), "registry")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := "version: \"1.0\"\nrules:\n  - name: test_caps\n    when:\n      always: true\n    apply:\n" + apply
	if err := os.WriteFile(filepath.Join(dir, "policy.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The cap exists to stop a wholesale rewrite, so the file has to come back.
func TestEdit_DiffSizeCapRollsBackAnOversizedEdit(t *testing.T) {
	original := "package main\n\nfunc a() {}\nfunc b() {}\nfunc c() {}\nfunc d() {}\n"
	repo := editSandbox(t, marked("package main\n\nfunc z() {}\n"))
	writePolicyApply(t, "      diff_size_cap_pct: 10\n")

	file := filepath.Join(repo, "main.go")
	if err := os.WriteFile(file, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Edit(context.Background(), Request{
		File: file, Enum: "MODERATE", Prompt: "replace everything",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "fail" {
		t.Fatalf("status = %q, want fail: the edit rewrote the file past a 10%% cap", res.Status)
	}
	// The same string `hyctl parallel` reports, so a refusal reads the same
	// whichever command hit it.
	if !strings.Contains(res.Error, "diff_size_cap_exceeded") {
		t.Errorf("error = %q, want diff_size_cap_exceeded", res.Error)
	}
	if !res.RolledBack {
		t.Error("RolledBack = false on a refused edit")
	}

	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != original {
		t.Errorf("file = %q, want the original restored: a cap that refuses but "+
			"leaves the rewrite on disk is worse than no cap", raw)
	}
}

// A generous cap must not refuse a legitimate edit, or the fix trades one
// broken command for another.
func TestEdit_DiffSizeCapAllowsAnOrdinaryEdit(t *testing.T) {
	repo := editSandbox(t, marked("package main\n\nfunc main() {}\n"))
	writePolicyApply(t, "      diff_size_cap_pct: 900\n")

	file := filepath.Join(repo, "main.go")
	if err := os.WriteFile(file, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Edit(context.Background(), Request{
		File: file, Enum: "MODERATE", Prompt: "add main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "ok" {
		t.Fatalf("status = %q, error %q: a 900%% cap should refuse nothing", res.Status, res.Error)
	}
}

// A new file has no percent of itself changed to measure. Creating one is a
// 100% change by construction, so a cap below that would make `hyctl edit`
// unable to create files at all.
func TestEdit_DiffSizeCapDoesNotApplyToANewFile(t *testing.T) {
	repo := editSandbox(t, marked("package main\n\nfunc main() {}\n"))
	writePolicyApply(t, "      diff_size_cap_pct: 1\n")

	res, err := Edit(context.Background(), Request{
		File: filepath.Join(repo, "created.go"), Enum: "MODERATE", Prompt: "create it",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "ok" {
		t.Fatalf("status = %q, error %q: a 1%% cap blocked a file creation", res.Status, res.Error)
	}
}

// max_cost_usd is refused by dispatch before the head runs, so the file is
// never touched rather than touched and restored.
//
// The refusal string is asserted, not just the failure: with the ceiling
// unwired the edit ran and tripped the *default* 90% diff cap instead, so the
// status was "fail" and the file was back to its original either way. A test
// that only checked those two passed under the bug it exists to catch.
func TestEdit_CostCeilingRefusesBeforeTheFileIsTouched(t *testing.T) {
	// Long enough that the model's reply is a modest change, so the diff cap
	// cannot be what refuses and only the ceiling can.
	original := "package main\n\nfunc a() {}\nfunc b() {}\nfunc c() {}\n" +
		"func d() {}\nfunc e() {}\nfunc f() {}\nfunc g() {}\nfunc h() {}\n"
	repo := editSandbox(t, marked(original+"func i() {}\n"))
	// A ceiling no head can be under, so the refusal is the policy's and not
	// a pricing accident.
	writePolicyApply(t, "      max_cost_usd: 0.0000000001\n")

	file := filepath.Join(repo, "main.go")
	if err := os.WriteFile(file, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Edit(context.Background(), Request{
		File: file, Enum: "MODERATE", Prompt: "add one function",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "fail" {
		t.Fatalf("status = %q: a ceiling of $1e-10 refused nothing", res.Status)
	}
	if !strings.Contains(res.Error, "exceeds limit") {
		t.Fatalf("error = %q, want the cost ceiling; something else refused this "+
			"edit and the ceiling may still be unwired", res.Error)
	}
	// Nothing was written, so there was nothing to restore. A rolled-back
	// refusal is a different mechanism reaching the same file content.
	if res.RolledBack {
		t.Error("RolledBack = true: the head ran and was undone, but the ceiling " +
			"is meant to refuse it before it runs")
	}

	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != original {
		t.Errorf("file = %q, want it untouched", raw)
	}
}

// The wall-clock cap, fired by the policy's own number.
//
// Cancelling the context by hand proves nothing: the dispatch then fails on
// the cancellation whether or not max_wall_seconds was ever read, and that
// version of this test passed with fp.Deadline deleted. So the head is made
// slower than the budget and the refusal string is asserted.
//
// The margin is one-directional: a 5s head against a 1s budget cannot finish
// early, and a loaded machine that takes longer to start it only makes the
// deadline fire more surely.
func TestEdit_WallClockCapRefusesAndRestoresTheFile(t *testing.T) {
	original := "package main\n"
	repo := editSandbox(t, marked("package main\n\nfunc main() {}\n"))
	slowHead(t, 5)
	writePolicyApply(t, "      max_wall_seconds: 1\n")

	file := filepath.Join(repo, "main.go")
	if err := os.WriteFile(file, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Edit(context.Background(), Request{
		File: file, Enum: "MODERATE", Prompt: "add main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "fail" {
		t.Fatalf("status = %q: a 1s budget did not bound a 5s head", res.Status)
	}
	// The same string `hyctl parallel` reports. A deadline is the policy
	// refusing, not the head failing, and the two want different answers.
	if !strings.Contains(res.Error, "max_wall_seconds_exceeded") {
		t.Fatalf("error = %q, want max_wall_seconds_exceeded: the dispatch was "+
			"bounded by something other than the policy", res.Error)
	}

	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != original {
		t.Errorf("file = %q, want it untouched", raw)
	}
}

// slowHead replaces the sandbox's fake head with one that outlives a deadline.
// Found through PATH, which is where editSandbox put it.
func slowHead(t *testing.T, seconds int) {
	t.Helper()
	path, err := exec.LookPath("cody")
	if err != nil {
		t.Fatalf("the sandbox head is not on PATH: %v", err)
	}
	// Absolute paths: the sandbox empties PATH, so a bare `sleep` is not found
	// and the head exits 127 instead of outliving the budget.
	body := fmt.Sprintf("#!/bin/sh\n/bin/sleep %d\n", seconds)
	if runtime.GOOS == "windows" {
		body = fmt.Sprintf("@echo off\r\n%%SystemRoot%%\\System32\\ping.exe -n %d 127.0.0.1 > nul\r\n", seconds+1)
	}
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
}
