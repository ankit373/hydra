// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/cost"
)

// seedCostLog writes dispatch rows the way the router would, with propensities
// produced by a given exploration rate over three tiers.
func seedCostLog(t *testing.T, rows []cost.Row) {
	t.Helper()
	path := cost.DefaultLogPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, r := range rows {
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(raw)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

// exploredRows simulates an epsilon-greedy router over three tiers, which is
// what makes a counterfactual answerable at all.
func exploredRows(n int) []cost.Row {
	rng := rand.New(rand.NewPCG(11, 13))
	const eps = 0.3
	tiers := []struct {
		tier int
		cost float64
	}{{3, 0.02}, {4, 0.008}, {8, 0.001}}
	greedy := 1 - eps + eps/3
	explore := eps / 3

	out := make([]cost.Row, 0, n)
	for i := 0; i < n; i++ {
		pick, prob := tiers[0], greedy
		if rng.Float64() < eps {
			pick, prob = tiers[1+rng.IntN(2)], explore
		}
		out = append(out, cost.Row{
			TS: "2026-09-01T10:00:00Z", Tier: pick.tier, Enum: "STANDARD",
			Model: "m", Executor: "http", Pool: "api",
			PromptTokens: 100, ResponseTokens: 50, EstCostUSD: pick.cost,
			WallMS: 900, ActProb: prob, KeepProb: 1,
		})
	}
	return out
}

func TestCLI_TraceEvaluateReportsAnIntervalNotAPointEstimate(t *testing.T) {
	cliSandbox(t)
	seedCostLog(t, exploredRows(3000))

	out, _, err := run(t, "trace", "evaluate", "--policy", "tier:8")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[$") || !strings.Contains(out, "at 95%") {
		t.Errorf("output carries no confidence interval:\n%s", out)
	}
	if !strings.Contains(out, "effective sample size") {
		t.Errorf("output does not report effective sample size:\n%s", out)
	}
	if !strings.Contains(out, "self-normalized IPS") {
		t.Errorf("output does not name the estimator:\n%s", out)
	}
}

// The whole point: a policy the router never explored is unanswerable, and a
// confident number there would be worse than no answer.
func TestCLI_TraceEvaluateRefusesWithoutSupport(t *testing.T) {
	cliSandbox(t)
	seedCostLog(t, exploredRows(500)) // no local rows at all

	out, _, err := run(t, "trace", "evaluate", "--policy", "local")
	if err != nil {
		t.Fatalf("evaluate errored instead of reporting a refusal: %v", err)
	}
	if !strings.Contains(out, "No answer") {
		t.Errorf("output does not refuse:\n%s", out)
	}
	if strings.Contains(out, "Estimate $") {
		t.Errorf("a refusal still printed an estimate:\n%s", out)
	}
	if !strings.Contains(out, "explore_rate") {
		t.Errorf("the refusal does not say what would make this answerable:\n%s", out)
	}
}

// A pre-propensity log has a different remedy from no overlap, and pointing at
// explore_rate there sends someone after a setting that cannot fix old rows.
func TestCLI_TraceEvaluateNamesTheRightRemedyForAStaleLog(t *testing.T) {
	cliSandbox(t)
	stale := exploredRows(200)
	for i := range stale {
		stale[i].ActProb = 0 // written before propensity logging existed
	}
	seedCostLog(t, stale)

	out, _, err := run(t, "trace", "evaluate")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "before Hydra recorded routing propensity") {
		t.Errorf("output does not diagnose a pre-propensity log:\n%s", out)
	}
	if strings.Contains(out, "explore_rate") {
		t.Errorf("output suggests explore_rate, which cannot fix rows already written:\n%s", out)
	}
}

func TestCLI_TraceEvaluateJSONCarriesTheDiagnostics(t *testing.T) {
	cliSandbox(t)
	seedCostLog(t, exploredRows(3000))

	out, _, err := run(t, "trace", "evaluate", "--policy", "tier:4", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Policy   string `json:"policy"`
		Rows     int    `json:"rows"`
		Estimate struct {
			Mean       float64 `json:"mean"`
			Lo         float64 `json:"lo"`
			Hi         float64 `json:"hi"`
			ESS        float64 `json:"ess"`
			Supporting int     `json:"supporting"`
			Method     string  `json:"method"`
		} `json:"estimate"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("--json did not emit parseable JSON: %v\n%s", err, out)
	}
	if got.Rows != 3000 {
		t.Errorf("rows = %d, want 3000", got.Rows)
	}
	if !(got.Estimate.Lo <= got.Estimate.Mean && got.Estimate.Mean <= got.Estimate.Hi) {
		t.Errorf("the mean %v is outside its own interval [%v, %v]",
			got.Estimate.Mean, got.Estimate.Lo, got.Estimate.Hi)
	}
	if got.Estimate.ESS <= 0 || got.Estimate.Supporting <= 0 {
		t.Errorf("diagnostics are empty: %+v", got.Estimate)
	}
	// tier 4 costs 0.008 in the fixture; the estimate must land on it.
	if got.Estimate.Mean < 0.007 || got.Estimate.Mean > 0.009 {
		t.Errorf("estimate %v does not recover tier 4's known cost of 0.008", got.Estimate.Mean)
	}
}

func TestCLI_TraceEvaluateRejectsAnUnknownPolicy(t *testing.T) {
	cliSandbox(t)
	seedCostLog(t, exploredRows(100))

	_, _, err := run(t, "trace", "evaluate", "--policy", "wishful")
	if err == nil {
		t.Fatal("an unknown policy was accepted")
	}
	if !strings.Contains(err.Error(), "cheaper") {
		t.Errorf("the error does not list valid policies: %v", err)
	}
}

func TestCLI_TraceEvaluateWithAnEmptyLog(t *testing.T) {
	cliSandbox(t)
	seedCostLog(t, nil)

	_, _, err := run(t, "trace", "evaluate")
	if err == nil {
		t.Fatal("evaluate reported something for an empty log")
	}
	if !strings.Contains(err.Error(), "nothing to evaluate") {
		t.Errorf("error = %v, want it to say there is nothing to evaluate", err)
	}
}

// ── export ──────────────────────────────────────────────────────────────────

// Nothing may leave the machine unless an endpoint was named. Local-first is
// the product, so the default has to be inert.
func TestCLI_TraceExportSendsNothingWithoutAnEndpoint(t *testing.T) {
	cliSandbox(t)
	seedCostLog(t, exploredRows(5))

	out, _, err := run(t, "trace", "export")
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		ResourceSpans []struct {
			ScopeSpans []struct {
				Spans []map[string]any `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"resourceSpans"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("export did not emit a parseable OTLP payload: %v\n%s", err, out)
	}
	if n := len(payload.ResourceSpans[0].ScopeSpans[0].Spans); n != 5 {
		t.Errorf("got %d spans, want 5", n)
	}
}

func TestCLI_TraceExportWritesToAFileAndSaysNothingWasSent(t *testing.T) {
	cliSandbox(t)
	seedCostLog(t, exploredRows(3))
	dest := filepath.Join(t.TempDir(), "spans.json")

	out, _, err := run(t, "trace", "export", "--out", dest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Nothing was sent") {
		t.Errorf("output does not state that nothing left the machine:\n%s", out)
	}
	raw, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	var check map[string]any
	if err := json.Unmarshal(raw, &check); err != nil {
		t.Fatalf("the written file is not valid JSON: %v", err)
	}
}

func TestCLI_TraceExportLimitTakesTheNewest(t *testing.T) {
	cliSandbox(t)
	rows := exploredRows(50)
	rows[len(rows)-1].Model = "newest-model"
	seedCostLog(t, rows)

	out, _, err := run(t, "trace", "export", "--limit", "2")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "newest-model") {
		t.Errorf("--limit dropped the newest rows instead of the oldest:\n%s", out[:min(len(out), 400)])
	}
}

func TestCLI_TraceExportWithAnEmptyLog(t *testing.T) {
	cliSandbox(t)
	seedCostLog(t, nil)

	if _, _, err := run(t, "trace", "export"); err == nil {
		t.Fatal("export produced a payload for an empty log")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
