// SPDX-License-Identifier: MIT

//go:build !windows

// These two need a fake verifier with a controllable exit code, which means an
// executable script. Windows has no portable equivalent, and a runtime t.Skip
// would spend one of the suite's forty skip slots to say so, hence the tag.
// Windows still runs the resolution tests in internal/verify and both refusal
// paths beside this file.

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ankit373/hydra/internal/reliability"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/swarm"
	"github.com/ankit373/hydra/internal/trust"
)

// goRepo makes the working directory look like a Go repo, so verify.Command
// resolves `go test ./...` and the run is judged by a command we control.
func goRepo(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A `go` on PATH that does whatever the test needs, so no real suite runs.
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
}

// The whole point: one command leaves a scored span AND a trained calibrator,
// with the dissenter recorded, which is the only thing that moves specificity.
func TestVerifyAndScoreRun_ClosesBothLoops(t *testing.T) {
	cliSandbox(t)
	goRepo(t, "exit 1") // the suite fails, so the answer was wrong

	const runID, taskID = "r1", "t1"
	logSPRTSpan(t, runID, taskID, 0.82)
	verifyAndScoreRun(context.Background(), sprtRun(runID, taskID), runID, taskID, "x.go", "go")

	// 1. The span carries the verdict, which is what trust reliability joins.
	events, err := runlog.Load(runID)
	if err != nil {
		t.Fatal(err)
	}
	scores := runlog.Scores(events, swarm.SPRTSpanID(taskID))
	if len(scores) != 1 {
		t.Fatalf("scores on the SPRT span = %d, want 1", len(scores))
	}
	if passed, known := runlog.Verdict(scores); passed || !known {
		t.Errorf("verdict = (%v, %v), want a known failure", passed, known)
	}

	// 2. The calibrator trained, dissenter included.
	cal, err := trust.New(trust.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	var dissenter, backer trust.Stat
	for _, s := range cal.Report() {
		switch s.Source {
		case "ollama/phi4:14b":
			dissenter = s
		case "claude":
			backer = s
		}
	}
	if dissenter.Neg == 0 {
		t.Error("the dissenter recorded no negative verdict, so specificity is still unmeasured")
	}
	if dissenter.Sp <= 0.5 {
		t.Errorf("sp(dissenter) = %v, want above the prior: it flagged an answer the suite rejected", dissenter.Sp)
	}
	if backer.Sp >= 0.5 {
		t.Errorf("sp(backer) = %v, want below the prior after a false positive", backer.Sp)
	}
	if dissenter.Domain != "go" {
		t.Errorf("trained domain = %q, want the run's own domain", dissenter.Domain)
	}
}

// A passing suite is the other polarity, and the pair has to reach the
// reliability report as a real observation.
func TestVerifyAndScoreRun_FeedsTheReliabilityReport(t *testing.T) {
	cliSandbox(t)
	goRepo(t, "exit 0")

	for i := 0; i < reliability.MinObservations; i++ {
		runID := "run" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		taskID := "task" + runID
		logSPRTSpan(t, runID, taskID, 0.9)
		verifyAndScoreRun(context.Background(), sprtRun(runID, taskID), runID, taskID, "x.go", "go")
	}

	obs, unscored, err := reliabilityObservations()
	if err != nil {
		t.Fatal(err)
	}
	if unscored != 0 {
		t.Errorf("%d runs left unjudged; --verify should have scored every one", unscored)
	}
	if len(obs) != reliability.MinObservations {
		t.Fatalf("collected %d observations, want %d", len(obs), reliability.MinObservations)
	}
	rep, err := reliability.Evaluate(obs, 10)
	if err != nil {
		t.Fatalf("the report still refuses after %d verified runs: %v", len(obs), err)
	}
	if rep.BaseRate != 1 {
		t.Errorf("base rate = %v, want 1: every suite passed", rep.BaseRate)
	}
}
