// SPDX-License-Identifier: MIT

package security

import (
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/ledger"
)

func TestAuditPolicy_HitCountsAndDeadRules(t *testing.T) {
	pol := ledger.Policy{
		Default: ledger.Deny,
		Rules: []ledger.Rule{
			{Tool: "a", Decision: ledger.Allow},
			{Tool: "b", Decision: ledger.Deny},
		},
	}
	events := []ledger.Event{
		{Reason: "rule 0 (allow a/*)"},
		{Reason: "rule 0 (allow a/*)"},
		{Reason: "default"},
		{Reason: "manually recorded"}, // never went through Decide
	}

	a := AuditPolicy(pol, events)
	if a.Rules[0].Hits != 2 || a.Rules[0].Dead {
		t.Errorf("rule 0 = %+v, want 2 hits and not dead", a.Rules[0])
	}
	if a.Rules[1].Hits != 0 || !a.Rules[1].Dead {
		t.Errorf("rule 1 = %+v, want 0 hits and dead", a.Rules[1])
	}
	if a.DefaultHits != 1 {
		t.Errorf("DefaultHits = %d, want 1", a.DefaultHits)
	}
	// The manually-recorded event never went through Decide, so it is not part
	// of this population at all.
	if a.Evaluated != 3 {
		t.Errorf("Evaluated = %d, want 3 (the non-Decide event must be excluded)", a.Evaluated)
	}
	if a.FailOpen {
		t.Error("FailOpen = true for a default-deny policy")
	}
}

// A zero Default is treated as Allow by Policy.Decide, so the audit has to
// read it the same way, otherwise the dashboard reports fail-closed on a
// policy that is actually fail-open.
func TestAuditPolicy_ZeroDefaultIsFailOpen(t *testing.T) {
	a := AuditPolicy(ledger.Policy{}, nil)
	if !a.FailOpen || a.Default != string(ledger.Allow) {
		t.Errorf("audit = %+v, want fail-open allow for a zero Default", a)
	}
}

func TestAuditPolicy_ShadowDetection(t *testing.T) {
	// Rule 1 is unreachable: rule 0 wildcards every field rule 1 constrains.
	pol := ledger.Policy{
		Rules: []ledger.Rule{
			{Decision: ledger.Allow},                                // matches everything
			{Tool: "fs", Resource: "/etc/*", Decision: ledger.Deny}, // can never fire
		},
	}
	a := AuditPolicy(pol, nil)
	if a.Rules[0].ShadowedBy != nil {
		t.Error("the first rule cannot be shadowed by anything")
	}
	if a.Rules[1].ShadowedBy == nil || *a.Rules[1].ShadowedBy != 0 {
		t.Errorf("rule 1 ShadowedBy = %v, want 0", a.Rules[1].ShadowedBy)
	}
}

// The detector is deliberately conservative: it must never invent a policy bug
// that isn't there. Disjoint rules are not shadows.
func TestAuditPolicy_NoFalseShadowOnDisjointRules(t *testing.T) {
	pol := ledger.Policy{
		Rules: []ledger.Rule{
			{Tool: "fs", Decision: ledger.Deny},
			{Tool: "net", Decision: ledger.Deny},    // different tool, reachable
			{Resource: "/x", Decision: ledger.Deny}, // different dimension, reachable
		},
	}
	a := AuditPolicy(pol, nil)
	for i, r := range a.Rules {
		if r.ShadowedBy != nil {
			t.Errorf("rule %d reported shadowed by %d, but the rules are disjoint", i, *r.ShadowedBy)
		}
	}
}

func TestRuleIndex_ParsesOnlyDecideReasons(t *testing.T) {
	cases := []struct {
		reason string
		want   int
		ok     bool
	}{
		{"rule 0 (allow a/*)", 0, true},
		{"rule 12 (deny fs//etc/*)", 12, true},
		{"default", 0, false},
		{"manually recorded", 0, false},
		{"rule x (bad)", 0, false},
		{"", 0, false},
	}
	for _, tc := range cases {
		got, ok := ruleIndex(tc.reason)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("ruleIndex(%q) = %d,%v want %d,%v", tc.reason, got, ok, tc.want, tc.ok)
		}
	}
}

// The three DefaultPolicy rules differ in nothing but Classification, so a
// summary that drops it rendered all three as "* */* *", and a pii-scoped
// network deny read as a universal one (#1169).
func TestRuleSummary_RendersTheClassificationScope(t *testing.T) {
	scoped := ruleSummary(ledger.Rule{Action: ledger.Network, Classification: "pii", Decision: ledger.Deny})
	universal := ruleSummary(ledger.Rule{Action: ledger.Network, Decision: ledger.Deny})
	if scoped == universal {
		t.Fatalf("a pii-scoped network deny and a universal one both summarise as %q", scoped)
	}
	if !strings.Contains(scoped, "pii") {
		t.Errorf("summary = %q, does not name the classification it is scoped to", scoped)
	}

	a := AuditPolicy(ledger.DefaultPolicy(), nil)
	seen := map[string]int{}
	for _, r := range a.Rules {
		if prev, dup := seen[r.Summary]; dup {
			t.Errorf("rules %d and %d both render as %q, so the quarantine deny is "+
				"indistinguishable from the rest", prev, r.Index, r.Summary)
		}
		seen[r.Summary] = r.Index
	}
	// Carried as its own field too, so a dashboard need not parse the string.
	if a.Rules[0].Classification == "" {
		t.Error("RuleStat.Classification is empty on a classification-scoped rule")
	}
}
