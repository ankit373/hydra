// SPDX-License-Identifier: MIT

package security

import (
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/ledger"
	"github.com/ankit373/hydra/internal/provider"
)

// The least-privilege finding is "this agent changes state and no rule names
// it", so the two halves that matter are the scoped/unscoped split and the
// ordering that puts the consequential agent first.

func TestReviewPrivilege_UnscopedAgentThatMutatesRanksFirst(t *testing.T) {
	pol := ledger.Policy{Rules: []ledger.Rule{{Agent: "scribe", Decision: ledger.Allow}}}
	events := []ledger.Event{
		{Agent: "scribe", Tool: "gpt", Resource: "a.go", Action: ledger.Read, Decision: ledger.Allow},
		{Agent: "rogue", Tool: "gpt", Resource: "b.go", Action: ledger.Write, Decision: ledger.Allow},
		{Agent: "rogue", Tool: "gpt", Resource: "c.sh", Action: ledger.Exec, Decision: ledger.Deny},
	}

	got := ReviewPrivilege(events, pol)
	if len(got) != 2 {
		t.Fatalf("got %d agents, want 2", len(got))
	}
	if got[0].Agent != "rogue" {
		t.Errorf("first row is %q; the unscoped state-changing agent should lead", got[0].Agent)
	}
	if !got[0].Unscoped {
		t.Error("rogue is named by no rule, so it must be Unscoped")
	}
	if got[0].WritesOrExecs != 2 {
		t.Errorf("WritesOrExecs = %d, want 2 (one write, one exec)", got[0].WritesOrExecs)
	}
	if got[0].Denied != 1 || got[0].Allowed != 1 {
		t.Errorf("allowed/denied = %d/%d, want 1/1", got[0].Allowed, got[0].Denied)
	}
	for _, p := range got {
		if p.Agent == "scribe" && p.Unscoped {
			t.Error("scribe is named by a rule and must not be reported as unscoped")
		}
	}
}

// A wildcard rule scopes nothing: it is the default wearing a rule's clothes.
func TestReviewPrivilege_WildcardRuleDoesNotCountAsScoping(t *testing.T) {
	pol := ledger.Policy{Rules: []ledger.Rule{{Agent: "*", Decision: ledger.Allow}}}
	events := []ledger.Event{
		{Agent: "any", Tool: "gpt", Resource: "b.go", Action: ledger.Write, Decision: ledger.Allow},
	}
	got := ReviewPrivilege(events, pol)
	if len(got) != 1 || !got[0].Unscoped {
		t.Error(`a rule with Agent:"*" scopes nothing, so the agent is still unscoped`)
	}
}

func TestReviewPrivilege_IgnoresEventsWithNoAgent(t *testing.T) {
	events := []ledger.Event{{Tool: "gpt", Resource: "a.go", Action: ledger.Read, Decision: ledger.Allow}}
	if got := ReviewPrivilege(events, ledger.Policy{}); len(got) != 0 {
		t.Errorf("got %d rows from an event naming no agent, want 0", len(got))
	}
}

func TestPrivilegeCheck_SaysNothingRatherThanNothingFound(t *testing.T) {
	c := privilegeCheck(nil)
	if c.Status != "no agent activity" {
		t.Errorf("status = %q; an empty ledger is not a clean entitlement review", c.Status)
	}
}

// The BOM is an inventory with provenance, so "installed" and "actually used"
// must stay distinguishable, and a local head must never read as remote.
func TestBuildBOM_MarksUsageOriginAndLocality(t *testing.T) {
	heads := []provider.Head{
		{ID: "ollama/qwen", Name: "Qwen", Provider: "ollama", Source: "port", LocalOnly: true},
		{ID: "api/gpt", Name: "GPT", Provider: "openai", Source: "env",
			Meta: map[string]string{"model_source": "user"}},
	}
	events := []ledger.Event{{Agent: "a", Tool: "api/gpt", Action: ledger.Read, Decision: ledger.Allow}}
	sc := SupplyChain{Binaries: []HeadBinary{{HeadID: "api/gpt", SHA256: "deadbeef"}}}

	bom := BuildBOM(heads, events, sc)
	if len(bom) != 2 {
		t.Fatalf("got %d entries, want 2", len(bom))
	}
	byID := map[string]BOMEntry{}
	for _, b := range bom {
		byID[b.HeadID] = b
	}
	if !byID["ollama/qwen"].Local {
		t.Error("a LocalOnly head must be reported Local, or it reads as an egress path")
	}
	if byID["ollama/qwen"].Used {
		t.Error("nothing in the ledger drove ollama/qwen, so Used must be false")
	}
	if !byID["api/gpt"].Used {
		t.Error("api/gpt appears as a Tool in the ledger, so Used must be true")
	}
	if byID["api/gpt"].Origin != "user" {
		t.Errorf("Origin = %q, want %q, a runtime-added model is not from the curated catalog",
			byID["api/gpt"].Origin, "user")
	}
	if byID["api/gpt"].Fingerprint != "deadbeef" {
		t.Errorf("Fingerprint = %q, want the supply-chain hash", byID["api/gpt"].Fingerprint)
	}
}

func TestBomCheck_CountsRemoteHeadsAndRuntimeAdditions(t *testing.T) {
	bom := []BOMEntry{
		{HeadID: "local", Local: true, Used: true},
		{HeadID: "remote", Local: false, Origin: "user"},
	}
	c := bomCheck(bom)
	if c.Status != "2 head(s), 1 remote" {
		t.Errorf("status = %q, want %q", c.Status, "2 head(s), 1 remote")
	}
	if !strings.Contains(c.Detail, "added at runtime") {
		t.Errorf("detail %q should name the runtime-added head", c.Detail)
	}
}
