// SPDX-License-Identifier: MIT

//go:build !windows

// These need a fake verifier with a controllable exit code, so an executable
// script, for the same reason dispatch_verify_unix_test.go is tagged.

package main

import (
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/ankit373/hydra/internal/evalset"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/swarm"
	"github.com/ankit373/hydra/internal/trust"
)

// sprtRunWith is a finished ensemble whose attempts are under the test's control,
// which is what head attribution reads.
func sprtRunWith(enum string, tier int, attempts []swarm.Attempt) *swarm.SPRTResult {
	return &swarm.SPRTResult{
		Domain: "go", Prompt: "p", Target: 0.95, Enum: enum, Tier: tier,
		Attempts: attempts,
		Trust: &trust.Result{
			Candidate:  "A",
			Confidence: 0.82,
			Ledger: []trust.Evidence{
				{Source: "ollama/qwen3:4b", Agreed: true, Candidate: "A"},
				{Source: "claude", Agreed: false, Candidate: "A"},
			},
		},
	}
}

func attempt(id, out string) swarm.Attempt {
	return swarm.Attempt{Head: provider.Head{ID: id}, Output: out}
}

func corpus(t *testing.T) []evalset.Example {
	t.Helper()
	all, err := evalset.Load(evalset.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	return all
}

// The corpus is the only thing the router can be improved against, and this is
// the command that produces ground truth. Before #969 it computed the verdict
// and filed nothing, so the store had exactly one writer and stayed empty.
func TestVerifyAndScoreRun_FilesTheVerifiedExample(t *testing.T) {
	cliSandbox(t)
	goRepo(t, "exit 0") // the suite passes, so the answer held up

	const runID, taskID = "rc1", "tc1"
	logSPRTSpan(t, runID, taskID, 0.82)
	res := sprtRunWith("SIMPLE", 8, []swarm.Attempt{
		attempt("ollama/qwen3:4b", "A"),
		attempt("claude", "B"),
	})
	verifyAndScoreRun(context.Background(), res, runID, taskID, "x.go", "go")

	all := corpus(t)
	if len(all) != 1 {
		t.Fatalf("corpus holds %d examples, want 1; --verify produced ground truth and dropped it", len(all))
	}
	e := all[0]
	if !e.Passed {
		t.Errorf("Passed = false, want true: the verifier exited 0")
	}
	if e.Candidate != "A" {
		t.Errorf("Candidate = %q, want the answer that was verified", e.Candidate)
	}
	// Enum and head are what Readiness gates on. An example missing either is
	// kept but can never make an enum fittable, so storing it is half the job.
	if e.Enum != "SIMPLE" || e.Tier != 8 {
		t.Errorf("routing decision = (%q, %d), want (SIMPLE, 8)", e.Enum, e.Tier)
	}
	if e.Head != "ollama/qwen3:4b" {
		t.Errorf("Head = %q, want the head whose output was verified", e.Head)
	}
	if e.Domain != "go" {
		t.Errorf("Domain = %q, want go", e.Domain)
	}
}

// A wrong answer with ground truth attached is as valuable as a right one: a
// corpus of only passes cannot tell any head from any other.
func TestVerifyAndScoreRun_FilesAFailedAnswerToo(t *testing.T) {
	cliSandbox(t)
	goRepo(t, "exit 1")

	const runID, taskID = "rc2", "tc2"
	logSPRTSpan(t, runID, taskID, 0.82)
	res := sprtRunWith("EXPERT", 2, []swarm.Attempt{attempt("claude", "A")})
	verifyAndScoreRun(context.Background(), res, runID, taskID, "x.go", "go")

	all := corpus(t)
	if len(all) != 1 {
		t.Fatalf("corpus holds %d examples, want 1", len(all))
	}
	if all[0].Passed {
		t.Errorf("Passed = true, want false: the verifier exited 1")
	}
}

// Two heads returning byte-identical text means neither is *the* author. Naming
// one would attribute a pass to a head on the strength of another's work, which
// is exactly the evidence Readiness must not accept.
func TestVerifyAndScoreRun_AgreedAnswerIsUnattributed(t *testing.T) {
	cliSandbox(t)
	goRepo(t, "exit 0")

	const runID, taskID = "rc3", "tc3"
	logSPRTSpan(t, runID, taskID, 0.9)
	res := sprtRunWith("SIMPLE", 8, []swarm.Attempt{
		attempt("ollama/qwen3:4b", "A"),
		attempt("claude", "A"),
	})
	verifyAndScoreRun(context.Background(), res, runID, taskID, "x.go", "go")

	all := corpus(t)
	if len(all) != 1 {
		t.Fatalf("corpus holds %d examples, want 1", len(all))
	}
	if all[0].Head != "" {
		t.Errorf("Head = %q, want empty: two heads produced that exact answer", all[0].Head)
	}
}

// Calibration runs after this and returns early on its own errors. Filing the
// example afterwards would let an unwritable calibrator silently cost ground truth.
func TestVerifyAndScoreRun_FilesTheExampleEvenWhenCalibrationCannotLoad(t *testing.T) {
	cliSandbox(t)
	goRepo(t, "exit 0")

	// A directory where the calibration file belongs: opening it as a file fails.
	if err := os.MkdirAll(trust.DefaultPath(), 0o700); err != nil {
		t.Fatal(err)
	}

	const runID, taskID = "rc4", "tc4"
	logSPRTSpan(t, runID, taskID, 0.9)
	res := sprtRunWith("SIMPLE", 8, []swarm.Attempt{attempt("claude", "A")})
	verifyAndScoreRun(context.Background(), res, runID, taskID, "x.go", "go")

	if n := len(corpus(t)); n != 1 {
		t.Fatalf("corpus holds %d examples, want 1: a broken calibrator must not cost the example", n)
	}
}

// No verifier is no verdict, so there is nothing to file. Recording an unchecked
// answer would put a guess in the one store that is supposed to be ground truth.
func TestVerifyAndScoreRun_UnverifiableFilesNothing(t *testing.T) {
	cliSandbox(t)
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })

	const runID, taskID = "rc5", "tc5"
	logSPRTSpan(t, runID, taskID, 0.9)
	res := sprtRunWith("SIMPLE", 8, []swarm.Attempt{attempt("claude", "A")})
	verifyAndScoreRun(context.Background(), res, runID, taskID, "x.rs", "rust")

	if n := len(corpus(t)); n != 0 {
		t.Fatalf("corpus holds %d examples, want 0: nothing verified that answer", n)
	}
}

// The prompt is the task. Two different questions that happen to get the same
// answer must stay two examples, which is only true if --verify actually passes
// the prompt through to the task hash (#973).
func TestVerifyAndScoreRun_DifferentPromptsWithOneAnswerStayTwoExamples(t *testing.T) {
	cliSandbox(t)
	goRepo(t, "exit 0")

	for i, prompt := range []string{"make Close idempotent", "make Flush idempotent"} {
		runID := "rp" + strconv.Itoa(i)
		taskID := "tp" + strconv.Itoa(i)
		logSPRTSpan(t, runID, taskID, 0.9)
		res := sprtRunWith("SIMPLE", 8, []swarm.Attempt{attempt("claude", "A")})
		res.Prompt = prompt
		verifyAndScoreRun(context.Background(), res, runID, taskID, "x.go", "go")
	}

	if n := len(corpus(t)); n != 2 {
		t.Fatalf("corpus holds %d examples, want 2: two tasks were collapsed into one", n)
	}
}

// And the same task verified twice is still one example, or every re-run grows
// the corpus by a copy.
func TestVerifyAndScoreRun_TheSameTaskTwiceIsOneExample(t *testing.T) {
	cliSandbox(t)
	goRepo(t, "exit 0")

	for i := 0; i < 2; i++ {
		runID := "rq" + strconv.Itoa(i)
		taskID := "tq" + strconv.Itoa(i)
		logSPRTSpan(t, runID, taskID, 0.9)
		res := sprtRunWith("SIMPLE", 8, []swarm.Attempt{attempt("claude", "A")})
		res.Prompt = "make Close idempotent"
		verifyAndScoreRun(context.Background(), res, runID, taskID, "x.go", "go")
	}

	if n := len(corpus(t)); n != 1 {
		t.Fatalf("corpus holds %d examples, want 1", n)
	}
}
