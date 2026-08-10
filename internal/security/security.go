// SPDX-License-Identifier: MIT

// Package security aggregates Hydra's security-relevant data — the ledger's
// accountability trail, per-head risk, and a short list of honest checks —
// into one report. It reuses ledger.Summarize/ByHeadRisk/VerifyChain and
// ledger.Policy.FrameworksCovered rather than reimplementing any of them, the
// same discipline desktop/api/dashboard.go already applies to cost/trust.
package security

import (
	"fmt"
	"strings"

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

	r.Checks = []Check{
		chainCheck(),
		costCeilingCheck(events),
		provenanceCheck(heads),
		frameworkCheck(pol),
	}
	return r, nil
}

func chainCheck() Check {
	const name = "Ledger chain integrity"
	res, err := ledger.VerifyChain(ledger.DefaultPath())
	if err != nil {
		return Check{Name: name, Status: "error", Detail: err.Error()}
	}
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

func costCeilingCheck(events []ledger.Event) Check {
	const name = "Denial-of-wallet guard"
	n := 0
	for _, e := range events {
		if e.Decision == ledger.Deny && strings.Contains(e.Reason, "cost ceiling") {
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
