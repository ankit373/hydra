// SPDX-License-Identifier: MIT

package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/evalset"
	"github.com/ankit373/hydra/internal/testutil"
)

// seedHead adds n wrong answers ground never passed and 30 right ones it did,
// under head, using the kind and check names a plain run just logged.
func seedHead(t *testing.T, head string, n int) {
	t.Helper()
	joint, err := evalset.Joint(evalset.VerdictsPath())
	if err != nil || len(joint) == 0 || len(joint[0].Checks) == 0 {
		t.Fatalf("no verdict to copy names from: %+v (%v)", joint, err)
	}
	ref := joint[0].Checks[0]
	p := evalset.VerdictsPath()
	for i := range n + 30 {
		right := i >= n
		span := fmt.Sprintf("seed-%s-%d", head, i)
		v := evalset.Verdict{TS: "2026-11-01T00:00:00Z", SpanID: span, Head: head, Domain: ref.Domain, Check: ref.Check, Passed: right}
		out := evalset.OutcomeIncorrect
		if right {
			out = evalset.OutcomeCorrect
		}
		if err := evalset.AddVerdict(p, v); err != nil {
			t.Fatal(err)
		}
		if err := evalset.AddTruth(p, evalset.Truth{TS: v.TS, SpanID: span, Outcome: out}); err != nil {
			t.Fatal(err)
		}
	}
}

func certifyArgs(extra ...string) []string {
	return append(append([]string{"dispatch", "--no-stream", "--kind", "summary", "--check", "ground"}, extra...), invoiceSummary)
}

func TestDispatch_CertifyWalksToSecondHeadOnFutility(t *testing.T) {
	_, f := escalating(t, "summary", "The total is 450 dollars.", "The total is 120 dollars.")
	if _, out, err := run(t, certifyArgs()...); err != nil {
		t.Fatalf("plain dispatch failed: %v (%s)", err, out)
	}
	seedHead(t, "litellm/b", 150)
	a, b := f.called("a"), f.called("b")

	out, cobraOut, err := run(t, certifyArgs("--certify", "0.05", "--certify-candidates", "2")...)
	if err != nil {
		t.Fatalf("dispatch failed: %v (%s)", err, cobraOut)
	}
	if f.called("a")-a != 1 || f.called("b")-b != 1 {
		t.Fatalf("calls a=%d b=%d, want one each", f.called("a")-a, f.called("b")-b)
	}
	if strings.Contains(f.prompt("b"), "REJECTED ANSWER") {
		t.Error("the second candidate was sent the first answer; candidates must answer on their own")
	}
	// Two candidates by spend: 1/2 each, bar 1/(0.05 x 1/2) = 40. b's ground check passed
	// none of 150 wrong answers: q̄ = 1 - 0.05^(1/150) = 0.0198, a pass is worth 50.6.
	if !strings.Contains(out, "certified (under independence) at alpha 0.05: product 50.6 >= bar 40, candidate 2 of 2") {
		t.Fatalf("want b certified as the second candidate:\n%s", out)
	}
	if !strings.Contains(out, "candidate 1, litellm/a: not verified") {
		t.Errorf("the first candidate's outcome is not shown:\n%s", out)
	}
	s := certifiedScores(t)
	if len(s) != 1 || s[0].Value != 1 || s[0].Outcome.Candidates != 2 {
		t.Fatalf("want one certified score on the task span, got %+v", s)
	}
	// b disagreed with a, and that verdict is filed so agreement can be calibrated per pair.
	joint, _ := evalset.Joint(evalset.VerdictsPath())
	found := false
	for _, ans := range joint {
		for _, v := range ans.Checks {
			if v.Check == "model:litellm/a" && v.Head == "litellm/b" && !v.Passed {
				found = true
			}
		}
	}
	if !found {
		t.Error("b's disagreement with a was not filed as a model:litellm/a verdict")
	}
}

func TestDispatch_CertifyStopsAtCandidateBudget(t *testing.T) {
	testutil.NewSandbox(t)
	f := litellmHeads(t, map[string]string{"a": "The total is 450 dollars.", "b": "It is 450 dollars.", "c": "The total is 120 dollars."}, "summary.general")
	out, cobraOut, err := run(t, certifyArgs("--certify", "0.05", "--certify-candidates", "2")...)
	if err != nil {
		t.Fatalf("dispatch failed: %v (%s)", err, cobraOut)
	}
	if f.called("a") != 1 || f.called("b") != 1 || f.called("c") != 0 {
		t.Fatalf("calls a=%d b=%d c=%d, want a and b only", f.called("a"), f.called("b"), f.called("c"))
	}
	if !strings.Contains(out, "not verified: candidate budget after 2 of 2 candidates") {
		t.Fatalf("want the candidate budget named:\n%s", out)
	}
}

// a's call is estimated near half b's, so a ceiling of $0.0001 fits a and leaves too little for b.
func TestDispatch_CertifyRespectsCostCeiling(t *testing.T) {
	_, f := escalating(t, "summary", "The total is 450 dollars. "+strings.Repeat("Paid in full. ", 300), "The total is 120 dollars.")
	out, cobraOut, err := run(t, certifyArgs("--certify", "0.05", "--max-cost", "0.0001")...)
	if err != nil {
		t.Fatalf("dispatch failed: %v (%s)", err, cobraOut)
	}
	if f.called("a") != 1 || f.called("b") != 0 {
		t.Fatalf("calls a=%d b=%d, want only a: the second candidate does not fit the ceiling", f.called("a"), f.called("b"))
	}
	if !strings.Contains(out, "not verified: cost ceiling after 1 of 3 candidates") {
		t.Fatalf("want the cost ceiling named:\n%s", out)
	}
}
