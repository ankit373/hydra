// SPDX-License-Identifier: MIT

package tui

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/cost"
)

// The three tiles and the breakdown come from one fold of the cost rows, so
// no figure can contradict another on the same screen.
func TestViewUsage_TilesCohere(t *testing.T) {
	m := testCockpit()
	m.metrics = usageMetrics(t)
	m.view = ckViewUsage
	out := stripANSI(m.viewUsage(120, 30))

	for _, want := range []string{
		"TODAY", "$0.0300", "3 runs · 2 requests",
		"THIS MONTH", "$0.0700", "3 requests",
		"SAVED vs ALL-FRONTIER",
		"BY MODEL · today",
		"CONTEXT BUDGET",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("usage view missing %q:\n%s", want, out)
		}
	}
}

// usageMetrics folds a small deterministic row set: two rows today (one local
// free, one paid), one row last week (same month).
func usageMetrics(t *testing.T) ckMetrics {
	t.Helper()
	m := ckMetrics{
		stats:       map[string]*ckModelStat{},
		localModels: map[string]bool{},
		runCost:     map[string]ckRunCost{},
	}
	now := fixedNow()
	day := now.Format("2006-01-02")
	rows := []cost.Row{
		{TS: day + "T10:00:00Z", Tier: 10, Model: "qwen (Ollama)", Executor: "local",
			PromptTokens: 1000, ResponseTokens: 500, EstCostUSD: 0, WallMS: 300, TokensSource: "actual", RunID: "r1"},
		{TS: day + "T11:00:00Z", Tier: 7, Model: "Gemini Flash", Executor: "agy",
			PromptTokens: 2000, ResponseTokens: 1000, EstCostUSD: 0.03, WallMS: 900, TokensSource: "estimated", RunID: "r2"},
		{TS: now.AddDate(0, 0, -6).Format("2006-01-02") + "T10:00:00Z", Tier: 7, Model: "Gemini Flash", Executor: "agy",
			PromptTokens: 1000, ResponseTokens: 500, EstCostUSD: 0.04, TokensSource: "estimated", RunID: "r3"},
	}
	m.fold(rows, stubPricer{}, now)
	return m
}

// fixedNow is mid-month so the "-6 days" row stays in the same UTC month.
func fixedNow() time.Time {
	t, err := time.Parse(time.RFC3339, "2026-09-15T12:00:00Z")
	if err != nil {
		panic(err)
	}
	return t
}

// The est/actual honesty line reflects the real token provenance mix.
func TestUsageToday_TokenHonesty(t *testing.T) {
	m := testCockpit()
	m.metrics = usageMetrics(t)
	out := stripANSI(m.usageToday())
	if !strings.Contains(out, "actual") || !strings.Contains(out, "estimated") {
		t.Errorf("mixed provenance not disclosed:\n%s", out)
	}
	if !strings.Contains(out, "estimated, never billed") {
		t.Errorf("the cost-source honesty line is missing:\n%s", out)
	}

	empty := testCockpit()
	empty.metrics = ckMetrics{}
	empty.runsToday = nil
	out = stripANSI(empty.usageToday())
	if !strings.Contains(out, "no requests yet") {
		t.Errorf("empty today tile not honest:\n%s", out)
	}
}

// SAVED must cohere: saved = tier1 − actual and the % = saved/tier1. When the
// baseline cannot be priced, the tile renders "—", never a fabricated figure.
func TestUsageSaved_MathCoheres(t *testing.T) {
	m := testCockpit()
	m.metrics = usageMetrics(t)
	out := stripANSI(m.usageSaved())

	base := m.metrics.baseTodayUSD
	saved := m.metrics.savedTodayUSD
	if base <= 0 || saved <= 0 {
		t.Fatalf("fixture: base=%v saved=%v", base, saved)
	}
	wantPct := fmt.Sprintf("(%.0f%%)", saved/base*100)
	if !strings.Contains(out, wantPct) {
		t.Errorf("the %% does not cohere with the two $ figures: want %s in\n%s", wantPct, out)
	}
	if !strings.Contains(out, fmt.Sprintf("$%.4f", base)) {
		t.Errorf("the tier-1 total is not shown:\n%s", out)
	}

	// No pricing → no baseline → an honest dash.
	none := testCockpit()
	none.metrics = ckMetrics{}
	out = stripANSI(none.usageSaved())
	if !strings.Contains(out, "—") {
		t.Errorf("an unpriceable baseline did not render a dash:\n%s", out)
	}

	// Spend above the tier-1 estimate must not claim a saving.
	worse := testCockpit()
	worse.metrics.savedTodayUSD, worse.metrics.baseTodayUSD = -0.01, 0.02
	out = stripANSI(worse.usageSaved())
	if !strings.Contains(out, "$0.0000 (0%)") || !strings.Contains(out, "not below") {
		t.Errorf("negative savings not handled honestly:\n%s", out)
	}
}

// The savings arithmetic itself: like-for-like via the same pricer, and the
// baseline is zero when no pricer resolves.
func TestCkSavings(t *testing.T) {
	rows := []cost.Row{
		{Tier: 10, PromptTokens: 1000, ResponseTokens: 500, EstCostUSD: 0.0001},
		{Tier: 8, PromptTokens: 2000, ResponseTokens: 1000, EstCostUSD: 0.0004},
	}
	saved, baseline := ckSavings(rows, stubPricer{})
	if baseline <= 0 || saved <= 0 || saved > baseline {
		t.Errorf("saved=%v baseline=%v", saved, baseline)
	}
	if saved, base := ckSavings(rows, nil); saved != 0 || base != 0 {
		t.Errorf("nil pricer: %v/%v", saved, base)
	}
	// Spending more than tier-1 yields a negative saving — reported raw; the
	// tile decides how to word it.
	expensive := []cost.Row{{Tier: 1, PromptTokens: 100, ResponseTokens: 50, EstCostUSD: 999}}
	if saved, _ := ckSavings(expensive, stubPricer{}); saved >= 0 {
		t.Errorf("overspend hidden: saved=%v", saved)
	}
}

// stubPricer prices tiers deterministically, so the arithmetic is tested
// without a resolvable registry/pricing.yaml on disk.
type stubPricer struct{}

func (stubPricer) EstimateCost(tier, in, out int) float64 {
	return float64(in+out) * 0.00002 / float64(tier)
}

// BY MODEL marks local models "local · free" instead of a $0 that looks like
// missing data; m/t/d switch the grouping.
func TestUsageBreakdown_LocalFreeAndGroupings(t *testing.T) {
	m := testCockpit()
	m.metrics = usageMetrics(t)

	out := stripANSI(m.usageBreakdown())
	if !strings.Contains(out, "local · free") {
		t.Errorf("the local model is not marked free:\n%s", out)
	}
	if !strings.Contains(out, "$0.0300") {
		t.Errorf("the paid model's cost is missing:\n%s", out)
	}
	if !strings.Contains(out, "1 call") {
		t.Errorf("call counts missing:\n%s", out)
	}

	m.usageGroup = 't'
	out = stripANSI(m.usageBreakdown())
	if !strings.Contains(out, "BY TIER · today") || !strings.Contains(out, "T7") {
		t.Errorf("tier grouping wrong:\n%s", out)
	}
	m.usageGroup = 'd'
	out = stripANSI(m.usageBreakdown())
	if !strings.Contains(out, "BY DAY · last 14 days") {
		t.Errorf("day grouping wrong:\n%s", out)
	}

	// An all-free day scales bars by calls and says so.
	free := testCockpit()
	free.metrics = ckMetrics{
		byModelToday: []cost.GroupRow{{Key: "qwen", Calls: 5, EstCostUSD: 0}},
		localModels:  map[string]bool{"qwen": true},
	}
	out = stripANSI(free.usageBreakdown())
	if !strings.Contains(out, "bars by calls — everything was free") {
		t.Errorf("the by-calls fallback is not disclosed:\n%s", out)
	}
	if !strings.Contains(out, "▮") {
		t.Errorf("an all-free day rendered a blank chart:\n%s", out)
	}

	// Empty is empty.
	none := testCockpit()
	none.metrics = ckMetrics{}
	if out := stripANSI(none.usageBreakdown()); !strings.Contains(out, "no requests recorded") {
		t.Errorf("empty breakdown not honest:\n%s", out)
	}
}

// The context budget panel: band from internal/budget, burn/risk only with
// history, honest unknown state, and the exact band legend.
func TestUsageContextBudget(t *testing.T) {
	m := testCockpit()
	m.pctKnown, m.claudePct, m.pctHist = true, 52, []int{40, 46, 52}
	out := stripANSI(m.usageContextBudget())
	for _, want := range []string{
		"CONTEXT BUDGET", "52% · compact",
		"burn", "risk of hitting 80%",
		"bands 0–49 normal · 50 compact · 65 caution",
		"70 downgrade · 75 critical · 80 stop",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("context budget missing %q:\n%s", want, out)
		}
	}

	// Not enough history for a rate: say so, never a fabricated trend.
	m.pctHist = []int{52}
	out = stripANSI(m.usageContextBudget())
	if !strings.Contains(out, "not enough history for a rate") {
		t.Errorf("missing-history honesty line absent:\n%s", out)
	}

	// Unknown pct: honest empty state.
	m.pctKnown = false
	out = stripANSI(m.usageContextBudget())
	if !strings.Contains(out, "no context data yet") {
		t.Errorf("unknown claude_pct not honest:\n%s", out)
	}
}

// The usage view scrolls as one document: a long breakdown never buries the
// context-budget panel beyond reach (#630).
func TestUsageView_ScrollsToEverything(t *testing.T) {
	m := testCockpit()
	m.w, m.h = 80, 24
	m.view = ckViewUsage
	m.metrics.byModelToday = manyGroupRows(30)
	m.usageGroup = 'm'

	out := stripANSI(m.viewUsage(80, 21))
	if !strings.Contains(out, "↓") {
		t.Errorf("an overflowing usage view shows no scroll cue:\n%s", out)
	}
	if strings.Contains(out, "enlarge the terminal") {
		t.Errorf("usage told the user to resize instead of scrolling:\n%s", out)
	}
	// Every model listed, not a window of them.
	if !strings.Contains(stripANSI(m.usageBreakdown()), "model-d") {
		t.Error("the breakdown dropped rows instead of letting the view scroll")
	}

	m = m.scrollBy(ckScrollAll)
	out = stripANSI(m.viewUsage(80, 21))
	if !strings.Contains(out, "CONTEXT BUDGET") {
		t.Errorf("scrolling to the end never reaches the context budget:\n%s", out)
	}
	if !strings.Contains(out, "↑") {
		t.Errorf("a scrolled usage view shows no top cue:\n%s", out)
	}
}

// The tier label in byTierToday is "T<tier>" — strconv, not a homegrown itoa.
func TestFold_TierLabels(t *testing.T) {
	m := usageMetrics(t)
	found := false
	for _, r := range m.byTierToday {
		if r.Key == "T"+strconv.Itoa(7) {
			found = true
		}
	}
	if !found {
		t.Errorf("byTierToday = %+v, want a T7 row", m.byTierToday)
	}
}
