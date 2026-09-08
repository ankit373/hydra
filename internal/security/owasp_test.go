// SPDX-License-Identifier: MIT

package security

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/ledger"
	"github.com/ankit373/hydra/internal/testutil"
	"github.com/ankit373/hydra/internal/trust"
)

// LLM01 is Partial, LLM07 is always Gap (no mechanism exists), LLM04/LLM08
// are always N/A, these don't depend on install state, unlike LLM02/03/05/06
// /09/10. LLM03 is Gap only while nothing is being fingerprinted, which is
// what an empty SupplyChain means here.
//
// LLM01 is pinned Partial deliberately. It was Enforced while the mechanism
// behind it documented itself otherwise: internal/policy/injection.go calls
// its scan "trivially evaded by anyone who tries... not to prevent an attack".
// Promoting it back needs a preventive mechanism, not a better detector
// (#722). LLM02 earned Enforced when the egress gate shipped (#723) and is
// asserted separately, since it now reads real runtime state.
func TestComputeCoverage_StaticCategoriesAreFixed(t *testing.T) {
	testutil.NewSandbox(t)

	cov := computeCoverage(ledger.Policy{}, SupplyChain{}, nil, 0)
	want := map[string]CoverageStatus{
		"LLM01": Partial,
		"LLM04": Gap, "LLM08": Gap,
		"LLM05": NotApplicable, "LLM09": NotApplicable,
	}
	got := map[string]CoverageStatus{}
	for _, c := range cov.Categories {
		got[c.ID] = c.Status
	}
	for id, status := range want {
		if got[id] != status {
			t.Errorf("%s = %q, want %q", id, got[id], status)
		}
	}
}

// LLM02 reports what the egress gate is actually doing, never a fixed claim.
// Enforced needs both halves: path rules loaded, and the strict floor on. With
// strict off a secret payload still leaves when nothing local is routable, so
// that is Configured rather than Enforced, and the detail has to say which.
func TestLLM02_ReadsTheEgressGatesRealState(t *testing.T) {
	testutil.NewSandbox(t)

	if got := sensitiveInfoCategory(); got.Status != Enforced {
		t.Fatalf("LLM02 = %q with the gate shipped and strict defaulting on, want %q (%s)",
			got.Status, Enforced, got.Detail)
	}

	cfg := &config.Config{}
	off := false
	cfg.Egress.Strict = &off
	if err := config.Save(cfg); err != nil {
		t.Skipf("cannot write a config in this sandbox: %v", err)
	}
	got := sensitiveInfoCategory()
	if got.Status != Configured {
		t.Errorf("LLM02 = %q with egress.strict off, want %q", got.Status, Configured)
	}
	if !strings.Contains(got.Detail, "egress.strict") {
		t.Errorf("the detail does not name what is turned off: %q", got.Detail)
	}
}

func TestComputeCoverage_NAExcludedFromBothNumeratorAndDenominator(t *testing.T) {
	testutil.NewSandbox(t)

	cov := computeCoverage(ledger.Policy{}, SupplyChain{}, nil, 0)
	if cov.Applicable != 8 {
		t.Errorf("Applicable = %d, want 8 (10 categories minus LLM05 and LLM09)", cov.Applicable)
	}
	for _, c := range cov.Categories {
		if c.ID == "LLM05" || c.ID == "LLM09" {
			continue
		}
		if c.Status == NotApplicable {
			t.Errorf("%s unexpectedly marked N/A", c.ID)
		}
	}
}

func TestExcessiveAgency_ConfiguredOnlyWithAResourceScopedRule(t *testing.T) {
	none := ledger.Policy{Rules: []ledger.Rule{{Tool: "a", Decision: ledger.Allow}}}
	if got := excessiveAgencyCategory(none).Status; got != Gap {
		t.Errorf("no resource-scoped rule: Status = %q, want Gap", got)
	}

	scoped := ledger.Policy{Rules: []ledger.Rule{{Resource: "internal/auth/*", Decision: ledger.Deny}}}
	if got := excessiveAgencyCategory(scoped).Status; got != Configured {
		t.Errorf("a resource-scoped rule exists: Status = %q, want Configured", got)
	}
}

func TestLLM09Misinformation_ConfiguredOnlyWithARecordedRun(t *testing.T) {
	testutil.NewSandbox(t)

	if got := misinformationCategory(nil).Status; got != Gap {
		t.Errorf("no trust.jsonl: Status = %q, want Gap", got)
	}

	if err := os.MkdirAll(filepath.Dir(trust.DefaultLogPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	row := `{"ts":"2026-01-01T00:00:00Z","task_hash":"abc","domain":"go","target_conf":0.9,"final_conf":0.9,"samples":1,"decision":"accept"}` + "\n"
	if err := os.WriteFile(trust.DefaultLogPath(), []byte(row), 0o600); err != nil {
		t.Fatal(err)
	}
	runs, err := trust.LoadRuns(trust.DefaultLogPath())
	if err != nil {
		t.Fatal(err)
	}
	if got := misinformationCategory(runs).Status; got != Configured {
		t.Errorf("a recorded run exists: Status = %q, want Configured", got)
	}
}

func TestLLM10UnboundedConsumption_ConfiguredOnlyWithACostCeilingDenial(t *testing.T) {
	none := []ledger.Event{{Tool: "a", Decision: ledger.Deny, Reason: "denied by ledger policy"}}
	if got := unboundedConsumptionCategory(countCostCeilingDenials(none)).Status; got != Gap {
		t.Errorf("no cost-ceiling denial: Status = %q, want Gap", got)
	}

	withCeiling := []ledger.Event{{Tool: "a", Decision: ledger.Deny, Reason: "exceeds cost ceiling: estimated $1 > limit $0.5"}}
	if got := unboundedConsumptionCategory(countCostCeilingDenials(withCeiling)).Status; got != Configured {
		t.Errorf("a cost-ceiling denial exists: Status = %q, want Configured", got)
	}
}

// A custom workspace.yaml with every validator explicitly nulled must report
// Gap: the only way LLM05 should ever be Gap, since the embedded default
// ships real validators.
func TestLLM05OutputHandling_GapWhenNoValidatorsConfigured(t *testing.T) {
	s := testutil.NewSandbox(t)
	regDir := filepath.Join(s.HydraHome, "registry")
	if err := os.MkdirAll(regDir, 0o700); err != nil {
		t.Fatal(err)
	}
	yaml := "workspaces: {}\nvalidators:\n  go: null\n  py: null\n"
	if err := os.WriteFile(filepath.Join(regDir, "workspace.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := outputHandlingCategory().Status; got != Gap {
		t.Errorf("every validator nulled out: Status = %q, want Gap", got)
	}
}

func TestLLM05OutputHandling_EnforcedByDefault(t *testing.T) {
	testutil.NewSandbox(t)
	if got := outputHandlingCategory().Status; got != Enforced {
		t.Errorf("embedded default registry: Status = %q, want Enforced", got)
	}
}

// A gap with no matching history entry is brand-new, this run is the first
// evidence of it, so age must be zero rather than undefined/negative.
func TestAnnotateGapAge_BrandNewGapHasZeroAge(t *testing.T) {
	cats := []Category{{ID: "LLM03", Status: Gap}}
	got := annotateGapAge(cats, nil, time.Now().UTC())
	if got[0].GapAgeDays != 0 || got[0].GapSince != "" {
		t.Errorf("brand-new gap = %+v, want zero age and no GapSince", got[0])
	}
}

// The *earliest* history entry naming this category wins, not the most
// recent, age is "how long has this been broken," not "when did we last
// check."
func TestAnnotateGapAge_UsesEarliestHistoryOccurrence(t *testing.T) {
	now := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	history := []scoreEntry{
		{TS: now.Add(-40 * 24 * time.Hour).Format(time.RFC3339), Gaps: []string{"LLM03"}, Edition: LLMEdition},
		{TS: now.Add(-20 * 24 * time.Hour).Format(time.RFC3339), Gaps: []string{"LLM03"}, Edition: LLMEdition},
	}
	got := annotateGapAge([]Category{{ID: "LLM03", Status: Gap}}, history, now)
	if got[0].GapAgeDays != 40 {
		t.Errorf("GapAgeDays = %d, want 40 (the earliest occurrence, not the later one)", got[0].GapAgeDays)
	}
}

// The hazard #748 was split out for. Category IDs are only stable for LLM01
// and LLM02, so a stored "LLM06" from the 2025 ordering names Excessive Agency
// while a 2026 "LLM06" names something else. Matching across editions would
// report one category's history as another's age, wrong and invisible, in the
// field a reader trusts to say how long something has been broken.
func TestAnnotateGapAge_NeverMatchesAcrossEditions(t *testing.T) {
	now := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	history := []scoreEntry{
		// A different edition's LLM06 is a different category. Two years of it
		// must not become this category's age.
		{TS: now.Add(-700 * 24 * time.Hour).Format(time.RFC3339),
			Gaps: []string{"LLM06"}, Edition: "1999"},
		{TS: now.Add(-30 * 24 * time.Hour).Format(time.RFC3339),
			Gaps: []string{"LLM06"}, Edition: LLMEdition},
	}
	got := annotateGapAge([]Category{{ID: "LLM06", Status: Gap}}, history, now)
	if got[0].GapAgeDays != 30 {
		t.Errorf("GapAgeDays = %d, want 30: the other edition's LLM06 is a "+
			"different category and must not date this one", got[0].GapAgeDays)
	}
}

// Every row written before the edition field existed was scored against the
// 2025 ordering, so absence has to mean 2025 and not "unknown". Reading it as
// unknown would make each of those rows foreign and silently reset every
// existing gap's age to zero, which is the same class of lie in the other
// direction.
func TestScoreEntry_UnstampedIsThePreFieldEdition(t *testing.T) {
	if got := (scoreEntry{}).edition(); got != "2025" {
		t.Errorf("edition() = %q for an unstamped row, want 2025: that is what "+
			"every row written before the field existed was scored against", got)
	}
	if got := (scoreEntry{Edition: "2026"}).edition(); got != "2026" {
		t.Errorf("edition() = %q, want the stamped value", got)
	}
}

// A row of the current edition dates a gap normally. Paired with the
// cross-edition test above: together they show the filter admits what it
// should and refuses what it should, rather than refusing everything.
func TestAnnotateGapAge_CurrentEditionHistoryStillCounts(t *testing.T) {
	now := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	history := []scoreEntry{{
		TS:      now.Add(-45 * 24 * time.Hour).Format(time.RFC3339),
		Gaps:    []string{"LLM03"},
		Edition: LLMEdition,
	}}
	got := annotateGapAge([]Category{{ID: "LLM03", Status: Gap}}, history, now)
	if got[0].GapAgeDays != 45 {
		t.Errorf("GapAgeDays = %d, want 45", got[0].GapAgeDays)
	}
}

// The corrupt-timestamp fallback is a second reader of the same history and
// must apply the same rule, or a bad TS on the current edition's row silently
// reopens the cross-edition match the indexed path refuses.
func TestFirstParseableGapSince_AlsoRefusesOtherEditions(t *testing.T) {
	now := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	history := []scoreEntry{
		{TS: "not-a-timestamp", Gaps: []string{"LLM06"}, Edition: LLMEdition},
		{TS: now.Add(-500 * 24 * time.Hour).Format(time.RFC3339),
			Gaps: []string{"LLM06"}, Edition: "1999"},
	}
	if ts, _, ok := firstParseableGapSince(history, "LLM06"); ok {
		t.Errorf("fell back to a foreign edition's entry (%s); the only "+
			"current-edition row has a corrupt timestamp, so there is no age", ts)
	}
}

// A category that is not currently a Gap must never get an age, even if an
// older history entry happens to name it (it was a gap once and got fixed).
func TestAnnotateGapAge_SkipsNonGapCategories(t *testing.T) {
	history := []scoreEntry{{TS: "2020-01-01T00:00:00Z", Gaps: []string{"LLM01"}}}
	got := annotateGapAge([]Category{{ID: "LLM01", Status: Enforced}}, history, time.Now().UTC())
	if got[0].GapAgeDays != 0 || got[0].GapSince != "" {
		t.Errorf("non-Gap category = %+v, want no age annotation", got[0])
	}
}

// annotateGapAgeOld is the pre-#524 nested-loop implementation (categories ×
// history × gaps-per-entry via slices.Contains), kept only in this test file
// as a reference oracle to prove the linear rewrite above is behavior
// preserving before the original was deleted.
func annotateGapAgeOld(cats []Category, history []scoreEntry, now time.Time) []Category {
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

// Exercises earliest-wins, multiple gaps per entry, a category with no
// history at all, a non-gap category, and, the case most likely to break a
// naive index rewrite, a malformed timestamp on the *first* entry naming a
// category, where the old scan silently skipped it and kept looking for the
// next-oldest entry naming the same ID.
func TestAnnotateGapAge_MatchesOldImplementation(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	// Stamped with the current edition throughout: this exercises earliest-wins
	// and the malformed-timestamp skip, not the cross-edition refusal, which has
	// its own test. annotateGapAgeOld ignores the field, so it is neutral there.
	history := []scoreEntry{
		{TS: now.Add(-90 * 24 * time.Hour).Format(time.RFC3339), Gaps: []string{"LLM07"}, Edition: LLMEdition},
		{TS: "not-a-timestamp", Gaps: []string{"LLM03", "LLM06"}, Edition: LLMEdition},
		{TS: now.Add(-60 * 24 * time.Hour).Format(time.RFC3339), Gaps: []string{"LLM03", "LLM10"}, Edition: LLMEdition},
		{TS: now.Add(-30 * 24 * time.Hour).Format(time.RFC3339), Gaps: []string{"LLM06", "LLM10"}, Edition: LLMEdition},
		{TS: now.Add(-5 * 24 * time.Hour).Format(time.RFC3339), Gaps: []string{"LLM07", "LLM09"}, Edition: LLMEdition},
	}
	cats := []Category{
		{ID: "LLM01", Status: Enforced}, // never a gap, must stay unannotated
		{ID: "LLM03", Status: Gap},      // first (bad-ts) sighting must be skipped, second used
		{ID: "LLM06", Status: Gap},      // same, different entries
		{ID: "LLM07", Status: Gap},      // earliest of two valid sightings must win
		{ID: "LLM09", Status: Gap},      // single sighting
		{ID: "LLM10", Status: Gap},      // earliest of two valid sightings must win
	}

	want := annotateGapAgeOld(cats, history, now)
	got := annotateGapAge(cats, history, now)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("annotateGapAge diverged from the reference implementation:\ngot  %+v\nwant %+v", got, want)
	}
	// Pin the actual values too, so a bug that happens to move both
	// implementations the same wrong way can't hide behind DeepEqual.
	byID := map[string]Category{}
	for _, c := range got {
		byID[c.ID] = c
	}
	if got := byID["LLM03"].GapAgeDays; got != 60 {
		t.Errorf("LLM03 GapAgeDays = %d, want 60 (bad-ts entry skipped)", got)
	}
	if got := byID["LLM06"].GapAgeDays; got != 30 {
		t.Errorf("LLM06 GapAgeDays = %d, want 30 (bad-ts entry skipped)", got)
	}
	if got := byID["LLM07"].GapAgeDays; got != 90 {
		t.Errorf("LLM07 GapAgeDays = %d, want 90 (earliest sighting)", got)
	}
	if got := byID["LLM09"].GapAgeDays; got != 5 {
		t.Errorf("LLM09 GapAgeDays = %d, want 5", got)
	}
	if got := byID["LLM10"].GapAgeDays; got != 60 {
		t.Errorf("LLM10 GapAgeDays = %d, want 60 (earliest sighting)", got)
	}
}

// buildGapHistory synthesizes n history entries in the shape
// security_score.jsonl actually accumulates, engineered for the old scan's
// worst case: the categories being looked up only start appearing as gaps in
// the very last entry, so a per-category rescan that walks oldest-first
// cannot short-circuit early for any of them, it must cross nearly the
// whole file before it finds (or fails to find) a match. That is the
// realistic shape too: a gap that has existed since day one already matches
// at history[0] and old's "break on first match" makes it cheap; a nested
// scan only gets expensive for a gap that is recent relative to a long history.
func buildGapHistory(n int) []scoreEntry {
	ids := []string{"LLM03", "LLM05", "LLM06", "LLM07", "LLM09", "LLM10"}
	base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	out := make([]scoreEntry, n)
	for i := range out {
		gaps := []string{"LLM_UNRELATED"} // padding row: a real entry, never a match
		if i == n-1 {
			gaps = ids
		}
		out[i] = scoreEntry{TS: base.Add(time.Duration(i) * time.Hour).Format(time.RFC3339), Gaps: gaps}
	}
	return out
}

// A relative wall-clock comparison against annotateGapAgeOld is inherently
// flaky on shared CI runners (#526, both sides are sub-2ms at n=50000, so
// runner jitter alone can erase the gap). This instead pins an absolute
// ceiling generous enough to only fire if the one-pass rewrite regresses back
// toward quadratic behavior, never on ordinary scheduling noise.
func TestAnnotateGapAge_CompletesQuicklyAtScale(t *testing.T) {
	const n = 50000
	history := buildGapHistory(n)
	cats := []Category{
		{ID: "LLM03", Status: Gap}, {ID: "LLM05", Status: Gap}, {ID: "LLM06", Status: Gap},
		{ID: "LLM07", Status: Gap}, {ID: "LLM09", Status: Gap}, {ID: "LLM10", Status: Gap},
	}
	now := time.Now().UTC()

	start := time.Now()
	annotateGapAge(cats, history, now)
	elapsed := time.Since(start)

	const ceiling = 100 * time.Millisecond
	if elapsed > ceiling {
		t.Errorf("annotateGapAge took %v at n=%d history entries, want under %v", elapsed, n, ceiling)
	}
}

// BenchmarkAnnotateGapAge and BenchmarkAnnotateGapAgeOld are the real
// before/after comparison at #524's "50x" synthetic data volume, run with
// `go test ./internal/security/ -bench AnnotateGapAge -run ^$`.
func BenchmarkAnnotateGapAge(b *testing.B) {
	history := buildGapHistory(50000)
	cats := []Category{
		{ID: "LLM03", Status: Gap}, {ID: "LLM05", Status: Gap}, {ID: "LLM06", Status: Gap},
		{ID: "LLM07", Status: Gap}, {ID: "LLM09", Status: Gap}, {ID: "LLM10", Status: Gap},
	}
	now := time.Now().UTC()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		annotateGapAge(cats, history, now)
	}
}

func BenchmarkAnnotateGapAgeOld(b *testing.B) {
	history := buildGapHistory(50000)
	cats := []Category{
		{ID: "LLM03", Status: Gap}, {ID: "LLM05", Status: Gap}, {ID: "LLM06", Status: Gap},
		{ID: "LLM07", Status: Gap}, {ID: "LLM09", Status: Gap}, {ID: "LLM10", Status: Gap},
	}
	now := time.Now().UTC()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		annotateGapAgeOld(cats, history, now)
	}
}

// The published 2026 ordering, transcribed from the contents page of
// OWASP-GenAI-LLM-Top-10-2026-v1.0.pdf ("Version 2026", August 4th 2026).
//
// Hardcoded on purpose, and in slice order rather than as a set: the whole of
// #748 is that a category can silently inherit a neighbour's number, and a
// test that only checked membership would pass through exactly that. Seven of
// the ten moved between 2025 and 2026; only LLM01 and LLM02 held.
func TestComputeCoverage_MatchesThePublished2026Ordering(t *testing.T) {
	testutil.NewSandbox(t)

	want := []struct{ id, name string }{
		{"LLM01", "Prompt Injection"},
		{"LLM02", "Sensitive Information Disclosure"},
		{"LLM03", "Excessive Agency"},
		{"LLM04", "Supply Chain"},
		{"LLM05", "Data and Model Poisoning"},
		{"LLM06", "Unbounded Consumption"},
		{"LLM07", "Misinformation"},
		{"LLM08", "Hidden Context Exposure"},
		{"LLM09", "Vector and Embedding Weaknesses"},
		{"LLM10", "Improper Output Handling"},
	}

	cov := computeCoverage(ledger.Policy{}, SupplyChain{}, nil, 0)
	if len(cov.Categories) != len(want) {
		t.Fatalf("scored %d categories, want %d", len(cov.Categories), len(want))
	}
	for i, w := range want {
		got := cov.Categories[i]
		if got.ID != w.id || got.Name != w.name {
			t.Errorf("position %d = %s %q, want %s %q", i+1, got.ID, got.Name, w.id, w.name)
		}
	}

	// The stamp has to move with the numbers or gap age is computed across two
	// different lists, which is the data hazard #803 landed the gate for.
	if cov.Edition != "2026" {
		t.Errorf("Edition = %q alongside the 2026 ordering, want 2026", cov.Edition)
	}
}
