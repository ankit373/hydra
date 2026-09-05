// SPDX-License-Identifier: MIT

package api

import (
	"bufio"
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
// test never reads (or writes) the developer's real ~/.hydra. Clearing
// HYDRA_HOME too matters since config.Dir() prefers it over HOME (#442) — a
// developer or CI runner with one already exported would otherwise leak
// straight through every "HOME-only" sandbox in this file.
func sandbox(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // windows
	t.Setenv("HYDRA_HOME", "")
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

// A fresh machine has no ~/.hydra/calibration.jsonl. That is a real empty
// leaderboard, not an error — trust.New treats a missing file as an empty
// store, and the frontend needs a non-nil slice to render "no data yet"
// rather than a null it can't .map over.
func TestCalibrationPanel_EmptyOnFreshMachine(t *testing.T) {
	sandbox(t)

	d, err := New().GetDashboard()
	if err != nil {
		t.Fatalf("GetDashboard on a fresh machine must not error: %v", err)
	}
	if d.Calibration == nil {
		t.Error("Calibration is nil; the view iterates it directly and a nil slice marshals to null")
	}
	if len(d.Calibration) != 0 {
		t.Errorf("Calibration has %d rows with no calibration.jsonl", len(d.Calibration))
	}
}

// Real calibration history — written the same way `hyctl trust record` does,
// through trust.Calibrator.Update — must come back out through the Dashboard
// exactly as trust.Calibrator.Report (the same call `hyctl trust calibration`
// makes) sees it: most-diagnostic-source first, with Se/Sp/N intact.
func TestCalibrationPanel_MatchesCalibratorReport(t *testing.T) {
	home := sandbox(t)
	path := filepath.Join(home, ".hydra", "calibration.jsonl")

	cal, err := trust.New(path)
	if err != nil {
		t.Fatal(err)
	}
	// verifier:go-test: near-perfect — should out-rank the model by D.
	for i := 0; i < 40; i++ {
		mustUpdate(t, cal, "verifier:go-test", "go", true, trust.OutcomeCorrect)
	}
	for i := 0; i < 40; i++ {
		mustUpdate(t, cal, "verifier:go-test", "go", false, trust.OutcomeIncorrect)
	}
	// model:claude-sonnet: right most of the time, but noisier.
	for i := 0; i < 8; i++ {
		mustUpdate(t, cal, "model:claude-sonnet", "go", true, trust.OutcomeCorrect)
	}
	for i := 0; i < 2; i++ {
		mustUpdate(t, cal, "model:claude-sonnet", "go", true, trust.OutcomeIncorrect)
	}

	d, err := New().GetDashboard()
	if err != nil {
		t.Fatal(err)
	}

	want := cal.Report()
	if len(d.Calibration) != len(want) {
		t.Fatalf("Calibration has %d rows, Calibrator.Report says %d", len(d.Calibration), len(want))
	}
	for i, w := range want {
		got := d.Calibration[i]
		if got.Source != w.Source || got.Domain != w.Domain {
			t.Errorf("row %d: got %s/%s, want %s/%s (ordering must match Report's D-descending sort)",
				i, got.Source, got.Domain, w.Source, w.Domain)
		}
		if got.D != w.D || got.Se != w.Se || got.Sp != w.Sp {
			t.Errorf("row %d (%s): D/Se/Sp = %v/%v/%v, want %v/%v/%v",
				i, got.Source, got.D, got.Se, got.Sp, w.D, w.Se, w.Sp)
		}
		if got.N != int(w.N) {
			t.Errorf("row %d (%s): N = %d, want %d", i, got.Source, got.N, int(w.N))
		}
	}
	if want[0].Source != "verifier:go-test" {
		t.Fatalf("test setup: expected verifier:go-test to lead, got %s", want[0].Source)
	}
}

// mustUpdate mirrors internal/trust's own test helper of the same name —
// duplicated here rather than exported from trust for one call site, since
// trust.Calibrator.Update already returns a plain error.
func mustUpdate(t *testing.T, c *trust.Calibrator, source, domain string, saidCorrect bool, outcome trust.Outcome) {
	t.Helper()
	if err := c.Update(source, domain, saidCorrect, outcome); err != nil {
		t.Fatalf("Update: %v", err)
	}
}

// writeManyCostRows fills cost.jsonl with n synthetic rows — large enough
// that cost.jsonl's read+parse cost dominates a GetDashboard call, so any
// difference between reading it once and reading it twice shows up in wall
// time (#524).
func writeManyCostRows(t *testing.T, home string, n int) {
	t.Helper()
	path := filepath.Join(home, ".hydra", "logs", "cost.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	defer w.Flush()

	today := time.Now().UTC().Format("2006-01-02")
	for i := 0; i < n; i++ {
		row := map[string]any{
			"ts": today + "T10:00:00Z", "tier": (i % 8) + 1, "model": fmt.Sprintf("model-%d", i%5),
			"prompt_tokens": 100 + i%50, "response_tokens": 50 + i%20, "est_cost_usd": 0.001,
			"wall_ms": 200, "tokens_source": "actual", "run_id": fmt.Sprintf("run-%d", i), "task_id": fmt.Sprintf("task-%d", i),
		}
		raw, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(raw); err != nil {
			t.Fatal(err)
		}
		if err := w.WriteByte('\n'); err != nil {
			t.Fatal(err)
		}
	}
}

// GetDashboard used to call cost.LoadAll() directly and then cost.Summary()
// — which itself calls LoadAll() again — parsing cost.jsonl twice per call.
// On a large log this test's cost.jsonl deliberately reproduces the shape of
// the bug: enough rows that the JSONL parse dominates wall time, so a
// regression back to a second full read shows up as roughly double the time
// a single cost.LoadAll of the same file takes. The correctness half of this
// (that Spend actually matches cost.SummaryFromRows) is already covered by
// TestGetDashboard_MatchesCLI; this test is about the read count.
func TestGetDashboard_ReadsCostLogOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large-file timing test in -short mode")
	}
	home := sandbox(t)
	const n = 40000
	writeManyCostRows(t, home, n)

	start := time.Now()
	rows, err := cost.LoadAll()
	if err != nil {
		t.Fatalf("cost.LoadAll: %v", err)
	}
	if len(rows) != n {
		t.Fatalf("wrote %d rows, LoadAll read %d", n, len(rows))
	}
	oneRead := time.Since(start)

	start = time.Now()
	d, err := New().GetDashboard()
	if err != nil {
		t.Fatalf("GetDashboard: %v", err)
	}
	dashboardTime := time.Since(start)

	if !d.HasData || d.Spend.TotalCalls != n {
		t.Fatalf("GetDashboard did not see all %d rows: HasData=%v TotalCalls=%d", n, d.HasData, d.Spend.TotalCalls)
	}

	// A single-read GetDashboard costs one LoadAll plus a handful of cheap
	// O(n) in-memory passes (ByModel/ByTier/ByDay/SummaryFromRows) — well
	// under 2x one read. A regression to calling cost.Summary() (its own
	// second LoadAll) on top of the first would cost close to 2x oneRead
	// before those passes even run. 1.6x gives headroom for scheduling noise
	// while still catching a real regression.
	if budget := oneRead * 16 / 10; dashboardTime > budget {
		t.Errorf("GetDashboard took %v for %d rows — more than 1.6x a single cost.LoadAll (%v); "+
			"looks like it is reading cost.jsonl twice again", dashboardTime, n, oneRead)
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

// A single-point (or absent) history gives RiskFromHistory no rate signal, so
// burn and risk come back zero. Observations must ship alongside them, or a UI
// cannot tell "measured no risk" from "never measured" (#555).
func TestGovernor_NoRateSignalIsDistinguishableFromNoRisk(t *testing.T) {
	home := sandbox(t)
	state := filepath.Join(home, ".hydra", "logs", "state.json")
	if err := os.WriteFile(state, []byte(`{"claude_pct": 40, "claude_pct_history": [40]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	d, err := New().GetDashboard()
	if err != nil {
		t.Fatal(err)
	}
	g := d.Governor
	if g.BurnRatePct != 0 || g.Risk != 0 {
		t.Errorf("burn=%v risk=%v; one observation cannot yield a rate", g.BurnRatePct, g.Risk)
	}
	if g.Observations != 1 {
		t.Errorf("Observations = %d, want 1 — without it a zero risk reads as measured", g.Observations)
	}
	if g.HorizonObs <= 0 {
		t.Errorf("HorizonObs = %d; the UI cannot state the horizon it is reporting", g.HorizonObs)
	}
	// With no rate signal the governor must fall back to the level band exactly.
	if g.EffectiveMode != g.Mode {
		t.Errorf("EffectiveMode = %q, Mode = %q; they must agree with no rate signal",
			g.EffectiveMode, g.Mode)
	}
}

// A climbing trajectory must produce a positive burn rate, and a fast climb must
// be able to raise the effective mode above the static band — that escalation is
// the entire point of the rate model.
func TestGovernor_FastBurnRaisesEffectiveModeAboveTheBand(t *testing.T) {
	home := sandbox(t)
	state := filepath.Join(home, ".hydra", "logs", "state.json")
	// Steady +8pp per observation, ending at 64: ModeFor(64) is "compact", but
	// at this rate the 80% barrier is about two observations away.
	body := `{"claude_pct": 64, "claude_pct_history": [24,32,40,48,56,64]}`
	if err := os.WriteFile(state, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	d, err := New().GetDashboard()
	if err != nil {
		t.Fatal(err)
	}
	g := d.Governor
	if g.BurnRatePct <= 0 {
		t.Errorf("BurnRatePct = %v on a monotonically climbing series", g.BurnRatePct)
	}
	if g.Risk <= 0 {
		t.Errorf("Risk = %v; a fast climb toward 80%% must carry non-zero risk", g.Risk)
	}
	if g.Mode != "compact" {
		t.Fatalf("Mode = %q, want compact — fixture no longer pins the band it means to", g.Mode)
	}
	if g.EffectiveMode == g.Mode {
		t.Errorf("EffectiveMode = %q, same as the static band; the rate floor never applied",
			g.EffectiveMode)
	}
}
