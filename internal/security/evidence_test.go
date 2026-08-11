// SPDX-License-Identifier: MIT

package security

import (
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/testutil"
	"github.com/ankit373/hydra/internal/trust"
)

// A machine that has never run an ensemble must not be told anything about
// the quality of confidence it has never claimed.
func TestAssessEvidence_EmptyOnAFreshMachine(t *testing.T) {
	testutil.NewSandbox(t)
	q := AssessEvidence()
	if q.Runs != 0 || len(q.Families) != 0 || len(q.WeakSources) != 0 {
		t.Errorf("AssessEvidence = %+v, want empty", q)
	}
	if got := evidenceCheck(q).Status; got != "no ensemble runs" {
		t.Errorf("Status = %q, want the no-data status", got)
	}
}

// A source used in a run but never calibrated is an absence of data, not a
// measured weakness — conflating the two would accuse a new head of being
// useless purely for being new.
func TestAssessEvidence_UncalibratedIsNotWeak(t *testing.T) {
	testutil.NewSandbox(t)
	if err := trust.LogRun(trust.DefaultLogPath(), trust.RunLog{
		TaskHash: "a", Domain: "go", Samples: 1, Models: []string{"brand-new-head"}, Decision: "accept",
	}); err != nil {
		t.Fatal(err)
	}

	q := AssessEvidence()
	if len(q.WeakSources) != 0 {
		t.Errorf("WeakSources = %+v, want none — the source was never calibrated", q.WeakSources)
	}
	if len(q.UncalibratedSources) != 1 || q.UncalibratedSources[0] != "brand-new-head" {
		t.Errorf("UncalibratedSources = %+v, want the one unmeasured source", q.UncalibratedSources)
	}
	// It must not read as a problem.
	if got := evidenceCheck(q).Status; got != "no correlation detected" {
		t.Errorf("Status = %q, want a clean status for merely-uncalibrated sources", got)
	}
}

// A source that HAS been measured and discriminates no better than a coin
// flip is a real finding: its agreement moves the posterior by nothing, so
// sampling it inflates confidence for free.
func TestAssessEvidence_MeasuredCoinFlipSourceIsWeak(t *testing.T) {
	testutil.NewSandbox(t)
	if err := trust.LogRun(trust.DefaultLogPath(), trust.RunLog{
		TaskHash: "a", Domain: "go", Samples: 1, Models: []string{"coinflip"}, Decision: "accept",
	}); err != nil {
		t.Fatal(err)
	}
	cal, err := trust.New(trust.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	// Right as often as wrong, in both directions: se+sp == 1, so D == 0.
	for i := 0; i < 40; i++ {
		saidCorrect := i%2 == 0
		outcome := trust.OutcomeCorrect
		if i%4 < 2 {
			outcome = trust.OutcomeIncorrect
		}
		if err := cal.Update("coinflip", "go", saidCorrect, outcome); err != nil {
			t.Fatal(err)
		}
	}

	q := AssessEvidence()
	if len(q.WeakSources) != 1 || q.WeakSources[0].Source != "coinflip" {
		t.Fatalf("WeakSources = %+v, want the measured coin-flip source", q.WeakSources)
	}
	if q.WeakSources[0].Observations <= 0 {
		t.Error("Observations = 0, so this would be indistinguishable from uncalibrated")
	}
	c := evidenceCheck(q)
	if !strings.Contains(c.Status, "undiagnostic") {
		t.Errorf("Status = %q, want it to name the undiagnostic source", c.Status)
	}
	if !strings.Contains(c.Detail, "coinflip") {
		t.Errorf("Detail = %q, want it to name which source", c.Detail)
	}
}

// A weak source nobody actually routes to is not a finding worth raising.
func TestAssessEvidence_UnusedSourcesAreNotReported(t *testing.T) {
	testutil.NewSandbox(t)
	cal, err := trust.New(trust.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if err := cal.Update("never-used", "go", i%2 == 0, trust.OutcomeCorrect); err != nil {
			t.Fatal(err)
		}
	}
	// No run references it.
	if q := AssessEvidence(); len(q.WeakSources) != 0 {
		t.Errorf("WeakSources = %+v, want none for a source no run used", q.WeakSources)
	}
}

// Correlated families and weak sources both misrepresent confidence, so both
// belong in the work queue.
func TestBuildActions_EvidenceProblemsRaiseActions(t *testing.T) {
	ev := EvidenceQuality{
		Runs:        3,
		Families:    []FamilyRisk{{Family: "claude", Coupling: 0.82, Critical: true}},
		WeakSources: []SourcePower{{Source: "coinflip", D: 0.001, Observations: 40}},
	}
	actions := buildActions(Coverage{}, nil, nil, PolicyAudit{}, nil, ev, ConfigDrift{})
	if len(actions) != 2 {
		t.Fatalf("actions = %+v, want one per problem", actions)
	}
	if actions[0].Priority != PriorityNow || !strings.Contains(actions[0].Title, "vote as one") {
		t.Errorf("first action = %+v, want the correlated family ranked highest", actions[0])
	}
}

func TestBuildActions_ConfigDriftRaisesAnAction(t *testing.T) {
	actions := buildActions(Coverage{}, nil, nil, PolicyAudit{}, nil, EvidenceQuality{}, ConfigDrift{Changed: true})
	if len(actions) != 1 || !strings.Contains(actions[0].Title, "configuration changed") {
		t.Fatalf("actions = %+v, want a config-drift action", actions)
	}
}
