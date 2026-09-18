// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/swarm"
	"github.com/ankit373/hydra/internal/trust"
)

// sprtRun is a finished ensemble: two heads backed the answer, one dissented.
func sprtRun(runID, taskID string) *swarm.SPRTResult {
	return &swarm.SPRTResult{
		Domain: "go",
		Prompt: "p",
		Target: 0.95,
		Trust: &trust.Result{
			Candidate:  "A",
			Confidence: 0.82,
			Ledger: []trust.Evidence{
				{Source: "ollama/qwen3:4b", Agreed: true, Candidate: "A"},
				{Source: "claude", Agreed: true, Candidate: "A"},
				{Source: "ollama/phi4:14b", Agreed: false, Candidate: "A"},
			},
		},
	}
}

// logSPRTSpan writes the span a confidence run leaves behind, so AppendScore
// has something real to attach to.
func logSPRTSpan(t *testing.T, runID, taskID string, conf float64) {
	t.Helper()
	rl := runlog.New(runID)
	if err := rl.Append(runlog.Event{
		Kind: runlog.KindTaskFinished, TaskID: taskID,
		SpanID: swarm.SPRTSpanID(taskID), Agent: "ensemble", Confidence: conf,
	}); err != nil {
		t.Fatal(err)
	}
}

// No verifier is not a pass. Recording one would be worse than recording
// nothing, since it would train calibration on an unchecked answer.
func TestVerifyAndScoreRun_UnconfiguredRecordsNothing(t *testing.T) {
	cliSandbox(t)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })

	const runID, taskID = "r2", "t2"
	logSPRTSpan(t, runID, taskID, 0.9)
	verifyAndScoreRun(context.Background(), sprtRun(runID, taskID), runID, taskID, "x.rs", "rust")

	events, err := runlog.Load(runID)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(runlog.Scores(events, swarm.SPRTSpanID(taskID))); n != 0 {
		t.Errorf("recorded %d score(s) with no verifier configured", n)
	}
	cal, err := trust.New(trust.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	if n := len(cal.Report()); n != 0 {
		t.Errorf("trained %d cell(s) with nothing to judge the answer", n)
	}
}

// A verifier that cannot launch is not a failing answer. Recording a failure
// would blame the heads for a broken toolchain.
func TestVerifyAndScoreRun_UnrunnableVerifierRecordsNothing(t *testing.T) {
	cliSandbox(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	t.Setenv("PATH", t.TempDir()) // no `go` on PATH at all

	const runID, taskID = "r3", "t3"
	logSPRTSpan(t, runID, taskID, 0.9)
	verifyAndScoreRun(context.Background(), sprtRun(runID, taskID), runID, taskID, "x.go", "go")

	events, err := runlog.Load(runID)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(runlog.Scores(events, swarm.SPRTSpanID(taskID))); n != 0 {
		t.Errorf("recorded %d score(s) when the verifier could not run", n)
	}
	cal, err := trust.New(trust.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	if n := len(cal.Report()); n != 0 {
		t.Errorf("trained %d cell(s) from a verifier that never ran", n)
	}
}

// The flag has to be discoverable, and must not render as if it took a value.
func TestCLI_DispatchVerifyFlagIsABoolean(t *testing.T) {
	cliSandbox(t)
	_, help, err := run(t, "dispatch", "--help")
	if err != nil {
		t.Fatal(err)
	}
	var line string
	for _, l := range strings.Split(help, "\n") {
		if strings.Contains(l, "--verify") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatalf("--verify is not in dispatch --help:\n%s", help)
	}
	// Cobra reads backticks in a usage string as the argument placeholder, so a
	// stray pair renders a bool flag as though it took one. A bool is padded
	// with several spaces; a placeholder sits one space after the name.
	rest := line[strings.Index(line, "--verify")+len("--verify"):]
	if !strings.HasPrefix(rest, "  ") {
		t.Errorf("--verify renders with an argument placeholder:\n%s", line)
	}
}
