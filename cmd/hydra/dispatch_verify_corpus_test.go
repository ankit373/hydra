// SPDX-License-Identifier: MIT

//go:build !windows

// These need a fake verifier with a controllable exit code, so an executable
// script, for the same reason dispatch_verify_unix_test.go is tagged.

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/evalset"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/swarm"
	"github.com/ankit373/hydra/internal/trust"
)

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

// This is the mechanism, and it is the reason nothing may be filed from here.
// A dispatch applies nothing to disk, and the repo's own suite carries no
// placeholder, so the candidate is never handed to the verifier: the verdict is
// true of the working tree and says nothing about the answer (#982).
//
// If this ever fails, the candidate has started reaching the verifier and the
// filing decision below is worth revisiting. It is not a failure to paper over.
func TestVerifyAndScoreRun_TheCandidateNeverReachesTheVerifier(t *testing.T) {
	cliSandbox(t)
	argsFile := filepath.Join(t.TempDir(), "argv.txt")
	goRepo(t, "printf '%s\\n' \"$@\" > "+argsFile+"; exit 0")

	logSPRTSpan(t, "rv", "tv", 0.9)
	res := sprtRunWith("SIMPLE", 8, []swarm.Attempt{attempt("claude", "A")})
	res.Trust.Candidate = "UNIQUE_CANDIDATE_TEXT_12345"
	verifyAndScoreRun(context.Background(), res, "rv", "tv", "x.go", "go")

	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("the verifier never ran, so this proves nothing: %v", err)
	}
	if strings.Contains(string(raw), "UNIQUE_CANDIDATE_TEXT_12345") {
		t.Fatalf("the candidate now reaches the verifier (argv %q); the verdict may judge the "+
			"answer after all, so revisit whether an example should be filed", strings.TrimSpace(string(raw)))
	}
}

// Given the above, filing would write a verdict about the repo as if it were
// ground truth about the answer. An empty corpus is recoverable; a corpus of
// confident mislabels is not, and it is what the router would be fitted on.
func TestVerifyAndScoreRun_FilesNoExample(t *testing.T) {
	cliSandbox(t)
	goRepo(t, "exit 0") // the suite passes, which says nothing about the answer

	logSPRTSpan(t, "rw", "tw", 0.9)
	res := sprtRunWith("SIMPLE", 8, []swarm.Attempt{attempt("claude", "A")})
	verifyAndScoreRun(context.Background(), res, "rw", "tw", "x.go", "go")

	if n := len(corpus(t)); n != 0 {
		t.Fatalf("corpus holds %d examples, want 0: this verdict did not judge the candidate", n)
	}
}

// A failing suite is the same argument. The answer may have been perfect.
func TestVerifyAndScoreRun_FilesNothingOnFailureEither(t *testing.T) {
	cliSandbox(t)
	goRepo(t, "exit 1")

	logSPRTSpan(t, "rx", "tx", 0.9)
	res := sprtRunWith("EXPERT", 2, []swarm.Attempt{attempt("claude", "A")})
	verifyAndScoreRun(context.Background(), res, "rx", "tx", "x.go", "go")

	if n := len(corpus(t)); n != 0 {
		t.Fatalf("corpus holds %d examples, want 0", n)
	}
}
