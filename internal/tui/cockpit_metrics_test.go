// SPDX-License-Identifier: MIT

package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/cost"
	"github.com/ankit373/hydra/internal/graph"
	"github.com/ankit373/hydra/internal/ledger"
	"github.com/ankit373/hydra/internal/security"
)

// cost.jsonl carries wall_ms on every row, so a per-head latency trace is real
// data — the thing the old LCG-hashed "sparkline" was imitating.
func TestLatencySeries_FromRealRows(t *testing.T) {
	rows := []cost.Row{
		{Model: "A", WallMS: 100},
		{Model: "B", WallMS: 900},
		{Model: "A", WallMS: 200},
		{Model: "A", WallMS: 300},
		{Model: "C"}, // no timing — must not create a series
	}
	series, last := ckLatencySeries(rows)

	if got := series["A"]; len(got) != 3 || got[0] != 100 || got[2] != 300 {
		t.Errorf("A series = %v, want [100 200 300] oldest-first", got)
	}
	if last["A"] != 300 {
		t.Errorf("A last = %d, want the most recent (300)", last["A"])
	}
	if _, ok := series["C"]; ok {
		t.Error("a row with no wall_ms produced a series")
	}
}

func TestLatencySeries_CapsToSparkWidth(t *testing.T) {
	var rows []cost.Row
	for i := 0; i < ckSparkWidth*3; i++ {
		rows = append(rows, cost.Row{Model: "A", WallMS: int64(i + 1)})
	}
	series, _ := ckLatencySeries(rows)
	got := series["A"]
	if len(got) != ckSparkWidth {
		t.Fatalf("series length = %d, want %d", len(got), ckSparkWidth)
	}
	// Must keep the newest samples, not the oldest.
	if got[len(got)-1] != float64(ckSparkWidth*3) {
		t.Errorf("last sample = %v, want the most recent", got[len(got)-1])
	}
}

// One sample is not a trace. Rendering a single bar would imply a trend that
// was never measured.
func TestSpark_TooFewSamplesRendersDash(t *testing.T) {
	if got := ckSpark(nil); got != "—" {
		t.Errorf("ckSpark(nil) = %q, want —", got)
	}
	if got := ckSpark([]float64{5}); got != "—" {
		t.Errorf("ckSpark(one sample) = %q, want —", got)
	}
}

func TestSpark_ScalesToSeriesRange(t *testing.T) {
	got := ckSpark([]float64{10, 20, 30})
	if len([]rune(got)) != 3 {
		t.Fatalf("ckSpark produced %d glyphs, want 3", len([]rune(got)))
	}
	r := []rune(got)
	if r[0] != '▁' {
		t.Errorf("minimum sample rendered %q, want the lowest block", string(r[0]))
	}
	if r[2] != '█' {
		t.Errorf("maximum sample rendered %q, want the highest block", string(r[2]))
	}
	// A flat series must not render as noise.
	flat := ckSpark([]float64{7, 7, 7, 7})
	for _, g := range flat {
		if g != '▁' {
			t.Errorf("flat series rendered %q — a constant latency must look constant", flat)
			break
		}
	}
}

// Savings compares real spend against the same work priced at tier 1. Both
// sides come from real rows, so the comparison is like-for-like.
func TestSavings_RealComparison(t *testing.T) {
	rows := []cost.Row{
		{Tier: 10, PromptTokens: 1000, ResponseTokens: 500, EstCostUSD: 0.0001},
		{Tier: 8, PromptTokens: 2000, ResponseTokens: 1000, EstCostUSD: 0.0004},
	}
	saved, baseline := ckSavings(rows, testPricing(t))

	if baseline <= 0 {
		t.Fatal("baseline is zero — tier-1 pricing did not resolve")
	}
	if saved <= 0 {
		t.Errorf("saved = %v, want positive (cheap tiers vs a tier-1 baseline)", saved)
	}
	if saved > baseline {
		t.Errorf("saved (%v) exceeds the baseline (%v)", saved, baseline)
	}
}

// Routing everything to tier 1 saves nothing. The panel must never claim a
// saving that did not happen.
func TestSavings_NeverNegative(t *testing.T) {
	rows := []cost.Row{
		{Tier: 1, PromptTokens: 1000, ResponseTokens: 500, EstCostUSD: 999.0},
	}
	saved, _ := ckSavings(rows, testPricing(t))
	if saved != 0 {
		t.Errorf("saved = %v, want 0 — spending more than baseline is not a saving", saved)
	}
}

func TestSavings_NilPricingIsZero(t *testing.T) {
	saved, base := ckSavings([]cost.Row{{Tier: 1, EstCostUSD: 1}}, nil)
	if saved != 0 || base != 0 {
		t.Errorf("with no pricing DB got saved=%v base=%v, want 0/0", saved, base)
	}
}

// With no graph loaded there is no blast radius to report — and reporting one
// anyway is exactly the bug this replaced.
func TestBlastFor_NoGraphSaysNothing(t *testing.T) {
	var m ckMetrics
	if _, _, _, ok := m.ckBlastFor("internal/auth/token.go"); ok {
		t.Error("reported a blast radius with no graph loaded")
	}
	if _, _, _, ok := m.ckBlastFor(""); ok {
		t.Error("reported a blast radius for an empty path")
	}
}

// An uncalibrated head has no diagnosticity to show.
func TestDiagnosticity_UncalibratedIsZero(t *testing.T) {
	var m ckMetrics
	if d := m.ckDiagnosticity("some-head", ""); d != 0 {
		t.Errorf("uncalibrated head reported D=%v, want 0 so the view renders —", d)
	}
}

func TestFmtMS(t *testing.T) {
	for _, tt := range []struct {
		in   int64
		want string
	}{
		{0, "—"}, {-1, "—"}, {250, "250ms"}, {9999, "9999ms"}, {10000, "10.0s"}, {37654, "37.7s"},
	} {
		if got := ckFmtMS(tt.in); got != tt.want {
			t.Errorf("ckFmtMS(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSortedModels_MostSampledFirstAndStable(t *testing.T) {
	m := ckMetrics{latency: map[string][]float64{
		"few":  {1, 2},
		"many": {1, 2, 3, 4},
		"mid":  {1, 2, 3},
	}}
	got := m.ckSortedModels()
	want := []string{"many", "mid", "few"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
	// Stable across calls — not map-random.
	for i := 0; i < 20; i++ {
		if strings.Join(m.ckSortedModels(), ",") != strings.Join(got, ",") {
			t.Fatal("ordering is not stable between calls")
		}
	}
}

// A head with no recorded runs must render "—", never a zero-filled chart that
// implies it ran and was instant.
func TestDashboard_HeadWithNoHistoryRendersDash(t *testing.T) {
	m := Cockpit{
		w: 120, h: 30, ready: true, mode: "dispatch",
		heads:   []ckHead{{name: "never-ran", tier: 5, up: true, color: ckCyan}},
		metrics: ckMetrics{latency: map[string][]float64{}, lastMS: map[string]int64{}},
	}
	out := m.dash(120, 30)
	if !strings.Contains(out, "—") {
		t.Errorf("a head with no history did not render an em dash:\n%s", out)
	}
}

// The real series must actually reach the rendered dashboard.
func TestDashboard_RendersRealLatency(t *testing.T) {
	m := Cockpit{
		w: 120, h: 30, ready: true, mode: "dispatch",
		heads: []ckHead{{name: "busy", tier: 3, up: true, color: ckCyan}},
		metrics: ckMetrics{
			latency: map[string][]float64{"busy": {100, 500, 200, 900}},
			lastMS:  map[string]int64{"busy": 900},
		},
	}
	out := m.dash(120, 30)
	if !strings.Contains(out, "900ms") {
		t.Errorf("real last-latency not rendered:\n%s", out)
	}
	if !strings.ContainsAny(out, "▁▂▃▄▅▆▇█") {
		t.Errorf("real sparkline not rendered:\n%s", out)
	}
}

// stubPricer prices tiers deterministically, so the savings arithmetic is
// tested without depending on a resolvable registry/pricing.yaml.
type stubPricer struct{}

func (stubPricer) EstimateCost(tier, in, out int) float64 {
	// Cheap tiers cost proportionally less, mirroring the real ramp.
	perTok := 0.00002 / float64(tier)
	return float64(in+out) * perTok
}

func testPricing(t *testing.T) ckPricer {
	t.Helper()
	return stubPricer{}
}

// A graph that does contain the file must yield real numbers — dependents and
// κ walked from the graph, never a literal. This is the wiring that replaced
// the hardcoded "κ=3.1 ⚠ 12 dependents".
func TestBlastFor_RealGraphYieldsRealNumbers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "graph.json")
	// hub.go is depended on by three others; leaf.go by none.
	doc := `{"nodes":[
	  {"id":"hub","file":"hub.go"},
	  {"id":"a","file":"a.go"},
	  {"id":"b","file":"b.go"},
	  {"id":"c","file":"c.go"},
	  {"id":"leaf","file":"leaf.go"}
	],"edges":[
	  {"from":"a","to":"hub"},
	  {"from":"b","to":"hub"},
	  {"from":"c","to":"hub"}
	]}`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	g, err := graph.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	m := ckMetrics{graph: g}

	radius, deps, _, ok := m.ckBlastFor("hub.go")
	if !ok {
		t.Fatal("a file with real dependents reported no blast radius")
	}
	if deps != 3 {
		t.Errorf("dependents = %d, want 3 — walked from the graph, not a literal", deps)
	}
	if radius <= 1.0 {
		t.Errorf("radius = %v, want >1 for a file with dependents", radius)
	}

	// A file nobody depends on must not be dressed up as risky.
	if _, _, _, ok := m.ckBlastFor("leaf.go"); ok {
		t.Error("a leaf file reported a blast radius")
	}
	// A file absent from the graph reports nothing at all.
	if _, _, _, ok := m.ckBlastFor("not-in-graph.go"); ok {
		t.Error("a file absent from the graph reported a blast radius")
	}
}

// dashSecurity must handle all three real states without panicking or
// rendering an empty frame: no report at all (Build failed), a normal
// report, and one where the ledger chain has been tampered with.

func TestDashSecurity_NilReportRendersUnavailable(t *testing.T) {
	m := Cockpit{w: 120, h: 30, ready: true, security: nil}
	out := m.dashSecurity(120, 30)
	if !strings.Contains(out, "unavailable") {
		t.Errorf("a nil security report should say so plainly:\n%s", out)
	}
}

func TestDashSecurity_RendersCoverageAndActions(t *testing.T) {
	m := Cockpit{w: 120, h: 30, ready: true, security: &security.Report{
		IntegrityIntact: true,
		Coverage: security.Coverage{
			Applicable: 8, Covered: 3, PercentCovered: 37.5,
			Categories: []security.Category{
				{ID: "LLM01", Name: "Prompt Injection", Status: security.Enforced},
				{ID: "LLM03", Name: "Supply Chain", Status: security.Gap, Detail: "no integrity verification", GapAgeDays: 45},
				{ID: "LLM04", Name: "Data and Model Poisoning", Status: security.NotApplicable},
			},
		},
		Trend:  security.Trend{Available: true, DeltaPct: 12, FirstPct: 25.5},
		ByHead: []ledger.HeadRisk{{Head: "sketchy", Denied: 2, Flagged: 1}},
		History: []security.HistoryPoint{
			{TS: "2026-07-01T00:00:00Z", PercentCovered: 12.5},
			{TS: "2026-08-01T00:00:00Z", PercentCovered: 37.5},
		},
		Actions: []security.Action{
			{ID: "LLM03", Kind: "gap", Title: "Supply Chain", Detail: "no integrity verification",
				AgeDays: 45, Priority: security.PriorityNow},
		},
	}}
	out := m.dashSecurity(120, 30)
	for _, want := range []string{
		"37%", "LLM01", "enforced", "LLM03", "gap", "45d", "sketchy", "denied 2",
		"NOW", "Supply Chain", "history",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dashSecurity output missing %q:\n%s", want, out)
		}
	}
	// The real ckSpark sparkline, not a placeholder — same block glyphs the
	// latency panel already uses.
	if !strings.ContainsAny(out, "▁▂▃▄▅▆▇█") {
		t.Errorf("dashSecurity did not render a real history sparkline:\n%s", out)
	}
	// N/A categories must never appear in the rendered list.
	if strings.Contains(out, "LLM04") {
		t.Errorf("an N/A category (LLM04) was rendered:\n%s", out)
	}
}

func TestDashSecurity_IntegrityCompromisedOverridesTheScore(t *testing.T) {
	m := Cockpit{w: 120, h: 30, ready: true, security: &security.Report{
		IntegrityIntact: false,
		Coverage:        security.Coverage{Applicable: 8, Covered: 8, PercentCovered: 100},
	}}
	out := m.dashSecurity(120, 30)
	if !strings.Contains(out, "INTEGRITY COMPROMISED") {
		t.Errorf("a broken chain must override the headline, got:\n%s", out)
	}
	if strings.Contains(out, "100%") {
		t.Errorf("the coverage percentage must not render when integrity is compromised:\n%s", out)
	}
}
