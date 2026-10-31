// SPDX-License-Identifier: MIT

//go:build !windows

package parallel

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A validator killed mid-run exits non-zero exactly like a validator that
// rejected the edit. Reporting the first as the second tells the operator the
// model wrote bad code when the run was simply interrupted, the same
// distinction the dispatch above it already draws for a deadline (#424).
func TestEdit_CancelledValidatorIsNotAValidationFailure(t *testing.T) {
	repo := editSandbox(t, marked("package main\n\nfunc main() {}\n"))
	tmpl, pidFile := hangingValidator(t)
	writeWorkspaceYAML(t, repo, tmpl)

	file := filepath.Join(repo, "a.go")
	original := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(file, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	// The pid file is the synchronisation point, not a timer: the validator
	// records its child before blocking, so cancelling on that is deterministic
	// however long the head above it took.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan []Result, 1)
	go func() {
		results, _ := Run(ctx, []Task{{Label: "cancel", Enum: "MODERATE", File: file, Prompt: "x"}}, Options{})
		done <- results
	}()

	helper := readHelperPID(t, pidFile)
	cancel()

	var results []Result
	select {
	case results = <-done:
	case <-time.After(cancelBudget):
		_ = syscall.Kill(helper, syscall.SIGKILL)
		t.Fatalf("the batch did not return within %v of cancellation: the kill never reached the validator's process group", cancelBudget)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	var got EditResult
	if err := json.Unmarshal(results[0].raw, &got); err != nil {
		t.Fatalf("result is not an EditResult: %v\n%s", err, results[0].raw)
	}

	if got.Error == "validation_failed" {
		t.Fatalf("a cancelled validator was reported as a rejected edit: %+v", got)
	}
	if !strings.Contains(got.Error, "validator_cancelled") {
		t.Fatalf("Error = %q, want it to name the cancellation", got.Error)
	}
	if !got.RolledBack {
		t.Error("RolledBack = false; an unvalidated edit was left on disk")
	}
	if raw, _ := os.ReadFile(file); string(raw) != original {
		t.Errorf("the file was left in its unvalidated state: %q", raw)
	}
	if !processGone(helper, 5*time.Second) {
		_ = syscall.Kill(helper, syscall.SIGKILL) // never leak it past the test
		t.Errorf("helper %d outlived the validator that spawned it", helper)
	}
}

// The unit contract the reporting above rests on. Both cancellation shapes are
// covered because the caller maps them to different answers: a deadline is
// max_wall_seconds refusing, a cancel is the operator interrupting.
func TestRunValidate_CancellationIsNotAnExitCode(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		tmpl, pidFile := hangingValidator(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		type result struct {
			rc  int
			err error
		}
		done := make(chan result, 1)
		go func() {
			rc, err := runValidate(ctx, tmpl, "ignored")
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
	})

	// Only the mapping here: the kill is the same code path the cancel case
	// above already pins down, and reading the child's pid after the deadline
	// has killed it races a validator that may not have started yet.
	t.Run("deadline", func(t *testing.T) {
		tmpl, _ := hangingValidator(t)
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()

		rc, err := runValidate(ctx, tmpl, "ignored")
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want context.DeadlineExceeded; this is what the caller maps to max_wall_seconds_exceeded", err)
		}
		if rc != 0 {
			t.Errorf("rc = %d past the deadline, want 0", rc)
		}
	})
}

// cancelBudget bounds how long a cancelled validator may take to return. The
// process-group kill is immediate; without it Run blocks until the child the
// validator spawned closes the pipe, which is what this catches.
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
