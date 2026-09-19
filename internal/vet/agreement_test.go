// SPDX-License-Identifier: MIT

package vet

import (
	"context"
	"testing"
)

// votingRouter answers with the accepted output and reports the sampled Heads'
// answers alongside it, which is what the SPRT ensemble already produces.
type votingRouter struct {
	output string
	votes  []Vote
}

func (v votingRouter) Review(_ context.Context, _, _, _ string) (Answer, error) {
	return Answer{Output: v.output, Head: "head-a", Votes: v.votes}, nil
}

const claimA = `[{"line":10,"severity":"blocking","title":"unchecked error"}]`
const claimAB = `[{"line":10,"severity":"blocking","title":"unchecked error"},
                 {"line":42,"severity":"non-blocking","title":"name could be clearer"}]`

// The file's single confidence says the same thing about a claim three Heads
// made and one only one of them made. It is the wrong unit.
func TestCountAgreement_CountsPerClaimNotPerFile(t *testing.T) {
	found, _, ok := parseFindings(claimAB, "a.go", "head-a")
	if !ok || len(found) != 2 {
		t.Fatalf("setup: parsed %d findings, ok=%v", len(found), ok)
	}
	countAgreement(found, []Vote{
		{Head: "head-a", Output: claimAB},
		{Head: "head-b", Output: claimA},
		{Head: "head-c", Output: claimA},
	}, "a.go")

	byTitle := map[string]Finding{}
	for _, f := range found {
		byTitle[f.Title] = f
	}
	if got := byTitle["unchecked error"]; got.Agreed != 3 || got.Voters != 3 {
		t.Errorf("the unanimous claim reads %d/%d, want 3/3", got.Agreed, got.Voters)
	}
	if got := byTitle["name could be clearer"]; got.Agreed != 1 || got.Voters != 3 {
		t.Errorf("the lone claim reads %d/%d, want 1/3", got.Agreed, got.Voters)
	}
	if !byTitle["unchecked error"].Unanimous() {
		t.Error("a claim every Head made does not read as unanimous")
	}
	if !byTitle["name could be clearer"].Lone() {
		t.Error("a claim one Head of three made does not read as lone")
	}
}

// "1 of 1" dresses a single opinion as a consensus, so nothing is counted when
// only one Head was asked.
func TestCountAgreement_SaysNothingWhenOneHeadWasAsked(t *testing.T) {
	found, _, _ := parseFindings(claimA, "a.go", "head-a")
	countAgreement(found, []Vote{{Head: "head-a", Output: claimA}}, "a.go")

	if found[0].Voters != 0 || found[0].Agreed != 0 {
		t.Errorf("a single dispatch reported %d/%d, want nothing",
			found[0].Agreed, found[0].Voters)
	}
	if found[0].Agreement() {
		t.Error("a single answer renders an agreement count")
	}
}

// A Head that answered unreadably did not agree with anything, but it was
// still asked, so dropping it from the denominator would inflate every count.
func TestCountAgreement_AnUnreadableVoteStillCounts(t *testing.T) {
	found, _, _ := parseFindings(claimA, "a.go", "head-a")
	countAgreement(found, []Vote{
		{Head: "head-a", Output: claimA},
		{Head: "head-b", Output: "I could not review this file."},
	}, "a.go")

	if found[0].Agreed != 1 || found[0].Voters != 2 {
		t.Errorf("reads %d/%d, want 1/2: the unreadable vote agreed with nothing but was asked",
			found[0].Agreed, found[0].Voters)
	}
}

// The stated limit. Matching is line and severity, so the same defect a line
// away is its own claim and the count is a lower bound rather than a
// measurement of how many Heads really agreed.
func TestCountAgreement_ANeighbouringLineIsItsOwnClaim(t *testing.T) {
	found, _, _ := parseFindings(claimA, "a.go", "head-a")
	countAgreement(found, []Vote{
		{Head: "head-a", Output: claimA},
		{Head: "head-b", Output: `[{"line":11,"severity":"blocking","title":"unchecked error"}]`},
	}, "a.go")

	if found[0].Agreed != 1 {
		t.Errorf("agreed = %d, want 1: matching is exact on line, and the report says so",
			found[0].Agreed)
	}
}

// A claim at the same line but a different severity is a different claim: one
// Head calling it blocking and another a nit is disagreement, not agreement.
func TestCountAgreement_SeverityIsPartOfTheClaim(t *testing.T) {
	found, _, _ := parseFindings(claimA, "a.go", "head-a")
	countAgreement(found, []Vote{
		{Head: "head-a", Output: claimA},
		{Head: "head-b", Output: `[{"line":10,"severity":"non-blocking","title":"unchecked error"}]`},
	}, "a.go")

	if found[0].Agreed != 1 {
		t.Errorf("agreed = %d, want 1: the two Heads disagree about whether it blocks", found[0].Agreed)
	}
}

// End to end through Run, which is where the votes actually arrive.
func TestRun_FindingsCarryTheirAgreement(t *testing.T) {
	dir := gitRepo(t)
	write(t, dir, "a.go", "package p\n")
	spec := &Spec{
		Mode: "workspace", Repository: dir,
		Reviewable: []File{{Path: "a.go"}},
		Groups:     []Group{{Pattern: "**/*.go", Files: []string{"a.go"}, Rule: "be careful"}},
	}
	res, err := Run(context.Background(), votingRouter{
		output: claimAB,
		votes: []Vote{
			{Head: "head-a", Output: claimAB},
			{Head: "head-b", Output: claimA},
		},
	}, spec, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 2 {
		t.Fatalf("got %d findings, want 2", len(res.Findings))
	}
	for _, f := range res.Findings {
		if f.Voters != 2 {
			t.Errorf("%q carries %d voters, so the votes never reached Run", f.Title, f.Voters)
		}
	}
}
