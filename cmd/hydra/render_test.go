// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/swarm"
	"github.com/ankit373/hydra/internal/testutil"
	"github.com/ankit373/hydra/internal/trust"
)

// The result printers are the only place a swarm or SPRT run becomes visible.
// A printer that omits a failed head, or renders a cost of $0 for a paid run,
// is a report the user makes decisions from.

func TestPrintSwarmResult_ShowsEveryAttemptAndMarksTheWinner(t *testing.T) {
	testutil.NewSandbox(t)

	winner := swarm.Attempt{
		Head:   provider.Head{ID: "strong", Name: "strong"},
		Status: swarm.StatusOK, Output: "the winning answer",
		InputTokens: 100, OutputTokens: 50, EstCostUSD: 0.02,
		Duration: 2 * time.Second, Rank: 1,
	}
	res := &swarm.SwarmResult{
		Mode:   swarm.ModeBest,
		Prompt: "the question",
		Attempts: []swarm.Attempt{
			winner,
			{Head: provider.Head{ID: "weak", Name: "weak"}, Status: swarm.StatusOK,
				Output: "a worse answer", EstCostUSD: 0.01},
			{Head: provider.Head{ID: "broken", Name: "broken"}, Status: swarm.StatusFailed},
			{Head: provider.Head{ID: "slow", Name: "slow"}, Status: swarm.StatusTimeout},
		},
		Winner:       &winner,
		TotalCostUSD: 0.03,
		WallDuration: 3 * time.Second,
	}

	out := captureStdout(t, func() { printSwarmResult(res) })

	for _, want := range []string{"strong", "weak", "broken", "slow"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report omits head %q, the user paid for it:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "the winning answer") {
		t.Errorf("the winning answer is not shown:\n%s", out)
	}
	if !strings.Contains(out, "0.03") {
		t.Errorf("the total cost is not reported:\n%s", out)
	}

	// A run where everything failed must still render, and must not claim a
	// winner it does not have.
	empty := &swarm.SwarmResult{
		Mode:     swarm.ModeAll,
		Attempts: []swarm.Attempt{{Head: provider.Head{ID: "a"}, Status: swarm.StatusFailed}},
	}
	if got := captureStdout(t, func() { printSwarmResult(empty) }); strings.TrimSpace(got) == "" {
		t.Error("a fully-failed swarm printed nothing")
	}
}

// A judge fallback must say why the LLM judge was skipped, CompositeJudge
// already carries the reason on JudgeMeta.FallbackReason, but the printer
// discarded it, leaving a runtime judge failure indistinguishable from a
// healthy run (#501).
func TestPrintSwarmResult_ShowsWhyTheJudgeFellBack(t *testing.T) {
	testutil.NewSandbox(t)

	winner := swarm.Attempt{
		Head: provider.Head{ID: "strong", Name: "strong"}, Status: swarm.StatusOK,
		Output: "the winning answer", Rank: 1,
	}
	res := &swarm.SwarmResult{
		Mode:     swarm.ModeBest,
		Prompt:   "the question",
		Attempts: []swarm.Attempt{winner},
		Winner:   &winner,
		Verdict: &swarm.JudgeVerdict{
			WinnerIndex: 0,
			Reason:      "ranked by capability score",
			Meta:        swarm.JudgeMeta{UsedFallback: true, FallbackReason: "judge dispatch: connection refused"},
		},
	}

	out := captureStdout(t, func() { printSwarmResult(res) })
	if !strings.Contains(out, "cap-score fallback") {
		t.Errorf("the fallback is not labelled:\n%s", out)
	}
	if !strings.Contains(out, "connection refused") {
		t.Errorf("the fallback reason is not shown, so a real judge failure looks identical "+
			"to a healthy run:\n%s", out)
	}
}

// The SPRT report is the evidence ledger behind a confidence number. Every
// source that was sampled must appear, or the number is unauditable.
func TestPrintSPRTResult_ShowsTheWholeLedger(t *testing.T) {
	testutil.NewSandbox(t)

	res := &swarm.SPRTResult{
		Domain: "go",
		Target: 0.95,
		Trust: &trust.Result{
			Confidence: 0.96,
			Samples:    3,
			SpentUSD:   0.0412,
			Candidate:  "yes, the migration is safe",
			Ledger: []trust.Evidence{
				{Source: "model:a", Agreed: true, LLR: 1.2, LambdaAfter: 1.2},
				{Source: "model:b", Agreed: false, LLR: -0.4, LambdaAfter: 0.8},
				{Source: "verifier:go-test", Agreed: true, LLR: 2.5, LambdaAfter: 3.3},
			},
		},
	}

	out := captureStdout(t, func() { printSPRTResult(res) })

	for _, want := range []string{"model:a", "model:b", "verifier:go-test", "go"} {
		if !strings.Contains(out, want) {
			t.Errorf("the ledger omits %q, so the confidence is unauditable:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "agree") || !strings.Contains(out, "disagree") {
		t.Errorf("verdicts are not labelled:\n%s", out)
	}
	if !strings.Contains(out, "96.0%") {
		t.Errorf("the confidence reached is not reported:\n%s", out)
	}
	if !strings.Contains(out, "yes, the migration is safe") {
		t.Errorf("the answer itself is not printed:\n%s", out)
	}
	if !strings.Contains(out, "0.0412") {
		t.Errorf("the spend is not reported:\n%s", out)
	}
}

// An SPRT run is logged so `hyctl trust stats` and `hyctl trust explain` can
// read it back. A run that is not logged is a paid ensemble with no record.
func TestLogTrustRun_WritesAReadableRecord(t *testing.T) {
	testutil.NewSandbox(t)

	res := &swarm.SPRTResult{
		Domain: "",
		Target: 0.9,
		Attempts: []swarm.Attempt{
			{Head: provider.Head{ID: "a"}, TokensEstimated: true},
			{Head: provider.Head{ID: "a"}}, // same head twice
			{Head: provider.Head{ID: "b"}},
		},
		Trust: &trust.Result{Confidence: 0.93, Samples: 3, SpentUSD: 0.01,
			Decision: trust.DecisionAccept},
	}

	logTrustRun(res, "is this safe?", "")

	raw, err := os.ReadFile(trust.DefaultLogPath())
	if err != nil {
		t.Fatalf("no trust run was logged: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(strings.Split(string(raw), "\n")[0])), &got); err != nil {
		t.Fatalf("the logged run is not valid JSON: %v\n%s", err, raw)
	}
	if got["domain"] != "default" {
		t.Errorf("domain = %v, want it defaulted rather than logged empty", got["domain"])
	}
	models, _ := got["models"].([]any)
	if len(models) != 2 {
		t.Errorf("models = %v, want each head once", models)
	}
	// An estimated-token run must be labelled, so the spend is not read as
	// measured.
	if got["cost_source"] == "actual" {
		t.Error("a run with estimated tokens was logged as actual spend")
	}
	if got["task_hash"] == "" {
		t.Error("no task hash, so `hyctl trust explain` cannot find this run")
	}
}

func TestAnyEstimated(t *testing.T) {
	if anyEstimated(nil) {
		t.Error("anyEstimated(nil) = true")
	}
	measured := []swarm.Attempt{{}, {}}
	if anyEstimated(measured) {
		t.Error("anyEstimated = true with no estimated attempts")
	}
	// One estimate taints the whole run: the total cannot be presented as
	// measured when part of it was guessed.
	mixed := []swarm.Attempt{{}, {TokensEstimated: true}, {}}
	if !anyEstimated(mixed) {
		t.Error("anyEstimated = false with one estimated attempt; the total would " +
			"be reported as measured spend")
	}
}

func TestStatusIcon_CoversEveryStatus(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range []swarm.HeadStatus{
		swarm.StatusOK, swarm.StatusFailed, swarm.StatusTimeout,
		swarm.StatusCanceled, swarm.StatusAuthRequired, swarm.HeadStatus("unknown"),
	} {
		icon := statusIcon(s)
		if icon == "" {
			t.Errorf("status %q has no icon", s)
		}
		seen[icon] = true
	}
	if len(seen) < 2 {
		t.Error("every status renders the same icon; a failure looks like a success")
	}
	if statusIcon(swarm.StatusOK) == statusIcon(swarm.StatusFailed) {
		t.Error("a failed head shows the same icon as a successful one")
	}
}

// The ensemble plan is what --dry-run prints for a confidence run. It must name
// the heads and the estimated spend, since that is the decision the flag exists
// to inform.
func TestPrintEnsemblePlan_NamesTheHeadsAndTheCost(t *testing.T) {
	testutil.NewSandbox(t)

	heads := []provider.Head{
		{ID: "a", Name: "vendor · model-a", CapScore: 90},
		{ID: "b", Name: "vendor · model-b", CapScore: 80},
	}
	out := captureStdout(t, func() { printEnsemblePlan(heads, 0.0825, 0.95, swarm.ModeBest, 0) })

	for _, want := range []string{"model-a", "model-b"} {
		if !strings.Contains(out, want) {
			t.Errorf("the plan omits %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "0.08") {
		t.Errorf("the plan does not state the estimated cost:\n%s", out)
	}
	if !strings.Contains(out, "95") {
		t.Errorf("the plan does not state the target confidence:\n%s", out)
	}

	// Without a confidence target the same plan describes a swarm, and must
	// name the mode rather than an SPRT target it does not have.
	swarmOut := captureStdout(t, func() {
		printEnsemblePlan(heads, 0.02, 0, swarm.ModeRace, 0.10)
	})
	if !strings.Contains(swarmOut, string(swarm.ModeRace)) {
		t.Errorf("the swarm plan does not name its mode:\n%s", swarmOut)
	}
	if strings.Contains(swarmOut, "SPRT") {
		t.Errorf("a swarm dry run was labelled as an SPRT run:\n%s", swarmOut)
	}
}

// `hyctl cost` and `hyctl stats` against real rows: the read path nothing else
// exercises end to end.
func TestCLI_CostAndStatsAgainstRealRows(t *testing.T) {
	cliSandbox(t)

	dir := filepath.Join(config.Dir(), "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	rows := []string{
		`{"ts":"` + now + `","model":"claude","tier":1,"enum":"CORE","prompt_tokens":1000,` +
			`"response_tokens":500,"est_cost_usd":0.05,"wall_ms":1200,"tokens_source":"actual"}`,
		`{"ts":"` + now + `","model":"qwen","tier":10,"enum":"GRUNT","prompt_tokens":200,` +
			`"response_tokens":100,"est_cost_usd":0,"wall_ms":300,"tokens_source":"estimated"}`,
	}
	if err := os.WriteFile(filepath.Join(dir, "cost.jsonl"),
		[]byte(strings.Join(rows, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{{"cost"}, {"stats"}, {"cost", "--json"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			out, cobraOut, err := run(t, args...)
			if err != nil {
				t.Fatalf("`hyctl %s` failed with real rows: %v", strings.Join(args, " "), err)
			}
			combined := out + cobraOut
			if strings.TrimSpace(combined) == "" {
				t.Fatalf("`hyctl %s` printed nothing", strings.Join(args, " "))
			}
			if args[len(args)-1] == "--json" {
				var v any
				if jerr := json.Unmarshal([]byte(strings.TrimSpace(out)), &v); jerr != nil {
					t.Errorf("--json did not emit valid JSON: %v\n%s", jerr, out)
				}
				return
			}
			if !strings.Contains(combined, "claude") {
				t.Errorf("the report does not name the model that was billed:\n%s", combined)
			}
		})
	}
}

// relativeTime is the trend line's human-facing duration, a bad format
// string here would silently misreport how stale the comparison point is.
func TestRelativeTime(t *testing.T) {
	cases := []struct {
		ago  time.Duration
		want string
	}{
		{30 * time.Second, "just now"},
		{5 * time.Minute, "5 minutes ago"},
		{1 * time.Minute, "1 minute ago"},
		{3 * time.Hour, "3 hours ago"},
		{1 * time.Hour, "1 hour ago"},
		{48 * time.Hour, "2 days ago"},
	}
	for _, tc := range cases {
		ts := time.Now().Add(-tc.ago).Format(time.RFC3339)
		if got := relativeTime(ts); got != tc.want {
			t.Errorf("relativeTime(%s ago) = %q, want %q", tc.ago, got, tc.want)
		}
	}

	if got := relativeTime("not a timestamp"); got != "not a timestamp" {
		t.Errorf("relativeTime(garbage) = %q, want the raw string back, not a crash", got)
	}
}
