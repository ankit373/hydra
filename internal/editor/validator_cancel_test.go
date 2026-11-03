// SPDX-License-Identifier: MIT

//go:build !windows

package editor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/testutil"
	"github.com/ankit373/hydra/internal/trust"
)

// A validator's exit code is ground truth about the head, and #738 wired
// Ctrl+C through to the subprocess, so a killed validator now exits non-zero
// exactly like a rejecting one. Recording that would teach the calibrator that
// an interrupted run is a head writing broken code, which is a confident lie
// about a model, written from a keystroke.
func TestEdit_CancelledValidatorIsNotRecordedAsAFailedValidation(t *testing.T) {
	repo := editSandbox(t, marked("package main\n\nfunc main() {}\n"))
	tmpl, fifo := testutil.HangingValidator(t)
	writeWorkspaceYAML(t, repo, tmpl)
	stalled := testutil.HeadAnswered(t, "cody")

	file := filepath.Join(repo, "a.go")
	original := "package main\n"
	if err := os.WriteFile(file, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	// The fifo is the synchronisation point: the validator blocks handing over
	// its child's pid until the test reads it, however long the head took.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan editOutcome, 1)
	go func() {
		res, err := Edit(ctx, Request{File: file, Enum: "MODERATE", Prompt: "x", Validate: true})
		done <- editOutcome{res, err}
	}()

	helper := testutil.AwaitValidator(t, fifo, done, stalled)
	cancel()

	var got editOutcome
	select {
	case got = <-done:
	case <-time.After(testutil.CancelBudget):
		_ = syscall.Kill(helper, syscall.SIGKILL)
		const why = "the kill never reached the validator's process group"
		t.Fatalf("the edit did not return within %v of cancellation: %s", testutil.CancelBudget, why)
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

	if !testutil.ProcessGone(helper, 5*time.Second) {
		_ = syscall.Kill(helper, syscall.SIGKILL) // never leak it past the test
		t.Errorf("helper %d outlived the validator that spawned it", helper)
	}
}

// editOutcome prints the result behind its pointer, so an early return says what it was.
type editOutcome struct {
	res *Result
	err error
}

func (o editOutcome) String() string {
	return fmt.Sprintf("err=%v result=%+v", o.err, o.res)
}

// The unit contract the calibration guard rests on: cancellation is an error,
// never an exit code, and the kill reaches the validator's children.
func TestRunValidatorCmd_CancellationIsNotAnExitCode(t *testing.T) {
	tmpl, fifo := testutil.HangingValidator(t)
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
}
