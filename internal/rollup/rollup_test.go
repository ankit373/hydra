// SPDX-License-Identifier: MIT

package rollup

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/ankit373/hydra/internal/cost"
)

func row(ts, model string, tier int, wall int64, usd, act, keep float64) cost.Row {
	return cost.Row{TS: ts, Model: model, Executor: "agy", Enum: "STANDARD", Tier: tier,
		PromptTokens: 100, ResponseTokens: 50, EstCostUSD: usd, WallMS: wall,
		ActProb: act, KeepProb: keep}
}

func TestBuildAggregatesByDayAndKey(t *testing.T) {
	in := []cost.Row{
		row("2026-09-01T10:00:00Z", "m1", 2, 1000, 0.01, 1, 1),
		row("2026-09-01T11:00:00Z", "m1", 2, 3000, 0.02, 1, 1),
		row("2026-09-02T10:00:00Z", "m1", 2, 2000, 0.03, 1, 1),
		row("2026-09-01T12:00:00Z", "m2", 8, 500, 0.00, 1, 1),
	}
	got := Build(in)
	if len(got) != 3 {
		t.Fatalf("got %d rows, want 3", len(got))
	}
	var day1 *Row
	for i := range got {
		if got[i].Date == "2026-09-01" && got[i].Model == "m1" {
			day1 = &got[i]
		}
	}
	if day1 == nil {
		t.Fatal("missing 2026-09-01/m1")
	}
	if day1.Calls != 2 || day1.PromptTokens != 200 || day1.ResponseTokens != 100 {
		t.Errorf("calls=%d prompt=%d resp=%d", day1.Calls, day1.PromptTokens, day1.ResponseTokens)
	}
	if math.Abs(day1.EstCostUSD-0.03) > 1e-9 {
		t.Errorf("cost = %v, want 0.03", day1.EstCostUSD)
	}
	if day1.Latency.Count() != 2 {
		t.Errorf("latency count = %d, want 2", day1.Latency.Count())
	}
}

// A rollup must replace the raw rows it summarises, so the totals have to
// survive exactly. If they drift, deleting the raw log loses money.
func TestAggregatesMatchTheRawTotals(t *testing.T) {
	var in []cost.Row
	var wantCost float64
	var wantCalls int64
	for i := 0; i < 500; i++ {
		usd := float64(i%17) * 0.001
		in = append(in, row(fmt.Sprintf("2026-09-0%dT10:00:00Z", 1+i%3), "m1", 2, int64(100+i), usd, 1, 1))
		wantCost += usd
		wantCalls++
	}
	var gotCost float64
	var gotCalls int64
	for _, r := range Build(in) {
		gotCost += r.EstCostUSD
		gotCalls += r.Calls
	}
	if gotCalls != wantCalls {
		t.Errorf("calls %d, want %d", gotCalls, wantCalls)
	}
	if math.Abs(gotCost-wantCost) > 1e-9 {
		t.Errorf("cost %v, want %v", gotCost, wantCost)
	}
}

// Rows written before propensity existed carry 0, which is not evidence the
// router explored. Counting them would invent exploration that never happened.
func TestExploredCountsOnlyRealPropensities(t *testing.T) {
	in := []cost.Row{
		row("2026-09-01T10:00:00Z", "m1", 2, 100, 0, 0, 0),    // pre-#605 row
		row("2026-09-01T10:00:00Z", "m1", 2, 100, 0, 1, 1),    // greedy
		row("2026-09-01T10:00:00Z", "m1", 2, 100, 0, 0.02, 1), // explored
	}
	got := Build(in)
	if len(got) != 1 || got[0].Explored != 1 {
		t.Fatalf("explored = %d, want 1", got[0].Explored)
	}
}

func TestUnparseableTimestampIsSkipped(t *testing.T) {
	got := Build([]cost.Row{row("not-a-date", "m1", 2, 100, 0.5, 1, 1)})
	if len(got) != 0 {
		t.Errorf("got %d rows, want 0, a row with no day cannot be attributed", len(got))
	}
}

// Mergeable rollups are what let two machines combine statistics without
// either shipping a raw row.
func TestMergeCombinesSketchesAndTotals(t *testing.T) {
	a := Build([]cost.Row{row("2026-09-01T10:00:00Z", "m1", 2, 1000, 0.01, 1, 1)})
	b := Build([]cost.Row{row("2026-09-01T11:00:00Z", "m1", 2, 5000, 0.02, 1, 1)})
	got, err := Merge(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1", len(got))
	}
	if got[0].Calls != 2 || math.Abs(got[0].EstCostUSD-0.03) > 1e-9 {
		t.Errorf("calls=%d cost=%v", got[0].Calls, got[0].EstCostUSD)
	}
	if got[0].Latency.Count() != 2 {
		t.Errorf("merged latency count = %d, want 2", got[0].Latency.Count())
	}
	// Merging must not mutate the inputs a caller still holds.
	if a[0].Latency.Count() != 1 {
		t.Errorf("Merge mutated its input: count %d", a[0].Latency.Count())
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "rollups.jsonl")
	in := Build([]cost.Row{
		row("2026-09-01T10:00:00Z", "m1", 2, 1200, 0.01, 1, 1),
		row("2026-09-01T11:00:00Z", "m1", 2, 4800, 0.02, 0.05, 1),
	})
	if err := Save(p, in); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(in) {
		t.Fatalf("got %d rows, want %d", len(got), len(in))
	}
	if got[0].Calls != in[0].Calls || got[0].Explored != in[0].Explored {
		t.Errorf("totals drifted: %+v vs %+v", got[0].Key, in[0].Key)
	}
	if a, b := got[0].Latency.Quantile(0.9), in[0].Latency.Quantile(0.9); a != b {
		t.Errorf("p90 %v != %v after round trip", a, b)
	}
}

func TestLoadMissingFileIsNotAnError(t *testing.T) {
	got, err := Load(filepath.Join(t.TempDir(), "nope.jsonl"))
	if err != nil || got != nil {
		t.Errorf("got %v, %v; want nil, nil", got, err)
	}
}

// A crash mid-write leaves a torn last line. It must not hide every good row
// before it.
func TestLoadSkipsTornTail(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "rollups.jsonl")
	good, _ := json.Marshal(Build([]cost.Row{row("2026-09-01T10:00:00Z", "m1", 2, 100, 0.01, 1, 1)})[0])
	torn := append(append(good, '\n'), []byte(`{"v":1,"date":"2026-`)...)
	if err := os.WriteFile(p, torn, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("got %d rows, want 1 (the good row before the torn tail)", len(got))
	}
}

func TestRefreshBuildsFromCostLog(t *testing.T) {
	dir := t.TempDir()
	costPath := filepath.Join(dir, "cost.jsonl")
	f, err := os.Create(costPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []cost.Row{
		row("2026-09-01T10:00:00Z", "m1", 2, 1000, 0.01, 1, 1),
		row("2026-09-01T11:00:00Z", "m1", 2, 2000, 0.02, 1, 1),
	} {
		raw, _ := json.Marshal(r)
		fmt.Fprintln(f, string(raw))
	}
	f.Close()

	rp := filepath.Join(dir, "rollups.jsonl")
	rows, err := Refresh(costPath, rp)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Calls != 2 {
		t.Fatalf("rows=%d calls=%d", len(rows), rows[0].Calls)
	}
	// Refresh is idempotent: rebuilding from the same source must not double.
	again, err := Refresh(costPath, rp)
	if err != nil {
		t.Fatal(err)
	}
	if again[0].Calls != 2 {
		t.Errorf("second refresh doubled: calls=%d", again[0].Calls)
	}
}
