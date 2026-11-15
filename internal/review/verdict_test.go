// SPDX-License-Identifier: MIT

package review

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ankit373/hydra/internal/editor"
	"github.com/ankit373/hydra/internal/evalset"
)

// editThenReview files the validator's verdict on the edited file the way
// `hyctl edit` does, then hands the file to a human's review.
func editThenReview(t *testing.T, review func(string) error) evalset.Answer {
	t.Helper()
	repo := repoSandbox(t, false)
	file := filepath.Join(repo, "docs", "intro.md")
	write(t, file, "# Intro\n\nWelcome.\n")
	write(t, file+".hydra-bak", "# Intro\n")
	editor.RecordVerifiedEdit(context.Background(), nil, editor.VerifiedEdit{
		Prompt: "add a welcome line", File: file, Head: "cody",
		Candidate: "# Intro\n\nWelcome.", Passed: true,
	})
	if err := review(file); err != nil {
		t.Fatal(err)
	}

	answers, err := evalset.Joint(evalset.VerdictsPath())
	if err != nil || len(answers) != 1 {
		t.Fatalf("verdict log = %+v, err=%v; want the review joined to the edit's answer", answers, err)
	}
	a := answers[0]
	if len(a.Checks) != 1 || a.Truth == nil || a.Truth.Source != "review" {
		t.Fatalf("answer = %+v, want one validator check and a review truth", a)
	}
	return a
}

func TestApprove_WritesTruthForTheEditedCandidate(t *testing.T) {
	a := editThenReview(t, func(f string) error { _, err := Approve(f); return err })
	if a.Truth.Outcome != evalset.OutcomeCorrect {
		t.Errorf("truth = %+v, want correct", a.Truth)
	}
}

// Reject rolls the file back, so the truth must be keyed by what was judged,
// not by the restored bytes.
func TestReject_WritesIncorrectTruthForTheEditedCandidate(t *testing.T) {
	a := editThenReview(t, func(f string) error { _, err := Reject(f); return err })
	if a.Truth.Outcome != evalset.OutcomeIncorrect {
		t.Errorf("truth = %+v, want incorrect", a.Truth)
	}
}
