// SPDX-License-Identifier: MIT

package main

import (
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/evalset"
	"github.com/ankit373/hydra/internal/util"
)

// A checked dispatch is ground truth like an edit's validator: one example under
// its kind of work, visible in eval stats, and the head that answered is trained.
func TestDispatch_CheckFilesAnExampleUnderItsKind(t *testing.T) {
	dispatchable(t, "The total is 120 dollars.")

	prompt := "Summarise this invoice.\n\n" +
		util.WrapUntrusted("CONTEXT", "The invoice total is 120 dollars, due on 3 March.")
	if _, cobraOut, err := run(t, "dispatch", "--no-stream", "--tier", "6",
		"--kind", "summary", "--check", "ground", prompt); err != nil {
		t.Fatalf("dispatch failed: %v (%s)", err, cobraOut)
	}
	examples, err := evalset.Load(evalset.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(examples) != 1 {
		t.Fatalf("filed %d examples, want 1: %+v", len(examples), examples)
	}
	if e := examples[0]; e.Domain != "summary.general" || !e.Passed || e.Source != "verifier:grounding" || e.Head == "" {
		t.Errorf("example = %+v, want a passing grounding example under summary.general with its head", e)
	}

	stats, _, err := run(t, "eval", "stats")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stats, "summary.general") {
		t.Errorf("eval stats has no summary.general row:\n%s", stats)
	}
	cal, _, err := run(t, "trust", "calibration")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cal, "summary.general") {
		t.Errorf("the answering head was not trained under summary.general:\n%s", cal)
	}
}

// "Not checked" is not a verdict, so it files nothing.
func TestDispatch_NotCheckedFilesNothing(t *testing.T) {
	dispatchable(t, "The total is 120 dollars.")

	if _, cobraOut, err := run(t, "dispatch", "--no-stream", "--tier", "6",
		"--kind", "summary", "--check", "ground", "summarise the invoice"); err != nil {
		t.Fatalf("dispatch failed: %v (%s)", err, cobraOut)
	}
	examples, err := evalset.Load(evalset.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(examples) != 0 {
		t.Errorf("an unchecked answer filed %d examples: %+v", len(examples), examples)
	}
}
