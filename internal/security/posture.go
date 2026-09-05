// SPDX-License-Identifier: MIT

package security

import (
	"fmt"

	"github.com/ankit373/hydra/internal/ledger"
)

// One line, and what decided it.
//
// Ten checks is an engineer's output. The first question is "do I need to do
// something today", and it needs a single answer that can be defended, which
// is not the same as a single number. So this is a state machine over facts:
// each verdict names the exact condition that produced it, and every condition
// that fired is listed so the answer can be argued with rather than trusted.
// Nothing here is weighted or averaged.

// Verdict is the top-line state.
type Verdict string

const (
	VerdictActNow    Verdict = "act now"
	VerdictAttention Verdict = "attention"
	VerdictOK        Verdict = "ok"
)

// Posture is the answer plus its reasoning.
type Posture struct {
	Verdict Verdict `json:"verdict"`
	// Trigger is the single most severe condition that fired, the reason.
	Trigger string `json:"trigger"`
	// Because lists every condition that fired, most severe first.
	Because []string `json:"because,omitempty"`
	// Checked names what was evaluated, so an "ok" verdict states its scope
	// rather than implying everything imaginable was verified.
	Checked []string `json:"checked"`
}

// AssessPosture decides the verdict from conditions already computed
// elsewhere; it detects nothing of its own.
func AssessPosture(r *Report, chain ledger.ChainResult) Posture {
	p := Posture{Verdict: VerdictOK, Checked: []string{
		"audit-log integrity", "sensitive-data exposure", "correlated incidents",
		"control effectiveness", "access policy posture",
	}}

	var critical, warning []string

	// ── conditions that demand action today ──────────────────────────────
	switch {
	case chain.Truncated:
		critical = append(critical, "the audit ledger was truncated, records were deleted from the end")
	case !chain.Intact && chain.Chained > 0:
		critical = append(critical, fmt.Sprintf("the audit ledger was modified after recording (index %d)", chain.BrokenAt))
	}
	if n := ConfirmedRemote(r.Exposures); n > 0 {
		critical = append(critical, fmt.Sprintf("%d sensitive access(es) reached a remote head", n))
	}
	for _, in := range r.Incidents {
		if in.Severity == SeverityCritical {
			critical = append(critical, "critical incident, "+in.Narrative)
		}
	}
	if n := r.Register.Breached; n > 0 {
		critical = append(critical, fmt.Sprintf("%d risk(s) are past their remediation SLA", n))
	}

	// ── conditions worth attention, not alarm ────────────────────────────
	for _, in := range r.Incidents {
		if in.Severity == SeverityHigh {
			warning = append(warning, "high-severity incident, "+in.Narrative)
		}
	}
	if n := InertControls(r.Controls); n > 0 {
		warning = append(warning, fmt.Sprintf("%d control(s) are configured but cannot fire", n))
	}
	// A fail-open default only matters once something is actually being
	// denied: on a machine with no traffic it is a default, not a finding.
	if r.PolicyAudit.FailOpen && r.Ledger.Denied > 0 {
		warning = append(warning, "the access policy is fail-open while denials are occurring")
	}
	if r.SupplyChain.Changed > 0 {
		warning = append(warning, fmt.Sprintf("%d head binary(ies) changed since last seen", r.SupplyChain.Changed))
	}

	switch {
	case len(critical) > 0:
		p.Verdict, p.Trigger = VerdictActNow, critical[0]
		p.Because = append(critical, warning...)
	case len(warning) > 0:
		p.Verdict, p.Trigger = VerdictAttention, warning[0]
		p.Because = warning
	default:
		p.Trigger = "no condition fired across the checks listed"
	}
	return p
}
