// SPDX-License-Identifier: MIT

package security

import (
	"fmt"
	"path/filepath"
	"strings"

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
		signalRulesControl(),
		a2aConflictControl(),
		boundApprovalControl(events),
		ledgerRuleControl(audit),
		chainControl(chain),
	}
}

// filePolicyEnforcementSite names where registry/policy.yaml's caps are
// applied. Which caps take effect comes from policy.EnforcedKeys, whose tags a
// policy test checks against the fields callers read. Reading the source is
// not a runtime check, so this control stays Verified:false.
const filePolicyEnforcementSite = "internal/policy/caps.go"

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
	// Wired, and deliberately Limited: the caps an operator is most likely to
	// be relying on are applied, and several fields still are not. Reporting
	// this as fully active would overstate it in the other direction.
	c.Wired, c.Limited = true, true
	enforced, all := policy.EnforcedKeys(), len(policy.Keys())
	c.Detail = fmt.Sprintf("%d rule(s) declared, and %d of %d policy fields take effect (%s). From %s, "+
		"an over-large diff is rolled back, a head over the cost ceiling is refused before it runs, "+
		"and the wall-clock limit deadlines the dispatch and the validator after it. The memory and "+
		"CPU limits cap a subprocess head through internal/sandbox. Both "+
		"`hyctl edit` and `hyctl parallel` enforce them, so a refusal reads the same "+
		"either way (#769). prompt_cache reaches every dispatch, whatever the kind of work, and asks "+
		"a provider that caches only on request to cache the stable start of the prompt. "+
		"max_retries and escalate_on_fail set how many stronger heads a failed "+
		"`hyctl dispatch --check` may ask (#1587). The other %d are declared and read by nothing",
		n, len(enforced), all, strings.Join(enforced, ", "),
		filePolicyEnforcementSite, all-len(enforced))
	// A condition naming a field the evaluator does not know can never be
	// satisfied, so the rule around it is not merely unexercised, it is dead.
	// Said separately from the field count above because the remedy differs: a
	// field nothing reads is Hydra's gap, a condition nothing can match is a
	// typo in the operator's own file (#854).
	if dead := eng.DeadConditions(); len(dead) > 0 {
		names := make([]string, 0, len(dead))
		for _, d := range dead {
			names = append(names, d.String())
		}
		c.Detail += fmt.Sprintf(". %d condition(s) can never match, so their rule never fires: %s",
			len(dead), strings.Join(names, "; "))
	}
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
