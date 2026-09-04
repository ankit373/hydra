// SPDX-License-Identifier: MIT

package security

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/ankit373/hydra/internal/ledger"
)

// The desktop looks checks up by name, with an exact string compare
// (findCheckStatus). Two of those names did not exist — the backend emits
// "Sensitive data exposure" and "Policy posture", the view asked for
// "PII/sensitive-data detections" and "Policy adherence" — so both hero cards
// silently never rendered. Nothing was checking the two sides agreed.
//
// dto_test.go guards the shape of the wire and typesparity_test.go guards the
// TypeScript mirror; this guards a name used as a key across the bridge.

// producedNames is every name a Check can carry, collected by calling the
// builders rather than restating a list that could go stale on its own.
func producedNames() map[string]bool {
	checks := []Check{
		chainCheck(ledger.ChainResult{}),
		costCeilingCheck(0),
		provenanceCheck(nil),
		frameworkCheck(ledger.Policy{}),
		exposureCheck(nil),
		policyPostureCheck(PolicyAudit{}),
		evidenceCheck(EvidenceQuality{}),
		driftCheck(ConfigDrift{}),
		supplyChainCheck(SupplyChain{}),
		blastCheck(BlastReport{}),
		incidentCheck(nil),
		privilegeCheck(nil),
		bomCheck(nil),
	}
	out := map[string]bool{}
	for _, c := range checks {
		if c.Name != "" {
			out[c.Name] = true
		}
	}
	return out
}

var lookupRE = regexp.MustCompile(`findCheckStatus\([^,]+,\s*'([^']+)'\)`)

func TestCheckNamesAreReal(t *testing.T) {
	// Same relative-path approach typesparity_test.go uses to reach the mirror.
	path := filepath.Join("..", "..", "desktop", "frontend", "src", "views", "Security.tsx")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read the Audit view: %v", err)
	}

	names := producedNames()
	if len(names) == 0 {
		t.Fatal("no check names collected — the guard would silently pass")
	}

	found := lookupRE.FindAllStringSubmatch(string(raw), -1)
	if len(found) == 0 {
		t.Fatal("no findCheckStatus lookups found — the regex has drifted from the view")
	}
	for _, m := range found {
		if !names[m[1]] {
			t.Errorf("the Audit view looks up a check named %q, which no builder emits — "+
				"findCheckStatus compares exactly, so that card can never render", m[1])
		}
	}
}

// A builder returning an unnamed Check is unreachable by the view, and an
// empty name would also make the guard above vacuous.
func TestEveryCheckIsNamed(t *testing.T) {
	if got := len(producedNames()); got != 13 {
		t.Errorf("collected %d distinct check names, want 13 — a builder was added, "+
			"removed, or returned an unnamed Check", got)
	}
}
