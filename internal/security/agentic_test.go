// SPDX-License-Identifier: MIT

package security

import (
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/ledger"
	"github.com/ankit373/hydra/internal/testutil"
)

func agenticByID(cov AgenticCoverage) map[string]Category {
	out := make(map[string]Category, len(cov.Categories))
	for _, c := range cov.Categories {
		out[c.ID] = c
	}
	return out
}

// All ten are assessed, none silently dropped, and every one carries a real
// detail. A category with a status and no explanation is a number nobody can
// act on, which is the thing this package exists to avoid.
func TestComputeAgentic_AssessesAllTenWithEvidence(t *testing.T) {
	testutil.NewSandbox(t)

	cov := computeAgentic(ledger.Policy{}, SupplyChain{}, true)
	if len(cov.Categories) != 10 {
		t.Fatalf("assessed %d categories, want 10", len(cov.Categories))
	}
	for i, c := range cov.Categories {
		want := "ASI" + [...]string{"01", "02", "03", "04", "05", "06", "07", "08", "09", "10"}[i]
		if c.ID != want {
			t.Errorf("category %d is %s, want %s", i, c.ID, want)
		}
		if c.Status == "" || strings.TrimSpace(c.Detail) == "" {
			t.Errorf("%s carries no status or no detail: %+v", c.ID, c)
		}
	}
}

// ASI03 is the one Phase 2 actually closed: a head subprocess used to inherit
// every provider's credential and now receives only its own.
func TestComputeAgentic_IdentityAbuseIsEnforced(t *testing.T) {
	testutil.NewSandbox(t)

	got := agenticByID(computeAgentic(ledger.Policy{}, SupplyChain{}, true))["ASI03"]
	if got.Status != Enforced {
		t.Errorf("ASI03 = %q, want %q (%s)", got.Status, Enforced, got.Detail)
	}
}

// ASI02 is the ledger gate, and a fail-open policy is not a gate. The three
// states have to be distinguishable or the score rewards a permissive policy.
func TestComputeAgentic_ToolMisuseTracksThePolicyDefault(t *testing.T) {
	testutil.NewSandbox(t)

	noPolicy := agenticByID(computeAgentic(ledger.Policy{}, SupplyChain{}, true))["ASI02"]
	if noPolicy.Status != Gap {
		t.Errorf("with no rules and default allow: ASI02 = %q, want %q", noPolicy.Status, Gap)
	}

	failOpen := ledger.Policy{Default: ledger.Allow, Rules: []ledger.Rule{{Decision: ledger.Deny, Classification: "x"}}}
	if got := agenticByID(computeAgentic(failOpen, SupplyChain{}, true))["ASI02"]; got.Status != Partial {
		t.Errorf("with rules over default allow: ASI02 = %q, want %q", got.Status, Partial)
	}

	closed := ledger.Policy{Default: ledger.Deny, Rules: []ledger.Rule{{Decision: ledger.Allow, Tool: "ollama"}}}
	if got := agenticByID(computeAgentic(closed, SupplyChain{}, true))["ASI02"]; got.Status != Configured {
		t.Errorf("with a default-deny policy: ASI02 = %q, want %q", got.Status, Configured)
	}
}

// A broken chain means the record that would show an agent acting outside
// policy cannot be trusted, so ASI10 cannot claim anything from it.
func TestComputeAgentic_RogueAgentsFallsToGapOnABrokenChain(t *testing.T) {
	testutil.NewSandbox(t)

	sc := SupplyChain{Binaries: []HeadBinary{{HeadID: "claude", SHA256: "abc"}}}
	if got := agenticByID(computeAgentic(ledger.Policy{}, sc, true))["ASI10"]; got.Status != Configured {
		t.Errorf("with an intact chain: ASI10 = %q, want %q", got.Status, Configured)
	}
	if got := agenticByID(computeAgentic(ledger.Policy{}, sc, false))["ASI10"]; got.Status != Gap {
		t.Errorf("with a broken chain: ASI10 = %q, want %q", got.Status, Gap)
	}
}

// An unreviewed artifact change is the rug-pull signal, so it must show up
// rather than being absorbed into an otherwise-clean Configured.
func TestComputeAgentic_AChangedArtifactDowngradesRogueAgents(t *testing.T) {
	testutil.NewSandbox(t)

	sc := SupplyChain{Binaries: []HeadBinary{{HeadID: "claude", SHA256: "abc", Changed: true}}, Changed: 1}
	got := agenticByID(computeAgentic(ledger.Policy{}, sc, true))["ASI10"]
	if got.Status != Partial {
		t.Errorf("ASI10 = %q with a changed artifact, want %q", got.Status, Partial)
	}
	if !strings.Contains(got.Detail, "changed") {
		t.Errorf("the detail does not name the change: %q", got.Detail)
	}
}

// Partial must not count as covered. It is the whole reason the status exists:
// a detective-only control reporting as coverage is what #722 removed.
func TestComputeAgentic_PartialIsNotCounted(t *testing.T) {
	testutil.NewSandbox(t)

	cov := computeAgentic(ledger.Policy{}, SupplyChain{}, true)
	if cov.Partial == 0 {
		t.Fatal("expected at least one partial category to exercise the arithmetic")
	}
	if cov.Covered+cov.Partial > cov.Applicable {
		t.Errorf("covered(%d) + partial(%d) exceeds applicable(%d)", cov.Covered, cov.Partial, cov.Applicable)
	}
	want := 100 * float64(cov.Covered) / float64(cov.Applicable)
	if cov.PercentCovered != want {
		t.Errorf("PercentCovered = %.2f, want %.2f: partial must not count as covered", cov.PercentCovered, want)
	}
}
