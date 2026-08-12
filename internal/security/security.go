// SPDX-License-Identifier: MIT

// Package security aggregates Hydra's security-relevant data — the ledger's
// accountability trail, per-head risk, and a short list of honest checks —
// into one report. It reuses ledger.Summarize/ByHeadRisk/VerifyChain and
// ledger.Policy.FrameworksCovered rather than reimplementing any of them, the
// same discipline desktop/api/dashboard.go already applies to cost/trust.
package security

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ankit373/hydra/internal/ledger"
	"github.com/ankit373/hydra/internal/provider"
)

// LedgerPanel is the headline accountability figures.
type LedgerPanel struct {
	Total   int `json:"total"`
	Allowed int `json:"allowed"`
	Denied  int `json:"denied"`
	Flagged int `json:"flagged"`
}

// Check is one honest fact about the current security posture — never a
// manufactured score. Status is a short human-readable summary; Detail
// explains it.
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// Report is everything `hyctl security` renders.
type Report struct {
	// HasData is false when the ledger has never recorded an event — the
	// view must render "no data yet", not a wall of zeros indistinguishable
	// from a clean bill of health.
	HasData bool `json:"hasData"`

	Ledger LedgerPanel       `json:"ledger"`
	ByHead []ledger.HeadRisk `json:"byHead"`
	Checks []Check           `json:"checks"`

	// Coverage is Hydra's posture against the OWASP LLM Top 10 — the score.
	Coverage Coverage `json:"coverage"`
	// IntegrityIntact is false when VerifyChain found tampering — a hard
	// override on Coverage's own percentage, since a tampered ledger means
	// none of the other evidence in this report can be trusted (mirrors SSL
	// Labs' pattern of a single catastrophic flaw capping an otherwise-decent
	// weighted grade).
	IntegrityIntact bool `json:"integrityIntact"`
	// Trend compares Coverage against the first-ever recorded run, when history exists.
	Trend Trend `json:"trend"`
	// History is the full persisted coverage series, oldest first, this run
	// last — the real trend a chart is drawn from, not just Trend's single
	// collapsed delta.
	History []HistoryPoint `json:"history,omitempty"`
	// RiskHistory is denied/flagged activity bucketed by day — the "blocked
	// over time" trend WAF-style dashboards report, from ledger.ByDayRisk.
	RiskHistory []ledger.DayRisk `json:"riskHistory,omitempty"`
	// PolicyAudit is the real analysis of the access policy: per-rule hit
	// counts, dead and provably-unreachable rules, and the fail-open posture.
	PolicyAudit PolicyAudit `json:"policyAudit"`
	// Exposures is every PII-classified access and whether it left the
	// machine — the finding LLM02 is actually about.
	Exposures []Exposure `json:"exposures,omitempty"`
	// Threats is the forensic breakdown behind the blocked/flagged counts.
	Threats Threats `json:"threats"`
	// Evidence is whether the ensemble's reported confidence rests on
	// independent, discriminating sources.
	Evidence EvidenceQuality `json:"evidence"`
	// Incidents are correlated attack sequences — the reading that a list of
	// counts cannot give.
	Incidents []Incident `json:"incidents,omitempty"`
	// Attestation is the checkable, point-in-time statement of posture — what
	// was true, under which rules, over which evidence.
	Attestation Attestation `json:"attestation"`
	// Posture is the one-line verdict and the condition that decided it.
	Posture Posture `json:"posture"`
	// Privilege is the per-agent least-privilege review.
	Privilege []AgentPrivilege `json:"privilege,omitempty"`
	// BOM is the model estate: provenance, locality, and what is actually used.
	BOM []BOMEntry `json:"bom,omitempty"`
	// Register is the governed view: every finding above as one kind of
	// object, rated, aged against an SLA, priced and mapped to frameworks.
	Register RiskRegister `json:"register"`
	// Blast is the reach of what agents actually edited, joined against the
	// code graph — consequences rather than access decisions.
	Blast BlastReport `json:"blast"`
	// SupplyChain fingerprints each CLI head's binary so a replacement is
	// visible — the local form of the rug-pull pattern.
	SupplyChain SupplyChain `json:"supplyChain"`
	// Drift reports whether the ledger spans more than one configuration —
	// decisions made under rules that later changed.
	Drift ConfigDrift `json:"drift"`
	// Controls answers "does each declared control actually run" — a control
	// that is configured but cannot fire is worse than a missing one, since
	// it reads as protection everywhere it is listed.
	Controls []Control `json:"controls,omitempty"`
	// Events is a capped tail of the raw ledger, newest last — the evidence
	// rows behind every finding above. Capped by maxEvidenceEvents; Truncated
	// says so rather than silently showing a partial log as if it were whole.
	Events    []ledger.Event `json:"events,omitempty"`
	Truncated bool           `json:"truncated,omitempty"`
	// Actions is the feedback loop: one item per coverage Gap plus one per
	// above-threshold risky head, ranked most-urgent first — the backlog for
	// the next hardening round. Priority comes from real signals (a gap's
	// persisted age, or a head's active denied/flagged count) — never an
	// invented severity score.
	Actions []Action `json:"actions,omitempty"`
}

// ActionPriority ranks an Action by real urgency — a gap's persisted age, or
// whether a head is actively risky right now — never a guessed severity.
type ActionPriority string

const (
	// PriorityNow: a stale gap (>=30 days old) or an actively risky head.
	// A risky head never gets an age-based downgrade — it's live, ongoing
	// exposure, not aging debt.
	PriorityNow ActionPriority = "now"
	// PrioritySoon: a gap aging 7-29 days.
	PrioritySoon ActionPriority = "soon"
	// PriorityWatch: a gap under 7 days old.
	PriorityWatch ActionPriority = "watch"
)

// gapStaleDays/gapAgingDays mirror the aging-bucket convention vulnerability
// management dashboards use (fresh / aging / stale) — applied here to a
// coverage gap's own persisted age instead of a CVE's.
const (
	gapStaleDays = 30
	gapAgingDays = 7
)

// Action is one item in the prioritized action queue.
type Action struct {
	ID       string         `json:"id"`
	Kind     string         `json:"kind"` // "gap" | "risk"
	Title    string         `json:"title"`
	Detail   string         `json:"detail"`
	AgeDays  int            `json:"ageDays"`
	Priority ActionPriority `json:"priority"`
}

// Build assembles the report from the ledger, the loaded access policy, and
// heads is the currently-discovered head list (e.g. probe.Run's result) —
// passed in rather than probed here, so this package stays free of any
// network/subprocess dependency and is testable on plain data.
func Build(heads []provider.Head) (*Report, error) {
	events, err := ledger.Load(ledger.DefaultPath())
	if err != nil {
		return nil, err
	}

	s := ledger.Summarize(events)
	r := &Report{
		HasData: len(events) > 0,
		Ledger:  LedgerPanel{Total: s.Total, Allowed: s.Allowed, Denied: s.Denied, Flagged: s.Flagged},
		ByHead:  ledger.ByHeadRisk(events),
	}

	pol, err := ledger.LoadPolicy(ledger.DefaultPolicyPath())
	if err != nil {
		return nil, err
	}

	chainRes, err := ledger.VerifyChain(ledger.DefaultPath())
	if err != nil {
		return nil, err
	}
	r.IntegrityIntact = chainRes.Intact

	r.Exposures = Exposures(events, heads)
	r.PolicyAudit = AuditPolicy(pol, events)
	r.Threats = ThreatBreakdown(events)
	r.Controls = Controls(events, r.PolicyAudit, chainRes)
	r.SupplyChain = FingerprintHeads(heads)
	r.Blast = AssessBlastRadius()
	r.Evidence = AssessEvidence()
	r.Drift = DetectConfigDrift(events)
	r.Incidents = CorrelateIncidents(events, r.Blast)
	r.Privilege = ReviewPrivilege(events, pol)
	r.BOM = BuildBOM(heads, events, r.SupplyChain)
	r.Events, r.Truncated = evidenceTail(events)

	r.Checks = []Check{
		chainCheck(chainRes),
		costCeilingCheck(events),
		provenanceCheck(heads),
		frameworkCheck(pol),
		exposureCheck(r.Exposures),
		policyPostureCheck(r.PolicyAudit),
		evidenceCheck(r.Evidence),
		driftCheck(r.Drift),
		supplyChainCheck(r.SupplyChain),
		blastCheck(r.Blast),
		incidentCheck(r.Incidents),
		privilegeCheck(r.Privilege),
		bomCheck(r.BOM),
	}
	r.RiskHistory = ledger.ByDayRisk(events)

	r.Coverage = computeCoverage(pol, events, r.SupplyChain)

	historyPath := DefaultScoreHistoryPath()
	prior := loadScoreHistory(historyPath)
	r.Trend = buildTrend(prior, r.Coverage)

	now := time.Now().UTC()
	r.Coverage.Categories = annotateGapAge(r.Coverage.Categories, prior, now)
	r.History = append(toHistoryPoints(prior), HistoryPoint{TS: now.Format(time.RFC3339), PercentCovered: r.Coverage.PercentCovered})

	appendScoreHistory(historyPath, r.Coverage)

	r.Register = BuildRegister(r, now)
	r.Posture = AssessPosture(r, chainRes)
	r.Attestation = Attest(r, chainRes, now)

	r.Actions = buildActions(r.Coverage, r.ByHead, r.Exposures, r.PolicyAudit, r.Controls, r.Evidence, r.Drift, r.SupplyChain, r.Blast)

	return r, nil
}

func chainCheck(res ledger.ChainResult) Check {
	const name = "Ledger chain integrity"
	if res.Chained == 0 {
		return Check{Name: name, Status: "no chained events",
			Detail: fmt.Sprintf("%d unchained event(s) predate this feature", res.Unchained)}
	}
	// A truncation and an in-place edit are different attacks and get
	// different words: "broken at index N" is meaningless for a truncation,
	// where every surviving event verifies and the deleted ones left no gap.
	if res.Truncated {
		return Check{Name: name, Status: "TRUNCATED",
			Detail: "the chain anchor names an event no longer in the log — records were deleted from the end"}
	}
	if !res.Intact {
		return Check{Name: name, Status: "BROKEN",
			Detail: fmt.Sprintf("tampering detected at event index %d", res.BrokenAt)}
	}
	if res.AnchorMissing {
		return Check{Name: name, Status: "no anchor",
			Detail: fmt.Sprintf("%d chained event(s) verify, but with no chain anchor deletion from the end cannot be ruled out", res.Chained)}
	}
	return Check{Name: name, Status: "intact",
		Detail: fmt.Sprintf("%d chained event(s), %d unchained", res.Chained, res.Unchained)}
}

// buildActions is the feedback loop: exactly the coverage Gaps plus heads
// whose denied+flagged activity crosses riskThreshold — nothing else —
// ranked most-urgent first so the queue reads top-to-bottom as work order.
func buildActions(cov Coverage, byHead []ledger.HeadRisk, exps []Exposure, audit PolicyAudit,
	controls []Control, ev EvidenceQuality, drift ConfigDrift, sc SupplyChain, blast BlastReport) []Action {
	const riskThreshold = 2
	var out []Action

	// A hub file edited in a graph with a cascade-capable core is where an
	// agent's change reaches furthest. Molloy-Reed kappa>=2 is the published
	// criterion internal/graph already implements, so this is not a threshold
	// invented for this dashboard.
	if blast.Percolates {
		if top, ok := riskiestEdit(blast); ok {
			out = append(out, Action{
				ID: "blast-" + top.File, Kind: "blast",
				Title:    fmt.Sprintf("%s was edited and %d file(s) depend on it", filepath.Base(top.File), top.Dependents),
				Detail:   fmt.Sprintf("radius %.2f× in a percolating graph (kappa=%.1f) — a defect here propagates", top.Radius, blast.Kappa),
				Priority: PrioritySoon,
			})
		}
	}

	// A control that is configured but cannot fire outranks a coverage gap:
	// the gap is a known absence, while this is protection the operator
	// believes they already have.
	for _, ctl := range controls {
		if ctl.Declared && !ctl.Wired {
			out = append(out, Action{
				ID: ctl.Name, Kind: "control",
				Title:    ctl.Name + " is declared but never runs",
				Detail:   ctl.Detail,
				Priority: PriorityNow,
			})
		}
	}

	// A real leak outranks everything else in the queue: it already happened,
	// and unlike a coverage gap it is not hypothetical.
	// Only a *confirmed* remote destination raises an action. An unidentified
	// head is still counted as remote in the report (fail-closed), but it is
	// not evidence enough to head the work queue with.
	if remote := ConfirmedRemote(exps); remote > 0 {
		out = append(out, Action{
			ID: "exposure", Kind: "exposure",
			Title:    fmt.Sprintf("%d sensitive access(es) reached a remote head", remote),
			Detail:   exposureSummary(exps),
			Priority: PriorityNow,
		})
	}
	// An agent binary that changed under you is the rug-pull pattern itself.
	for _, b := range sc.Binaries {
		if b.Changed {
			out = append(out, Action{
				ID: "binary-" + b.HeadID, Kind: "supply-chain",
				Title:    fmt.Sprintf("%s binary changed since it was last seen", b.HeadID),
				Detail:   fmt.Sprintf("%s — an upgrade and a swap look identical here, so confirm which it was", b.Path),
				Priority: PriorityNow,
			})
		}
	}

	// A confidence figure assembled from correlated or undiagnostic sources
	// is a number, not an assurance — and unlike a coverage gap it is
	// actively misleading, because it reads as a result.
	for _, f := range ev.Families {
		out = append(out, Action{
			ID: "family-" + f.Family, Kind: "evidence",
			Title:    fmt.Sprintf("%s heads vote as one", f.Family),
			Detail:   fmt.Sprintf("they agree %.0f%% beyond chance, so an ensemble of them reports more confidence than it earned", f.Coupling*100),
			Priority: PriorityNow,
		})
	}
	for _, w := range ev.WeakSources {
		out = append(out, Action{
			ID: "weak-" + w.Source, Kind: "evidence",
			Title:    fmt.Sprintf("%s carries no diagnostic weight", w.Source),
			Detail:   fmt.Sprintf("D=%.3f nats over %.0f recorded outcomes — its agreement barely moves the posterior", w.D, w.Observations),
			Priority: PrioritySoon,
		})
	}
	if drift.Changed {
		out = append(out, Action{
			ID: "config-drift", Kind: "policy",
			Title:    "Routing configuration changed mid-history",
			Detail:   "earlier ledger decisions were made under different rules than the current ones",
			Priority: PrioritySoon,
		})
	}

	// An unreachable rule is a policy bug: someone wrote a control believing
	// it applied, and it can never fire.
	for _, r := range audit.Rules {
		if r.ShadowedBy != nil {
			out = append(out, Action{
				ID: fmt.Sprintf("rule-%d", r.Index), Kind: "policy",
				Title:    fmt.Sprintf("Rule #%d is unreachable", r.Index),
				Detail:   fmt.Sprintf("%s — rule #%d always matches first", r.Summary, *r.ShadowedBy),
				Priority: PriorityNow,
			})
		}
	}
	if audit.FailOpen && len(audit.Rules) > 0 {
		out = append(out, Action{
			ID: "fail-open", Kind: "policy",
			Title:    "Access policy is fail-open",
			Detail:   "anything no rule names is allowed — set \"default\": \"deny\" to invert that",
			Priority: PrioritySoon,
		})
	}

	for _, c := range cov.Categories {
		if c.Status != Gap {
			continue
		}
		out = append(out, Action{
			ID: c.ID, Kind: "gap", Title: c.Name, Detail: c.Detail,
			AgeDays: c.GapAgeDays, Priority: gapPriority(c.GapAgeDays),
		})
	}
	for _, h := range byHead {
		if h.Denied+h.Flagged >= riskThreshold {
			out = append(out, Action{
				ID:       h.Head,
				Kind:     "risk",
				Title:    fmt.Sprintf("%s — risky head", h.Head),
				Detail:   fmt.Sprintf("%d denied, %d flagged — review its ledger rules", h.Denied, h.Flagged),
				Priority: PriorityNow,
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		pi, pj := priorityRank(out[i].Priority), priorityRank(out[j].Priority)
		if pi != pj {
			return pi < pj
		}
		return out[i].AgeDays > out[j].AgeDays
	})
	return out
}

// gapPriority buckets a gap's persisted age the way vulnerability-management
// dashboards bucket finding age: fresh, aging, stale.
func gapPriority(ageDays int) ActionPriority {
	switch {
	case ageDays >= gapStaleDays:
		return PriorityNow
	case ageDays >= gapAgingDays:
		return PrioritySoon
	default:
		return PriorityWatch
	}
}

func priorityRank(p ActionPriority) int {
	switch p {
	case PriorityNow:
		return 0
	case PrioritySoon:
		return 1
	default:
		return 2
	}
}

// costCeilingReason reports whether e was a --max-cost refusal — the one
// substring check shared by the cost-ceiling Check and the LLM10 detector,
// so the two can never disagree about what counts.
func costCeilingReason(e ledger.Event) bool {
	return e.Decision == ledger.Deny && strings.Contains(e.Reason, "cost ceiling")
}

func costCeilingCheck(events []ledger.Event) Check {
	const name = "Denial-of-wallet guard"
	n := 0
	for _, e := range events {
		if costCeilingReason(e) {
			n++
		}
	}
	if n == 0 {
		return Check{Name: name, Status: "0 refusals",
			Detail: "no dispatch has been refused for exceeding a cost ceiling — set one with --max-cost"}
	}
	return Check{Name: name, Status: fmt.Sprintf("%d refusal(s)", n),
		Detail: "at least one dispatch was refused for exceeding its --max-cost ceiling"}
}

func provenanceCheck(heads []provider.Head) Check {
	const name = "Model provenance"
	var builtin, user, unknown int
	for _, h := range heads {
		switch h.Meta["model_source"] {
		case "builtin":
			builtin++
		case "user":
			user++
		default:
			unknown++
		}
	}
	return Check{Name: name,
		Status: fmt.Sprintf("%d builtin, %d user-added, %d unclassified", builtin, user, unknown),
		Detail: "a user-added model isn't vetted by the curated catalog — not malicious, just worth knowing about"}
}

func frameworkCheck(pol ledger.Policy) Check {
	const name = "Framework tag coverage"
	covered := pol.FrameworksCovered()
	if len(covered) == 0 {
		return Check{Name: name, Status: "0 tagged",
			Detail: "no policy rule carries a Framework tag (e.g. owasp:llm06)"}
	}
	return Check{Name: name, Status: fmt.Sprintf("%d tagged", len(covered)),
		Detail: strings.Join(covered, ", ")}
}

// piiCheck counts ledger events classified "pii" — content the policy engine
// auto-detected as sensitive when it was passed to ledger.Check. This is a
// distinct signal from LLM02's automatic local-only routing (a separate,
// dispatch-time mechanism in internal/policy.Engine) — additional visibility,
// not a duplicate of it.
// maxEvidenceEvents caps the raw ledger tail carried in the report. A machine
// that has dispatched for months has an unbounded log, and a dashboard does
// not need all of it to show the rows behind a finding.
const maxEvidenceEvents = 200

// evidenceTail returns the newest events, and whether anything was dropped —
// reported rather than silently trimmed, so a partial log is never mistaken
// for the whole one.
func evidenceTail(events []ledger.Event) ([]ledger.Event, bool) {
	if len(events) <= maxEvidenceEvents {
		return events, false
	}
	return events[len(events)-maxEvidenceEvents:], true
}

// exposureCheck reports sensitive-data detections split by where they went.
// The count alone says nothing: PII routed to a local head is the local-only
// control working, while the same PII routed to a cloud head is data that has
// left the machine. Only the second is a finding.
func exposureCheck(exps []Exposure) Check {
	const name = "Sensitive data exposure"
	if len(exps) == 0 {
		return Check{Name: name, Status: "none detected",
			Detail: "no ledger-recorded access has been classified as sensitive on this machine"}
	}
	remote, confirmed := RemoteCount(exps), ConfirmedRemote(exps)
	if remote == 0 {
		return Check{Name: name, Status: fmt.Sprintf("%d detected, all local", len(exps)),
			Detail: "every sensitive access stayed on a local-only head — the control worked"}
	}
	// Separate observed leaks from assumed ones. An unrecognised head is
	// still treated as remote (fail-closed), but saying so is what keeps the
	// headline number trustworthy — a stopped Ollama server must not read as
	// a confirmed leak.
	assumed := remote - confirmed
	status := fmt.Sprintf("%d detected, %d to a remote head", len(exps), confirmed)
	if assumed > 0 {
		status = fmt.Sprintf("%d detected, %d to a remote head, %d unidentified", len(exps), confirmed, assumed)
	}

	// Only assert a leak when one was actually observed. With no confirmed
	// destination the honest statement is that the head could not be
	// identified — claiming data "reached a remote head" off an undiscovered
	// head would be the same overclaiming this whole rewrite exists to remove.
	var detail string
	switch {
	case confirmed > 0 && assumed > 0:
		detail = fmt.Sprintf("%s — and %d more to a head not currently discoverable", exposureSummary(exps), assumed)
	case confirmed > 0:
		detail = fmt.Sprintf("%s — sensitive data reached a head that leaves this machine", exposureSummary(exps))
	default:
		detail = fmt.Sprintf("%d access(es) went to a head that is not currently discoverable, so it cannot be "+
			"confirmed local — run `hyctl probe` with those heads available to resolve this", assumed)
	}
	return Check{Name: name, Status: status, Detail: detail}
}

// exposureSummary names the remote destinations and secret types, so the
// detail line is specific enough to act on rather than a restated count.
func exposureSummary(exps []Exposure) string {
	heads := map[string]bool{}
	types := map[string]bool{}
	for _, e := range exps {
		if !e.Remote || !e.Known {
			continue // summarise only confirmed destinations
		}
		if e.Head != "" {
			heads[e.Head] = true
		}
		for _, t := range e.PIITypes {
			types[t] = true
		}
	}
	s := "to " + strings.Join(sortedKeys(heads), ", ")
	if len(types) > 0 {
		s += " (" + strings.Join(sortedKeys(types), ", ") + ")"
	}
	return s
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// policyPostureCheck replaces an earlier "policy adherence %" that measured
// the wrong thing — see policyaudit.go's header for why. This reports the
// facts an operator can act on: fail-open default, dead rules, unreachable
// rules.
func policyPostureCheck(a PolicyAudit) Check {
	const name = "Policy posture"
	if len(a.Rules) == 0 {
		return Check{Name: name, Status: "no rules defined",
			Detail: fmt.Sprintf("every access falls through to the %s default — nothing is scoped", a.Default)}
	}
	var parts []string
	if a.FailOpen {
		parts = append(parts, "fail-open default")
	} else {
		parts = append(parts, "fail-closed default")
	}
	if n := a.DeadCount(); n > 0 {
		parts = append(parts, fmt.Sprintf("%d never matched", n))
	}
	if n := a.ShadowedCount(); n > 0 {
		parts = append(parts, fmt.Sprintf("%d unreachable", n))
	}
	return Check{Name: name, Status: strings.Join(parts, ", "),
		Detail: fmt.Sprintf("%d rule(s); %d access(es) matched a rule, %d fell through to the %s default",
			len(a.Rules), a.Evaluated-a.DefaultHits, a.DefaultHits, a.Default)}
}
