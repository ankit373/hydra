// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/swarm"
	"github.com/ankit373/hydra/internal/trust"
	"github.com/ankit373/hydra/internal/vet"
)

// The count is the whole point of #981: a claim three Heads made and one only
// one made have to look different in the report, not only in --json.
func TestPrintVet_ShowsAgreementPerFinding(t *testing.T) {
	res := &vet.Result{
		Spec:  &vet.Spec{Mode: "workspace"},
		Files: []vet.FileOutcome{{File: "a.go", Head: "h1", Tier: 10, Findings: 2}},
		Findings: []vet.Finding{
			{File: "a.go", Line: 10, Severity: "blocking", Title: "unchecked error", Agreed: 3, Voters: 3},
			{File: "a.go", Line: 42, Severity: "non-blocking", Title: "naming nit", Agreed: 1, Voters: 3},
		},
	}
	var b bytes.Buffer
	printVet(&b, res)
	out := b.String()

	if !strings.Contains(out, "3/3") || !strings.Contains(out, "1/3") {
		t.Errorf("the report does not say how many Heads agreed:\n%s", out)
	}
	// The limit has to be stated, because the count is a lower bound.
	if !strings.Contains(out, "line and severity") {
		t.Errorf("the report claims an agreement count without saying how it matched:\n%s", out)
	}
}

// "1 of 1" dresses a single opinion as a consensus.
func TestPrintVet_SaysNothingAboutAgreementForASingleHead(t *testing.T) {
	res := &vet.Result{
		Spec:     &vet.Spec{Mode: "workspace"},
		Files:    []vet.FileOutcome{{File: "a.go", Head: "h1", Tier: 10, Findings: 1}},
		Findings: []vet.Finding{{File: "a.go", Line: 10, Severity: "blocking", Title: "boom"}},
	}
	var b bytes.Buffer
	printVet(&b, res)
	out := b.String()

	// Asserted on the finding's own line: the summary carries an unrelated
	// "1/1 file(s) reviewed" that a whole-output match reads as a consensus.
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "boom") && strings.Contains(line, "/") {
			t.Errorf("a single dispatch was rendered as a consensus: %q", line)
		}
	}
	if strings.Contains(out, "line and severity") {
		t.Errorf("the agreement note was printed with nothing to agree about:\n%s", out)
	}
}

func TestAgreementLabel_SilentBelowTwoVoters(t *testing.T) {
	if got := agreementLabel(vet.Finding{Agreed: 1, Voters: 1}); got != "" {
		t.Errorf("got %q, want nothing for one voter", got)
	}
	if got := agreementLabel(vet.Finding{Agreed: 2, Voters: 4}); !strings.Contains(got, "2/4") {
		t.Errorf("got %q, want it to carry 2/4", got)
	}
}

// The ensemble already produced every Head's answer; reading only the winner's
// was throwing the agreement between them away. Without this the whole of #981
// is inert, which is the shape #950 shipped with.
func TestVotesFrom_CarriesEveryHeadThatAnswered(t *testing.T) {
	votes := votesFrom([]swarm.Attempt{
		{Head: provider.Head{ID: "h1"}, Status: swarm.StatusOK, Output: "a", Rank: 1},
		{Head: provider.Head{ID: "h2"}, Status: swarm.StatusOK, Output: "b"},
	})
	if len(votes) != 2 {
		t.Fatalf("got %d votes, want both Heads: %+v", len(votes), votes)
	}
	if votes[0].Head != "h1" || votes[1].Output != "b" {
		t.Errorf("the votes lost their head or output: %+v", votes)
	}
}

// A Head that failed was asked and could not answer, which is not the same as
// answering and not agreeing. Counting it would deflate every finding.
func TestVotesFrom_AFailedHeadIsNotAVoter(t *testing.T) {
	votes := votesFrom([]swarm.Attempt{
		{Head: provider.Head{ID: "h1"}, Status: swarm.StatusOK, Output: "a", Rank: 1},
		{Head: provider.Head{ID: "h2"}, Status: swarm.StatusFailed},
	})
	if len(votes) != 1 {
		t.Fatalf("got %d votes, want only the Head that answered: %+v", len(votes), votes)
	}
}

// fakeSPRT stands in for the ensemble, so what this adapter does with the
// attempts can be checked without a model.
type fakeSPRT struct{ res *swarm.SPRTResult }

func (f fakeSPRT) RunSPRT(context.Context, string, swarm.Options) (*swarm.SPRTResult, error) {
	return f.res, nil
}

// Without this the whole of #981 is inert: the counting works and nothing ever
// hands it the votes. That is the shape #950 shipped with.
func TestReviewEnsemble_HandsEveryHeadsAnswerToTheReview(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())

	r := vetRouter{sw: fakeSPRT{res: &swarm.SPRTResult{
		Trust: &trust.Result{Candidate: "winner", Confidence: 0.9, Samples: 2},
		Attempts: []swarm.Attempt{
			{Head: provider.Head{ID: "h1"}, Status: swarm.StatusOK, Output: "winner", Rank: 1},
			{Head: provider.Head{ID: "h2"}, Status: swarm.StatusOK, Output: "dissent"},
		},
	}}}

	ans, err := r.reviewEnsemble(context.Background(), "prompt", "go", "a.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(ans.Votes) != 2 {
		t.Fatalf("the answer carries %d votes, so agreement can never be counted: %+v",
			len(ans.Votes), ans.Votes)
	}
	// The accepted answer has to be among them, or a unanimous finding reads
	// as one voter short of unanimous.
	var sawWinner bool
	for _, v := range ans.Votes {
		if v.Output == "winner" {
			sawWinner = true
		}
	}
	if !sawWinner {
		t.Errorf("the accepted answer is not among the votes: %+v", ans.Votes)
	}
}
