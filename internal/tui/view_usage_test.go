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
		"PER PASSED ANSWER", "not enough checked answers yet",
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
	m.fold(rows, nil, now)
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

// Below cost.MinChecked checked answers the tile shows no figure, only how far
// the history has to go, and no surface compares against a model nobody ran.
func TestUsagePerPassed_ThinHistoryShowsNoFigure(t *testing.T) {
	now := fixedNow()
	day := now.Format("2006-01-02")
	rows, passed := checkedRows(day, "summarise", "ollama/qwen", cost.MinChecked-1, 0.001, 1)
	m := testCockpit()
	m.metrics = ckMetrics{stats: map[string]*ckModelStat{}, localModels: map[string]bool{}, runCost: map[string]ckRunCost{}}
	m.metrics.fold(rows, passed, now)

	out := stripANSI(m.usagePerPassed())
	want := fmt.Sprintf("%d of %d checked", cost.MinChecked-1, cost.MinChecked)
	for _, s := range []string{"not enough checked answers yet", want} {
		if !strings.Contains(out, s) {
			t.Errorf("thin history tile missing %q:\n%s", s, out)
		}
	}
	if strings.Contains(out, "$") || strings.Contains(out, "%") {
		t.Errorf("thin history tile shows a figure:\n%s", out)
	}
	compact := stripANSI(m.usageTilesCompact(60))
	if !strings.Contains(compact, want) || strings.Contains(strings.ToLower(compact), "saved") {
		t.Errorf("compact tiles not honest about thin history:\n%s", compact)
	}
}

// With enough history the tile pairs cost per passed answer with its pass rate
// and interval, and names the best single model measured here, never a vendor.
func TestUsagePerPassed_PairsCostWithPassRate(t *testing.T) {
	now := fixedNow()
	day := now.Format("2006-01-02")
	rows, passed := checkedRows(day, "extract", "ollama/qwen", 40, 0.002, 0.75)
	m := testCockpit()
	m.metrics = ckMetrics{stats: map[string]*ckModelStat{}, localModels: map[string]bool{}, runCost: map[string]ckRunCost{}}
	m.metrics.fold(rows, passed, now)

	out := stripANSI(m.usagePerPassed())
	// 40 answers at $0.002 = $0.08 over 30 passes.
	for _, s := range []string{"$0.0027 · 75% passed (", "40 checked", "on extract, routed:", "best single model, ollama/qwen:"} {
		if !strings.Contains(out, s) {
			t.Errorf("tile missing %q:\n%s", s, out)
		}
	}
	if strings.Contains(strings.ToLower(out), "saved") || strings.Contains(strings.ToLower(out), "would") {
		t.Errorf("tile claims a hypothetical saving:\n%s", out)
	}
	compact := stripANSI(m.usageTilesCompact(200))
	if !strings.Contains(compact, "PER PASS $0.0027 · 75% passed (") || !strings.Contains(compact, "on extract routed $0.0027 · 75% passed (60–86%), best single ollama/qwen $0.0027") {
		t.Errorf("compact tiles lost the comparison:\n%s", compact)
	}
}

// checkedRows is n checked answers of one kind on one model, the first
// passRate*n of them passed.
func checkedRows(day, kind, model string, n int, usd, passRate float64) ([]cost.Row, map[string]bool) {
	rows := make([]cost.Row, n)
	passed := make(map[string]bool, n)
	for i := range rows {
		span := "s" + strconv.Itoa(i)
		rows[i] = cost.Row{TS: day + "T08:00:00Z", Model: model, Domain: kind, SpanID: span, EstCostUSD: usd}
		passed[span] = float64(i) < passRate*float64(n)
	}
	return rows, passed
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
	if !strings.Contains(out, "bars by calls, everything was free") {
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
		"bands 0-49 normal · 50 compact · 65 caution",
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

// The tier label in byTierToday is "T<tier>", strconv, not a homegrown itoa.
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
