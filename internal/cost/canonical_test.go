// SPDX-License-Identifier: MIT

package cost

import (
	"testing"
	"time"
)

// The three naming eras that coexist in a real log, and what each denotes.
func table() aliasTable {
	return aliasTable{
		byName: map[string]string{
			"claude": "claude", "Claude Code": "claude",
			"codex": "codex", "OpenAI Codex": "codex",
			"flash-med": "flash-med", "Gemini 3.8 Flash (Medium)": "flash-med",
			"Qwen2.5-Coder:7b":        "ollama/Qwen2.5-Coder:7b",
			"ollama/Qwen2.5-Coder:7b": "ollama/Qwen2.5-Coder:7b",
		},
		ids: map[string]bool{
			"claude": true, "codex": true, "flash-med": true,
			"ollama/Qwen2.5-Coder:7b": true,
		},
	}
}

// The defect: one head counted as several, so no per-head total was its spend.
func TestCanonicalKey_EveryNameForAHeadLandsOnOneKey(t *testing.T) {
	for _, name := range []string{"claude", "Claude Code"} {
		if got := canonicalKey(Row{Model: name}, table()); got != "claude" {
			t.Errorf("Model %q keyed as %q, want claude; splitting it understates "+
				"every per-head total", name, got)
		}
	}
	for _, name := range []string{"Qwen2.5-Coder:7b", "Qwen2.5-Coder:7b (Ollama)"} {
		if got := canonicalKey(Row{Model: name}, table()); got != "ollama/Qwen2.5-Coder:7b" {
			t.Errorf("Model %q keyed as %q, want ollama/Qwen2.5-Coder:7b", name, got)
		}
	}
}

// Head is what the writer recorded deliberately; a stale Model beside it must
// not override it.
func TestCanonicalKey_RecordedHeadWins(t *testing.T) {
	r := Row{Head: "flash-med", Model: "Gemini 3.5 Flash (Medium)"}
	if got := canonicalKey(r, table()); got != "flash-med" {
		t.Errorf("keyed as %q, want the recorded head flash-med", got)
	}
}

// A wrong match files one head's spend against another, which is worse than
// leaving it separate. Names nothing declares are preserved, never guessed.
func TestCanonicalKey_UnknownNameIsPreservedNotGuessed(t *testing.T) {
	// Renamed away by #693, so nothing on the machine declares it any more.
	r := Row{Model: "Gemini 3.5 Flash (High)"}
	if got := canonicalKey(r, table()); got != "Gemini 3.5 Flash (High)" {
		t.Errorf("keyed as %q; an unrecognized name must survive verbatim so it "+
			"is not silently merged into another head", got)
	}
}

func TestCanonicalKey_NoNameAtAllIsUnknown(t *testing.T) {
	if got := canonicalKey(Row{}, table()); got != UnknownPoolKey {
		t.Errorf("keyed as %q, want %q", got, UnknownPoolKey)
	}
}

// The " (Ollama)" suffix is the port provider's own display construction over
// ID "ollama/X", so inverting it resolves rather than guesses. An allowlisted
// OpenRouter model is one head per model the same way (#752).
func TestResolveHeadName_InvertsThePortProviderDisplayName(t *testing.T) {
	for name, want := range map[string]string{
		"qwen3:0.6b (Ollama)":                      "ollama/qwen3:0.6b",
		"phi-4 (LM Studio)":                        "lmstudio/phi-4",
		"anthropic/claude-sonnet-4.5 (OpenRouter)": "openrouter/anthropic/claude-sonnet-4.5",
		"nothing-declares-this":                    "",
	} {
		if got := resolveName(name, table()); got != want {
			t.Errorf("resolveName(%q) = %q, want %q", name, got, want)
		}
	}
}

// A bare " (Ollama)" is not a model name, and "ollama/" is not a head.
func TestResolveHeadName_RefusesAnEmptyModelPart(t *testing.T) {
	if got := resolveName(" (Ollama)", table()); got != "" {
		t.Errorf("resolveName(%q) = %q, want \"\"", " (Ollama)", got)
	}
}

func TestAttributable(t *testing.T) {
	for key, want := range map[string]bool{
		"claude":            true,
		"ollama/qwen3:0.6b": true, // a local head, declared by prefix
		// A named OpenRouter model, so its spend attributes to it rather than
		// falling into the unattributed footnote.
		"openrouter/google/gemini-2.5-pro": true,
		"Gemini 3.5 Flash (High)":          false,
		UnknownPoolKey:                     false,
	} {
		if got := attributable(key, table()); got != want {
			t.Errorf("attributable(%q) = %v, want %v", key, got, want)
		}
	}
}

// Regrouping must move rows between groups, never lose or duplicate one: the
// total is the number the user checks against their bill.
func TestGroupByCanonicalKey_MovesRowsWithoutChangingTheTotal(t *testing.T) {
	rows := []Row{
		{Model: "Claude Code", EstCostUSD: 0.004890, PromptTokens: 31},
		{Model: "claude", EstCostUSD: 0.077295, PromptTokens: 14},
		{Model: "Qwen2.5-Coder:7b", EstCostUSD: 0.000009, PromptTokens: 17},
		{Model: "Qwen2.5-Coder:7b (Ollama)", EstCostUSD: 0.005124, PromptTokens: 26},
		{Model: "Gemini 3.5 Flash (High)", EstCostUSD: 0.000629, PromptTokens: 2},
	}
	groups := groupBy(rows, func(r Row) string { return canonicalKey(r, table()) })

	var gotCalls, gotTokens int
	var gotCost float64
	byKey := map[string]GroupRow{}
	for _, g := range groups {
		gotCalls += g.Calls
		gotTokens += g.PromptTokens
		gotCost += g.EstCostUSD
		byKey[g.Key] = g
	}
	if gotCalls != len(rows) {
		t.Errorf("grouped %d calls from %d rows; regrouping must not lose or "+
			"duplicate a row", gotCalls, len(rows))
	}
	if gotTokens != 90 {
		t.Errorf("prompt tokens = %d, want 90", gotTokens)
	}
	if diff := gotCost - 0.087947; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("cost = %.6f, want 0.087947", gotCost)
	}

	// Claude Code's real spend, in one row rather than two.
	if g := byKey["claude"]; g.Calls != 2 || g.EstCostUSD < 0.082184 || g.EstCostUSD > 0.082186 {
		t.Errorf("claude = %d calls $%.6f, want 2 calls $0.082185", g.Calls, g.EstCostUSD)
	}
	if g := byKey["ollama/Qwen2.5-Coder:7b"]; g.Calls != 2 {
		t.Errorf("qwen = %d calls, want the 2 spellings merged into 1 group", g.Calls)
	}
	// And the one nothing declares stays on its own, counted as unattributed.
	if _, ok := byKey["Gemini 3.5 Flash (High)"]; !ok {
		t.Error("the stale name was merged into another head instead of kept apart")
	}
}

// FilterDays' own comment says "the last n calendar days (UTC)". It returned
// n+1, so `--days 1` meant today and yesterday and `stats` reported "today" for
// two days of spend while `cost` reported one (#729).
func TestFilterDays_CountsNDaysIncludingToday(t *testing.T) {
	day := func(n int) string {
		return time.Now().UTC().AddDate(0, 0, -n).Format("2006-01-02") + "T12:00:00Z"
	}
	rows := []Row{
		{TS: day(0), Model: "a"},
		{TS: day(1), Model: "b"},
		{TS: day(2), Model: "c"},
		{TS: day(3), Model: "d"},
	}
	for n, want := range map[int]int{1: 1, 2: 2, 3: 3, 4: 4} {
		if got := len(FilterDays(rows, n)); got != want {
			t.Errorf("FilterDays(n=%d) returned %d rows, want %d calendar days", n, got, want)
		}
	}
	if got := len(FilterDays(rows, 0)); got != 4 {
		t.Errorf("n=0 must return everything, got %d", got)
	}
}

// The two commands must answer the same question the same way: this is the
// exact comparison that read 6 against 34 on a real log.
func TestFilterDays_OneDayMatchesTheCalendarTodayFilter(t *testing.T) {
	today := time.Now().UTC().Format("2006-01-02")
	rows := []Row{
		{TS: today + "T01:00:00Z", Model: "a"},
		{TS: today + "T23:00:00Z", Model: "b"},
		{TS: time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02") + "T23:00:00Z", Model: "c"},
	}
	var calendar int
	for _, r := range rows {
		if len(r.TS) >= 10 && r.TS[:10] == today {
			calendar++
		}
	}
	if got := len(FilterDays(rows, 1)); got != calendar {
		t.Errorf("FilterDays(1) = %d rows but the calendar-today filter = %d; "+
			"`hyctl cost` and `hyctl stats` disagree about today again", got, calendar)
	}
}
