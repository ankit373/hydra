// SPDX-License-Identifier: MIT

package main

import (
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/evalset"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/util"
)

// An answer that invents a figure the source never gave is reported, with the
// span that produced it scored as a failure so `hyctl trace view` shows it.
func TestDispatch_CheckGround_FlagsAnInventedNumber(t *testing.T) {
	dispatchable(t, "The total is 450 dollars.")

	prompt := "Summarise this invoice.\n\n" +
		util.WrapUntrusted("CONTEXT", "The invoice total is 120 dollars, due on 3 March.")
	out, cobraOut, err := run(t, "dispatch", "--no-stream", "--tier", "6", "--check", "ground", prompt)
	if err != nil {
		t.Fatalf("dispatch failed: %v (%s)", err, cobraOut)
	}
	if !strings.Contains(out, "UNSUPPORTED") || !strings.Contains(out, "450") {
		t.Errorf("the invented number is not reported:\n%s", out)
	}

	runs, err := runlog.Runs()
	if err != nil || len(runs) == 0 {
		t.Fatalf("no run logged: %v", err)
	}
	events, err := runlog.Load(runs[0])
	if err != nil {
		t.Fatal(err)
	}
	var scored []runlog.Score
	for _, s := range runlog.ScoresBySpan(events) {
		scored = append(scored, s...)
	}
	if len(scored) != 1 || scored[0].Name != "grounding" || scored[0].Value != 0 {
		t.Errorf("span scores = %+v, want one failing grounding score", scored)
	}
	verdicts, err := evalset.VerdictBySpan(evalset.VerdictsPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(verdicts) != 1 {
		t.Errorf("verdict log = %v, want the one failing span", verdicts)
	}
	for _, passed := range verdicts {
		if passed {
			t.Error("the verdict log records the ungrounded answer as passing")
		}
	}
}

// No fenced source means nothing to check against, which is never a pass.
func TestDispatch_CheckGround_UnfencedPromptIsNotChecked(t *testing.T) {
	dispatchable(t, "The total is 450 dollars.")

	out, cobraOut, err := run(t, "dispatch", "--no-stream", "--tier", "6", "--check", "ground",
		"summarise the invoice")
	if err != nil {
		t.Fatalf("dispatch failed: %v (%s)", err, cobraOut)
	}
	if !strings.Contains(out, "NOT CHECKED") || strings.Contains(out, "GROUNDED") {
		t.Errorf("an unfenced prompt was not reported as not checked:\n%s", out)
	}
}

func TestDispatch_UnknownCheckRefusedBeforeRouting(t *testing.T) {
	cliSandbox(t)

	_, _, err := run(t, "dispatch", "--check", "tests", "q")
	if err == nil || !strings.Contains(err.Error(), "unknown --check") {
		t.Fatalf("err = %v, want the unknown-check refusal", err)
	}
}
