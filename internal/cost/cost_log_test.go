// SPDX-License-Identifier: MIT

package cost

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
