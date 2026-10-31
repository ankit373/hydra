// SPDX-License-Identifier: MIT

package security

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ankit373/hydra/internal/build"
	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/ledger"
)

// What was true, under which rules (config.Breadcrumb), over which evidence
// (the chained ledger), with a digest so two copies can be compared without
// trusting either holder. Deliberately unsigned: Hydra has no key management.

// Attestation is a point-in-time, checkable statement of posture.
type Attestation struct {
	GeneratedAt string `json:"generatedAt"`
	// Tool identifies the binary that produced this.
	Tool    string `json:"tool"`
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`

	// ConfigFingerprint is the deployment breadcrumb: which routing/policy
	// files were in force when these claims were computed.
	ConfigFingerprint string `json:"configFingerprint,omitempty"`

	// Evidence describes the ledger the claims rest on.
	Evidence AttestedEvidence `json:"evidence"`

	// Verdict and the register summary are the claims themselves.
	Verdict  Verdict `json:"verdict"`
	Trigger  string  `json:"trigger"`
	Coverage float64 `json:"coveragePercent"`

	OpenRisks  int              `json:"openRisks"`
	BySeverity map[Severity]int `json:"bySeverity"`
	Breached   int              `json:"slaBreached"`
	Incidents  int              `json:"incidents"`

	// Frameworks is the per-framework count of open risks, so a reader can
	// see the claim from their own standard's point of view.
	Frameworks map[string]int `json:"frameworks,omitempty"`

	// Digest is sha256 over every field above, so two copies of this
	// attestation can be compared without trusting either holder.
	Digest string `json:"digest"`
}

// AttestedEvidence is the state of the underlying audit log.
type AttestedEvidence struct {
	Events        int  `json:"events"`
	ChainedEvents int  `json:"chainedEvents"`
	ChainIntact   bool `json:"chainIntact"`
	// Truncated and AnchorMissing are reported rather than hidden: an
	// attestation over an unverifiable log must say so, or it is worse than
	// no attestation at all.
	Truncated     bool `json:"truncated,omitempty"`
	AnchorMissing bool `json:"anchorMissing,omitempty"`
}

// Attest produces the checkable statement.
func Attest(r *Report, chain ledger.ChainResult, now time.Time) Attestation {
	a := Attestation{
		GeneratedAt: now.UTC().Format(time.RFC3339),
		Tool:        "hyctl security",
		Version:     build.Version,
		Commit:      build.Commit,
		Evidence: AttestedEvidence{
			Events:        r.Ledger.Total,
			ChainedEvents: chain.Chained,
			ChainIntact:   chain.Intact,
			Truncated:     chain.Truncated,
			AnchorMissing: chain.AnchorMissing,
		},
		Verdict:    r.Posture.Verdict,
		Trigger:    r.Posture.Trigger,
		Coverage:   r.Coverage.PercentCovered,
		BySeverity: r.Register.BySeverity,
		Breached:   r.Register.Breached,
		Incidents:  len(r.Incidents),
		Frameworks: FrameworkExposure(r.Register),
	}
	for _, k := range r.Register.Risks {
		if k.Status == StatusOpen {
			a.OpenRisks++
		}
	}
	if bc, err := config.Breadcrumb(); err == nil {
		a.ConfigFingerprint = bc
	}
	a.Digest = digestAttestation(a)
	return a
}

// digestAttestation hashes the attestation with Digest cleared, so the value
// covers every claim without covering itself.
func digestAttestation(a Attestation) string {
	a.Digest = ""
	raw, err := json.Marshal(a)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// VerifyAttestation recomputes the digest and reports whether the document is
// internally consistent, the check a recipient runs.
func VerifyAttestation(a Attestation) bool {
	return a.Digest != "" && digestAttestation(a) == a.Digest
}

// Trustworthy reports whether the evidence underneath the attestation can be
// relied on at all. A truncated or unanchored ledger does not invalidate the
// document; it invalidates the claims, and the two must be distinguishable.
//
// Events with no chain coverage count as unverifiable. "No break was found"
// over a log that carries no hashes is not integrity, it is the absence of a
// test, and reporting it as clean would be the exact overclaiming this
// package refuses everywhere else.
func (a Attestation) Trustworthy() bool {
	if a.Evidence.Events > 0 && a.Evidence.ChainedEvents == 0 {
		return false
	}
	return a.Evidence.ChainIntact && !a.Evidence.Truncated && !a.Evidence.AnchorMissing
}

// ExecutiveSummary renders the attestation as the short narrative a board or
// auditor reads, rather than the operator's console.
func ExecutiveSummary(a Attestation) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Security posture: %s\n", strings.ToUpper(string(a.Verdict)))
	fmt.Fprintf(&b, "  %s\n\n", a.Trigger)

	fmt.Fprintf(&b, "Open risks: %d", a.OpenRisks)
	if a.Breached > 0 {
		fmt.Fprintf(&b, " (%d past remediation SLA)", a.Breached)
	}
	fmt.Fprintf(&b, "\n")
	for _, s := range []Severity{SeverityCritical, SeverityHigh, SeverityMedium, SeverityLow} {
		if n := a.BySeverity[s]; n > 0 {
			fmt.Fprintf(&b, "  %-8s %d\n", s, n)
		}
	}
	fmt.Fprintf(&b, "\nCorrelated incidents: %d\n", a.Incidents)
	fmt.Fprintf(&b, "OWASP LLM Top-10 coverage: %.0f%%\n", a.Coverage)

	if len(a.Frameworks) > 0 {
		fmt.Fprintf(&b, "\nOpen risks by framework:\n")
		for _, f := range Frameworks() {
			if n := a.Frameworks[f]; n > 0 {
				fmt.Fprintf(&b, "  %-16s %d\n", f, n)
			}
		}
	}

	fmt.Fprintf(&b, "\nEvidence: %d ledger event(s), %d hash-chained.\n",
		a.Evidence.Events, a.Evidence.ChainedEvents)
	if !a.Trustworthy() {
		// Stated first-class rather than footnoted: an attestation over an
		// unverifiable log is the one thing a reader must not miss.
		reason := "could not be fully verified"
		if a.Evidence.Events > 0 && a.Evidence.ChainedEvents == 0 {
			reason = "carries no hash chain at all, so it is not tamper-evident"
		}
		fmt.Fprintf(&b, "  WARNING: the audit log %s, so the claims\n"+
			"  above rest on evidence that cannot be independently checked.\n", reason)
	}
	if a.ConfigFingerprint != "" {
		fmt.Fprintf(&b, "Rules in force: %s\n", a.ConfigFingerprint[:12])
	}
	fmt.Fprintf(&b, "Attestation digest: %s\n", a.Digest[:16])
	return b.String()
}
