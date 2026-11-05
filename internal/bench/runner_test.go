// SPDX-License-Identifier: MIT

package bench

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/testutil"
)

var testArmSpecs = map[string][]string{
	"frontier":        {"openrouter:m-front"},
	"cheapest":        {"openrouter:m-cheap"},
	"mid":             {"openrouter:m-mid"},
	"static-fallback": {"openrouter:m-front", "openrouter:m-mid"},
}

// testRunner is n fake items over the named arms, on a sandbox with an OpenRouter key
// so preflight passes, and every call answered by exec.
func testRunner(t *testing.T, n int, exec *fakeExec, arms ...string) *Runner {
	t.Helper()
	sb := testutil.NewSandbox(t)
	sb.SetKey(t, "OPENROUTER_API_KEY", "test-key")
	items := fakeItems(n)
	all := make([]Item, 0, n)
	for _, it := range items {
		all = append(all, it)
	}
	var ids []string
	for _, it := range FirstByRank(all, SplitTest, n) {
		ids = append(ids, it.ID)
	}
	cfgs := map[string]ArmConfig{}
	for _, a := range arms {
		cfgs[a] = ArmConfig{Kind: "direct", Models: testArmSpecs[a]}
	}
	run := Run{Track: "core", Suite: "fake", Tier: "fixture", Seed: 1, Arms: cfgs, ArmOrder: arms, ItemIDs: ids,
		Prices: testPrices("m-front", "m-cheap", "m-mid"),
		Crumb:  Breadcrumb{SplitSHA256: "split", DataSHA256: map[string]string{"f": "h"}}}
	return &Runner{Run: run, Suite: fakeSuite{}, Items: items, Exec: exec.For, Sleep: (&recordSleep{}).sleep,
		MaxUSD: 100, ResultsDir: t.TempDir(), StateDir: t.TempDir(), Now: stepClock(), Out: io.Discard}
}

// rowsOf is the result rows: one per answered pair, plus any retried ones.
func rowsOf(t *testing.T, r *Runner, o Progress) []Result {
	t.Helper()
	var out []Result
	for _, row := range allRowsOf(t, o) {
		if row.ErrKind != spendRow {
			out = append(out, row)
		}
	}
	return out
}

// allRowsOf is every line, spend rows included: where the money is.
func allRowsOf(t *testing.T, o Progress) []Result {
	t.Helper()
	rows, _, err := readResults(filepath.Join(o.Dir, "items.jsonl"), o.RunID)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// spentOn sums the spend rows of one (item, arm).
func spentOn(t *testing.T, o Progress, item, arm string) (sum float64, rows int) {
	t.Helper()
	for _, row := range allRowsOf(t, o) {
		if row.ErrKind == spendRow && row.ItemID == item && row.Arm == arm {
			sum, rows = sum+row.CostUSD, rows+1
		}
	}
	return sum, rows
}

// canonical is the scored rows, without what legitimately differs between an
// interrupted run and an uninterrupted one: when each call started, and spend-only rows.
func canonical(rows []Result) []Result {
	var out []Result
	for _, r := range rows {
		if !r.Retryable {
			r.Started = time.Time{}
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

func TestSchedule_SeedOrdersItemsAndArmsRotate(t *testing.T) {
	var items []Item
	for _, it := range fakeItems(30) {
		items = append(items, it)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	a, b := schedule(items, []string{"A", "B", "C"}, 7), schedule(items, []string{"A", "B", "C"}, 7)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("one seed gave two schedules")
	}
	c := schedule(items, []string{"A", "B", "C"}, 8)
	if reflect.DeepEqual(a, c) {
		t.Fatal("two seeds gave one schedule")
	}
	set := func(ps []pair) []string {
		var s []string
		for _, p := range ps {
			s = append(s, p.Item.ID+p.Arm)
		}
		sort.Strings(s)
		return s
	}
	if !reflect.DeepEqual(set(a), set(c)) {
		t.Fatal("a different seed scheduled a different item set")
	}
	// ABC, BCA, CAB: every arm goes first equally often.
	if a[0].Arm != "A" || a[3].Arm != "B" || a[6].Arm != "C" || a[9].Arm != "A" || a[4].Arm != "C" {
		t.Fatalf("rotation = %v %v %v %v", a[0].Arm, a[3].Arm, a[6].Arm, a[9].Arm)
	}
	for i, p := range a {
		if p.Seq != i {
			t.Fatalf("seq %d at position %d", p.Seq, i)
		}
	}
}

func TestExecute_WritesRunJSONFirstThenOneRowPerPair(t *testing.T) {
	exec := newFakeExec()
	exec.set("bench/openrouter/m-cheap", "q001", reply{answer: "yes"})                         // wrong
	exec.set("bench/openrouter/m-cheap", "q003", reply{answer: "maybe so"})                    // format
	exec.set("bench/openrouter/m-cheap", "q005", reply{err: &executor.StatusError{Code: 400}}) // a final refusal
	r := testRunner(t, 20, exec, "frontier", "cheapest")
	o, err := r.Execute(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(o.RunID, "core-fake-fixture-s1-") || len(o.RunID) != len("core-fake-fixture-s1-")+8 {
		t.Fatalf("run id %q", o.RunID)
	}
	var run Run
	if err := readJSON(filepath.Join(o.Dir, "run.json"), &run); err != nil || run.ID != o.RunID || len(run.ItemIDs) != 20 {
		t.Fatalf("run.json = %+v, %v", run, err)
	}
	rows := rowsOf(t, r, o)
	if len(rows) != 40 || o.Written != 40 || exec.calls.Load() != 40 {
		t.Fatalf("%d rows, %d written, %d calls; want 40 each", len(rows), o.Written, exec.calls.Load())
	}
	by := map[string]Result{}
	for _, row := range rows {
		by[row.Arm+"/"+row.ItemID] = row
	}
	var cost float64
	for _, row := range allRowsOf(t, o) {
		cost += row.CostUSD
	}
	if row := by["cheapest/test/001"]; row.Passed || row.Format || row.Score != 0 {
		t.Errorf("wrong answer row %+v", row)
	}
	if row := by["cheapest/test/003"]; !row.Format {
		t.Errorf("format row %+v", row)
	}
	if row := by["cheapest/test/005"]; row.ErrKind != "status:400" || row.Retryable || row.Passed || row.Calls[0].PriceRef != "reserved" || row.CostUSD != 0 {
		t.Errorf("400 row %+v: want a scored failure, settled at its reservation", row)
	}
	if row := by["frontier/test/002"]; !row.Passed || row.Score != 1 || row.Calls[0].Model != "m-front" || row.Latency != time.Second {
		t.Errorf("pass row %+v", row)
	}
	if d := cost - o.Spent; d > 1e-12 || d < -1e-12 {
		t.Errorf("rows sum to $%v, the budget says $%v", cost, o.Spent)
	}
	// The money is in spend rows, one per billed call; a result row adds none.
	if sum, n := spentOn(t, o, "test/005", "cheapest"); n != 1 || sum == 0 {
		t.Errorf("the 400 was not recorded at its ceiling: $%v in %d rows", sum, n)
	}
	// Running it again resolves to the same run and has nothing left to do.
	again, err := r.Execute(context.Background())
	if err != nil || again.RunID != o.RunID || again.Written != 0 || exec.calls.Load() != 40 {
		t.Fatalf("rerun of a finished run: %+v, %v, %d calls", again, err, exec.calls.Load())
	}
}

// A run killed mid-call and left with a torn last line resumes to exactly the rows,
// and the spend, an uninterrupted run produces (scope E7.1-6).
func TestExecute_KilledRunResumesToTheSameResult(t *testing.T) {
	arms := []string{"frontier", "cheapest", "mid"}
	clean := testRunner(t, 60, newFakeExec(), arms...)
	co, err := clean.Execute(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	exec := newFakeExec()
	r := testRunner(t, 60, exec, arms...)
	ctx, cancel := context.WithCancel(context.Background())
	exec.before = func(head, prompt string) {
		if head == "bench/openrouter/m-mid" && prompt == "q017" {
			cancel()
		}
	}
	o, err := r.Execute(ctx)
	if !errors.Is(err, context.Canceled) || o.Written == 0 || o.Written >= 180 {
		t.Fatalf("killed run: %v after %d rows", err, o.Written)
	}
	// The kill also tore the last write in half.
	path := filepath.Join(o.Dir, "items.jsonl")
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"seq":999,"item_id":"test/0`)
	f.Close()

	exec.before = nil
	var log bytes.Buffer
	r.Out = &log
	o2, err := r.Execute(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log.String(), "dropped a partial line of 28 bytes") {
		t.Fatalf("resume did not report the torn line: %q", log.String())
	}
	got, want := canonical(rowsOf(t, r, o2)), canonical(rowsOf(t, clean, co))
	if len(got) != 180 || !reflect.DeepEqual(got, want) {
		t.Fatalf("resumed rows differ from an uninterrupted run (%d vs %d rows)", len(got), len(want))
	}
	// The call the kill interrupted may have been billed: it is kept, at its ceiling,
	// and the pair it belonged to was asked again.
	if sum, n := spentOn(t, o2, "test/017", "mid"); n != 2 || sum != 0.004 {
		t.Fatalf("test/017 on mid: $%v in %d spend rows, want the killed call and its retry", sum, n)
	}
	for _, row := range rowsOf(t, r, o2) {
		if row.Retryable {
			t.Fatalf("a killed call left a result row: %+v", row)
		}
	}
	if d := o2.Spent - (co.Spent + 0.002); d > 1e-12 || d < -1e-12 {
		t.Fatalf("resumed run spent $%v, uninterrupted $%v plus the interrupted call", o2.Spent, co.Spent)
	}
}

// A kill after a billed call never loses its spend, so across any number of resumes
// the recorded spend covers every call made and stays under --max-usd.
func TestExecute_KilledAfterABilledCallStaysUnderTheCap(t *testing.T) {
	exec := newFakeExec()
	r := testRunner(t, 10, exec, "frontier")
	r.MaxUSD = 6*0.002 + 0.001
	for _, kill := range []string{"q002", "q007"} {
		ctx, cancel := context.WithCancel(context.Background())
		exec.before = func(_, prompt string) {
			if prompt == kill {
				cancel()
			}
		}
		r.Execute(ctx)
		cancel()
	}
	exec.before = nil
	o, err := r.Execute(context.Background())
	if !errors.Is(err, ErrBudget) {
		t.Fatalf("the cap should stop the run: %v", err)
	}
	var recorded float64
	for _, row := range allRowsOf(t, o) {
		recorded += row.CostUSD
	}
	billed := float64(exec.calls.Load()) * 0.002
	if recorded < billed-1e-12 || recorded > r.MaxUSD || billed > r.MaxUSD {
		t.Fatalf("recorded $%v for $%v billed in %d calls under a $%v cap", recorded, billed, exec.calls.Load(), r.MaxUSD)
	}
}

// A transient failure that outlives every retry is unscored: its row keeps its spend,
// is not a result, and the next invocation asks the pair again.
func TestExecute_TransientFailureIsUnscoredAndAskedAgain(t *testing.T) {
	exec := newFakeExec()
	exec.set("bench/openrouter/m-front", "q003", reply{err: &executor.StatusError{Code: 503}})
	r := testRunner(t, 6, exec, "frontier")
	o, err := r.Execute(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var row Result
	for _, x := range rowsOf(t, r, o) {
		if x.ItemID == "test/003" {
			row = x
		}
	}
	if !row.Retryable || row.ErrKind != "status:503" || row.Passed || row.CostUSD != 0 || len(row.Calls) != 1+len(retryBackoff) {
		t.Fatalf("503 row %+v: want unscored, free, after every retry", row)
	}
	exec.set("bench/openrouter/m-front", "q003", reply{answer: "no"})
	o2, err := r.Execute(context.Background())
	if err != nil || o2.Written != 1 {
		t.Fatalf("resume re-asked %d pairs (%v), want the one unscored", o2.Written, err)
	}
}

// gradeFails is fakeSuite with a grader that breaks on one item.
type gradeFails struct {
	fakeSuite
	id string
}

func (g gradeFails) Grade(ctx context.Context, it Item, answer string) (Grade, error) {
	if it.ID == g.id {
		return Grade{}, errors.New("grader broke")
	}
	return g.fakeSuite.Grade(ctx, it, answer)
}

func TestExecute_AGradeErrorKeepsTheBilledCall(t *testing.T) {
	r := testRunner(t, 4, newFakeExec(), "frontier")
	r.Suite = gradeFails{id: "test/002"}
	o, err := r.Execute(context.Background())
	if err == nil || !strings.Contains(err.Error(), "grader broke") {
		t.Fatalf("err = %v", err)
	}
	// The grade failed after the call was billed: its spend row is there, no result is.
	if sum, n := spentOn(t, o, "test/002", "frontier"); n != 1 || sum != 0.002 {
		t.Fatalf("test/002: $%v in %d spend rows, want the billed call", sum, n)
	}
	for _, row := range rowsOf(t, r, o) {
		if row.ItemID == "test/002" {
			t.Fatalf("a result row for an ungraded answer: %+v", row)
		}
	}
	r.Suite = fakeSuite{}
	if o2, err := r.Execute(context.Background()); err != nil || o2.Written == 0 {
		t.Fatalf("resume: %+v %v", o2, err)
	}
}

// Prices are frozen into run.json on the first start: a pricing-cache change resumes
// the same run, priced as it began.
func TestExecute_APriceChangeResumesTheSameFrozenRun(t *testing.T) {
	exec := newFakeExec()
	r := testRunner(t, 6, exec, "frontier")
	r.MaxUSD = 3*0.002 + 0.001
	o, _ := r.Execute(context.Background())
	p := r.Run.Prices["m-front"]
	p.InPerM *= 10
	r.Run.Prices["m-front"] = p
	r.MaxUSD = 1
	o2, err := r.Execute(context.Background())
	if err != nil || o2.RunID != o.RunID || o2.Written != 3 {
		t.Fatalf("resume after a price change: %+v %v", o2, err)
	}
	for _, row := range allRowsOf(t, o2) {
		if row.ErrKind == spendRow && row.CostUSD != 0.002 {
			t.Fatalf("call priced at today's price, $%v", row.CostUSD)
		}
	}
	var run Run
	readJSON(filepath.Join(o.Dir, "run.json"), &run)
	if run.Prices["m-front"].InPerM != 1000 {
		t.Fatalf("run.json prices moved: %+v", run.Prices["m-front"])
	}
}

// --max-usd stops before the call that could cross it; a higher cap resumes the same
// run to the uninterrupted total (scope E7.1-7).
func TestExecute_MaxUSDStopsBeforeTheCallAndResumes(t *testing.T) {
	exec := newFakeExec()
	r := testRunner(t, 10, exec, "frontier")
	r.MaxUSD = 7*0.002 + 0.001 // each call is exactly $0.002
	o, err := r.Execute(context.Background())
	if !errors.Is(err, ErrBudget) || exec.calls.Load() != 7 || o.Written != 7 {
		t.Fatalf("err=%v after %d calls, %d rows; want ErrBudget after 7", err, exec.calls.Load(), o.Written)
	}
	r.MaxUSD = 1
	o2, err := r.Execute(context.Background())
	if err != nil || o2.RunID != o.RunID || o2.Written != 3 || exec.calls.Load() != 10 {
		t.Fatalf("resume: %+v %v after %d calls", o2, err, exec.calls.Load())
	}
	if d := o2.Spent - 10*0.002; d > 1e-12 || d < -1e-12 {
		t.Fatalf("total $%v, want $0.020", o2.Spent)
	}
	var run Run
	readJSON(filepath.Join(o.Dir, "run.json"), &run)
	if !reflect.DeepEqual(run.MaxUSDHistory, []float64{0.015, 1}) {
		t.Fatalf("cap history %v", run.MaxUSDHistory)
	}
	// The starting balance on resume is what the file already spent.
	r.MaxUSD = 0.0005
	exec.set("bench/openrouter/m-front", "*", reply{answer: "yes"})
	if _, err := r.Execute(context.Background()); err != nil {
		t.Fatalf("a finished run should need no budget: %v", err)
	}
	r.MaxUSD = 0
	if _, err := r.Execute(context.Background()); err == nil {
		t.Fatal("a run with no cap started")
	}
}

func TestExecute_ContractViolationsWriteTheRowAndAbort(t *testing.T) {
	for name, rep := range map[string]reply{
		"estimated": {answer: "yes", estimated: true},
		"reasoning": {answer: "yes", out: 33},
	} {
		t.Run(name, func(t *testing.T) {
			exec := newFakeExec()
			r := testRunner(t, 6, exec, "frontier")
			exec.set("bench/openrouter/m-front", "q004", rep)
			o, err := r.Execute(context.Background())
			var ce *ContractError
			if !errors.As(err, &ce) || ce.Item != "test/004" {
				t.Fatalf("err = %v, want a contract violation on test/004", err)
			}
			rows := rowsOf(t, r, o)
			last := rows[len(rows)-1]
			if last.ItemID != "test/004" || last.ErrKind != "contract" || !last.Retryable {
				t.Fatalf("contract row %+v: want it written and retryable", last)
			}
			if sum, _ := spentOn(t, o, "test/004", "frontier"); sum == 0 {
				t.Fatal("the call that broke the contract was not recorded as spend")
			}
			// Fixed, the run resumes and retries the pair rather than counting it done.
			exec.set("bench/openrouter/m-front", "q004", reply{answer: "yes"})
			o2, err := r.Execute(context.Background())
			if err != nil || len(rowsOf(t, r, o2)) != 7 {
				t.Fatalf("resume after the fix: %v, %d rows; want 6 pairs plus the retried one", err, len(rowsOf(t, r, o2)))
			}
		})
	}
}

// The model a head first reported is read back from the rows, so a model that moves
// between two invocations still aborts the run.
func TestExecute_AModelThatMovesAbortsEvenAcrossAResume(t *testing.T) {
	exec := newFakeExec()
	r := testRunner(t, 6, exec, "frontier")
	r.MaxUSD = 3*0.002 + 0.001
	if _, err := r.Execute(context.Background()); !errors.Is(err, ErrBudget) {
		t.Fatalf("first leg: %v", err)
	}
	exec.set("bench/openrouter/m-front", "*", reply{answer: "yes", model: "m-front-2"})
	r.MaxUSD = 1
	var ce *ContractError
	if _, err := r.Execute(context.Background()); !errors.As(err, &ce) || !strings.Contains(ce.What, "earlier m-front") {
		t.Fatalf("a moved model: %v", err)
	}
}

func TestExecute_RefusesCorruptionAndAChangedRun(t *testing.T) {
	r := testRunner(t, 4, newFakeExec(), "frontier")
	o, err := r.Execute(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(o.Dir, "items.jsonl")
	good, _ := os.ReadFile(path)
	lines := strings.SplitAfter(string(good), "\n")

	os.WriteFile(path, []byte(lines[0]+"{not json}\n"+strings.Join(lines[1:], "")), 0o644)
	if _, err := r.Execute(context.Background()); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("a malformed middle line: %v", err)
	}
	os.WriteFile(path, []byte(string(good)+lines[1]), 0o644)
	if _, err := r.Execute(context.Background()); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("a duplicate pair: %v", err)
	}
	os.WriteFile(path, []byte(`{"schema":"other"}`+"\n"), 0o644)
	if _, err := r.Execute(context.Background()); err == nil {
		t.Fatal("a foreign header was accepted")
	}
	os.WriteFile(path, good, 0o644)
	// A run.json whose inputs hash differently is another run, not this one.
	var run Run
	readJSON(filepath.Join(o.Dir, "run.json"), &run)
	run.ItemIDs = run.ItemIDs[:2]
	writeJSON(filepath.Join(o.Dir, "run.json"), &run)
	if _, err := r.Execute(context.Background()); err == nil || !strings.Contains(err.Error(), "different run") {
		t.Fatalf("a changed run.json: %v", err)
	}
	os.WriteFile(filepath.Join(o.Dir, "run.json"), []byte("{"), 0o644)
	if _, err := r.Execute(context.Background()); err == nil {
		t.Fatal("an unreadable run.json was accepted")
	}
}

func TestExecute_RerunStartsAFreshNumberedRun(t *testing.T) {
	r := testRunner(t, 3, newFakeExec(), "frontier")
	o, _ := r.Execute(context.Background())
	r.Rerun = true
	o2, err := r.Execute(context.Background())
	if err != nil || o2.RunID != o.RunID+"-r2" || o2.Written != 3 {
		t.Fatalf("rerun = %+v, %v", o2, err)
	}
	o3, _ := r.Execute(context.Background())
	if o3.RunID != o.RunID+"-r3" {
		t.Fatalf("second rerun = %s", o3.RunID)
	}
}

func TestExecute_ASleepGapMarksLatencyInvalid(t *testing.T) {
	r := testRunner(t, 3, newFakeExec(), "frontier")
	tick := stepClock()
	n := 0
	r.Now = func() time.Time {
		n++
		if n == 3 { // the second call starts after the machine slept
			return tick().Add(2 * time.Hour)
		}
		return tick()
	}
	o, err := r.Execute(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	invalid := 0
	for _, row := range rowsOf(t, r, o) {
		if row.LatencyInvalid {
			invalid++
		}
	}
	if invalid != 1 {
		t.Fatalf("%d rows marked, want the one after the gap", invalid)
	}
}

func TestExecute_PreflightAndPlanningFailuresSpendNothing(t *testing.T) {
	exec := newFakeExec()
	r := testRunner(t, 3, exec, "frontier")
	t.Setenv("OPENROUTER_API_KEY", "")
	if _, err := r.Execute(context.Background()); err == nil || !strings.Contains(err.Error(), "cannot reach") {
		t.Fatalf("preflight without a key: %v", err)
	}
	t.Setenv("OPENROUTER_API_KEY", "k")
	r.Run.ItemIDs = append(r.Run.ItemIDs, "test/999")
	if _, err := r.Execute(context.Background()); err == nil || !strings.Contains(err.Error(), "did not load") {
		t.Fatalf("an unloaded item: %v", err)
	}
	if exec.calls.Load() != 0 {
		t.Fatalf("%d calls made", exec.calls.Load())
	}
}

func TestExecute_AuthFailureStopsTheRunWithoutARow(t *testing.T) {
	exec := newFakeExec()
	exec.set("bench/openrouter/m-front", "*", reply{err: &executor.StatusError{Code: 403}})
	r := testRunner(t, 3, exec, "frontier")
	o, err := r.Execute(context.Background())
	if err == nil || !strings.Contains(err.Error(), "refused the credentials") || o.Written != 0 || exec.calls.Load() != 1 {
		t.Fatalf("403: %v after %d rows and %d calls; want one call, no row", err, o.Written, exec.calls.Load())
	}
}

// A row whose attempt retried or fell back timed a backoff or a second model, not a
// provider: its latency is marked invalid and kept out of the stats.
func TestExecute_ARetriedAttemptHasNoValidLatency(t *testing.T) {
	r := testRunner(t, 4, newFakeExec(), "static-fallback")
	r.Run.Arms["static-fallback"] = ArmConfig{Kind: "direct", Models: testArmSpecs["static-fallback"], FaultRate: 1}
	o, err := r.Execute(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rowsOf(t, r, o) {
		if !row.LatencyInvalid || len(row.Calls) != 2 {
			t.Fatalf("fell back but latency counted: %+v", row)
		}
	}
	s, _ := Report(o.Dir, r.StateDir)
	if lat := s.Arms["static-fallback"].LatencyMS; lat.Valid != 0 || lat.Skipped != 4 {
		t.Fatalf("latency %+v, want every fallen-back row skipped", lat)
	}
}

// A hard kill inside the retry loop, after a billed attempt and before anything else
// runs, still leaves that attempt in items.jsonl, so the resumed budget covers it.
func TestExecute_AHardKillMidRetryKeepsTheBilledAttempt(t *testing.T) {
	exec := newFakeExec()
	exec.set("bench/openrouter/m-front", "q001", reply{err: &executor.StatusError{Code: 500}})
	r := testRunner(t, 3, exec, "frontier")
	r.Sleep = func(context.Context, time.Duration) error {
		runtime.Goexit() // the process dies: no error path, no further write
		return nil
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Execute(context.Background())
	}()
	<-done
	billed := exec.calls.Load()
	var recorded float64
	for _, row := range allRowsOf(t, Progress{RunID: r.Run.baseID(), Dir: filepath.Join(r.ResultsDir, r.Run.baseID())}) {
		recorded += row.CostUSD
	}
	if billed == 0 || recorded < float64(billed)*0.002-1e-12 {
		t.Fatalf("%d calls billed at $0.002, $%v recorded before the kill", billed, recorded)
	}
	r.Sleep = (&recordSleep{}).sleep
	o, _ := r.Execute(context.Background())
	if o.Spent < float64(exec.calls.Load())*0.002-1e-12 {
		t.Fatalf("resumed budget $%v under $%v billed", o.Spent, float64(exec.calls.Load())*0.002)
	}
}

// --rerun resumes the latest rerun while it is incomplete, and starts the next only
// once it is done.
func TestExecute_RerunResumesAnIncompleteRerun(t *testing.T) {
	r := testRunner(t, 6, newFakeExec(), "frontier")
	if _, err := r.Execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.Rerun, r.MaxUSD = true, 3*0.002+0.001
	o, err := r.Execute(context.Background())
	if !errors.Is(err, ErrBudget) || !strings.HasSuffix(o.RunID, "-r2") {
		t.Fatalf("first rerun: %s %v", o.RunID, err)
	}
	r.MaxUSD = 1
	o2, err := r.Execute(context.Background())
	if err != nil || o2.RunID != o.RunID || o2.Written != 3 {
		t.Fatalf("--rerun on an incomplete -r2 started %s (%v, %d rows)", o2.RunID, err, o2.Written)
	}
	o3, err := r.Execute(context.Background())
	if err != nil || !strings.HasSuffix(o3.RunID, "-r3") || o3.Written != 6 {
		t.Fatalf("--rerun after a complete -r2: %s %v", o3.RunID, err)
	}
}

// A rerun whose run.json is unreadable is reported, not passed over for a new -r<k>.
func TestExecute_RerunReportsACorruptRerun(t *testing.T) {
	r := testRunner(t, 4, newFakeExec(), "frontier")
	r.Rerun, r.MaxUSD = true, 2*0.002+0.001
	o, err := r.Execute(context.Background())
	if !errors.Is(err, ErrBudget) {
		t.Fatalf("first rerun: %v", err)
	}
	os.WriteFile(filepath.Join(o.Dir, "run.json"), []byte("{"), 0o644)
	if done, err := complete(o.Dir); err == nil || done {
		t.Fatalf("complete() read a corrupt run.json as done=%v, err=%v", done, err)
	}
	r.MaxUSD = 1
	if o2, err := r.Execute(context.Background()); err == nil || !strings.Contains(err.Error(), o.RunID) {
		t.Fatalf("a corrupt %s was passed over for %s (%v)", o.RunID, o2.RunID, err)
	}
	// A rerun killed before it wrote run.json at all is still resumed.
	os.Remove(filepath.Join(o.Dir, "run.json"))
	if o3, err := r.Execute(context.Background()); err == nil && o3.RunID != o.RunID {
		t.Fatalf("a rerun with no run.json yet was passed over for %s", o3.RunID)
	}
}
