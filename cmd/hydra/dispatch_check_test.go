// SPDX-License-Identifier: MIT

package main

import (
	"runtime"
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
	if err == nil || !strings.Contains(err.Error(), "unknown check") {
		t.Fatalf("err = %v, want the unknown-check refusal", err)
	}
}

// spanScores is every score the newest run holds.
func spanScores(t *testing.T) []runlog.Score {
	t.Helper()
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
	return scored
}

func TestDispatch_CheckMatch_AcceptsAllowedLabel(t *testing.T) {
	dispatchable(t, "billing")

	out, cobraOut, err := run(t, "dispatch", "--no-stream", "--tier", "6",
		"--check", "match:labels="+labelsFile(t, "billing\nbug\nhow-to\n"), "label this ticket: I was charged twice")
	if err != nil {
		t.Fatalf("dispatch failed: %v (%s)", err, cobraOut)
	}
	if !strings.Contains(out, "PASS") || !strings.Contains(out, "verifier:match:t.labels") {
		t.Errorf("an allowed label did not pass:\n%s", out)
	}
	if s := spanScores(t); len(s) != 1 || s[0].Name != "oracle" || s[0].Value != 1 {
		t.Errorf("span scores = %+v, want one passing oracle score", s)
	}
}

func TestDispatch_CheckMatch_RefusesOther(t *testing.T) {
	dispatchable(t, "refund")

	out, cobraOut, err := run(t, "dispatch", "--no-stream", "--tier", "6",
		"--check", "match:labels="+labelsFile(t, "billing\nbug\nhow-to\n"), "label this ticket: I was charged twice")
	if err != nil {
		t.Fatalf("dispatch failed: %v (%s)", err, cobraOut)
	}
	if !strings.Contains(out, "FAIL") {
		t.Errorf("a label outside the set was not failed:\n%s", out)
	}
}

// The command sees the answer: it finds what the answer holds and not what it lacks.
func TestDispatch_CheckCmd_ReadsTheAnswer(t *testing.T) {
	s, _ := dispatchable(t, `{"invoice": "A-17", "total": 120}`)
	checker := "#!/bin/sh\ncase \"$(/bin/cat \"$2\")\" in *\"$1\"*) exit 0;; esac\nexit 1\n"
	if runtime.GOOS == "windows" {
		checker = "@echo off\r\n\"%SystemRoot%\\System32\\findstr.exe\" /c:%1 %2 >nul\r\nexit /b %errorlevel%\r\n"
	}
	s.FakeBinary(t, "contains", checker)

	out, cobraOut, err := run(t, "dispatch", "--no-stream", "--tier", "6",
		"--check", "cmd:contains total {file}", "--check", "cmd:contains due_date {file}",
		"pull the invoice number and total from this email as JSON")
	if err != nil {
		t.Fatalf("dispatch failed: %v (%s)", err, cobraOut)
	}
	if !strings.Contains(out, "PASS") || !strings.Contains(out, "FAIL") {
		t.Errorf("want one pass (total is there) and one fail (due_date is not):\n%s", out)
	}
}

// Refused before routing: a command that cannot see the answer is --verify again.
func TestDispatch_CheckCmd_WithoutPlaceholderRefused(t *testing.T) {
	cliSandbox(t)

	_, _, err := run(t, "dispatch", "--check", "cmd:go test ./...", "q")
	if err == nil || !strings.Contains(err.Error(), "cannot see the answer") {
		t.Fatalf("err = %v, want a command without {answer} or {file} refused", err)
	}
}
