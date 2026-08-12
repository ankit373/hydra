// SPDX-License-Identifier: MIT

package security

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/a2a"
	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/ledger"
	"github.com/ankit373/hydra/internal/testutil"
)

func findControl(t *testing.T, cs []Control, name string) Control {
	t.Helper()
	for _, c := range cs {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no control named %q in %+v", name, cs)
	return Control{}
}

// The file-policy caps are the reason this whole section exists: policy.yaml
// declares cost ceilings and diff-size limits that no runtime path applies.
func TestControls_FilePolicyIsDeclaredButInert(t *testing.T) {
	testutil.NewSandbox(t)

	c := findControl(t, Controls(nil, PolicyAudit{}, ledger.ChainResult{}), "File-policy caps")
	if !c.Declared {
		t.Fatal("the embedded registry/policy.yaml declares rules; Declared = false")
	}
	if c.Wired {
		t.Error("Wired = true, but no runtime path applies the file policy")
	}
	if c.Status() != "inert" {
		t.Errorf("Status() = %q, want inert", c.Status())
	}
	// The finding is only actionable if it names where the decision is lost.
	if !strings.Contains(c.Detail, filePolicyEnforcementSite) {
		t.Errorf("Detail = %q, want it to name the discarding call site", c.Detail)
	}
	// This is the one source-derived claim in the set and must say so.
	if c.Verified {
		t.Error("Verified = true, but 'the caller discards the result' cannot be checked at runtime")
	}
}

// The A2A control is real evidence, not a constant: it reads the handoff
// dispatch actually wrote.
func TestControls_A2AIsInertWhenTheHandoffCarriesNoFiles(t *testing.T) {
	testutil.NewSandbox(t)
	path := filepath.Join(config.Dir(), "logs", "last_handoff.json")

	// Exactly what dispatch writes today: no Files.
	h := &a2a.Handoff{From: "hydra-tier-1", Task: "do a thing"}
	if err := h.Save(path); err != nil {
		t.Fatal(err)
	}

	c := findControl(t, Controls(nil, PolicyAudit{}, ledger.ChainResult{}), "A2A concurrent-edit detection")
	if c.Wired {
		t.Error("Wired = true, but ConflictsWith needs Files and the handoff has none")
	}
	if !c.Verified {
		t.Error("Verified = false, but this was checked against the handoff on disk")
	}
}

func TestControls_A2AIsWiredOnceTheHandoffListsFiles(t *testing.T) {
	testutil.NewSandbox(t)
	path := filepath.Join(config.Dir(), "logs", "last_handoff.json")

	h := &a2a.Handoff{From: "a", Task: "t", Files: []string{"internal/x.go"}}
	if err := h.Save(path); err != nil {
		t.Fatal(err)
	}

	c := findControl(t, Controls(nil, PolicyAudit{}, ledger.ChainResult{}), "A2A concurrent-edit detection")
	if !c.Wired {
		t.Errorf("Wired = false with a file list present: %+v", c)
	}
}

// An approval that never expires and is never consumed still works — it is
// weaker than it looks, which is a third state, not a binary.
func TestControls_BoundApprovalsAreLimitedNotInert(t *testing.T) {
	testutil.NewSandbox(t)
	events := []ledger.Event{
		{TS: "2026-01-02T00:00:00Z", Decision: ledger.Allow, ParametersHash: "h1"},
		{TS: "2026-01-01T00:00:00Z", Decision: ledger.Allow, ParametersHash: "h2"},
		{TS: "2026-01-03T00:00:00Z", Decision: ledger.Deny, ParametersHash: "h3"}, // denials don't authorise
		{TS: "2026-01-04T00:00:00Z", Decision: ledger.Allow},                      // unbound
	}

	c := findControl(t, Controls(events, PolicyAudit{}, ledger.ChainResult{}), "Parameter-bound approvals")
	if !c.Wired || !c.Limited || c.Status() != "limited" {
		t.Errorf("control = %+v, want wired but limited", c)
	}
	if !strings.Contains(c.Detail, "2 bound approval") {
		t.Errorf("Detail = %q, want exactly the 2 allow+bound events counted", c.Detail)
	}
	if !strings.Contains(c.Detail, "2026-01-01") {
		t.Errorf("Detail = %q, want the oldest approval's date", c.Detail)
	}
}

// A chain that caught tampering is a control doing its job — reporting it as
// inert would blame the smoke detector for the fire.
func TestControls_ChainStaysWiredWhenItDetectsTampering(t *testing.T) {
	testutil.NewSandbox(t)

	truncated := findControl(t,
		Controls(nil, PolicyAudit{}, ledger.ChainResult{Chained: 3, Truncated: true}),
		"Audit-log tamper evidence")
	if !truncated.Wired {
		t.Error("the chain detected a truncation and was reported inert")
	}

	// A missing anchor is the genuinely degraded state.
	noAnchor := findControl(t,
		Controls(nil, PolicyAudit{}, ledger.ChainResult{Chained: 3, Intact: true, AnchorMissing: true}),
		"Audit-log tamper evidence")
	if !noAnchor.Limited {
		t.Errorf("control = %+v, want limited when deletion cannot be ruled out", noAnchor)
	}
}

func TestControls_UnreachableLedgerRulesAreLimited(t *testing.T) {
	testutil.NewSandbox(t)
	shadow := 0
	audit := PolicyAudit{Rules: []RuleStat{{Index: 0}, {Index: 1, ShadowedBy: &shadow}}}

	c := findControl(t, Controls(nil, audit, ledger.ChainResult{}), "Ledger access rules")
	if !c.Limited || !strings.Contains(c.Detail, "1 of 2") {
		t.Errorf("control = %+v, want 1 of 2 reported unreachable", c)
	}
}

// An inert control must reach the work queue — this is protection the
// operator believes they already have, so it outranks a known gap.
func TestBuildActions_InertControlRaisesAPriorityNowAction(t *testing.T) {
	controls := []Control{{Name: "File-policy caps", Declared: true, Wired: false, Detail: "d"}}
	actions := buildActions(Coverage{}, nil, nil, PolicyAudit{}, controls, EvidenceQuality{}, ConfigDrift{}, SupplyChain{}, BlastReport{})
	if len(actions) != 1 || actions[0].Priority != PriorityNow || actions[0].Kind != "control" {
		t.Fatalf("actions = %+v, want one PriorityNow control action", actions)
	}
	if !strings.Contains(actions[0].Title, "never runs") {
		t.Errorf("Title = %q, want it to say the control does not run", actions[0].Title)
	}
}

// A control that is wired must not generate noise.
func TestBuildActions_WiredControlsRaiseNothing(t *testing.T) {
	controls := []Control{{Name: "ok", Declared: true, Wired: true}}
	if actions := buildActions(Coverage{}, nil, nil, PolicyAudit{}, controls, EvidenceQuality{}, ConfigDrift{}, SupplyChain{}, BlastReport{}); len(actions) != 0 {
		t.Errorf("actions = %+v, want none for a healthy control", actions)
	}
}
