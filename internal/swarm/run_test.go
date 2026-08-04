// SPDX-License-Identifier: MIT

package swarm

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/cost"
	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/testutil"
)

// Run fans one prompt out to N heads and charges the user for all of them. Its
// contract is that every head's answer is accounted for, exactly one winner is
// reported, and the spend is logged — none of which was covered.

// swarmHead builds a head backed by a fake CLI binary that prints reply.
func swarmHead(t *testing.T, s *testutil.Sandbox, id string, capScore int, reply string) provider.Head {
	t.Helper()
	body := "#!/bin/sh\n/bin/cat <<'HYDRA_EOF'\n" + reply + "\nHYDRA_EOF\n"
	if runtime.GOOS == "windows" {
		body = "@echo off\r\necho " + strings.ReplaceAll(reply, "\n", " ") + "\r\n"
	}
	return provider.Head{
		ID: id, Name: id, Provider: "openai", Source: "cli",
		CapScore: capScore, AuthReady: true,
		Executable: s.FakeBinary(t, "fake-"+id, body),
	}
}

// brokenHead points at a binary that is not there, so its attempt fails.
func brokenHead(s *testutil.Sandbox, id string, capScore int) provider.Head {
	return provider.Head{
		ID: id, Name: id, Provider: "openai", Source: "cli",
		CapScore: capScore, AuthReady: true,
		Executable: filepath.Join(s.BinDir, "does-not-exist-"+id),
	}
}

func newSwarm(t *testing.T, heads ...provider.Head) *Swarm {
	t.Helper()
	d, err := dispatch.New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// pricing nil: EstimateCost is exercised separately, and a live pricing DB
	// spawns a background OpenRouter fetch per construction.
	return New(d, heads, nil)
}

func swarmSandbox(t *testing.T) *testutil.Sandbox {
	t.Helper()
	s := testutil.NewSandbox(t)
	if err := config.Save(&config.Config{Cortex: "strong"}); err != nil {
		t.Fatal(err)
	}
	return s
}

// `--swarm-mode all` runs every head and ranks them, so the user can compare.
func TestRun_ModeAllRunsEveryHeadAndRanksThem(t *testing.T) {
	s := swarmSandbox(t)
	sw := newSwarm(t,
		swarmHead(t, s, "strong", 95, "answer from strong"),
		swarmHead(t, s, "weak", 60, "answer from weak"),
	)

	res, err := sw.Run(context.Background(), "the question", Options{Mode: ModeAll})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Attempts) != 2 {
		t.Fatalf("got %d attempts, want one per head", len(res.Attempts))
	}
	for _, a := range res.Attempts {
		if a.Status != StatusOK {
			t.Errorf("%s failed: %v", a.Head.ID, a.Err)
		}
		if a.Output == "" {
			t.Errorf("%s produced no output", a.Head.ID)
		}
		if a.Duration <= 0 {
			t.Errorf("%s has no measured duration", a.Head.ID)
		}
	}
	if res.Winner == nil || res.Winner.Head.ID != "strong" {
		t.Errorf("Winner = %+v, want the highest-CapScore success", res.Winner)
	}
	if res.WallDuration <= 0 {
		t.Error("WallDuration was not measured; the swarm's own cost is its wall time")
	}
	if res.Mode != ModeAll || res.Prompt != "the question" {
		t.Errorf("result did not carry the request back: %+v", res)
	}
}

// Race returns the first answer and marks it. The losers still ran and still
// cost money, so they must still appear in the attempt list.
func TestRun_ModeRaceReportsOneWinnerAndKeepsTheLosers(t *testing.T) {
	s := swarmSandbox(t)
	sw := newSwarm(t,
		swarmHead(t, s, "a", 90, "answer a"),
		swarmHead(t, s, "b", 80, "answer b"),
	)

	res, err := sw.Run(context.Background(), "q", Options{Mode: ModeRace})
	if err != nil {
		t.Fatal(err)
	}
	if res.Winner == nil {
		t.Fatal("race produced no winner")
	}
	if res.Winner.Rank != 1 {
		t.Errorf("the winner is rank %d, want 1", res.Winner.Rank)
	}
	winners := 0
	for _, a := range res.Attempts {
		if a.Rank == 1 {
			winners++
		}
	}
	if winners != 1 {
		t.Errorf("%d attempts are marked rank 1; a race has exactly one winner", winners)
	}
}

// Best mode judges. With no judge head reachable the LLM judge fails, and the
// deterministic CapScore fallback must still produce a winner rather than
// leaving the user with nothing after paying for N answers.
func TestRun_ModeBestFallsBackToCapScoreWhenTheJudgeFails(t *testing.T) {
	s := swarmSandbox(t)
	sw := newSwarm(t,
		swarmHead(t, s, "mid", 70, "answer from mid"),
		swarmHead(t, s, "best", 88, "answer from best"),
	)

	res, err := sw.Run(context.Background(), "q", Options{Mode: ModeBest})
	if err != nil {
		t.Fatal(err)
	}
	if res.Winner == nil {
		t.Fatal("best mode produced no winner; the user paid for two answers and got none")
	}
	if res.Winner.Head.ID != "best" {
		t.Errorf("Winner = %s, want the strongest success from the CapScore fallback",
			res.Winner.Head.ID)
	}
}

// A head that fails must not take the swarm down with it: the others answered.
func TestRun_OneFailingHeadDoesNotFailTheSwarm(t *testing.T) {
	s := swarmSandbox(t)
	sw := newSwarm(t,
		brokenHead(s, "broken", 95),
		swarmHead(t, s, "working", 70, "the answer"),
	)

	res, err := sw.Run(context.Background(), "q", Options{Mode: ModeAll})
	if err != nil {
		t.Fatalf("one failing head failed the whole swarm: %v", err)
	}
	if len(res.Attempts) != 2 {
		t.Fatalf("got %d attempts, want both recorded — a failed head is part of "+
			"what the run did", len(res.Attempts))
	}
	if res.Winner == nil || res.Winner.Head.ID != "working" {
		t.Errorf("Winner = %+v, want the head that answered", res.Winner)
	}
	var sawFailure bool
	for _, a := range res.Attempts {
		if a.Head.ID == "broken" {
			sawFailure = a.Status != StatusOK
			if a.Err == nil {
				t.Error("the failed attempt carries no error to show the user")
			}
		}
	}
	if !sawFailure {
		t.Error("the broken head was recorded as successful")
	}
}

// Every head failing is an answer of "none", not a crash.
func TestRun_AllHeadsFailingLeavesNoWinner(t *testing.T) {
	s := swarmSandbox(t)
	sw := newSwarm(t, brokenHead(s, "a", 90), brokenHead(s, "b", 80))

	res, err := sw.Run(context.Background(), "q", Options{Mode: ModeAll})
	if err != nil {
		t.Fatal(err)
	}
	if res.Winner != nil {
		t.Errorf("Winner = %+v with every head failed", res.Winner)
	}
}

// Spend is logged to cost.jsonl, which is what `hyctl cost` and `hyctl stats`
// read. A swarm that does not log is spend the user cannot see.
func TestRun_LogsEveryAttemptToTheCostLog(t *testing.T) {
	s := swarmSandbox(t)
	sw := newSwarm(t,
		swarmHead(t, s, "a", 90, "answer a"),
		swarmHead(t, s, "b", 80, "answer b"),
	)

	if _, err := sw.Run(context.Background(), "the prompt", Options{Mode: ModeAll}); err != nil {
		t.Fatal(err)
	}

	rows, err := cost.LoadAll()
	if err != nil {
		t.Fatalf("no cost log after a swarm run: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("cost.jsonl has %d rows, want one per attempt: %+v", len(rows), rows)
	}
	var winners int
	for _, r := range rows {
		if r.SwarmMode != string(ModeAll) {
			t.Errorf("row mode = %q, want %q — `hyctl stats` groups on this",
				r.SwarmMode, ModeAll)
		}
		if r.SwarmWinner {
			winners++
		}
	}
	if winners != 1 {
		t.Errorf("%d rows are flagged as the winner, want exactly 1", winners)
	}
}

// Unknown modes must be refused before any head is fired, not after.
func TestRun_UnknownModeIsRefused(t *testing.T) {
	s := swarmSandbox(t)
	sw := newSwarm(t, swarmHead(t, s, "a", 90, "x"))

	if _, err := sw.Run(context.Background(), "q", Options{Mode: SwarmMode("sideways")}); err == nil {
		t.Error("an unknown swarm mode fired the heads anyway")
	}
}

func TestRun_NoHeadsIsAnErrorNotAnEmptyResult(t *testing.T) {
	swarmSandbox(t)
	sw := newSwarm(t)

	if res, err := sw.Run(context.Background(), "q", Options{Mode: ModeAll}); err == nil {
		t.Errorf("Run succeeded with no heads: %+v", res)
	}
}

// The pre-flight cost guard exists so a wide fan-out cannot be fired by
// accident. It must refuse *before* spending, not report the overage after.
func TestRun_CostGuardRefusesBeforeFiring(t *testing.T) {
	s := swarmSandbox(t)
	d, err := dispatch.New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	heads := []provider.Head{
		swarmHead(t, s, "a", 90, "x"),
		swarmHead(t, s, "b", 80, "y"),
	}
	sw := New(d, heads, fixedPricing{perCall: 1.0})

	_, err = sw.Run(context.Background(), "q", Options{Mode: ModeAll, MaxEstCostUSD: 0.50})
	if err == nil {
		t.Fatal("the cost guard did not fire")
	}
	if !strings.Contains(err.Error(), "exceeds limit") {
		t.Errorf("error = %v, want it to name the limit", err)
	}
	// Nothing ran, so nothing was logged.
	if rows, lerr := cost.LoadAll(); lerr == nil && len(rows) != 0 {
		t.Errorf("%d cost rows were written despite the guard refusing", len(rows))
	}

	// Under the limit it proceeds.
	if _, err := sw.Run(context.Background(), "q", Options{Mode: ModeAll, MaxEstCostUSD: 10}); err != nil {
		t.Errorf("the guard refused a run inside its limit: %v", err)
	}
}

// Plan is what --dry-run reports. It must name the same heads Run would fire —
// a plan that describes a different run than the one it precedes is worse than
// no plan (#167).
func TestPlan_MatchesWhatRunWouldFire(t *testing.T) {
	s := swarmSandbox(t)
	heads := []provider.Head{
		swarmHead(t, s, "a", 90, "x"),
		swarmHead(t, s, "b", 80, "y"),
	}
	d, err := dispatch.New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sw := New(d, heads, fixedPricing{perCall: 0.25})

	planned, estUSD, err := sw.Plan("q", Options{Mode: ModeAll})
	if err != nil {
		t.Fatal(err)
	}
	if len(planned) != 2 {
		t.Fatalf("Plan named %d heads, want 2", len(planned))
	}
	if estUSD <= 0 {
		t.Error("Plan reported no estimated cost, so --dry-run says a paid fan-out is free")
	}

	// A dry run must not have executed anything.
	if _, err := os.Stat(filepath.Join(config.Dir(), "logs", "cost.jsonl")); err == nil {
		t.Error("Plan wrote cost rows; it is supposed to execute nothing")
	}

	res, err := sw.Run(context.Background(), "q", Options{Mode: ModeAll})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Attempts) != len(planned) {
		t.Errorf("Plan named %d heads but Run fired %d", len(planned), len(res.Attempts))
	}
	for i := range planned {
		if planned[i].ID != res.Attempts[i].Head.ID {
			t.Errorf("Plan named %q at position %d but Run fired %q",
				planned[i].ID, i, res.Attempts[i].Head.ID)
		}
	}
}

func TestPlan_NoHeadsIsAnError(t *testing.T) {
	swarmSandbox(t)
	sw := newSwarm(t)
	if heads, est, err := sw.Plan("q", Options{}); err == nil {
		t.Errorf("Plan succeeded with no heads: %v, $%v", heads, est)
	}
}

// fixedPricing charges the same for every call, so a cost assertion does not
// depend on the live pricing DB.
type fixedPricing struct{ perCall float64 }

func (f fixedPricing) EstimateCost(_, _, _ int) float64 { return f.perCall }

// ── SPRT ──────────────────────────────────────────────────────────────────────

// RunSPRT is `hyctl dispatch --confidence`: it samples heads adaptively and
// stops as soon as the calibrated confidence reaches the target. It is the
// production caller of trust.Run, and nothing exercised it.

func TestRunSPRT_RejectsATargetOutsideZeroToOne(t *testing.T) {
	s := swarmSandbox(t)
	sw := newSwarm(t, swarmHead(t, s, "a", 90, "x"))

	// A confidence of 0 or 1 has no stopping rule: 1 is unreachable and 0 is
	// already met, so both would either never stop or never sample.
	for _, target := range []float64{0, 1, -0.5, 1.5} {
		if res, err := sw.RunSPRT(context.Background(), "q", Options{Confidence: target}); err == nil {
			t.Errorf("RunSPRT accepted confidence %v: %+v", target, res)
		}
	}
}

func TestRunSPRT_NoHeadsIsAnError(t *testing.T) {
	swarmSandbox(t)
	sw := newSwarm(t)

	if res, err := sw.RunSPRT(context.Background(), "q", Options{Confidence: 0.9}); err == nil {
		t.Errorf("RunSPRT succeeded with no heads: %+v", res)
	}
}

// Heads that agree should reach the target and stop; the result must carry the
// attempts actually made, so the user can see what they paid for.
func TestRunSPRT_AgreeingHeadsReachTheTargetAndRecordTheirAttempts(t *testing.T) {
	s := swarmSandbox(t)
	sw := newSwarm(t,
		swarmHead(t, s, "a", 90, "the same answer"),
		swarmHead(t, s, "b", 85, "the same answer"),
		swarmHead(t, s, "c", 80, "the same answer"),
	)

	res, err := sw.RunSPRT(context.Background(), "q", Options{Confidence: 0.75, Domain: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Trust == nil {
		t.Fatal("no trust result; the confidence the user asked for is unreported")
	}
	if len(res.Attempts) == 0 {
		t.Fatal("no attempts recorded; the run's spend is invisible")
	}
	if len(res.Attempts) > 3 {
		t.Errorf("made %d attempts against 3 heads", len(res.Attempts))
	}
	if res.Target != 0.75 {
		t.Errorf("Target = %v, want the requested 0.75", res.Target)
	}
	if res.Domain != "go" {
		t.Errorf("Domain = %q, want the requested one — calibration is per-domain", res.Domain)
	}
	if res.Prompt != "q" {
		t.Errorf("Prompt = %q", res.Prompt)
	}
	for _, a := range res.Attempts {
		if a.Status == StatusOK && a.Output == "" {
			t.Errorf("%s succeeded with no output", a.Head.ID)
		}
		if a.FinishedAt.IsZero() {
			t.Errorf("%s has no finish time", a.Head.ID)
		}
	}
}

// A domain is required for calibration lookup; an empty one must default rather
// than being written to the ledger as the empty-string domain.
func TestRunSPRT_EmptyDomainDefaults(t *testing.T) {
	s := swarmSandbox(t)
	sw := newSwarm(t, swarmHead(t, s, "a", 90, "answer"))

	res, err := sw.RunSPRT(context.Background(), "q", Options{Confidence: 0.6})
	if err != nil {
		t.Fatal(err)
	}
	if res.Domain == "" {
		t.Error("Domain is empty; calibration would be recorded against no domain at all")
	}
}

// Every head failing must be an error rather than a confident verdict drawn
// from no evidence.
func TestRunSPRT_AllHeadsFailingDoesNotProduceAConfidentAnswer(t *testing.T) {
	s := swarmSandbox(t)
	sw := newSwarm(t, brokenHead(s, "a", 90), brokenHead(s, "b", 80))

	res, err := sw.RunSPRT(context.Background(), "q", Options{Confidence: 0.9})
	if err == nil && res != nil && res.Trust != nil && res.Trust.Confidence >= 0.9 {
		t.Errorf("reported %.2f confidence with every head failed", res.Trust.Confidence)
	}
}
