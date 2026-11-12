// SPDX-License-Identifier: MIT

package bench

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/util"
)

// sleepGap is the gap between consecutive calls past which the machine is taken to
// have slept, and the next row's latency is not a measurement.
const sleepGap = time.Minute

// ContractError aborts a run (exit 4): something the harness relies on stopped being
// true mid-run. The bad row is written first, so its spend is recorded.
type ContractError struct {
	Arm, Item, What string
}

func (e *ContractError) Error() string {
	return fmt.Sprintf("bench: contract violated on %s/%s: %s", e.Arm, e.Item, e.What)
}

// Runner executes one suite's run. Now and Out are seams; nothing here reads a real clock.
type Runner struct {
	Run   Run // inputs filled in; ID and MaxUSDHistory are derived
	Suite Suite
	Items map[string]Item // by id, every id Run.ItemIDs names
	// Exec and Sleep build the arms from the run as frozen in run.json, so a resumed
	// run is priced by the snapshot it started with, never by today's prices.
	Exec       func(provider.Head) executor.Executor
	Sleep      func(context.Context, time.Duration) error
	MaxUSD     float64
	Rerun      bool
	ResultsDir string // committed: bench/results
	StateDir   string // not committed: $HYDRA_HOME/bench/runs
	Now        func() time.Time
	Out        io.Writer
}

// Progress is what a finished or stopped run reports.
type Progress struct {
	RunID   string
	Dir     string
	Written int // rows this invocation wrote
	Spent   float64
}

// Execute runs every scheduled pair not already recorded. The same inputs resolve to
// the same run id, so running it again resumes; --rerun starts -r2, -r3, ...
func (r *Runner) Execute(ctx context.Context) (Progress, error) {
	if !(r.MaxUSD > 0) {
		return Progress{}, errors.New("bench: a run needs --max-usd or max_usd above 0")
	}
	base := r.Run.baseID()
	id := base
	if r.Rerun {
		var err error
		if id, err = r.rerunID(base); err != nil {
			return Progress{}, err
		}
	}
	out := Progress{RunID: id, Dir: filepath.Join(r.ResultsDir, id)}
	if err := os.MkdirAll(out.Dir, 0o755); err != nil {
		return out, err
	}
	state := filepath.Join(r.StateDir, id)
	if err := os.MkdirAll(state, 0o755); err != nil {
		return out, err
	}
	lk, err := util.Lock(filepath.Join(state, "run.lock"))
	if err != nil {
		return out, err
	}
	defer lk.Unlock()

	run, err := r.openRun(id, base, filepath.Join(out.Dir, "run.json"))
	if err != nil {
		return out, err
	}
	arms, err := BuildArms(*run, r.Exec, r.Sleep)
	if err != nil {
		return out, err
	}
	for _, name := range run.ArmOrder {
		if err := arms[name].Preflight(ctx); err != nil {
			return out, err
		}
	}
	itemsPath := filepath.Join(out.Dir, "items.jsonl")
	rows, dropped, err := readResults(itemsPath, id)
	if err != nil {
		return out, err
	}
	if dropped > 0 {
		fmt.Fprintf(r.Out, "  resume: dropped a partial line of %d bytes\n", dropped)
	}
	var spent float64
	done := map[pairKey]bool{}
	models := map[string]string{} // arm/head → first reported model
	for _, row := range rows {
		spent += row.CostUSD
		done[pairKey{row.ItemID, row.Arm}] = !row.Retryable
		for _, c := range row.Calls {
			if k := row.Arm + "/" + c.Head; c.Model != "" && models[k] == "" {
				models[k] = c.Model
			}
		}
	}
	budget := NewBudget(r.MaxUSD, spent)
	w, err := openResults(itemsPath, id)
	if err != nil {
		return out, err
	}
	defer w.Close()

	var items []Item
	for _, iid := range run.ItemIDs {
		it, ok := r.Items[iid]
		if !ok {
			return out, fmt.Errorf("bench: run %s schedules %s, which the suite did not load", id, iid)
		}
		items = append(items, it)
	}
	var last time.Time
	for _, p := range schedule(items, run.ArmOrder, run.Seed) {
		if done[pairKey{p.Item.ID, p.Arm}] {
			continue
		}
		row, err := r.one(ctx, arms[p.Arm], p, budget, models, last, w)
		out.Spent = budget.Spent()
		if row == nil {
			return out, err
		}
		last = row.Started.Add(row.Latency)
		if werr := w.write(row); werr != nil {
			return out, werr
		}
		out.Written++
		if err != nil {
			return out, err
		}
	}
	out.Spent = budget.Spent()
	return out, nil
}

// rerunID is the latest -r<k> while it is incomplete, so --rerun resumes it like any
// run; once it is complete, the next k.
func (r *Runner) rerunID(base string) (string, error) {
	k := 2
	for {
		if _, err := os.Stat(filepath.Join(r.ResultsDir, base+"-r"+strconv.Itoa(k+1))); err != nil {
			break
		}
		k++
	}
	id := base + "-r" + strconv.Itoa(k)
	dir := filepath.Join(r.ResultsDir, id)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return id, nil
	}
	done, err := complete(dir)
	if err != nil || !done {
		return id, err
	}
	return base + "-r" + strconv.Itoa(k+1), nil
}

// complete reports whether every scheduled (item, arm) pair of the run in dir has a
// scored row.
func complete(dir string) (bool, error) {
	var run Run
	if err := readJSON(filepath.Join(dir, "run.json"), &run); os.IsNotExist(err) {
		return false, nil // killed before run.json was written: resume it
	} else if err != nil {
		return false, fmt.Errorf("bench: %s: %w", dir, err)
	}
	rows, _, err := parseResults(filepath.Join(dir, "items.jsonl"), run.ID)
	if err != nil {
		return false, err
	}
	scored := map[pairKey]bool{}
	for _, row := range rows {
		if !row.Retryable {
			scored[pairKey{row.ItemID, row.Arm}] = true
		}
	}
	return len(scored) == len(run.ItemIDs)*len(run.ArmOrder), nil
}

// openRun writes run.json before the first call, or reads it back on resume and
// refuses one whose inputs hash differently. A changed cap is appended, not hashed.
func (r *Runner) openRun(id, base, path string) (*Run, error) {
	var run Run
	err := readJSON(path, &run)
	switch {
	case os.IsNotExist(err):
		run = r.Run
		run.ID, run.MaxUSDHistory = id, []float64{r.MaxUSD}
	case err != nil:
		return nil, fmt.Errorf("bench: %s: %w", path, err)
	case run.baseID() != base:
		return nil, fmt.Errorf("bench: %s describes a different run (inputs hash to %s, want %s)", path, run.cfg8(), r.Run.cfg8())
	case len(run.MaxUSDHistory) > 0 && run.MaxUSDHistory[len(run.MaxUSDHistory)-1] == r.MaxUSD:
		return &run, nil
	default:
		run.MaxUSDHistory = append(run.MaxUSDHistory, r.MaxUSD)
	}
	return &run, writeJSON(path, &run)
}

// one makes a single (item, arm) call and returns its result row. Every call that may
// have been billed is already in items.jsonl as its own spend row by then, so the
// result row adds no money, and a nil row with an error has lost none.
func (r *Runner) one(ctx context.Context, arm Arm, p pair, budget *Budget, models map[string]string,
	last time.Time, w *resultsWriter) (*Result, error) {
	req, err := r.Suite.Prompt(p.Item)
	if err != nil {
		return nil, err
	}
	started := r.Now()
	spend := func(c Call) error {
		return w.write(&Result{Seq: p.Seq, ItemID: p.Item.ID, Split: p.Item.Split.String(), Arm: p.Arm,
			Attempt: Attempt{Calls: []Call{c}, ErrKind: spendRow}, CostUSD: c.CostUSD, Started: started, Retryable: true})
	}
	att, err := arm.Call(ctx, p.Item, req, budget, spend)
	if err != nil {
		return nil, err
	}
	att.Latency = r.Now().Sub(started)
	row := &Result{Seq: p.Seq, ItemID: p.Item.ID, Split: p.Item.Split.String(), Arm: p.Arm, Attempt: att, Started: started}
	// Latency is the whole attempt; one that retried or fell back also timed a backoff
	// or a second model, so it is not a provider latency and is kept out of the stats.
	row.LatencyInvalid = (!last.IsZero() && started.Sub(last) > sleepGap) || len(att.Calls) > 1
	if what := contract(p.Arm, att, req.MaxTokens, models); what != "" {
		row.ErrKind, row.Retryable, row.Detail = "contract", true, what
		return row, &ContractError{Arm: p.Arm, Item: p.Item.ID, What: what}
	}
	if att.ErrKind != "" {
		// A transient failure every retry and model shared says nothing about the item.
		row.Retryable = att.transient
		return row, nil
	}
	g, err := r.Suite.Grade(ctx, p.Item, att.Answer)
	if err != nil {
		return nil, err
	}
	row.Passed, row.Format, row.Detail = g.Passed, g.Format, g.Detail
	if g.Passed {
		row.Score = 1
	}
	return row, nil
}

// contract checks what a direct call must never report: estimated tokens, more output
// than was allowed (hidden reasoning is billed as output), or a model that moved.
func contract(arm string, att Attempt, maxTokens int, models map[string]string) string {
	var what []string
	for _, c := range att.Calls {
		if c.TokensEstimated {
			what = append(what, c.Head+" reported estimated tokens")
		}
		if c.OutputTokens > maxTokens {
			what = append(what, fmt.Sprintf("%s billed %d output tokens, over the %d allowed", c.Head, c.OutputTokens, maxTokens))
		}
		k := arm + "/" + c.Head
		switch {
		case c.Model == "":
		case models[k] == "":
			models[k] = c.Model
		case models[k] != c.Model:
			what = append(what, fmt.Sprintf("%s reported model %s, earlier %s", c.Head, c.Model, models[k]))
		}
	}
	return strings.Join(what, "; ")
}
