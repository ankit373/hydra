// SPDX-License-Identifier: MIT

package security

import (
	"strings"
	"testing"
	"time"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("bad fixture time %q: %v", s, err)
	}
	return ts
}

// The register is the spine every analysis converts into, so the invariants
// that matter are the ones a reader would rely on: a stable identity, an SLA
// clock that actually breaches, and a total that only counts open risks.

func TestBuildRegister_IDsAreStableAcrossRuns(t *testing.T) {
	now := mustTime(t, "2026-08-12T10:00:00Z")
	r := &Report{
		Incidents: []Incident{{
			ID: "x", Actor: "gpt", Severity: SeverityCritical,
			Narrative: "gpt: it escalated to an exec/network action.",
			Start:     "2026-08-12T09:00:00Z", End: "2026-08-12T09:30:00Z",
		}},
	}

	first := BuildRegister(r, now)
	second := BuildRegister(r, now.Add(48*time.Hour))
	if len(first.Risks) == 0 {
		t.Fatal("expected at least one risk from a critical incident")
	}
	if first.Risks[0].ID != second.Risks[0].ID {
		t.Errorf("risk ID changed between runs: %q then %q, an ID that moves cannot be tracked",
			first.Risks[0].ID, second.Risks[0].ID)
	}
	if !strings.HasPrefix(first.Risks[0].ID, "R-") {
		t.Errorf("risk ID %q should be prefixed R-", first.Risks[0].ID)
	}
}

func TestBuildRegister_SLAClockBreachesAtTheSeverityDeadline(t *testing.T) {
	now := mustTime(t, "2026-08-12T10:00:00Z")
	// A coverage gap carries a real first-seen date, so its age is knowable.
	old := now.Add(-40 * 24 * time.Hour).Format(time.RFC3339)
	r := &Report{
		Coverage: Coverage{Categories: []Category{
			{ID: "LLM04", Name: "Data and Model Poisoning", Status: Gap,
				Detail: "no mechanism", GapSince: old, GapAgeDays: 40},
		}},
	}

	reg := BuildRegister(r, now)
	var gap *Risk
	for i := range reg.Risks {
		if reg.Risks[i].Class == ClassCoverage {
			gap = &reg.Risks[i]
			break
		}
	}
	if gap == nil {
		t.Fatal("a coverage gap should produce a coverage-class risk")
	}
	if gap.AgeDays != 40 {
		t.Errorf("AgeDays = %d, want 40 (carried from the persisted first-seen date)", gap.AgeDays)
	}
	want := slaDays[gap.Severity]
	if gap.AgeDays <= want && gap.Breached {
		t.Errorf("risk aged %dd against a %dd SLA must not be breached", gap.AgeDays, want)
	}
	if gap.AgeDays > want && !gap.Breached {
		t.Errorf("risk aged %dd against a %dd SLA should be breached", gap.AgeDays, want)
	}
	if gap.DueInDays != want-gap.AgeDays {
		t.Errorf("DueInDays = %d, want %d (%d SLA − %d age)", gap.DueInDays, want-gap.AgeDays, want, gap.AgeDays)
	}
}

func TestBuildRegister_TotalsCountOpenRisksOnly(t *testing.T) {
	now := mustTime(t, "2026-08-12T10:00:00Z")
	r := &Report{
		Incidents: []Incident{{
			ID: "a", Actor: "gpt", Severity: SeverityCritical, Narrative: "gpt: escalated.",
			Start: "2026-08-12T09:00:00Z", End: "2026-08-12T09:10:00Z",
		}},
		Coverage: Coverage{Categories: []Category{
			{ID: "LLM04", Name: "Poisoning", Status: Gap, Detail: "none"},
		}},
	}

	reg := BuildRegister(r, now)
	var sum float64
	counted := 0
	for _, k := range reg.Risks {
		if k.Status == StatusOpen {
			sum += k.DefectCostUSD
			counted++
		}
	}
	if reg.SumDefectCostUSD != sum {
		t.Errorf("SumDefectCostUSD = %.2f, want %.2f (open risks only)", reg.SumDefectCostUSD, sum)
	}
	total := 0
	for _, n := range reg.BySeverity {
		total += n
	}
	if total != counted {
		t.Errorf("BySeverity totals %d, want %d open risks", total, counted)
	}
}

func TestBuildRegister_EveryRiskCarriesACuratedCrosswalk(t *testing.T) {
	now := mustTime(t, "2026-08-12T10:00:00Z")
	r := &Report{
		Incidents: []Incident{{
			ID: "a", Actor: "gpt", Severity: SeverityHigh, Narrative: "gpt: escalated.",
			Start: "2026-08-12T09:00:00Z", End: "2026-08-12T09:10:00Z",
		}},
	}
	for _, k := range BuildRegister(r, now).Risks {
		for _, f := range k.Frameworks {
			if !f.Curated {
				t.Errorf("risk %q maps to %s/%s without Curated set, a crosswalk is an assertion, "+
					"and rendering it like measured data is the overclaim this package refuses",
					k.ID, f.Framework, f.Control)
			}
		}
	}
}

func TestBuildRegister_EmptyReportProducesNoRisks(t *testing.T) {
	// IntegrityIntact must be set explicitly: its zero value is false, which
	// BuildRegister correctly reads as an unverified chain and reports as a
	// critical risk. Fail-closed is right; the fixture has to opt in to clean.
	reg := BuildRegister(&Report{IntegrityIntact: true}, mustTime(t, "2026-08-12T10:00:00Z"))
	if len(reg.Risks) != 0 {
		t.Errorf("a report with no findings produced %d risk(s); a clean machine must read as clean",
			len(reg.Risks))
	}
	if reg.SumDefectCostUSD != 0 || reg.Breached != 0 {
		t.Errorf("empty register should total zero, got sum=%.2f breached=%d",
			reg.SumDefectCostUSD, reg.Breached)
	}
}

// A denied event is not on its own an incident, so an ordinary blocked access
// must not manufacture a register entry.
func TestBuildRegister_DoesNotInventRisksFromOrdinaryDenials(t *testing.T) {
	r := &Report{IntegrityIntact: true, Ledger: LedgerPanel{Total: 3, Allowed: 2, Denied: 1}}
	if reg := BuildRegister(r, mustTime(t, "2026-08-12T10:00:00Z")); len(reg.Risks) != 0 {
		t.Errorf("a single denial produced %d risk(s); the policy working is not a finding", len(reg.Risks))
	}
}

// A zero-valued report must NOT read as clean: an unverified chain is the one
// state a security tool has to fail closed on.
func TestBuildRegister_UnverifiedChainIsCritical(t *testing.T) {
	reg := BuildRegister(&Report{}, mustTime(t, "2026-08-12T10:00:00Z"))
	if len(reg.Risks) == 0 {
		t.Fatal("an unverified ledger chain must produce a risk, not silence")
	}
	k := reg.Risks[0]
	if k.Class != ClassEvidence || k.Severity != SeverityCritical {
		t.Errorf("got class=%s severity=%s, want evidence/critical", k.Class, k.Severity)
	}
}

// A confirmed leak is the most severe thing this register can carry, and it is
// the only path that attaches per-exposure evidence.
func TestBuildRegister_ConfirmedLeakIsCriticalAndCarriesEvidence(t *testing.T) {
	now := mustTime(t, "2026-08-12T10:00:00Z")
	r := &Report{
		IntegrityIntact: true,
		Exposures: []Exposure{
			// Remote and Known: a real leak off the machine.
			{TS: "2026-08-12T09:00:00Z", Agent: "a", Head: "api/gpt", Resource: "secrets.env",
				PIITypes: []string{"aws access key id"}, Remote: true, Known: true},
			// Remote but unknown: fail-closed, yet not a confirmed leak.
			{TS: "2026-08-12T09:05:00Z", Agent: "a", Head: "mystery", Resource: "b.env",
				PIITypes: []string{"email"}, Remote: true, Known: false},
		},
	}

	reg := BuildRegister(r, now)
	var leak *Risk
	for i := range reg.Risks {
		if reg.Risks[i].Class == ClassExposure {
			leak = &reg.Risks[i]
			break
		}
	}
	if leak == nil {
		t.Fatal("a confirmed remote exposure must produce an exposure-class risk")
	}
	if leak.Severity != SeverityCritical {
		t.Errorf("severity = %s, want critical for data that left the machine", leak.Severity)
	}
	if len(leak.Evidence) == 0 {
		t.Error("an exposure risk with no evidence is an assertion, not a finding")
	}
	if !strings.Contains(leak.Title, "1 ") {
		t.Errorf("title %q should count only the CONFIRMED leak, not the unknown head", leak.Title)
	}
}
