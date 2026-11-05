// SPDX-License-Identifier: MIT

package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/stats"
)

// withoutExecution is summary.json with its execution block, the one part allowed to
// differ between an interrupted run and an uninterrupted one, removed.
func withoutExecution(t *testing.T, dir string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "execution")
	out, _ := json.Marshal(m)
	return out
}

// A run killed mid-call, torn mid-write and resumed reports a summary byte-identical,
// execution aside, to a run that was never interrupted (scope E7.1-6).
func TestReport_AResumedRunSummarisesByteIdentically(t *testing.T) {
	arms := []string{"frontier", "cheapest", "mid"}
	// Enough discordance, on both sides, that a bootstrap over rows in another order
	// lands on other quantiles: order is part of what must be identical.
	script := func(e *fakeExec) {
		for i := 0; i < 60; i++ {
			answer := map[bool]string{true: "yes", false: "no"}[i%2 == 0]
			if i%4 == 3 || i%7 == 0 {
				answer = map[bool]string{true: "no", false: "yes"}[i%2 == 0]
			}
			e.set("bench/openrouter/m-cheap", fmt.Sprintf("q%03d", i), reply{answer: answer, out: i % 13})
		}
		for _, q := range []string{"q002", "q005", "q010", "q044"} {
			e.set("bench/openrouter/m-front", q, reply{answer: "maybe"})
		}
	}
	// Output is priced on the cheap model, so its cost varies by item.
	priced := func(r *Runner) {
		r.Run.Prices["m-cheap"] = Price{Model: "m-cheap", Ref: "ref/m-cheap", InPerM: 1000, OutPerM: 500, Source: "openrouter"}
	}
	cleanExec := newFakeExec()
	script(cleanExec)
	clean := testRunner(t, 60, cleanExec, arms...)
	priced(clean)
	co, err := clean.Execute(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	exec := newFakeExec()
	script(exec)
	r := testRunner(t, 60, exec, arms...)
	priced(r)
	ctx, cancel := context.WithCancel(context.Background())
	exec.before = func(head, prompt string) {
		if head == "bench/openrouter/m-cheap" && prompt == "q041" {
			cancel()
		}
	}
	o, _ := r.Execute(ctx)
	path := filepath.Join(o.Dir, "items.jsonl")
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"seq":3`)
	f.Close()
	// Reporting a killed run reads it as it stands, incomplete, and changes nothing.
	before, _ := os.ReadFile(path)
	s, err := Report(o.Dir, r.StateDir)
	if err != nil || !s.Incomplete || s.Execution.TornBytes != 8 || len(s.Paired) != 0 {
		t.Fatalf("report of a torn run: %+v, %v", s, err)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Fatal("report changed items.jsonl")
	}
	exec.before = nil
	if _, err := r.Execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := Report(co.Dir, clean.StateDir); err != nil {
		t.Fatal(err)
	}
	if _, err := Report(o.Dir, r.StateDir); err != nil {
		t.Fatal(err)
	}
	if a, b := withoutExecution(t, co.Dir), withoutExecution(t, o.Dir); !bytes.Equal(a, b) {
		t.Fatalf("summaries differ:\n%s\n%s", a, b)
	}
	// Reporting the same files twice is byte-identical, execution included.
	first, _ := os.ReadFile(filepath.Join(o.Dir, "summary.json"))
	Report(o.Dir, r.StateDir)
	second, _ := os.ReadFile(filepath.Join(o.Dir, "summary.json"))
	if !bytes.Equal(first, second) {
		t.Fatal("report twice gave two summaries")
	}
}

// report waits for run.lock, so one started while the run appends sees every row the
// run writes and loses none of them.
func TestReport_ConcurrentWithARunLosesNoRow(t *testing.T) {
	exec := newFakeExec()
	r := testRunner(t, 30, exec, "frontier", "cheapest")
	started := make(chan struct{})
	var once sync.Once
	exec.before = func(string, string) {
		once.Do(func() { close(started) })
		time.Sleep(time.Millisecond)
	}
	done := make(chan Progress)
	go func() {
		o, _ := r.Execute(context.Background())
		done <- o
	}()
	<-started
	dir := filepath.Join(r.ResultsDir, r.Run.baseID())
	reported := make(chan Summary)
	go func() {
		s, err := Report(dir, r.StateDir)
		if err != nil {
			t.Error(err)
		}
		reported <- s
	}()
	o := <-done
	s := <-reported
	if o.Written != 60 || len(rowsOf(t, r, o)) != 60 || s.Incomplete || s.Arms["frontier"].N != 30 {
		t.Fatalf("wrote %d, file has %d, report saw incomplete=%v n=%d", o.Written, len(rowsOf(t, r, o)), s.Incomplete, s.Arms["frontier"].N)
	}
}

func summarize(t *testing.T, n int, exec *fakeExec, tier string, arms ...string) Summary {
	t.Helper()
	r := testRunner(t, n, exec, arms...)
	r.Run.Tier, r.Run.TestSplitSize = tier, 5000
	o, err := r.Execute(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s, err := Report(o.Dir, r.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSummarize_ArmsPairsAndTheOracle(t *testing.T) {
	exec := newFakeExec()
	// cheapest: right where frontier is wrong on q000, wrong on q001..q004, a format fail on q005.
	exec.set("bench/openrouter/m-front", "q000", reply{answer: "no"})
	for _, q := range []string{"q001", "q002", "q003", "q004"} {
		exec.set("bench/openrouter/m-cheap", q, reply{answer: map[bool]string{true: "no", false: "yes"}[q == "q002" || q == "q004"]})
	}
	exec.set("bench/openrouter/m-cheap", "q005", reply{answer: "perhaps"})
	s := summarize(t, 40, exec, "fixture", "frontier", "cheapest")
	f, c, o := s.Arms["frontier"], s.Arms["cheapest"], s.Arms[OracleArm]
	if f.N != 40 || f.Passed != 39 || c.Passed != 35 || c.FormatFails != 1 || c.Accepted != 40 {
		t.Fatalf("frontier %+v cheapest %+v", f, c)
	}
	if o.Passed != 40 || o.PassRate < f.PassRate || o.PassRate < c.PassRate {
		t.Fatalf("oracle %+v is below an arm it is the best of", o)
	}
	ps := s.Paired["cheapest-vs-frontier"]
	want := stats.Discordant{N: 40, B: 1, C: 5}
	if ps.Discordant != want || ps.PDisc != 6.0/40 || ps.DeltaPass != -4.0/40 {
		t.Fatalf("paired %+v, want %+v", ps, want)
	}
	if ps.BootLower95 == nil || *ps.BootLower95 > ps.DeltaPass || ps.TangoZ == nil || ps.CostPerAccDiff == nil {
		t.Fatalf("intervals missing: %+v", ps)
	}
	if ps.CostPerAccDiff.Estimate != 0 || ps.McNemarP2 <= 0 {
		t.Fatalf("both arms cost the same per call, so the cost difference is 0: %+v", ps.CostPerAccDiff)
	}
	if *f.CostPerAccepted != f.CostUSD/40 || f.TokensIn.Mean != 2 || f.LatencyMS.Valid != 40 || f.LatencyMS.P50 == nil {
		t.Fatalf("frontier detail %+v", f)
	}
	if got := s.Breadcrumb.ProviderModels["cheapest"]; len(got) != 1 || got[0] != "m-cheap" {
		t.Fatalf("provider models %v", s.Breadcrumb.ProviderModels)
	}
	if len(s.Caveats) == 0 || s.Smoke != nil || s.Incomplete {
		t.Fatalf("a complete fixture run carries caveats only: %+v %+v", s.Caveats, s.Smoke)
	}
}

// An interval the data cannot support is refused, never reported as 0: here the
// cheapest arm was refused on every item, so it accepted nothing.
func TestSummarize_RefusesAnIntervalNoResampleDefines(t *testing.T) {
	exec := newFakeExec()
	exec.set("bench/openrouter/m-cheap", "*", reply{err: &executor.StatusError{Code: 400}})
	s := summarize(t, 8, exec, "fixture", "frontier", "cheapest")
	ps := s.Paired["cheapest-vs-frontier"]
	if ps.CostPerAccDiff != nil || len(ps.Refused) == 0 || s.Arms["cheapest"].CostPerAccepted != nil {
		t.Fatalf("cost interval reported for an arm that accepted nothing: %+v", ps)
	}
	if s.Arms["cheapest"].Errors != 8 || s.Arms["cheapest"].LatencyMS.Skipped != 8 {
		t.Fatalf("cheapest %+v", s.Arms["cheapest"])
	}
	if got := paired(nil, nil, 1); len(got.Refused) == 0 || got.BootLower95 != nil {
		t.Fatalf("no shared items gave %+v", got)
	}
	// All discordance on one side leaves Tango's bound to report; a single item cannot.
	one := paired([]Result{{ItemID: "a", Passed: true}}, []Result{{ItemID: "a", Passed: true}}, 1)
	if one.TangoZ == nil {
		t.Fatalf("one concordant item: %+v", one)
	}
}

// A retryable row is spend, not a result: the pair is scored by its last scored row,
// the arm's cost counts scored rows, and every dollar is still in the execution block.
func TestSummarize_ScoresTheLastScoredRowAndKeepsAllSpend(t *testing.T) {
	run := &Run{ID: "r", ItemIDs: []string{"a", "b"}, ArmOrder: []string{Frontier}, Seed: 1}
	rows := []Result{
		{ItemID: "a", Arm: Frontier, Attempt: Attempt{ErrKind: "contract"}, Retryable: true, CostUSD: 0.5},
		{ItemID: "b", Arm: Frontier, Passed: true, CostUSD: 0.25},
		{ItemID: "a", Arm: Frontier, Passed: true, CostUSD: 0.25},
		{ItemID: "a", Arm: Frontier, Attempt: Attempt{ErrKind: "status:503"}, Retryable: true},
	}
	s, err := Summarize(run, rows)
	if err != nil {
		t.Fatal(err)
	}
	if a := s.Arms[Frontier]; a.N != 2 || a.Passed != 2 || a.Errors != 0 || a.CostUSD != 0.5 || s.Incomplete {
		t.Fatalf("frontier %+v, want 2 scored passes costing their own rows", a)
	}
	if s.Execution.SpendUSD != 1 || s.Execution.SpendRows != 2 {
		t.Fatalf("execution %+v, want every dollar and both spend-only rows", s.Execution)
	}
	if _, ok := s.Arms[OracleArm]; ok {
		t.Fatal("an oracle over one arm is that arm")
	}
	if _, err := Summarize(run, []Result{{ItemID: "z", Arm: Frontier}}); err == nil {
		t.Fatal("a row for an unscheduled item was summarised")
	}
}

// A run missing any scored pair is incomplete: counts, no paired statistics, no
// oracle, no smoke sizing, and the table says so.
func TestSummarize_AnIncompleteRunReportsCountsOnly(t *testing.T) {
	exec := newFakeExec()
	exec.set("bench/openrouter/m-cheap", "q004", reply{err: errors.New("timeout")})
	s := summarize(t, 10, exec, "smoke", "frontier", "cheapest")
	if !s.Incomplete || s.Unscored != 1 || len(s.Paired) != 0 || s.Smoke != nil {
		t.Fatalf("incomplete=%v unscored=%d paired=%d smoke=%v", s.Incomplete, s.Unscored, len(s.Paired), s.Smoke)
	}
	if _, ok := s.Arms[OracleArm]; ok || s.Arms["cheapest"].N != 9 || s.Arms[Frontier].N != 10 {
		t.Fatalf("arms %+v", s.Arms)
	}
	if !strings.Contains(Table([]Summary{s}), "INCOMPLETE (1 unscored)") {
		t.Fatal("the table did not mark an incomplete run")
	}
}

func TestSummarize_SmokeSizesTheFullTier(t *testing.T) {
	exec := newFakeExec()
	for i := 0; i < 10; i++ {
		exec.set("bench/openrouter/m-cheap", "q00"+string(rune('0'+i)), reply{answer: "maybe"})
	}
	s := summarize(t, 40, exec, "smoke", "frontier", "cheapest")
	m := s.Smoke
	if m == nil || m.PDisc["cheapest-vs-frontier"] != 0.25 || m.FormatFailRate["cheapest"] != 0.25 {
		t.Fatalf("smoke %+v", m)
	}
	// ⌈6.18 · 0.25 / 0.0004⌉, from cheapest; the oracle's own p_disc is not a candidate.
	if m.RecommendedN == nil || *m.RecommendedN != 3863 {
		t.Fatalf("recommended n %v, want 3863", m.RecommendedN)
	}
	if _, ok := m.TokensIn[OracleArm]; ok {
		t.Fatal("the computed oracle has no tokens of its own")
	}
	withFrontier := Summary{Arms: map[string]ArmSummary{Frontier: {}}, Paired: map[string]PairedStats{
		"oracle-vs-frontier": {PDisc: 0.5}, "cheap-vs-frontier": {PDisc: 0.01}}}
	if n := smoke(withFrontier, 5000).RecommendedN; n == nil || *n != 1000 {
		t.Fatalf("the oracle's p_disc sized the run: %v", n)
	}
	withFrontier.Paired["cheap-vs-frontier"] = PairedStats{PDisc: 0.25}
	if n := smoke(withFrontier, 700).RecommendedN; *n != 700 {
		t.Fatal("the recommendation exceeded the test split")
	}
	none := smoke(Summary{Arms: map[string]ArmSummary{"cheapest": {N: 3}}}, 5000)
	if none.RecommendedN != nil || none.RecommendedNRefused == "" || none.FormatFailRate["cheapest"] != 0 {
		t.Fatalf("no frontier arm ran, yet: %+v", none)
	}
}

func TestReport_RefusesWhatItCannotRead(t *testing.T) {
	state := t.TempDir()
	if _, err := Report(t.TempDir(), state); err == nil {
		t.Fatal("reported a directory with no run.json")
	}
	dir := t.TempDir()
	writeJSON(filepath.Join(dir, "run.json"), &Run{ID: "x"})
	os.WriteFile(filepath.Join(dir, "items.jsonl"), []byte(`{"schema":"other","run_id":"x"}`+"\n"), 0o644)
	if _, err := Report(dir, state); err == nil {
		t.Fatal("reported an unknown items schema")
	}
	writeJSON(filepath.Join(dir, "s.json"), &Summary{Schema: "hydra.bench.summary/0"})
	if _, err := ReadSummary(filepath.Join(dir, "s.json")); err == nil {
		t.Fatal("read an unknown summary schema")
	}
	if _, err := ReadSummary(filepath.Join(dir, "none.json")); err == nil {
		t.Fatal("read a missing summary")
	}
	os.WriteFile(filepath.Join(dir, "items.jsonl"), []byte(`{"schema":"hydra.bench.items/1","run_id":"x"}`+"\n"+
		`{"item_id":"nope","arm":"frontier","started":"`+time.Now().Format(time.RFC3339)+`"}`+"\n"), 0o644)
	if _, err := Report(dir, state); err == nil {
		t.Fatal("reported a row for an unscheduled item")
	}
}
