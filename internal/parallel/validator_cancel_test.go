// SPDX-License-Identifier: MIT

//go:build !windows

package parallel

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/testutil"
)

// A validator killed mid-run exits non-zero exactly like a validator that
// rejected the edit. Reporting the first as the second tells the operator the
// model wrote bad code when the run was simply interrupted, the same
// distinction the dispatch above it already draws for a deadline (#424).
func TestEdit_CancelledValidatorIsNotAValidationFailure(t *testing.T) {
	repo := editSandbox(t, marked("package main\n\nfunc main() {}\n"))
	tmpl, fifo := testutil.HangingValidator(t)
	writeWorkspaceYAML(t, repo, tmpl)
	stalled := testutil.HeadAnswered(t, "cody")

	file := filepath.Join(repo, "a.go")
	original := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(file, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	// The fifo is the synchronisation point: the validator blocks handing over
	// its child's pid until the test reads it, however long the head took.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan batchOutcome, 1)
	go func() {
		results, _ := Run(ctx, []Task{{Label: "cancel", Enum: "MODERATE", File: file, Prompt: "x"}}, Options{})
		done <- results
	}()

	helper := testutil.AwaitValidator(t, fifo, done, stalled)
	cancel()

	var results batchOutcome
	select {
	case results = <-done:
	case <-time.After(testutil.CancelBudget):
		_ = syscall.Kill(helper, syscall.SIGKILL)
		const why = "the kill never reached the validator's process group"
		t.Fatalf("the batch did not return within %v of cancellation: %s", testutil.CancelBudget, why)
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
	if !testutil.ProcessGone(helper, 5*time.Second) {
		_ = syscall.Kill(helper, syscall.SIGKILL) // never leak it past the test
		t.Errorf("helper %d outlived the validator that spawned it", helper)
	}
}

// batchOutcome prints each result's JSON, which is what names why a batch ended.
type batchOutcome []Result

func (b batchOutcome) String() string {
	parts := make([]string, len(b))
	for i, r := range b {
		parts[i] = string(r.raw)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// The unit contract the reporting above rests on. Both cancellation shapes are
// covered because the caller maps them to different answers: a deadline is
// max_wall_seconds refusing, a cancel is the operator interrupting.
func TestRunValidate_CancellationIsNotAnExitCode(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		tmpl, fifo := testutil.HangingValidator(t)
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

		helper := testutil.AwaitValidator(t, fifo, done, nil)
		cancel()

		var got result
		select {
		case got = <-done:
		case <-time.After(testutil.CancelBudget):
			_ = syscall.Kill(helper, syscall.SIGKILL)
			const why = "the kill never reached its process group"
			t.Fatalf("the validator did not return within %v of cancellation: %s", testutil.CancelBudget, why)
		}
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled; a killed validator must not read as a verdict", got.err)
		}
		if got.rc != 0 {
			t.Errorf("rc = %d alongside a cancellation, want 0: a non-zero code here is a rejection the validator never made", got.rc)
		}
		if !testutil.ProcessGone(helper, 5*time.Second) {
			_ = syscall.Kill(helper, syscall.SIGKILL)
			t.Errorf("helper %d outlived the cancelled validator", helper)
		}
	})

	// Only the mapping here: the kill is the same code path the cancel case
	// above already pins down, and reading the child's pid after the deadline
	// has killed it races a validator that may not have started yet.
	t.Run("deadline", func(t *testing.T) {
		tmpl, _ := testutil.HangingValidator(t)
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
