// SPDX-License-Identifier: MIT

package security

import (
	"testing"

	"github.com/ankit373/hydra/internal/ledger"
)

func TestThreatBreakdown_GroupsMarkersResourcesAndActions(t *testing.T) {
	events := []ledger.Event{
		{Decision: ledger.Allow, Flagged: true, FlagReason: "ignore previous instructions", Action: ledger.Read},
		{Decision: ledger.Allow, Flagged: true, FlagReason: "ignore previous instructions", Action: ledger.Read},
		{Decision: ledger.Allow, Flagged: true, FlagReason: "do anything now", Action: ledger.Exec},
		{Decision: ledger.Deny, Resource: "/etc/passwd", Action: ledger.Read},
		{Decision: ledger.Deny, Resource: "/etc/passwd", Action: ledger.Read},
		{Decision: ledger.Deny, Resource: "/tmp/once", Action: ledger.Write},
		{Decision: ledger.Allow, Action: ledger.Read}, // clean — must not appear anywhere
	}

	th := ThreatBreakdown(events)

	// Which phrase was actually tried, not just how many.
	if len(th.ByMarker) != 2 || th.ByMarker[0].Label != "ignore previous instructions" || th.ByMarker[0].Count != 2 {
		t.Errorf("ByMarker = %+v, want the most-tried phrase first", th.ByMarker)
	}

	// Two denials on one resource is probing; a single denial is not.
	if len(th.ProbedResources) != 1 || th.ProbedResources[0].Label != "/etc/passwd" {
		t.Errorf("ProbedResources = %+v, want only the repeatedly-denied resource", th.ProbedResources)
	}

	// An attempted exec must be visible as its own risk class.
	byAction := map[string]int{}
	for _, c := range th.ByAction {
		byAction[c.Label] = c.Count
	}
	if byAction["exec"] != 1 {
		t.Errorf("ByAction = %+v, want the exec attempt counted separately", th.ByAction)
	}
	// 6 risky events total (the clean allow is excluded).
	total := 0
	for _, c := range th.ByAction {
		total += c.Count
	}
	if total != 6 {
		t.Errorf("ByAction totals %d, want 6 risky events (the clean allow must be excluded)", total)
	}
}

func TestThreatBreakdown_EmptyOnACleanLedger(t *testing.T) {
	th := ThreatBreakdown([]ledger.Event{{Decision: ledger.Allow, Action: ledger.Read}})
	if th.ByMarker != nil || th.ProbedResources != nil || th.ByAction != nil {
		t.Errorf("a clean ledger produced %+v, want all nil so the view can say 'nothing'", th)
	}
}
