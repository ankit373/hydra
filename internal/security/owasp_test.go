// SPDX-License-Identifier: MIT

package security

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/ledger"
	"github.com/ankit373/hydra/internal/testutil"
	"github.com/ankit373/hydra/internal/trust"
)

// LLM01/LLM02 are always Enforced (automatic, no config), LLM07 is always Gap
// (no mechanism exists), LLM04/LLM08 are always N/A — these don't depend on
// install state, unlike LLM03/05/06/09/10. LLM03 is Gap only while nothing is
// being fingerprinted, which is what an empty SupplyChain means here.
func TestComputeCoverage_StaticCategoriesAreFixed(t *testing.T) {
	testutil.NewSandbox(t)

	cov := computeCoverage(ledger.Policy{}, nil, SupplyChain{})
	want := map[string]CoverageStatus{
		"LLM01": Enforced, "LLM02": Enforced,
		"LLM03": Gap, "LLM07": Gap,
		"LLM04": NotApplicable, "LLM08": NotApplicable,
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

func TestComputeCoverage_NAExcludedFromBothNumeratorAndDenominator(t *testing.T) {
	testutil.NewSandbox(t)

	cov := computeCoverage(ledger.Policy{}, nil, SupplyChain{})
	if cov.Applicable != 8 {
		t.Errorf("Applicable = %d, want 8 (10 categories minus LLM04 and LLM08)", cov.Applicable)
	}
	for _, c := range cov.Categories {
		if c.ID == "LLM04" || c.ID == "LLM08" {
			continue
		}
		if c.Status == NotApplicable {
			t.Errorf("%s unexpectedly marked N/A", c.ID)
		}
	}
}

func TestLLM06ExcessiveAgency_ConfiguredOnlyWithAResourceScopedRule(t *testing.T) {
	none := ledger.Policy{Rules: []ledger.Rule{{Tool: "a", Decision: ledger.Allow}}}
	if got := llm06ExcessiveAgency(none).Status; got != Gap {
		t.Errorf("no resource-scoped rule: Status = %q, want Gap", got)
	}

	scoped := ledger.Policy{Rules: []ledger.Rule{{Resource: "internal/auth/*", Decision: ledger.Deny}}}
	if got := llm06ExcessiveAgency(scoped).Status; got != Configured {
		t.Errorf("a resource-scoped rule exists: Status = %q, want Configured", got)
	}
}

func TestLLM09Misinformation_ConfiguredOnlyWithARecordedRun(t *testing.T) {
	testutil.NewSandbox(t)

	if got := llm09Misinformation().Status; got != Gap {
		t.Errorf("no trust.jsonl: Status = %q, want Gap", got)
	}

	if err := os.MkdirAll(filepath.Dir(trust.DefaultLogPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	row := `{"ts":"2026-01-01T00:00:00Z","task_hash":"abc","domain":"go","target_conf":0.9,"final_conf":0.9,"samples":1,"decision":"accept"}` + "\n"
	if err := os.WriteFile(trust.DefaultLogPath(), []byte(row), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := llm09Misinformation().Status; got != Configured {
		t.Errorf("a recorded run exists: Status = %q, want Configured", got)
	}
}

func TestLLM10UnboundedConsumption_ConfiguredOnlyWithACostCeilingDenial(t *testing.T) {
	none := []ledger.Event{{Tool: "a", Decision: ledger.Deny, Reason: "denied by ledger policy"}}
	if got := llm10UnboundedConsumption(none).Status; got != Gap {
		t.Errorf("no cost-ceiling denial: Status = %q, want Gap", got)
	}

	withCeiling := []ledger.Event{{Tool: "a", Decision: ledger.Deny, Reason: "exceeds cost ceiling: estimated $1 > limit $0.5"}}
	if got := llm10UnboundedConsumption(withCeiling).Status; got != Configured {
		t.Errorf("a cost-ceiling denial exists: Status = %q, want Configured", got)
	}
}

// A custom workspace.yaml with every validator explicitly nulled must report
// Gap — the only way LLM05 should ever be Gap, since the embedded default
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

	if got := llm05OutputHandling().Status; got != Gap {
		t.Errorf("every validator nulled out: Status = %q, want Gap", got)
	}
}

func TestLLM05OutputHandling_EnforcedByDefault(t *testing.T) {
	testutil.NewSandbox(t)
	if got := llm05OutputHandling().Status; got != Enforced {
		t.Errorf("embedded default registry: Status = %q, want Enforced", got)
	}
}

// A gap with no matching history entry is brand-new — this run is the first
// evidence of it, so age must be zero rather than undefined/negative.
func TestAnnotateGapAge_BrandNewGapHasZeroAge(t *testing.T) {
	cats := []Category{{ID: "LLM03", Status: Gap}}
	got := annotateGapAge(cats, nil, time.Now().UTC())
	if got[0].GapAgeDays != 0 || got[0].GapSince != "" {
		t.Errorf("brand-new gap = %+v, want zero age and no GapSince", got[0])
	}
}

// The *earliest* history entry naming this category wins, not the most
// recent — age is "how long has this been broken," not "when did we last
// check."
func TestAnnotateGapAge_UsesEarliestHistoryOccurrence(t *testing.T) {
	now := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	history := []scoreEntry{
		{TS: now.Add(-40 * 24 * time.Hour).Format(time.RFC3339), Gaps: []string{"LLM03"}},
		{TS: now.Add(-20 * 24 * time.Hour).Format(time.RFC3339), Gaps: []string{"LLM03"}},
	}
	got := annotateGapAge([]Category{{ID: "LLM03", Status: Gap}}, history, now)
	if got[0].GapAgeDays != 40 {
		t.Errorf("GapAgeDays = %d, want 40 (the earliest occurrence, not the later one)", got[0].GapAgeDays)
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
// history at all, a non-gap category, and — the case most likely to break a
// naive index rewrite — a malformed timestamp on the *first* entry naming a
// category, where the old scan silently skipped it and kept looking for the
// next-oldest entry naming the same ID.
func TestAnnotateGapAge_MatchesOldImplementation(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	history := []scoreEntry{
		{TS: now.Add(-90 * 24 * time.Hour).Format(time.RFC3339), Gaps: []string{"LLM07"}},
		{TS: "not-a-timestamp", Gaps: []string{"LLM03", "LLM06"}},
		{TS: now.Add(-60 * 24 * time.Hour).Format(time.RFC3339), Gaps: []string{"LLM03", "LLM10"}},
		{TS: now.Add(-30 * 24 * time.Hour).Format(time.RFC3339), Gaps: []string{"LLM06", "LLM10"}},
		{TS: now.Add(-5 * 24 * time.Hour).Format(time.RFC3339), Gaps: []string{"LLM07", "LLM09"}},
	}
	cats := []Category{
		{ID: "LLM01", Status: Enforced}, // never a gap — must stay unannotated
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
// cannot short-circuit early for any of them — it must cross nearly the
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

// The pre-#524 scan re-walked all of history once per gap category; at the
// security_score.jsonl volumes real machines reach (the issue measured
// ~360ms at ~187k lines), that repeated rescan is what crossed the
// render-blocking threshold. This asserts the one-pass-index rewrite is
// substantially faster on identical input, not merely equivalent.
func TestAnnotateGapAge_FasterThanOldAtScale(t *testing.T) {
	const n = 50000
	history := buildGapHistory(n)
	cats := []Category{
		{ID: "LLM03", Status: Gap}, {ID: "LLM05", Status: Gap}, {ID: "LLM06", Status: Gap},
		{ID: "LLM07", Status: Gap}, {ID: "LLM09", Status: Gap}, {ID: "LLM10", Status: Gap},
	}
	now := time.Now().UTC()

	start := time.Now()
	want := annotateGapAgeOld(cats, history, now)
	oldTime := time.Since(start)

	start = time.Now()
	got := annotateGapAge(cats, history, now)
	newTime := time.Since(start)

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("annotateGapAge diverged from the reference implementation at scale")
	}
	// The fix removes the per-category rescan, so the speedup roughly tracks
	// len(cats)=6. 2x is a conservative floor that only fails if the rewrite
	// regresses back toward the old nested-loop shape.
	if newTime*2 > oldTime {
		t.Errorf("annotateGapAge (%v) is not meaningfully faster than the old nested-loop scan (%v) at n=%d history entries",
			newTime, oldTime, n)
	}
}

// BenchmarkAnnotateGapAge and BenchmarkAnnotateGapAgeOld are the real
// before/after comparison at #524's "50x" synthetic data volume — run with
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
