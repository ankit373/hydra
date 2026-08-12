// SPDX-License-Identifier: MIT

package security

import (
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/ledger"
)

// The worked example the whole design rests on: one actor, one afternoon,
// five events that every previous version of this dashboard reported as
// unrelated rows. It must correlate into exactly one incident carrying every
// stage, including the one that matters most — a flagged request that was
// ALLOWED rather than blocked.
func TestCorrelateIncidents_TheWorkedExample(t *testing.T) {
	events := []ledger.Event{
		{TS: "2026-08-09T15:11:02Z", Tool: "gpt-4o", Agent: "claude-code", Resource: "README.md",
			Action: ledger.Write, Decision: ledger.Allow, Flagged: true, FlagReason: "ignore previous instructions"},
		{TS: "2026-08-09T15:12:40Z", Tool: "gpt-4o", Resource: "/etc/passwd", Action: ledger.Read, Decision: ledger.Deny},
		{TS: "2026-08-09T15:31:07Z", Tool: "gpt-4o", Resource: "/etc/passwd", Action: ledger.Read, Decision: ledger.Deny},
		{TS: "2026-08-09T15:44:19Z", Tool: "gpt-4o", Resource: "/etc/shadow", Action: ledger.Exec, Decision: ledger.Deny,
			Flagged: true, FlagReason: "do anything now"},
		{TS: "2026-08-09T16:20:03Z", Tool: "gpt-4o", Resource: "internal/ledger/ledger.go",
			Action: ledger.Write, Decision: ledger.Deny},
	}

	got := CorrelateIncidents(events, BlastReport{})
	if len(got) != 1 {
		t.Fatalf("got %d incidents, want exactly 1 — the sequence was split", len(got))
	}
	in := got[0]
	if len(in.Events) != 5 {
		t.Errorf("incident holds %d events, want all 5", len(in.Events))
	}
	for _, want := range []Stage{StageInjection, StageRecon, StageEscalation, StageAuditTampering, StageSucceeded} {
		if !hasStage(in, want) {
			t.Errorf("stage %q not detected in %v", want, in.Stages)
		}
	}
	if in.Severity != SeverityCritical {
		t.Errorf("severity = %q, want critical (likelihood %d × impact %d)", in.Severity, in.Likelihood, in.Impact)
	}
	// The narrative must read as a sequence, not a count.
	for _, want := range []string{"injection marker", "allowed", "denied repeatedly", "exec", "audit trail"} {
		if !strings.Contains(in.Narrative, want) {
			t.Errorf("narrative missing %q:\n%s", want, in.Narrative)
		}
	}
}

// Blocked and landed are not the same event. A flagged request that succeeded
// must outrank the identical request that was denied.
func TestCorrelateIncidents_SucceededOutranksBlocked(t *testing.T) {
	blocked := CorrelateIncidents([]ledger.Event{
		{TS: "2026-08-09T10:00:00Z", Tool: "h", Resource: "x", Action: ledger.Write,
			Decision: ledger.Deny, Flagged: true, FlagReason: "m"},
	}, BlastReport{})
	landed := CorrelateIncidents([]ledger.Event{
		{TS: "2026-08-09T10:00:00Z", Tool: "h", Resource: "x", Action: ledger.Write,
			Decision: ledger.Allow, Flagged: true, FlagReason: "m"},
	}, BlastReport{})

	if landed[0].Likelihood <= blocked[0].Likelihood {
		t.Errorf("a flagged request that SUCCEEDED scored %d, blocked scored %d — succeeding must weigh more",
			landed[0].Likelihood, blocked[0].Likelihood)
	}
}

// A lull longer than the session window is a separate incident, and two
// actors never merge into one.
func TestCorrelateIncidents_SplitsOnGapAndByActor(t *testing.T) {
	events := []ledger.Event{
		{TS: "2026-08-09T10:00:00Z", Tool: "a", Resource: "x", Decision: ledger.Deny},
		{TS: "2026-08-09T14:00:00Z", Tool: "a", Resource: "x", Decision: ledger.Deny}, // 4h later
		{TS: "2026-08-09T10:05:00Z", Tool: "b", Resource: "y", Decision: ledger.Deny},
	}
	got := CorrelateIncidents(events, BlastReport{})
	if len(got) != 3 {
		t.Fatalf("got %d incidents, want 3 (actor a split by the gap, plus actor b)", len(got))
	}
}

// An ordinary allowed access is not an incident.
func TestCorrelateIncidents_IgnoresCleanTraffic(t *testing.T) {
	if got := CorrelateIncidents([]ledger.Event{
		{TS: "2026-08-09T10:00:00Z", Tool: "a", Resource: "x", Decision: ledger.Allow},
	}, BlastReport{}); len(got) != 0 {
		t.Errorf("clean traffic produced %d incidents", len(got))
	}
}

// Audit-tampering detection must be narrow: the accountability machinery, not
// any source file that happens to be edited.
func TestTargetsAuditMachinery_IsNarrow(t *testing.T) {
	for _, hit := range []string{
		"/home/u/.hydra/mcp_ledger.jsonl", "mcp_ledger.jsonl.chainhash",
		"~/.hydra/mcp_policy.json", "internal/ledger/ledger.go", "internal/security/risk.go",
	} {
		if !targetsAuditMachinery(hit) {
			t.Errorf("%q not recognised as audit machinery", hit)
		}
	}
	for _, miss := range []string{"internal/dispatch/dispatch.go", "README.md", "cmd/hydra/main.go", ""} {
		if targetsAuditMachinery(miss) {
			t.Errorf("%q wrongly flagged as audit machinery", miss)
		}
	}
}
