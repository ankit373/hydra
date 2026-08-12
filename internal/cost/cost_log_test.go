// SPDX-License-Identifier: MIT

package cost

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/testutil"
)

// cost.jsonl is Hydra's spend record. It is append-only, written by concurrent
// goroutines, and read by hyctl cost/stats and the desktop app — so it is a
// format contract as much as a data structure, and a parse that silently drops
// rows understates what the user has spent.

// fixture writes a cost log under the sandbox's HOME and returns its path.
func fixture(t *testing.T, lines ...string) string {
	t.Helper()
	s := testutil.NewSandbox(t)
	dir := filepath.Join(s.Home, ".hydra", "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "cost.jsonl")
	body := ""
	if len(lines) > 0 {
		body = strings.Join(lines, "\n") + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func row(t *testing.T, r Row) string {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// "Never dispatched" and "cannot read the log" are different states — the first
// renders an empty summary, the second is an error the user must see. Collapsing
// them would report "$0.00 spent" for an unreadable log.
func TestLoadAll_MissingLogIsErrNoLogNotAnEmptyResult(t *testing.T) {
	testutil.NewSandbox(t) // no log written

	_, err := LoadAll()
	if !errors.Is(err, ErrNoLog) {
		t.Fatalf("err = %v, want ErrNoLog", err)
	}
}

func TestLoadAll_EmptyLogIsNoRowsAndNoError(t *testing.T) {
	fixture(t)

	rows, err := LoadAll()
	if err != nil {
		t.Fatalf("an empty log errored: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("got %d rows from an empty log", len(rows))
	}
}

// A truncated final line is the normal consequence of a crash mid-append. It
// must not discard the rows before it.
func TestLoadRows_SkipsMalformedLinesAndKeepsTheRest(t *testing.T) {
	fixture(t,
		row(t, Row{TS: "2026-08-01T10:00:00Z", Tier: 8, Model: "a", EstCostUSD: 0.01, PromptTokens: 10, ResponseTokens: 5}),
		"{not json",
		"",
		row(t, Row{TS: "2026-08-01T11:00:00Z", Tier: 7, Model: "b", EstCostUSD: 0.02, PromptTokens: 20, ResponseTokens: 6}),
		`{"ts":"trunca`, // crash mid-append
	)

	rows, err := LoadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want the 2 well-formed ones: %+v", len(rows), rows)
	}
	if rows[0].Model != "a" || rows[1].Model != "b" {
		t.Errorf("rows out of order or wrong: %+v", rows)
	}
}

func TestAggregate_SumsEveryField(t *testing.T) {
	rows := []Row{
		{PromptTokens: 10, ResponseTokens: 5, EstCostUSD: 0.01, WallMS: 1000},
		{PromptTokens: 20, ResponseTokens: 7, EstCostUSD: 0.02, WallMS: 2500},
	}
	got := aggregate(rows)

	if got.Calls != 2 {
		t.Errorf("Calls = %d, want 2", got.Calls)
	}
	if got.PromptTokens != 30 || got.ResponseTokens != 12 {
		t.Errorf("tokens = %d/%d, want 30/12", got.PromptTokens, got.ResponseTokens)
	}
	if got.EstCostUSD < 0.0299 || got.EstCostUSD > 0.0301 {
		t.Errorf("EstCostUSD = %v, want ~0.03", got.EstCostUSD)
	}
	// Wall time is reported in seconds; 3500ms is 3s, and truncating to 0 is the
	// class of bug CLAUDE.md calls out by name ("used/1000 truncating to zero").
	if got.WallSeconds != 3 {
		t.Errorf("WallSeconds = %d, want 3", got.WallSeconds)
	}
}

func TestAggregate_EmptyInput(t *testing.T) {
	got := aggregate(nil)
	if got.Calls != 0 || got.EstCostUSD != 0 {
		t.Errorf("aggregate(nil) = %+v, want zero", got)
	}
}

func TestByPool_GroupsAndSorts(t *testing.T) {
	fixture(t,
		row(t, Row{TS: "2026-08-01T10:00:00Z", Pool: "cheap", EstCostUSD: 0.01, PromptTokens: 1}),
		row(t, Row{TS: "2026-08-01T10:00:00Z", Pool: "pricey", EstCostUSD: 0.50, PromptTokens: 1}),
		row(t, Row{TS: "2026-08-01T10:00:00Z", Pool: "cheap", EstCostUSD: 0.02, PromptTokens: 1}),
	)

	rows, err := ByPool()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d groups, want 2: %+v", len(rows), rows)
	}
	byKey := map[string]GroupRow{}
	for _, r := range rows {
		byKey[r.Key] = r
	}
	if byKey["cheap"].Calls != 2 {
		t.Errorf("cheap Calls = %d, want 2", byKey["cheap"].Calls)
	}
	if c := byKey["cheap"].EstCostUSD; c < 0.0299 || c > 0.0301 {
		t.Errorf("cheap cost = %v, want ~0.03", c)
	}
}

// ByTask and ByRun are how a user attributes spend to one piece of work. A
// filter that matches too broadly attributes someone else's cost to it.
func TestByTaskAndByRun_FilterExactly(t *testing.T) {
	fixture(t,
		row(t, Row{TS: "2026-08-01T10:00:00Z", TaskID: "t1", RunID: "r1", Tier: 8, EstCostUSD: 0.10, PromptTokens: 1}),
		row(t, Row{TS: "2026-08-01T10:00:00Z", TaskID: "t2", RunID: "r1", Tier: 7, EstCostUSD: 0.20, PromptTokens: 1}),
		row(t, Row{TS: "2026-08-01T10:00:00Z", TaskID: "t1", RunID: "r2", Tier: 8, EstCostUSD: 0.40, PromptTokens: 1}),
	)

	task, err := ByTask("t1")
	if err != nil {
		t.Fatal(err)
	}
	if task.Calls != 2 {
		t.Errorf("ByTask(t1).Calls = %d, want 2", task.Calls)
	}
	if c := task.EstCostUSD; c < 0.4999 || c > 0.5001 {
		t.Errorf("ByTask(t1) cost = %v, want ~0.50", c)
	}

	run, err := ByRun("r1")
	if err != nil {
		t.Fatal(err)
	}
	if run.Totals.Calls != 2 {
		t.Errorf("ByRun(r1).Calls = %d, want 2", run.Totals.Calls)
	}
	if len(run.ByTier) != 2 {
		t.Errorf("ByRun(r1) tiers = %+v, want two", run.ByTier)
	}

	// An unknown id is an error naming the task, not a zero total. "$0.00 for
	// t99" reads as "that task was free"; "no calls for task_id=t99" reads as
	// "you asked about something that never ran", which is what happened.
	if _, err := ByTask("does-not-exist"); err == nil {
		t.Error("an unknown task_id reported totals instead of erroring")
	} else if !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("error %q does not name the task asked about", err)
	}
}

// Tail returns the last N, newest first — deliberately, so a terminal reader
// sees the most recent dispatch at the top without scrolling. Asserted rather
// than assumed: I expected chronological order and was wrong.
func TestTail_TakesTheMostRecentNewestFirst(t *testing.T) {
	var lines []string
	for i := 0; i < 10; i++ {
		lines = append(lines, row(t, Row{TS: "2026-08-01T10:00:00Z", Model: string(rune('a' + i)), PromptTokens: 1}))
	}
	fixture(t, lines...)

	got, err := Tail(3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d rows, want 3", len(got))
	}
	if got[0].Model != "j" || got[2].Model != "h" {
		t.Errorf("Tail(3) = %v, want the last three newest-first (j, i, h)",
			[]string{got[0].Model, got[1].Model, got[2].Model})
	}

	// Asking for more than exists returns everything, not an error.
	all, err := Tail(100)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 10 {
		t.Errorf("Tail(100) returned %d of 10 rows", len(all))
	}
}

// ── golden output ────────────────────────────────────────────────────────────

// capture redirects stdout for the duration of fn. The Render* functions print
// rather than return, and their output is the product — a user reads it to
// decide whether Hydra is saving them money.
func capture(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	fn()
	_ = w.Close()
	os.Stdout = orig
	return <-done
}

func TestRenderSummary_Golden(t *testing.T) {
	s := testutil.NewSandbox(t)
	res := &SummaryResult{
		Today:   Totals{Calls: 3, PromptTokens: 1200, ResponseTokens: 450, EstCostUSD: 0.0345, WallSeconds: 12},
		AllTime: Totals{Calls: 128, PromptTokens: 900000, ResponseTokens: 250000, EstCostUSD: 12.3456, WallSeconds: 4200},
		Recent: []Row{
			{TS: "2026-08-01T10:00:00Z", Tier: 8, Model: "gemini-flash", EstCostUSD: 0.0012, PromptTokens: 400, ResponseTokens: 120},
		},
		ActualTokens:    1000000,
		EstimatedTokens: 150000,
	}

	out := capture(t, func() { RenderSummary(res) })
	testutil.Golden(t, "render_summary", out, s.Home, s.HydraHome)
}

func TestRenderTable_Golden(t *testing.T) {
	s := testutil.NewSandbox(t)
	rows := []GroupRow{
		{Key: "gemini-3.5-flash", Calls: 42, PromptTokens: 120000, ResponseTokens: 30000, EstCostUSD: 1.2345, WallMS: 90000},
		{Key: "claude-opus", Calls: 3, PromptTokens: 9000, ResponseTokens: 2500, EstCostUSD: 4.5678, WallMS: 30000},
		{Key: "ollama/qwen3:8b", Calls: 90, PromptTokens: 400000, ResponseTokens: 100000, EstCostUSD: 0, WallMS: 600000},
	}

	out := capture(t, func() { RenderTable("By model", rows) })
	testutil.Golden(t, "render_table", out, s.Home, s.HydraHome)
}

// An empty table must render an empty state, not a header with nothing under it
// or a crash on rows[0].
func TestRenderTable_EmptyIsAnEmptyState(t *testing.T) {
	out := capture(t, func() { RenderTable("By model", nil) })
	if strings.TrimSpace(out) == "" {
		t.Error("an empty table printed nothing at all — the user cannot tell it ran")
	}
}

func TestRenderSwarmStats_Golden(t *testing.T) {
	s := testutil.NewSandbox(t)
	sum := SwarmSummary{
		Runs: 12, WinnerRate: 0.75, AvgWallMS: 3400, TotalCost: 0.4567,
		ByMode: map[string]int{"best": 7, "race": 4, "all": 1},
	}
	out := capture(t, func() { RenderSwarmStats(sum) })
	testutil.Golden(t, "render_swarm_stats", out, s.Home, s.HydraHome)
}

// Zero spend must render as $0.00, never as an empty field — and a sub-cent
// total must not truncate to zero, which would tell the user a paid call was
// free.
func TestRenderSummary_SubCentSpendIsNotShownAsZero(t *testing.T) {
	res := &SummaryResult{
		Today:   Totals{Calls: 1, EstCostUSD: 0.0004},
		AllTime: Totals{Calls: 1, EstCostUSD: 0.0004},
	}
	out := capture(t, func() { RenderSummary(res) })

	if strings.Contains(out, "$0.00\n") && !strings.Contains(out, "0.0004") {
		t.Logf("output:\n%s", out)
		t.Error("a real sub-cent charge renders as $0.00 with no indication it was non-zero")
	}
}

// Summary is what `hyctl cost` prints. It must separate today from all-time and
// report the token-source split, since "estimated" spend must never be shown as
// measured.
func TestSummary_SeparatesTodayFromAllTimeAndLabelsTokenSource(t *testing.T) {
	today := time.Now().UTC().Format(time.RFC3339)
	old := time.Now().UTC().AddDate(0, 0, -30).Format(time.RFC3339)

	fixture(t,
		row(t, Row{TS: old, Model: "a", EstCostUSD: 1.00, PromptTokens: 100,
			ResponseTokens: 50, TokensSource: "actual"}),
		row(t, Row{TS: today, Model: "b", EstCostUSD: 0.25, PromptTokens: 20,
			ResponseTokens: 10, TokensSource: "estimated"}),
	)

	res, err := Summary()
	if err != nil {
		t.Fatal(err)
	}
	if res.AllTime.Calls != 2 {
		t.Errorf("AllTime.Calls = %d, want 2", res.AllTime.Calls)
	}
	if res.Today.Calls != 1 {
		t.Errorf("Today.Calls = %d, want just the row from today", res.Today.Calls)
	}
	if res.Today.EstCostUSD > res.AllTime.EstCostUSD {
		t.Error("today's spend exceeds all-time spend")
	}
	// The split is what stops estimated tokens being presented as measured.
	if res.ActualTokens == 0 || res.EstimatedTokens == 0 {
		t.Errorf("token source split = %d actual / %d estimated; both rows should be "+
			"counted and labelled", res.ActualTokens, res.EstimatedTokens)
	}
	if len(res.Recent) == 0 {
		t.Error("Summary carries no recent rows to show")
	}
}

// Today and All are the per-tier breakdown `hyctl cost` prints. Today must be a
// subset of All, and both are sorted by spend so the expensive tier is first.
func TestTodayAndAll_GroupByTier(t *testing.T) {
	today := time.Now().UTC().Format(time.RFC3339)
	old := time.Now().UTC().AddDate(0, 0, -10).Format(time.RFC3339)

	fixture(t,
		row(t, Row{TS: today, Tier: 3, Model: "m1", EstCostUSD: 0.10, PromptTokens: 1}),
		row(t, Row{TS: today, Tier: 3, Model: "m1", EstCostUSD: 0.20, PromptTokens: 1}),
		row(t, Row{TS: today, Tier: 8, Model: "m3", EstCostUSD: 0.90, PromptTokens: 1}),
		row(t, Row{TS: old, Tier: 1, Model: "m2", EstCostUSD: 0.30, PromptTokens: 1}),
	)

	todayRows, err := Today()
	if err != nil {
		t.Fatal(err)
	}
	if len(todayRows) != 2 {
		t.Fatalf("Today() = %+v, want tiers 3 and 8 only (tier 1 is 10 days old)", todayRows)
	}
	// Sorted by spend descending — the expensive tier is what the user must see.
	if todayRows[0].Key != "8" {
		t.Errorf("Today()[0].Key = %q, want the costliest tier 8 first: %+v",
			todayRows[0].Key, todayRows)
	}
	for _, g := range todayRows {
		if g.Key == "3" && g.Calls != 2 {
			t.Errorf("tier 3 has %d calls, want the 2 rows merged", g.Calls)
		}
	}

	allRows, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if len(allRows) != 3 {
		t.Errorf("All() = %+v, want all three tiers", allRows)
	}

	// ByPool labels rows with no pool rather than dropping them; an unlabelled
	// row that vanished would understate spend.
	pools, err := ByPool()
	if err != nil {
		t.Fatal(err)
	}
	if len(pools) != 1 || pools[0].Key != "unknown" || pools[0].Calls != 4 {
		t.Errorf("ByPool() = %+v, want all 4 rows under \"unknown\"", pools)
	}
}

// JSON is the scripting surface; `--since` must filter on real timestamps
// rather than string comparison, so timezone offsets behave.
func TestJSON_FiltersBySinceUsingRealTimestamps(t *testing.T) {
	fixture(t,
		row(t, Row{TS: "2026-08-01T10:00:00Z", Model: "old", PromptTokens: 1}),
		row(t, Row{TS: "2026-08-03T10:00:00+02:00", Model: "offset", PromptTokens: 1}),
		row(t, Row{TS: "2026-08-05T10:00:00Z", Model: "new", PromptTokens: 1}),
	)

	all, err := JSON("")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Errorf("JSON(\"\") returned %d rows, want all 3", len(all))
	}

	since, err := JSON("2026-08-02T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if len(since) != 2 {
		t.Errorf("JSON(since) returned %d rows, want the 2 at or after it: %+v", len(since), since)
	}

	if _, err := JSON("not-a-timestamp"); err == nil {
		t.Error("an unparsable --since was accepted; the user would silently get " +
			"unfiltered output")
	}
}

// FilterDays trims to the last N calendar days. n<=0 means "everything", which
// is what an unset flag produces.
func TestFilterDays_Windows(t *testing.T) {
	now := time.Now().UTC()
	rows := []Row{
		{TS: now.Format(time.RFC3339)},
		{TS: now.AddDate(0, 0, -2).Format(time.RFC3339)},
		{TS: now.AddDate(0, 0, -40).Format(time.RFC3339)},
		{TS: "unparsable"},
	}
	if got := len(FilterDays(rows, 0)); got != len(rows) {
		t.Errorf("FilterDays(n=0) returned %d rows, want all %d", got, len(rows))
	}
	if got := len(FilterDays(rows, 7)); got > 3 {
		t.Errorf("FilterDays(n=7) returned %d rows, want at most the recent ones", got)
	}
}

// SwarmStats only counts swarm rows, and reports the winner rate as a fraction
// — a rate above 1 would render as "150%".
func TestSwarmStats_CountsOnlySwarmRowsAndBoundsTheRate(t *testing.T) {
	rows := []Row{
		{SwarmMode: "best", SwarmWinner: true, WallMS: 1000, EstCostUSD: 0.1},
		{SwarmMode: "best", SwarmWinner: false, WallMS: 3000, EstCostUSD: 0.2},
		{SwarmMode: "race", SwarmWinner: true, WallMS: 2000, EstCostUSD: 0.3},
		{Model: "not-a-swarm-row", WallMS: 9999, EstCostUSD: 9.9},
	}
	got := SwarmStats(rows)

	if got.Runs != 3 {
		t.Errorf("Runs = %d, want the 3 swarm rows only", got.Runs)
	}
	if got.WinnerRate < 0 || got.WinnerRate > 1 {
		t.Errorf("WinnerRate = %v, outside [0,1] — it renders as a percentage", got.WinnerRate)
	}
	if got.TotalCost > 1.0 {
		t.Errorf("TotalCost = %v; the non-swarm row was included", got.TotalCost)
	}
	if got.ByMode["best"] != 2 || got.ByMode["race"] != 1 {
		t.Errorf("ByMode = %v", got.ByMode)
	}

	// No swarm rows at all must be a zero summary, not a divide by zero.
	empty := SwarmStats([]Row{{Model: "plain"}})
	if empty.Runs != 0 || empty.WinnerRate != 0 {
		t.Errorf("SwarmStats with no swarm rows = %+v", empty)
	}
}

// ByRun / ByTask are how a caller asks "what did that one run cost me". A
// missing id must be an error, not silently-zero spend.
func TestByRunAndByTask_ScopeToOneIDAndErrorWhenAbsent(t *testing.T) {
	ts := time.Now().UTC().Format(time.RFC3339)
	fixture(t,
		row(t, Row{TS: ts, RunID: "run-a", TaskID: "task-1", Tier: 1, EstCostUSD: 0.10, PromptTokens: 5}),
		row(t, Row{TS: ts, RunID: "run-a", TaskID: "task-2", Tier: 8, EstCostUSD: 0.02, PromptTokens: 5}),
		row(t, Row{TS: ts, RunID: "run-b", TaskID: "task-3", Tier: 1, EstCostUSD: 9.00, PromptTokens: 5}),
	)

	res, err := ByRun("run-a")
	if err != nil {
		t.Fatal(err)
	}
	if res.Totals.Calls != 2 {
		t.Errorf("ByRun(run-a).Calls = %d, want 2 — run-b leaked in", res.Totals.Calls)
	}
	if res.Totals.EstCostUSD > 1.0 {
		t.Errorf("ByRun(run-a) cost = %v; run-b's $9 was counted", res.Totals.EstCostUSD)
	}
	if len(res.ByTier) != 2 {
		t.Errorf("ByRun(run-a).ByTier = %+v, want tiers 1 and 8", res.ByTier)
	}

	tot, err := ByTask("task-3")
	if err != nil {
		t.Fatal(err)
	}
	if tot.Calls != 1 {
		t.Errorf("ByTask(task-3).Calls = %d, want 1", tot.Calls)
	}

	if _, err := ByRun("nope"); err == nil {
		t.Error("ByRun on an unknown run returned no error — a typo'd run id would " +
			"report $0.00 spent")
	}
	if _, err := ByTask("nope"); err == nil {
		t.Error("ByTask on an unknown task returned no error")
	}
}

// ByModel and ByDay back `hyctl stats`. ByDay's key must be the calendar day
// (not the full timestamp), or every call becomes its own "day".
func TestByModelAndByDay_KeyOnModelAndCalendarDay(t *testing.T) {
	rows := []Row{
		{TS: "2026-08-01T01:00:00Z", Model: "sonnet", EstCostUSD: 0.10, PromptTokens: 1},
		{TS: "2026-08-01T23:59:59Z", Model: "sonnet", EstCostUSD: 0.20, PromptTokens: 1},
		{TS: "2026-08-02T01:00:00Z", Model: "", EstCostUSD: 0.05, PromptTokens: 1},
		{TS: "bad", Model: "x"},
	}

	var sawUnknown bool
	for _, g := range ByModel(rows) {
		if g.Key == "sonnet" && g.Calls != 2 {
			t.Errorf("sonnet grouped into %d calls, want 2", g.Calls)
		}
		if g.Key == "unknown" {
			sawUnknown = true
		}
	}
	if !sawUnknown {
		t.Errorf("a row with no model was not labelled \"unknown\": %+v", ByModel(rows))
	}

	days := ByDay(rows)
	if len(days) != 3 {
		t.Fatalf("ByDay() = %+v, want two calendar days plus \"unknown\"", days)
	}
	// Ascending by date, unlike every other grouping — a chart drawn from this
	// reads left-to-right in time.
	for i := 1; i < len(days); i++ {
		if days[i-1].Key > days[i].Key {
			t.Errorf("ByDay is not sorted ascending: %+v", days)
			break
		}
	}
	if days[0].Key != "2026-08-01" {
		t.Errorf("ByDay()[0].Key = %q, want the earliest day", days[0].Key)
	}
}

// Tail is `hyctl cost --tail`: newest first, clamped to what exists.
func TestTail_NewestFirstAndClamped(t *testing.T) {
	fixture(t,
		row(t, Row{TS: "2026-08-01T00:00:00Z", Model: "oldest", PromptTokens: 1}),
		row(t, Row{TS: "2026-08-02T00:00:00Z", Model: "middle", PromptTokens: 1}),
		row(t, Row{TS: "2026-08-03T00:00:00Z", Model: "newest", PromptTokens: 1}),
	)

	two, err := Tail(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(two) != 2 || two[0].Model != "newest" || two[1].Model != "middle" {
		t.Errorf("Tail(2) = %+v, want newest first", two)
	}

	for _, n := range []int{-1, 999} {
		all, err := Tail(n)
		if err != nil {
			t.Fatal(err)
		}
		if len(all) != 3 {
			t.Errorf("Tail(%d) returned %d rows, want all 3 clamped", n, len(all))
		}
	}
}

// Tail(0) must mean "no rows", matching the `tail -n 0` convention — the
// previous behavior returned everything, identical to Tail(999999) (#455).
func TestTail_ZeroReturnsNoRows(t *testing.T) {
	fixture(t,
		row(t, Row{TS: "2026-08-01T00:00:00Z", Model: "a", PromptTokens: 1}),
		row(t, Row{TS: "2026-08-02T00:00:00Z", Model: "b", PromptTokens: 1}),
	)

	got, err := Tail(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("Tail(0) returned %d rows, want 0", len(got))
	}
}

// A log spanning several 64 KiB chunks must still tail correctly, not off-by-a-chunk.
func TestTailLines_CorrectAcrossChunkBoundaries(t *testing.T) {
	var lines []string
	pad := strings.Repeat("x", 100)
	for i := 0; i < 2000; i++ {
		lines = append(lines, row(t, Row{TS: "2026-08-01T00:00:00Z", Model: fmt.Sprintf("m%d-%s", i, pad), PromptTokens: 1}))
	}
	path := fixture(t, lines...)

	got, err := tailLines(path, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("tailLines returned %d lines, want 5", len(got))
	}
	for i, want := range []int{1995, 1996, 1997, 1998, 1999} {
		var r Row
		if err := json.Unmarshal([]byte(got[i]), &r); err != nil {
			t.Fatalf("line %d did not parse: %v", i, err)
		}
		wantModel := fmt.Sprintf("m%d-%s", want, pad)
		if r.Model != wantModel {
			t.Errorf("line %d: Model = %q, want %q — tailLines must be exact across chunk boundaries", i, r.Model, wantModel)
		}
	}
}

// The rendered tables are the actual product surface. They must not panic on
// empty input, must survive labels longer than the column, and must format
// large numbers readably rather than as an unreadable digit run.
func TestRenderers_HandleEmptyLongLabelsAndLargeNumbers(t *testing.T) {
	// Empty input: a fresh install runs `hyctl stats` before anything dispatched.
	_ = capture(t, func() { RenderStatsTable("today", nil) })
	_ = capture(t, func() { RenderTail(nil) })

	long := strings.Repeat("very-long-model-name-", 5)
	out := capture(t, func() {
		RenderStatsTable("all time", []GroupRow{
			{Key: long, Calls: 1234567, PromptTokens: 9876543, ResponseTokens: 12, EstCostUSD: 12.5},
			{Key: "short", Calls: 1, PromptTokens: 999, ResponseTokens: 0, EstCostUSD: 0.001},
		})
	})
	if strings.Contains(out, long) {
		t.Error("an over-long key was printed in full, breaking the column alignment")
	}
	if !strings.Contains(out, "1,234,567") {
		t.Errorf("large counts are not comma-grouped:\n%s", out)
	}
	if !strings.Contains(out, "999") || strings.Contains(out, ",999") {
		t.Errorf("a sub-1000 number was given a separator:\n%s", out)
	}
	if !strings.Contains(out, "Total") {
		t.Errorf("no total row:\n%s", out)
	}

	// A row with no enum must still print something — "?" — rather than a gap
	// that reads as a missing column.
	tail := capture(t, func() {
		RenderTail([]Row{{TS: "2026-08-01T00:00:00Z", Tier: 3, Model: "m", WallMS: 12}})
	})
	if !strings.Contains(tail, "?/3") {
		t.Errorf("a row with no enum did not render a placeholder:\n%s", tail)
	}
}
