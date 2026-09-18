// SPDX-License-Identifier: MIT

package tui

import (
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/cost"
)

// The cockpit took a local date and matched it against a UTC timestamp's text,
// so between local midnight and UTC midnight every row failed the prefix and
// the panel reported no spend today, minutes after spending (#916).
func TestFold_TodayIsTheReadersDayNotAPrefixMatch(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Kolkata") // UTC+5:30
	if err != nil {
		t.Skipf("zone unavailable: %v", err)
	}
	orig := time.Local
	time.Local = loc
	t.Cleanup(func() { time.Local = orig })

	// Half past midnight on the 19th in Kolkata, still the 18th in UTC.
	now := time.Date(2026, 9, 19, 0, 30, 0, 0, loc)
	m := ckMetrics{stats: map[string]*ckModelStat{}, localModels: map[string]bool{}, runCost: map[string]ckRunCost{}}
	m.fold([]cost.Row{
		// 00:30 local today, and no prefix of "2026-09-19" anywhere in it.
		{TS: "2026-09-18T19:00:00Z", Model: "qwen (Ollama)", Executor: "local", Tier: 10,
			WallMS: 100, PromptTokens: 10, ResponseTokens: 5, EstCostUSD: 0.50},
		// 15:30 local yesterday.
		{TS: "2026-09-18T10:00:00Z", Model: "qwen (Ollama)", Executor: "local", Tier: 10,
			WallMS: 100, PromptTokens: 10, ResponseTokens: 5, EstCostUSD: 0.25},
	}, stubPricer{}, now)

	var st *ckModelStat
	for _, v := range m.stats {
		st = v
	}
	if st == nil {
		t.Fatal("no per-model stats were folded")
	}
	if st.reqsToday != 1 {
		t.Errorf("requests today = %d, want 1: the 19:00Z row is half past midnight locally", st.reqsToday)
	}
	if st.costToday < 0.49 || st.costToday > 0.51 {
		t.Errorf("cost today = %v, want the 0.50 spent since local midnight", st.costToday)
	}
	// Both rows are in September wherever you read them from.
	if m.monthReqs != 2 {
		t.Errorf("month requests = %d, want both rows", m.monthReqs)
	}
}
