// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"strings"
	"testing"

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
