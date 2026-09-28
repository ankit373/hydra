// SPDX-License-Identifier: MIT

package main

import (
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/rank"
)

// A dry run on a machine that has measured nothing must print exactly what it
// printed before the constraint existed. A line announcing a bar that decided
// nothing is the same defect as a rule report on a dispatch no rule touched.
func TestRoutingRequirement_SaysNothingWhenNothingWasMeasured(t *testing.T) {
	cases := []struct {
		name string
		res  *dispatch.Result
	}{
		{"constraint never ran", &dispatch.Result{Domain: "go"}},
		{"ran, but nothing is judged here", &dispatch.Result{
			Domain: "go", Requirement: 0.9,
			Scores: map[string]rank.Score{"a": {Declared: 90, Effective: 90}},
		}},
	}
	for _, c := range cases {
		if got := routingRequirement(c.res); got != "" {
			t.Errorf("%s: printed %q", c.name, got)
		}
	}
}

// Measured and short of the bar is a different answer from measured and over
// it, and the line has to say which, or a reader cannot tell a constraint that
// chose from one that had nothing to choose between.
func TestRoutingRequirement_ReportsWhetherAnythingCleared(t *testing.T) {
	short := routingRequirement(&dispatch.Result{
		Domain: "go", Requirement: 0.9,
		Scores: map[string]rank.Score{"a": {Effective: 70, N: 40, InDomain: 40}},
	})
	if !strings.Contains(short, "the ranking stands") {
		t.Errorf("nothing cleared, got %q", short)
	}
	cleared := routingRequirement(&dispatch.Result{
		Domain: "go", Requirement: 0.9,
		Scores: map[string]rank.Score{"a": {Effective: 93, N: 60, InDomain: 60, Clears: true}},
	})
	if !strings.Contains(cleared, "cheapest head measured that competent") {
		t.Errorf("a head cleared, got %q", cleared)
	}
}

// The two reasons a head was not chosen must not read alike: one was judged
// and fell short, the other was never judged enough here to be asked at all.
func TestClearance_SeparatesFallingShortFromNotBeingAsked(t *testing.T) {
	cases := []struct {
		sc   rank.Score
		want string
	}{
		{rank.Score{Clears: true, InDomain: 60}, "clears"},
		{rank.Score{InDomain: rank.MinCommitments}, "under the bar"},
		{rank.Score{InDomain: rank.MinCommitments - 1}, "too little in-domain evidence"},
	}
	for _, c := range cases {
		if got := clearance(c.sc); got != c.want {
			t.Errorf("%+v: got %q, want %q", c.sc, got, c.want)
		}
	}
}

// The per-head line carries the price and the verdict only where a
// requirement was applied, so a pinned dispatch reads exactly as it always did.
func TestRoutingEvidence_CostAndVerdictOnlyUnderAConstraint(t *testing.T) {
	head := provider.Head{ID: "h"}
	sc := rank.Score{Effective: 93, N: 60, InDomain: 60, CostUSD: 0.0225, Clears: true}

	pinned := routingEvidence(&dispatch.Result{
		Domain: "go", Scores: map[string]rank.Score{"h": sc},
	}, head)
	if strings.Contains(pinned, "clears") || strings.Contains(pinned, "$") {
		t.Errorf("a pinned dispatch reported a verdict it never applied: %q", pinned)
	}
	if !strings.Contains(pinned, "60 in go") {
		t.Errorf("the measurement itself went missing: %q", pinned)
	}

	constrained := routingEvidence(&dispatch.Result{
		Domain: "go", Requirement: 0.9, Scores: map[string]rank.Score{"h": sc},
	}, head)
	if !strings.Contains(constrained, "clears") || !strings.Contains(constrained, "$") {
		t.Errorf("the constraint's own evidence is missing: %q", constrained)
	}

	// A head the ranking never scored has nothing to explain.
	if got := routingEvidence(&dispatch.Result{Requirement: 0.9}, head); got != "" {
		t.Errorf("an unscored head printed %q", got)
	}
}
