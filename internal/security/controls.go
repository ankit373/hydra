// SPDX-License-Identifier: MIT

package security

import (
	"fmt"
	"path/filepath"

	"github.com/ankit373/hydra/internal/a2a"
	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/ledger"
	"github.com/ankit373/hydra/internal/policy"
)

// Does a configured control actually run? A control that is declared but
// cannot fire reads as protection while doing nothing (see #424, #425). Answered
// by reading call sites, not config, hence Control.Verified.

// Control is one security control and whether it can actually fire.
type Control struct {
	Name string `json:"name"`
	// Declared: the control is configured, or its code exists.
	Declared bool `json:"declared"`
	// Wired: it can actually take effect at runtime.
	Wired bool `json:"wired"`
	// Limited marks a control that fires but is weaker than it appears,
	// distinct from inert, and it must not be reported as simply healthy.
	Limited bool   `json:"limited,omitempty"`
	Detail  string `json:"detail"`
	// Verified separates a fact checked at runtime from one established by
	// reading the source. A dashboard that blurs those two is guessing, and
	// the reader deserves to know which kind of claim each row is.
	Verified bool `json:"verified"`
}

// Status is the one-word state used by every surface, so the CLI, TUI and
// desktop cannot describe the same control differently.
func (c Control) Status() string {
	switch {
	case !c.Declared:
		return "absent"
	case !c.Wired:
		return "inert"
	case c.Limited:
		return "limited"
	default:
		return "active"
	}
}

// InertControls counts controls that are configured but cannot fire.
func InertControls(cs []Control) int {
	n := 0
	for _, c := range cs {
		if c.Declared && !c.Wired {
			n++
		}
	}
	return n
}

// Controls audits whether each declared control can actually take effect.
func Controls(events []ledger.Event, audit PolicyAudit, chain ledger.ChainResult) []Control {
	return []Control{
		filePolicyControl(),
		a2aConflictControl(),
		boundApprovalControl(events),
		ledgerRuleControl(audit),
		chainControl(chain),
	}
}

// filePolicyEnforcementSite names the one call site that evaluates
// registry/policy.yaml, and the fact that it throws the result away.
//
// This is the only claim in this file that cannot be checked at runtime: no
// amount of introspection tells a running binary whether its caller used a
// return value. It was established by reading the source, is reported with
// Verified:false so nobody mistakes it for an observation, and MUST be
// updated by hand if the wiring lands, the same deliberate-drift discipline
// desktop/api/dashboard_test.go already uses for its duplicated tier key.
const filePolicyEnforcementSite = "internal/parallel/parallel.go:242"

func filePolicyControl() Control {
	c := Control{Name: "File-policy caps", Verified: false}
	eng, err := policy.LoadFilePolicy(config.ScriptHome())
	if err != nil {
		c.Detail = fmt.Sprintf("registry/policy.yaml could not be loaded: %v", err)
		return c
	}
	n := eng.RuleCount()
	c.Declared = n > 0
	if n == 0 {
		c.Detail = "registry/policy.yaml declares no rules"
		return c
	}
	c.Detail = fmt.Sprintf("%d rule(s) declared (cost ceilings, diff-size caps, atomic writes, "+
		"worktree isolation), none are applied: the only caller evaluates them and discards the "+
		"result at %s, so no cap takes effect at runtime", n, filePolicyEnforcementSite)
	return c
}

// a2aConflictControl checks the handoff dispatch actually writes, so this one
// is real evidence rather than a source-derived claim: if the persisted
// handoff carries no Files, ConflictsWith cannot fire on it whatever the
// code says.
func a2aConflictControl() Control {
	c := Control{
		Name:     "A2A concurrent-edit detection",
		Declared: true, // a2a.ConflictsWith exists
		Verified: true,
	}
	path := filepath.Join(config.Dir(), "logs", "last_handoff.json")
	h, err := a2a.Load(path)
	if err != nil || h == nil {
		// Nothing observed either way. Reported as limited rather than active
		//, claiming a control is working on the strength of never having
		// seen it run is the kind of false assurance this section exists to
		// remove, but not as inert, which would assert a defect just as
		// baselessly.
		c.Wired, c.Limited = true, true
		c.Detail = "no handoff has been written yet, so whether conflict detection can fire is undetermined"
		return c
	}
	if len(h.Files) == 0 {
		c.Detail = "the last handoff carries no file list, and ConflictsWith needs one, " +
			"concurrent edits to the same file cannot be detected"
		return c
	}
	c.Wired = true
	c.Detail = fmt.Sprintf("the last handoff lists %d file(s), so overlapping concurrent edits are detectable", len(h.Files))
	return c
}

// boundApprovalControl reports the parameter-binding gate, which works but is
// weaker than it looks: an approval is never consumed and never expires.
func boundApprovalControl(events []ledger.Event) Control {
	c := Control{Name: "Parameter-bound approvals", Declared: true, Wired: true, Verified: true}
	var bound int
	oldest := ""
	for _, e := range events {
		if e.ParametersHash == "" || e.Decision != ledger.Allow {
			continue
		}
		bound++
		if oldest == "" || (e.TS != "" && e.TS < oldest) {
			oldest = e.TS
		}
	}
	if bound == 0 {
		// Same reasoning as the A2A control: never having been exercised is
		// not evidence that it works.
		c.Limited = true
		c.Detail = "no approval has been bound to a parameter hash yet, so the binding is unexercised"
		return c
	}
	c.Limited = true
	c.Detail = fmt.Sprintf("%d bound approval(s), oldest %s, an approval is never consumed and "+
		"never expires, so any one of them can authorise unlimited later executions", bound, oldest)
	return c
}

func ledgerRuleControl(a PolicyAudit) Control {
	c := Control{Name: "Ledger access rules", Declared: len(a.Rules) > 0, Wired: true, Verified: true}
	if len(a.Rules) == 0 {
		c.Detail = "no access rules are defined"
		return c
	}
	unreachable := a.ShadowedCount()
	if unreachable > 0 {
		c.Limited = true
		c.Detail = fmt.Sprintf("%d of %d rule(s) are unreachable, an earlier rule always matches first, "+
			"so they can never fire", unreachable, len(a.Rules))
		return c
	}
	c.Detail = fmt.Sprintf("%d rule(s), all reachable", len(a.Rules))
	return c
}

// chainControl reports whether tamper-evidence *works*, not whether tampering
// happened, the tampering itself is the chain check's and IntegrityIntact's
// job. A chain that detected a break is a control doing exactly what it is
// for, so it stays wired; the one genuinely degraded state is a missing
// anchor, where deletion from the end can no longer be ruled out.
func chainControl(chain ledger.ChainResult) Control {
	c := Control{Name: "Audit-log tamper evidence", Declared: true, Wired: true, Verified: true}
	switch {
	case chain.Chained == 0:
		c.Detail = "no chained events yet, nothing to verify"
	case chain.Truncated:
		c.Detail = "working: it detected that records were deleted from the end of the ledger"
	case !chain.Intact:
		c.Detail = fmt.Sprintf("working: it detected modification after recording (first break at index %d)", chain.BrokenAt)
	case chain.AnchorMissing:
		c.Limited = true
		c.Detail = "events verify, but the chain anchor is missing, so deletion from the end cannot be ruled out"
	default:
		c.Detail = fmt.Sprintf("%d chained event(s) verify, and the anchor confirms nothing was removed", chain.Chained)
	}
	return c
}
