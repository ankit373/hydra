// SPDX-License-Identifier: MIT

package security

import (
	"fmt"
	"slices"
	"time"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/ledger"
	"github.com/ankit373/hydra/internal/trust"
	"github.com/ankit373/hydra/internal/workspace"
)

// CoverageStatus is one OWASP LLM Top-10 category's state on this install.
type CoverageStatus string

const (
	// Enforced: the mechanism is automatic — no configuration needed.
	Enforced CoverageStatus = "enforced"
	// Configured: the mechanism exists and is actually set up/used on this install.
	Configured CoverageStatus = "configured"
	// Gap: nothing addresses this category today.
	Gap CoverageStatus = "gap"
	// NotApplicable: this category does not apply to an orchestrator that
	// routes prompts rather than trains models — excluded from scoring.
	NotApplicable CoverageStatus = "n/a"
)

// Category is one OWASP LLM Top-10 entry and Hydra's real, evidence-backed
// status against it — never a guess, always traceable to an actual mechanism.
type Category struct {
	ID     string         `json:"id"`
	Name   string         `json:"name"`
	Status CoverageStatus `json:"status"`
	Detail string         `json:"detail"`

	// GapSince/GapAgeDays are set only when Status is Gap, from the score
	// history Build already persists — the earliest recorded run where this
	// category was already a gap. A brand-new gap (no matching history) gets
	// age 0: this run is the first evidence of it.
	GapSince   string `json:"gapSince,omitempty"`
	GapAgeDays int    `json:"gapAgeDays,omitempty"`
}

// Coverage is Hydra's posture against the OWASP LLM Top 10: a percentage of
// *applicable* categories with a live mechanism, never a blended score and
// never presented as "you are X% secure" — always "X% of the OWASP LLM Top
// 10 has a live mechanism".
type Coverage struct {
	Categories     []Category `json:"categories"`
	Applicable     int        `json:"applicable"` // categories excluding N/A
	Covered        int        `json:"covered"`    // Enforced + Configured
	PercentCovered float64    `json:"percentCovered"`
}

// computeCoverage classifies all 10 OWASP LLM Top-10 categories against
// Hydra's real, currently-observable state. pol and events are what Build
// already loaded — passed in rather than reloaded here.
func computeCoverage(pol ledger.Policy, events []ledger.Event, sc SupplyChain) Coverage {
	cats := []Category{
		{ID: "LLM01", Name: "Prompt Injection", Status: Enforced,
			Detail: "untrusted content is framed as data (a2a/editor/parallel) and scanned for injection markers automatically"},
		{ID: "LLM02", Name: "Sensitive Information Disclosure", Status: Enforced,
			Detail: "PII detection forces local-only routing automatically"},
		llm03SupplyChain(sc),
		{ID: "LLM04", Name: "Data and Model Poisoning", Status: NotApplicable,
			Detail: "Hydra routes prompts to models — it does not train or fine-tune any"},
		llm05OutputHandling(),
		llm06ExcessiveAgency(pol),
		{ID: "LLM07", Name: "System Prompt Leakage", Status: Gap,
			Detail: "no protection exists for --system content today"},
		{ID: "LLM08", Name: "Vector and Embedding Weaknesses", Status: NotApplicable,
			Detail: "Hydra has no RAG pipeline or vector store of its own"},
		llm09Misinformation(),
		llm10UnboundedConsumption(events),
	}

	var applicable, covered int
	for _, c := range cats {
		if c.Status == NotApplicable {
			continue
		}
		applicable++
		if c.Status == Enforced || c.Status == Configured {
			covered++
		}
	}
	pct := 0.0
	if applicable > 0 {
		pct = 100 * float64(covered) / float64(applicable)
	}
	return Coverage{Categories: cats, Applicable: applicable, Covered: covered, PercentCovered: pct}
}

// llm03SupplyChain: Hydra fingerprints every CLI head's binary and reports
// when one changes. That is detection, not provenance — it cannot distinguish
// a legitimate upgrade from a swap, and verifies nothing about origin — so it
// is Configured once binaries are actually being tracked, never Enforced.
//
// The baseline it compares against is a plain file (head_binaries.json), so
// this detects a binary being swapped *under* Hydra, not an attacker who can
// also write Hydra's own state. See the note in supplychain.go.
func llm03SupplyChain(sc SupplyChain) Category {
	c := Category{ID: "LLM03", Name: "Supply Chain"}
	if len(sc.Binaries) == 0 {
		c.Status = Gap
		c.Detail = "no CLI head binary is being fingerprinted, so a replaced agent binary would go unnoticed"
		return c
	}
	c.Status = Configured
	c.Detail = fmt.Sprintf("%d head binary(ies) fingerprinted; a replacement is detected, though origin is not "+
		"verified and the stored baseline is not itself tamper-evident", len(sc.Binaries))
	return c
}

// llm05OutputHandling: Hydra ships default workspace validators (js, py,
// yaml, sh, ...) that run after every edit and roll back on failure — so
// this is Enforced whenever the loaded registry has any validator at all,
// Gap only if a custom workspace.yaml stripped every one of them.
func llm05OutputHandling() Category {
	c := Category{ID: "LLM05", Name: "Improper Output Handling"}
	reg, err := workspace.Load(config.ScriptHome())
	if err != nil || !reg.HasAnyValidator() {
		c.Status = Gap
		c.Detail = "no workspace validator is configured — hyctl edit/parallel would accept unvalidated model output"
		return c
	}
	c.Status = Enforced
	c.Detail = "a workspace validator runs after every edit and rolls back on failure"
	return c
}

// llm06ExcessiveAgency: Configured when at least one ledger rule scopes
// access by resource (real least-privilege), Gap otherwise — this is a
// per-install choice, not something Hydra can ship a default for.
func llm06ExcessiveAgency(pol ledger.Policy) Category {
	c := Category{ID: "LLM06", Name: "Excessive Agency"}
	for _, r := range pol.Rules {
		if r.Resource != "" {
			c.Status = Configured
			c.Detail = "at least one ledger rule scopes access by resource (least-privilege)"
			return c
		}
	}
	c.Status = Gap
	c.Detail = "no ledger rule scopes access by resource — any allowed head can touch any file"
	return c
}

// llm09Misinformation: the SPRT confidence ensemble is Hydra's real mitigation
// for hallucinated/wrong answers — Configured once it's actually been used.
func llm09Misinformation() Category {
	c := Category{ID: "LLM09", Name: "Misinformation"}
	runs, err := trust.LoadRuns(trust.DefaultLogPath())
	if err != nil || len(runs) == 0 {
		c.Status = Gap
		c.Detail = "the SPRT confidence ensemble (hyctl dispatch --confidence) has never been used"
		return c
	}
	c.Status = Configured
	c.Detail = fmt.Sprintf("%d confidence-ensemble run(s) recorded", len(runs))
	return c
}

// llm10UnboundedConsumption: Configured once a --max-cost ceiling has
// actually refused a dispatch — the ledger is the only durable record that
// the guard was ever exercised.
func llm10UnboundedConsumption(events []ledger.Event) Category {
	c := Category{ID: "LLM10", Name: "Unbounded Consumption"}
	for _, e := range events {
		if costCeilingReason(e) {
			c.Status = Configured
			c.Detail = "a --max-cost ceiling has refused at least one dispatch"
			return c
		}
	}
	c.Status = Gap
	c.Detail = "no dispatch has ever been refused for exceeding a cost ceiling — set one with --max-cost"
	return c
}

// annotateGapAge fills in GapSince/GapAgeDays for every currently-Gap
// category, using score history that Build already loads and persists
// (security_score.jsonl, since #396) — no new logging, just reading data
// that already exists. history must be oldest-first, which
// loadScoreHistory/appendScoreHistory already guarantee (the log is
// append-only).
func annotateGapAge(cats []Category, history []scoreEntry, now time.Time) []Category {
	out := make([]Category, len(cats))
	copy(out, cats)
	for i, c := range out {
		if c.Status != Gap {
			continue
		}
		for _, h := range history {
			if !slices.Contains(h.Gaps, c.ID) {
				continue
			}
			ts, err := time.Parse(time.RFC3339, h.TS)
			if err != nil {
				continue
			}
			out[i].GapSince = h.TS
			out[i].GapAgeDays = int(now.Sub(ts).Hours() / 24)
			break
		}
	}
	return out
}
