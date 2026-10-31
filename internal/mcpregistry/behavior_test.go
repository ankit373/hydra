// SPDX-License-Identifier: MIT

package mcpregistry

import (
	"testing"

	"github.com/ankit373/hydra/internal/ledger"
)

func TestBehaviorProfiles_IgnoresNonMCPTools(t *testing.T) {
	profiles := BehaviorProfiles([]ledger.Event{
		{Tool: "shell", Action: ledger.Exec},
		{Tool: "fs.read", Action: ledger.Read},
	})
	if len(profiles) != 0 {
		t.Errorf("non-MCP tools should not produce a profile, got %v", profiles)
	}
}

func TestBehaviorProfiles_GroupsByAliasCaseInsensitively(t *testing.T) {
	profiles := BehaviorProfiles([]ledger.Event{
		{Tool: "mcp__Grafana__query", Action: ledger.Read},
		{Tool: "mcp__grafana__other_tool", Action: ledger.Network},
	})
	profile, ok := profiles["grafana"]
	if !ok {
		t.Fatal("expected a merged profile under the lowercase alias")
	}
	if !profile[ledger.Read] || !profile[ledger.Network] {
		t.Errorf("expected both actions recorded, got %v", profile)
	}
}

func TestBehaviorClassification_FlagsANovelAction(t *testing.T) {
	history := []ledger.Event{
		{Tool: "mcp__fetch__get", Action: ledger.Read},
		{Tool: "mcp__fetch__get", Action: ledger.Read},
	}
	class, ok := BehaviorClassification(history, "mcp__fetch__send", ledger.Network)
	if !ok || class != ClassMCPBehaviorChange {
		t.Fatalf("got (%q, %v), want (%q, true), Network has never appeared before for this server", class, ok, ClassMCPBehaviorChange)
	}
}

func TestBehaviorClassification_DoesNotFlagARepeatedAction(t *testing.T) {
	history := []ledger.Event{
		{Tool: "mcp__fetch__get", Action: ledger.Read},
	}
	if _, ok := BehaviorClassification(history, "mcp__fetch__get_again", ledger.Read); ok {
		t.Error("a previously-seen action type should not be flagged")
	}
}

func TestBehaviorClassification_DoesNotFlagAServersFirstEverCall(t *testing.T) {
	// No history at all for "brand-new", everything would technically be
	// "novel" for a server with zero prior calls, which is meaningless noise,
	// not a signal. Must not flag.
	if _, ok := BehaviorClassification(nil, "mcp__brand-new__anything", ledger.Network); ok {
		t.Error("a server's first-ever call has nothing to compare against and must not be flagged")
	}
}

func TestBehaviorClassification_IgnoresNonMCPTools(t *testing.T) {
	history := []ledger.Event{{Tool: "shell", Action: ledger.Read}}
	if _, ok := BehaviorClassification(history, "shell", ledger.Network); ok {
		t.Error("a non-MCP tool name should never be classified by this check")
	}
}

func TestBehaviorClassification_ThePostmarkMCPShape(t *testing.T) {
	// The concrete scenario this feature exists for: a server that has only
	// ever performed Read actions (fetching/composing email content)
	// suddenly performs a Network action (the BCC exfiltration), this is
	// exactly postmark-mcp's real 15-clean-versions-then-malicious shape,
	// catchable from local ledger history alone.
	history := make([]ledger.Event, 15)
	for i := range history {
		history[i] = ledger.Event{Tool: "mcp__postmark__send_email", Action: ledger.Read}
	}
	class, ok := BehaviorClassification(history, "mcp__postmark__send_email", ledger.Network)
	if !ok || class != ClassMCPBehaviorChange {
		t.Fatalf("the postmark-mcp shape should be caught: got (%q, %v)", class, ok)
	}
}
