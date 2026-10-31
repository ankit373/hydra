// SPDX-License-Identifier: MIT

package security

import (
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/ledger"
)

// An attestation is only worth something if a recipient can check it without
// trusting the sender.
func TestAttestation_DigestDetectsAlteration(t *testing.T) {
	a := Attest(&Report{Posture: Posture{Verdict: VerdictOK}}, ledger.ChainResult{Intact: true}, time.Now())
	if !VerifyAttestation(a) {
		t.Fatal("a freshly produced attestation failed its own verification")
	}
	a.Verdict = VerdictActNow // someone edits the claim
	if VerifyAttestation(a) {
		t.Error("an altered attestation still verified, the digest covers nothing")
	}
}

// "No break found" over a log with no hashes is the absence of a test, not
// integrity. Reporting that as trustworthy would be the exact overclaiming
// this package refuses everywhere else.
func TestAttestation_UnchainedEvidenceIsNotTrustworthy(t *testing.T) {
	a := Attest(&Report{Ledger: LedgerPanel{Total: 10}},
		ledger.ChainResult{Intact: true, Chained: 0}, time.Now())
	if a.Trustworthy() {
		t.Error("10 events with 0 hash-chained reported as trustworthy")
	}
	if !strings.Contains(ExecutiveSummary(a), "no hash chain at all") {
		t.Error("the executive summary did not warn that the evidence is unverifiable")
	}

	// An empty ledger is a different case: nothing to verify, nothing claimed.
	empty := Attest(&Report{}, ledger.ChainResult{Intact: true}, time.Now())
	if !empty.Trustworthy() {
		t.Error("an empty ledger was reported untrustworthy; there is nothing to distrust")
	}
}

// A truncated log must surface in the attestation, not be smoothed over.
func TestAttestation_TruncationSurfaces(t *testing.T) {
	a := Attest(&Report{Ledger: LedgerPanel{Total: 5}},
		ledger.ChainResult{Chained: 5, Truncated: true}, time.Now())
	if a.Trustworthy() {
		t.Error("a truncated ledger reported as trustworthy")
	}
	if !a.Evidence.Truncated {
		t.Error("truncation was not recorded in the attestation's evidence")
	}
}
