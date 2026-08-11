// SPDX-License-Identifier: MIT

// Package security aggregates Hydra's security-relevant data — the ledger's
// accountability trail, per-head risk, and a short list of honest checks —
// into one report. It reuses ledger.Summarize/ByHeadRisk/VerifyChain and
// ledger.Policy.FrameworksCovered rather than reimplementing any of them, the
// same discipline desktop/api/dashboard.go already applies to cost/trust.
package security

import (
	"fmt"
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

	r.Checks = []Check{
		chainCheck(chainRes),
		costCeilingCheck(events),
		provenanceCheck(heads),
		frameworkCheck(pol),
		piiCheck(events),
		policyAdherenceCheck(events),
	}
	r.RiskHistory = ledger.ByDayRisk(events)

	r.Coverage = computeCoverage(pol, events)

	historyPath := DefaultScoreHistoryPath()
	prior := loadScoreHistory(historyPath)
	r.Trend = buildTrend(prior, r.Coverage)

	now := time.Now().UTC()
	r.Coverage.Categories = annotateGapAge(r.Coverage.Categories, prior, now)
	r.History = append(toHistoryPoints(prior), HistoryPoint{TS: now.Format(time.RFC3339), PercentCovered: r.Coverage.PercentCovered})

	appendScoreHistory(historyPath, r.Coverage)

	r.Actions = buildActions(r.Coverage, r.ByHead)

	return r, nil
}

func chainCheck(res ledger.ChainResult) Check {
	const name = "Ledger chain integrity"
	if res.Chained == 0 {
		return Check{Name: name, Status: "no chained events",
			Detail: fmt.Sprintf("%d unchained event(s) predate this feature", res.Unchained)}
	}
	if !res.Intact {
		return Check{Name: name, Status: "BROKEN",
			Detail: fmt.Sprintf("tampering detected at event index %d", res.BrokenAt)}
	}
	return Check{Name: name, Status: "intact",
		Detail: fmt.Sprintf("%d chained event(s), %d unchained", res.Chained, res.Unchained)}
}

// buildActions is the feedback loop: exactly the coverage Gaps plus heads
// whose denied+flagged activity crosses riskThreshold — nothing else —
// ranked most-urgent first so the queue reads top-to-bottom as work order.
func buildActions(cov Coverage, byHead []ledger.HeadRisk) []Action {
	const riskThreshold = 2
	var out []Action
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
func piiCheck(events []ledger.Event) Check {
	const name = "PII/sensitive-data detections"
	n := 0
	for _, e := range events {
		if e.Classification == "pii" {
			n++
		}
	}
	if n == 0 {
		return Check{Name: name, Status: "0 detected",
			Detail: "no ledger-recorded access has been classified as PII on this machine"}
	}
	return Check{Name: name, Status: fmt.Sprintf("%d detected", n),
		Detail: "each was auto-classified from content passed to hyctl mcp check"}
}

// policyAdherenceCheck reports what fraction of policy-evaluated accesses hit
// an explicit rule rather than falling through to the default. Only events
// whose Reason came from Policy.Decide ("rule N (...)" or exactly "default")
// count — an event recorded some other way (e.g. hyctl mcp record, which
// never calls Decide) isn't part of this population and must not skew it.
func policyAdherenceCheck(events []ledger.Event) Check {
	const name = "Policy adherence"
	var ruled, defaulted int
	for _, e := range events {
		switch {
		case strings.HasPrefix(e.Reason, "rule "):
			ruled++
		case e.Reason == "default":
			defaulted++
		}
	}
	total := ruled + defaulted
	if total == 0 {
		return Check{Name: name, Status: "no policy-evaluated events yet",
			Detail: "hyctl mcp check hasn't been exercised, or no explicit rules are defined"}
	}
	pct := 100 * float64(ruled) / float64(total)
	return Check{Name: name, Status: fmt.Sprintf("%.0f%% matched a rule", pct),
		Detail: fmt.Sprintf("%d of %d policy-evaluated accesses hit an explicit rule rather than the default", ruled, total)}
}
