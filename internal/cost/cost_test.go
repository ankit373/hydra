// SPDX-License-Identifier: MIT

package cost

import (
	"fmt"
	"testing"
	"time"
)

func makeRow(model string, tier int, inTok, outTok int, costUSD float64, wallMS int64, swarmMode string, swarmWinner bool) Row {
	return Row{
		TS:             time.Now().UTC().Format(time.RFC3339),
		Tier:           tier,
		Model:          model,
		PromptTokens:   inTok,
		ResponseTokens: outTok,
		EstCostUSD:     costUSD,
		WallMS:         wallMS,
		SwarmMode:      swarmMode,
		SwarmWinner:    swarmWinner,
	}
}

func TestFilterDays_Zero(t *testing.T) {
	rows := []Row{makeRow("m", 1, 100, 50, 0.01, 1000, "", false)}
	got := FilterDays(rows, 0)
	if len(got) != 1 {
		t.Fatalf("FilterDays(0) should return all rows, got %d", len(got))
	}
}

func TestFilterDays_Excludes_Old(t *testing.T) {
	old := Row{TS: "2020-01-01T00:00:00Z", Model: "old"}
	fresh := makeRow("fresh", 1, 0, 0, 0, 0, "", false)
	got := FilterDays([]Row{old, fresh}, 1)
	if len(got) != 1 || got[0].Model != "fresh" {
		t.Fatalf("FilterDays should exclude old rows, got %v", got)
	}
}

func TestByModel_Groups(t *testing.T) {
	rows := []Row{
		makeRow("claude", 1, 100, 50, 0.10, 500, "", false),
		makeRow("claude", 1, 200, 80, 0.20, 600, "", false),
		makeRow("qwen", 10, 50, 20, 0.00, 200, "", false),
	}
	groups := ByModel(rows)
	if len(groups) != 2 {
		t.Fatalf("expected 2 model groups, got %d", len(groups))
	}
	// Sorted by cost descending, claude first.
	if groups[0].Key != "claude" {
		t.Fatalf("expected claude first (highest cost), got %s", groups[0].Key)
	}
	if groups[0].Calls != 2 {
		t.Fatalf("claude should have 2 calls, got %d", groups[0].Calls)
	}
	if groups[0].PromptTokens != 300 {
		t.Fatalf("claude prompt tokens: want 300, got %d", groups[0].PromptTokens)
	}
}

func TestByDay_Sorted(t *testing.T) {
	rows := []Row{
		{TS: "2026-06-14T10:00:00Z", Model: "a", EstCostUSD: 0.01},
		{TS: "2026-06-12T10:00:00Z", Model: "b", EstCostUSD: 0.02},
		{TS: "2026-06-13T10:00:00Z", Model: "c", EstCostUSD: 0.03},
	}
	groups := ByDay(rows)
	if len(groups) != 3 {
		t.Fatalf("expected 3 day groups, got %d", len(groups))
	}
	// Must be ascending by date.
	if groups[0].Key != "2026-06-12" || groups[1].Key != "2026-06-13" || groups[2].Key != "2026-06-14" {
		t.Fatalf("days not sorted ascending: %v", groups)
	}
}

func TestSwarmStats_Empty(t *testing.T) {
	s := SwarmStats([]Row{makeRow("m", 1, 0, 0, 0, 0, "", false)})
	if s.Runs != 0 {
		t.Fatalf("no swarm rows should give Runs=0, got %d", s.Runs)
	}
}

func TestSwarmStats_WinnerRate(t *testing.T) {
	rows := []Row{
		makeRow("a", 1, 100, 50, 0.01, 1000, "race", true),
		makeRow("b", 2, 100, 50, 0.01, 800, "race", false),
		makeRow("c", 3, 100, 50, 0.01, 1200, "race", false),
		makeRow("d", 1, 100, 50, 0.01, 900, "best", true),
	}
	s := SwarmStats(rows)
	if s.Runs != 4 {
		t.Fatalf("want 4 swarm runs, got %d", s.Runs)
	}
	// 2 winners out of 4 runs = 0.5
	if fmt.Sprintf("%.2f", s.WinnerRate) != "0.50" {
		t.Fatalf("want winner rate 0.50, got %.2f", s.WinnerRate)
	}
	if s.ByMode["race"] != 3 || s.ByMode["best"] != 1 {
		t.Fatalf("ByMode wrong: %v", s.ByMode)
	}
}

func TestCommaInt(t *testing.T) {
	cases := [][2]any{
		{0, "0"},
		{999, "999"},
		{1000, "1,000"},
		{1234567, "1,234,567"},
	}
	for _, c := range cases {
		got := commaInt(c[0].(int))
		if got != c[1].(string) {
			t.Errorf("commaInt(%d) = %q, want %q", c[0], got, c[1])
		}
	}
}

func TestTokenSourceShare(t *testing.T) {
	rows := []Row{
		{PromptTokens: 100, ResponseTokens: 50, TokensSource: "actual"},   // 150 actual
		{PromptTokens: 40, ResponseTokens: 10, TokensSource: "estimated"}, // 50 estimated
		{PromptTokens: 20, ResponseTokens: 5, Source: "real"},             // 25 legacy → actual
		{PromptTokens: 8, ResponseTokens: 2, Source: "estimate"},          // 10 legacy → estimated
	}
	actual, estimated := TokenSourceShare(rows)
	if actual != 175 {
		t.Errorf("actual = %d, want 175", actual)
	}
	if estimated != 60 {
		t.Errorf("estimated = %d, want 60", estimated)
	}
}

func TestSourceLabels(t *testing.T) {
	// estimated=true → estimated everywhere, legacy "estimate"
	ts, cs, ls := SourceLabels(true)
	if ts != "estimated" || cs != "estimated" || ls != "estimate" {
		t.Errorf("SourceLabels(true) = %q/%q/%q, want estimated/estimated/estimate", ts, cs, ls)
	}
	// estimated=false → actual tokens, but cost is still derived, legacy "real"
	ts, cs, ls = SourceLabels(false)
	if ts != "actual" || cs != "estimated" || ls != "real" {
		t.Errorf("SourceLabels(false) = %q/%q/%q, want actual/estimated/real", ts, cs, ls)
	}
}

func TestTokensEstimated_Precedence(t *testing.T) {
	// tokens_source wins over legacy source when both are present.
	if tokensEstimated(Row{TokensSource: "actual", Source: "estimate"}) {
		t.Error("tokens_source=actual should not be estimated despite legacy source=estimate")
	}
	if !tokensEstimated(Row{TokensSource: "estimated", Source: "real"}) {
		t.Error("tokens_source=estimated should be estimated despite legacy source=real")
	}
	// Unlabeled legacy rows fall back to source.
	if !tokensEstimated(Row{Source: "estimate"}) {
		t.Error("legacy source=estimate should be estimated")
	}
	if tokensEstimated(Row{Source: "real"}) {
		t.Error("legacy source=real should be actual")
	}
}

// Equal-cost groups (commonly several $0 rows) must sort in a fixed order,
// otherwise the dashboard's "by model"/"by tier" rows reorder on every ~5s
// poll with no underlying data change, since groups is built from a map whose
// iteration order is randomized (#506).
func TestGroupBy_StableTiebreakOnEqualCost(t *testing.T) {
	rows := []Row{
		makeRow("zebra", 1, 0, 0, 0, 0, "", false),
		makeRow("apple", 1, 0, 0, 0, 0, "", false),
		makeRow("mango", 1, 0, 0, 0, 0, "", false),
		makeRow("kiwi", 1, 0, 0, 5, 0, "", false), // only non-zero cost, must sort first
	}
	var first []string
	for i := 0; i < 20; i++ {
		groups := GroupBy(rows, func(r Row) string { return r.Model })
		got := make([]string, len(groups))
		for j, g := range groups {
			got[j] = g.Key
		}
		if first == nil {
			first = got
		} else {
			for j := range got {
				if got[j] != first[j] {
					t.Fatalf("ordering changed between calls: %v then %v", first, got)
				}
			}
		}
	}
	want := []string{"kiwi", "apple", "mango", "zebra"}
	for i := range want {
		if first[i] != want[i] {
			t.Fatalf("order = %v, want %v (cost desc, then key ascending)", first, want)
		}
	}
}

func TestGroupBy_Exported(t *testing.T) {
	rows := []Row{
		makeRow("a", 1, 0, 0, 0.5, 0, "", false),
		makeRow("b", 2, 0, 0, 0.3, 0, "", false),
		makeRow("a", 1, 0, 0, 0.2, 0, "", false),
	}
	groups := GroupBy(rows, func(r Row) string { return fmt.Sprintf("tier-%d", r.Tier) })
	if len(groups) != 2 {
		t.Fatalf("want 2 groups, got %d", len(groups))
	}
	// tier-1 has cost 0.7, tier-2 has 0.3 → tier-1 first
	if groups[0].Key != "tier-1" {
		t.Fatalf("expected tier-1 first (highest cost), got %s", groups[0].Key)
	}
}
