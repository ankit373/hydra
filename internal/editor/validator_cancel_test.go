// SPDX-License-Identifier: MIT

//go:build !windows

package editor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/trust"
)

// A validator's exit code is ground truth about the head, and #738 wired
// Ctrl+C through to the subprocess, so a killed validator now exits non-zero
// exactly like a rejecting one. Recording that would teach the calibrator that
// an interrupted run is a head writing broken code, which is a confident lie
// about a model, written from a keystroke.
func TestEdit_CancelledValidatorIsNotRecordedAsAFailedValidation(t *testing.T) {
	repo := editSandbox(t, marked("package main\n\nfunc main() {}\n"))
	tmpl, pidFile := hangingValidator(t)
	writeWorkspaceYAML(t, repo, tmpl)

	file := filepath.Join(repo, "a.go")
	original := "package main\n"
	if err := os.WriteFile(file, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	// The pid file is the synchronisation point, not a timer: the validator
	// records its child before blocking, so cancelling on that is deterministic
	// however long the head above it took.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type outcome struct {
		res *Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := Edit(ctx, Request{File: file, Enum: "MODERATE", Prompt: "x", Validate: true})
		done <- outcome{res, err}
	}()

	helper := readHelperPID(t, pidFile)
	cancel()

	var got outcome
	select {
	case got = <-done:
	case <-time.After(cancelBudget):
		_ = syscall.Kill(helper, syscall.SIGKILL)
		t.Fatalf("the edit did not return within %v of cancellation: the kill never reached the validator's process group", cancelBudget)
	}

	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("err = %v (result %+v), want context.Canceled: an interrupted edit is not a verdict", got.err, got.res)
	}
	if raw, _ := os.ReadFile(file); string(raw) != original {
		t.Errorf("the file was left in its unvalidated state: %q", raw)
	}

	cal, err := trust.New(trust.DefaultPath())
	if err != nil {
		return // nothing was written at all, which is the point
	}
	// Any domain, not just "go": what must not happen is an observation about
	// this head at all, and pinning the domain would let a rename in
	// trust.DomainForFile quietly make this assertion vacuous.
	for _, s := range cal.Report() {
		if s.Source == "cody" {
			t.Errorf("a cancelled validator was recorded against the head: %+v", s)
		}
	}

	if !processGone(helper, 5*time.Second) {
		_ = syscall.Kill(helper, syscall.SIGKILL) // never leak it past the test
		t.Errorf("helper %d outlived the validator that spawned it", helper)
	}
}

// The unit contract the calibration guard rests on: cancellation is an error,
// never an exit code, and the kill reaches the validator's children.
func TestRunValidatorCmd_CancellationIsNotAnExitCode(t *testing.T) {
	tmpl, pidFile := hangingValidator(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type result struct {
		rc  int
		err error
	}
	done := make(chan result, 1)
	go func() {
		_, rc, err := runValidatorCmd(ctx, tmpl, "ignored")
		done <- result{rc, err}
	}()

	helper := readHelperPID(t, pidFile)
	cancel()

	var got result
	select {
	case got = <-done:
	case <-time.After(cancelBudget):
		_ = syscall.Kill(helper, syscall.SIGKILL)
		t.Fatalf("the validator did not return within %v of cancellation: the kill never reached its process group", cancelBudget)
	}
	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled; a killed validator must not read as a verdict", got.err)
	}
	if got.rc != 0 {
		t.Errorf("rc = %d alongside a cancellation, want 0: a non-zero code here is a rejection the validator never made", got.rc)
	}
	if !processGone(helper, 5*time.Second) {
		_ = syscall.Kill(helper, syscall.SIGKILL)
		t.Errorf("helper %d outlived the cancelled validator", helper)
	}
}

// cancelBudget bounds how long a cancelled validator may take to return. The
// process-group kill is immediate; without it CombinedOutput blocks until the
// child the validator spawned closes the pipe, which is what this catches.
const cancelBudget = 10 * time.Second

// hangingValidator is a validator that records a child's pid and then blocks
// for far longer than any test will wait, so "the child died" cannot be
// confused with "the child finished".
//
// A script rather than an inline `sh -c`: the template is split on whitespace,
// which would fragment a shell one-liner. Absolute paths and builtins only,
// because the test sandbox strips PATH to its own bin directory and dash will
// not resolve a bare `sleep` there (#738).
func hangingValidator(t *testing.T) (template, pidFile string) {
	t.Helper()
	dir := t.TempDir()
	pidFile = filepath.Join(dir, "helper.pid")
	script := filepath.Join(dir, "validate.sh")
	body := "#!/bin/sh\n/bin/sleep 300 &\necho $! > " + pidFile + "\n/bin/sleep 300\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script + " {file}", pidFile
}

// readHelperPID waits for the validator to record its child, so a test never
// races the process it is about to cancel.
func readHelperPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the validator never ran: nothing recorded a pid to %s", path)
	return 0
}

// processGone reports whether the pid has left the table. Signal 0 checks for
// existence without delivering anything.
func processGone(pid int, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) == syscall.ESRCH {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}
