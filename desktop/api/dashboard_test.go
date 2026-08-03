// SPDX-License-Identifier: MIT

package api

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/cost"
	"github.com/ankit373/hydra/internal/trust"
)

// sandbox points config.Dir() and trust.DefaultLogPath() at a temp HOME, so a
// test never reads (or writes) the developer's real ~/.hydra.
func sandbox(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // windows
	if err := os.MkdirAll(filepath.Join(home, ".hydra", "logs"), 0o700); err != nil {
		t.Fatal(err)
	}
	return home
}

func writeCostRows(t *testing.T, home string, rows []map[string]any) {
	t.Helper()
	path := filepath.Join(home, ".hydra", "logs", "cost.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, r := range rows {
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprintln(f, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
}

// fixture is a deliberately awkward spread: two models, three tiers including
// tier 0 (which groups as "unknown"), two days, and a mix of actual and
// estimated token provenance.
func fixture(t *testing.T, home string) {
	t.Helper()
	today := time.Now().UTC().Format("2006-01-02")
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")

	writeCostRows(t, home, []map[string]any{
		{"ts": today + "T10:00:00Z", "tier": 1, "model": "claude-opus", "prompt_tokens": 1000,
			"response_tokens": 500, "est_cost_usd": 0.05, "wall_ms": 1200, "tokens_source": "actual",
			"run_id": "run-a", "task_id": "task-a"},
		{"ts": today + "T11:00:00Z", "tier": 8, "model": "qwen2.5", "prompt_tokens": 2000,
			"response_tokens": 800, "est_cost_usd": 0, "wall_ms": 400, "tokens_source": "actual",
			"run_id": "run-a", "task_id": "task-b"},
		{"ts": today + "T12:00:00Z", "tier": 1, "model": "claude-opus", "prompt_tokens": 300,
			"response_tokens": 150, "est_cost_usd": 0.015, "wall_ms": 900, "tokens_source": "estimated",
			"run_id": "run-b", "task_id": "task-c"},
		// tier 0 — the "unknown" bucket both surfaces must label identically.
		{"ts": yesterday + "T09:00:00Z", "tier": 0, "model": "mystery", "prompt_tokens": 100,
			"response_tokens": 50, "est_cost_usd": 0.001, "wall_ms": 100, "tokens_source": "estimated",
			"run_id": "run-c", "task_id": "task-d"},
	})
}

// cliTierKey is copied verbatim from cmdStats' --tier closure in
// cmd/hydra/main.go. Duplicating it is the point: if either side changes, the
// test below fails and someone has to reconcile them deliberately.
func cliTierKey(r cost.Row) string {
	if r.Tier == 0 {
		return "unknown"
	}
	return fmt.Sprintf("tier-%d", r.Tier)
}

// The Phase 3 acceptance criterion: the Dashboard's numbers must equal what
// `hyctl cost` and `hyctl stats` report for the same file. This runs both paths
// over one fixture rather than eyeballing the UI.
func TestGetDashboard_MatchesCLI(t *testing.T) {
	home := sandbox(t)
	fixture(t, home)

	got, err := New().GetDashboard()
	if err != nil {
		t.Fatalf("GetDashboard: %v", err)
	}

	// ── hyctl cost ────────────────────────────────────────────────────────
	wantSummary, err := cost.Summary()
	if err != nil {
		t.Fatalf("cost.Summary: %v", err)
	}
	if got.Spend.TodayUSD != wantSummary.Today.EstCostUSD {
		t.Errorf("today spend = %v, `hyctl cost` says %v", got.Spend.TodayUSD, wantSummary.Today.EstCostUSD)
	}
	if got.Spend.AllTimeUSD != wantSummary.AllTime.EstCostUSD {
		t.Errorf("all-time spend = %v, `hyctl cost` says %v", got.Spend.AllTimeUSD, wantSummary.AllTime.EstCostUSD)
	}
	if got.Spend.TodayCalls != wantSummary.Today.Calls {
		t.Errorf("today calls = %d, `hyctl cost` says %d", got.Spend.TodayCalls, wantSummary.Today.Calls)
	}
	if got.Spend.TotalCalls != wantSummary.AllTime.Calls {
		t.Errorf("total calls = %d, `hyctl cost` says %d", got.Spend.TotalCalls, wantSummary.AllTime.Calls)
	}

	rows, err := cost.LoadAll()
	if err != nil {
		t.Fatalf("cost.LoadAll: %v", err)
	}

	// ── hyctl stats --model / --tier / --day ──────────────────────────────
	assertSameGrouping(t, "--model", got.ByModel, cost.ByModel(rows))
	assertSameGrouping(t, "--tier", got.ByTier, cost.GroupBy(rows, cliTierKey))
	assertSameGrouping(t, "--day", got.ByDay, cost.ByDay(rows))
}

func assertSameGrouping(t *testing.T, flag string, got []Breakdown, want []cost.GroupRow) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d groups, `hyctl stats %s` has %d", flag, len(got), flag, len(want))
	}
	for i := range want {
		if got[i].Key != want[i].Key {
			t.Errorf("%s row %d: key %q, CLI says %q (ordering must match too)", flag, i, got[i].Key, want[i].Key)
		}
		if got[i].Calls != want[i].Calls {
			t.Errorf("%s row %q: calls %d, CLI says %d", flag, got[i].Key, got[i].Calls, want[i].Calls)
		}
		if got[i].CostUSD != want[i].EstCostUSD {
			t.Errorf("%s row %q: cost %v, CLI says %v", flag, got[i].Key, got[i].CostUSD, want[i].EstCostUSD)
		}
		if got[i].PromptTokens != want[i].PromptTokens || got[i].ResponseTokens != want[i].ResponseTokens {
			t.Errorf("%s row %q: tokens %d/%d, CLI says %d/%d", flag, got[i].Key,
				got[i].PromptTokens, got[i].ResponseTokens, want[i].PromptTokens, want[i].ResponseTokens)
		}
		if got[i].WallMS != want[i].WallMS {
			t.Errorf("%s row %q: wall %dms, CLI says %dms", flag, got[i].Key, got[i].WallMS, want[i].WallMS)
		}
	}
}

// Tier 0 groups as "unknown" in `hyctl stats --tier`. A Dashboard that labelled
// it "tier-0" would show a bucket the CLI has no name for.
func TestGetDashboard_TierZeroIsUnknown(t *testing.T) {
	home := sandbox(t)
	fixture(t, home)

	d, err := New().GetDashboard()
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, b := range d.ByTier {
		if b.Key == "tier-0" {
			t.Errorf("tier 0 labelled %q; `hyctl stats --tier` calls it \"unknown\"", b.Key)
		}
		if b.Key == "unknown" {
			found = true
		}
	}
	if !found {
		t.Error("no \"unknown\" bucket, but the fixture has a tier-0 row")
	}
}

// A machine that has never dispatched must say so. Rendering $0.00 spend and
// empty tables looks like a measurement — it is the absence of one.
func TestGetDashboard_EmptyStateIsHonest(t *testing.T) {
	sandbox(t) // no cost.jsonl written

	d, err := New().GetDashboard()
	if err != nil {
		t.Fatalf("GetDashboard on a fresh machine must not error: %v", err)
	}
	if d.HasData {
		t.Error("HasData = true with no cost.jsonl")
	}
	if d.ByModel != nil || d.ByTier != nil || d.ByDay != nil || d.Recent != nil {
		t.Error("breakdowns must stay nil so the frontend can tell \"never ran\" from \"ran, produced nothing\"")
	}
	if d.Spend.TotalCalls != 0 || d.Spend.AllTimeUSD != 0 {
		t.Error("empty spend must be zero-valued, not fabricated")
	}
}

// state.json absent means nobody has reported the orchestrator's usage. That is
// unknown, not 0% — 0% would read as "plenty of headroom".
func TestGovernor_UnknownWithoutStateFile(t *testing.T) {
	sandbox(t)

	d, err := New().GetDashboard()
	if err != nil {
		t.Fatal(err)
	}
	if d.Governor.Known {
		t.Error("Known = true with no state.json")
	}
	if d.Governor.Mode != "" {
		t.Errorf("Mode = %q with no state.json; want empty so the view shows \"unknown\"", d.Governor.Mode)
	}
}

func TestGovernor_ReadsPctAndMode(t *testing.T) {
	cases := []struct {
		pct  int
		mode string
	}{
		{10, "normal"}, {52, "compact"}, {67, "caution"},
		{72, "warning"}, {77, "critical"}, {88, "emergency"},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			home := sandbox(t)
			state := filepath.Join(home, ".hydra", "logs", "state.json")
			body := fmt.Sprintf(`{"claude_pct": %d}`, tc.pct)
			if err := os.WriteFile(state, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}

			d, err := New().GetDashboard()
			if err != nil {
				t.Fatal(err)
			}
			if !d.Governor.Known {
				t.Fatal("Known = false with a valid state.json")
			}
			if d.Governor.Pct != tc.pct {
				t.Errorf("Pct = %d, want %d", d.Governor.Pct, tc.pct)
			}
			if d.Governor.Mode != tc.mode {
				t.Errorf("Mode = %q, want %q", d.Governor.Mode, tc.mode)
			}
		})
	}
}

// A malformed state.json must degrade to unknown, not to a confident zero.
func TestGovernor_MalformedStateIsUnknown(t *testing.T) {
	home := sandbox(t)
	state := filepath.Join(home, ".hydra", "logs", "state.json")
	if err := os.WriteFile(state, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	d, err := New().GetDashboard()
	if err != nil {
		t.Fatal(err)
	}
	if d.Governor.Known {
		t.Error("Known = true for malformed state.json")
	}
}

// state.json without a claude_pct key is not a report of 0%.
func TestGovernor_MissingKeyIsUnknown(t *testing.T) {
	home := sandbox(t)
	state := filepath.Join(home, ".hydra", "logs", "state.json")
	if err := os.WriteFile(state, []byte(`{"something_else": 1}`), 0o600); err != nil {
		t.Fatal(err)
	}

	d, err := New().GetDashboard()
	if err != nil {
		t.Fatal(err)
	}
	if d.Governor.Known {
		t.Error("Known = true when claude_pct is absent; absence is not 0%")
	}
}

// The trust panel must agree with `hyctl trust stats`, including the fixed-5
// baseline it compares against — a different baseline reports different savings
// for identical runs.
func TestTrustPanel_MatchesCLIAggregate(t *testing.T) {
	home := sandbox(t)
	path := filepath.Join(home, ".hydra", "trust.jsonl")
	for _, r := range []trust.RunLog{
		{TaskHash: "a", Domain: "go", TargetConf: 0.95, FinalConf: 0.97, Samples: 3, CostUSD: 0.01, Decision: "accept"},
		{TaskHash: "b", Domain: "go", TargetConf: 0.90, FinalConf: 0.88, Samples: 2, CostUSD: 0.02, Decision: "escalate"},
		{TaskHash: "c", Domain: "ts", TargetConf: 0.99, FinalConf: 0.99, Samples: 4, CostUSD: 0.03, Decision: "accept"},
	} {
		if err := trust.LogRun(path, r); err != nil {
			t.Fatal(err)
		}
	}

	d, err := New().GetDashboard()
	if err != nil {
		t.Fatal(err)
	}

	runs, err := trust.LoadRuns(path)
	if err != nil {
		t.Fatal(err)
	}
	// 5 is the literal `hyctl trust stats` passes to trust.Aggregate.
	want := trust.Aggregate(runs, 5)

	if d.Trust.Runs != want.Runs {
		t.Errorf("runs = %d, `hyctl trust stats` says %d", d.Trust.Runs, want.Runs)
	}
	if d.Trust.MeanSamples != want.MeanSamples {
		t.Errorf("mean samples = %v, CLI says %v", d.Trust.MeanSamples, want.MeanSamples)
	}
	if d.Trust.SamplesSavedPct != want.SamplesSavedPct {
		t.Errorf("samples saved = %v%%, CLI says %v%% — the fixed-N baselines have diverged",
			d.Trust.SamplesSavedPct, want.SamplesSavedPct)
	}
	if d.Trust.AutoClearedPct != want.AutoClearedPct {
		t.Errorf("auto-cleared = %v%%, CLI says %v%%", d.Trust.AutoClearedPct, want.AutoClearedPct)
	}
	if d.Trust.TotalCostUSD != want.TotalCostUSD {
		t.Errorf("total cost = %v, CLI says %v", d.Trust.TotalCostUSD, want.TotalCostUSD)
	}
	if d.Trust.FixedSwarmN != 5 {
		t.Errorf("FixedSwarmN = %d, want 5 to match `hyctl trust stats`", d.Trust.FixedSwarmN)
	}
}

// TokensActualPct tells the reader how much of the dollar figure rests on
// provider-reported tokens versus Hydra's char/4 estimate. Getting it wrong
// misrepresents the confidence of every number above it.
func TestSpend_TokenProvenanceShare(t *testing.T) {
	home := sandbox(t)
	today := time.Now().UTC().Format("2006-01-02")
	writeCostRows(t, home, []map[string]any{
		{"ts": today + "T10:00:00Z", "tier": 1, "model": "m", "prompt_tokens": 600,
			"response_tokens": 400, "est_cost_usd": 0.01, "tokens_source": "actual"},
		{"ts": today + "T11:00:00Z", "tier": 1, "model": "m", "prompt_tokens": 700,
			"response_tokens": 300, "est_cost_usd": 0.01, "tokens_source": "estimated"},
	})

	d, err := New().GetDashboard()
	if err != nil {
		t.Fatal(err)
	}
	// 1000 actual of 2000 total.
	if d.Spend.TokensActualPct != 50 {
		t.Errorf("TokensActualPct = %v, want 50", d.Spend.TokensActualPct)
	}
}

// GetDashboard reads fresh on every call and holds no state, which is what
// makes it safe for the frontend to poll from several places at once.
func TestGetDashboard_ConcurrentCallsAreSafe(t *testing.T) {
	home := sandbox(t)
	fixture(t, home)

	a := New()
	done := make(chan error, 16)
	for range cap(done) {
		go func() {
			_, err := a.GetDashboard()
			done <- err
		}()
	}
	for range cap(done) {
		if err := <-done; err != nil {
			t.Errorf("concurrent GetDashboard: %v", err)
		}
	}
}
