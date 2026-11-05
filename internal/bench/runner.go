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
	Run        Run // inputs filled in; ID and MaxUSDHistory are derived
	Suite      Suite
	Items      map[string]Item // by id, every id Run.ItemIDs names
	Arms       map[string]Arm
	MaxUSD     float64
	Rerun      bool
	ResultsDir string // committed: bench/results
	StateDir   string // not committed: $HYDRA_HOME/bench/runs
	Now        func() time.Time
	Out        io.Writer
}

// Outcome is what a finished or stopped run reports.
type Outcome struct {
	RunID   string
	Dir     string
	Written int // rows this invocation wrote
	Spent   float64
}

// Execute runs every scheduled pair not already recorded. The same inputs resolve to
// the same run id, so running it again resumes; --rerun starts -r2, -r3, ...
func (r *Runner) Execute(ctx context.Context) (Outcome, error) {
	if !(r.MaxUSD > 0) {
		return Outcome{}, errors.New("bench: a run needs --max-usd or max_usd above 0")
	}
	for _, name := range r.Run.ArmOrder {
		if err := r.Arms[name].Preflight(ctx); err != nil {
			return Outcome{}, err
		}
	}
	base := r.Run.baseID()
	id := base
	if r.Rerun {
		for k := 2; ; k++ {
			id = base + "-r" + strconv.Itoa(k)
			if _, err := os.Stat(filepath.Join(r.ResultsDir, id)); os.IsNotExist(err) {
				break
			}
		}
	}
	out := Outcome{RunID: id, Dir: filepath.Join(r.ResultsDir, id)}
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
		row, err := r.one(ctx, run, p, budget, models, last)
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

// one makes a single (item, arm) call. A nil row means nothing was spent or nothing
// can be recorded; a row with an error is written and then stops the run.
func (r *Runner) one(ctx context.Context, run *Run, p pair, budget *Budget, models map[string]string, last time.Time) (*Result, error) {
	arm := r.Arms[p.Arm]
	req, err := r.Suite.Prompt(p.Item)
	if err != nil {
		return nil, err
	}
	wc, err := arm.WorstCaseUSD(req)
	if err != nil {
		return nil, err
	}
	if err := budget.Reserve(wc); err != nil {
		return nil, err
	}
	started := r.Now()
	att, err := arm.Call(ctx, p.Item, req)
	if err != nil {
		budget.Settle(wc, 0) // no row is written, so a resume asks again
		return nil, err
	}
	att.Latency = r.Now().Sub(started)
	row := &Result{Seq: p.Seq, ItemID: p.Item.ID, Split: p.Item.Split.String(), Arm: p.Arm, Attempt: att, Started: started}
	for _, c := range att.Calls {
		row.CostUSD += c.CostUSD
	}
	budget.Settle(wc, row.CostUSD)
	row.LatencyInvalid = !last.IsZero() && started.Sub(last) > sleepGap
	if what := contract(p.Arm, att, req.MaxTokens, models); what != "" {
		row.ErrKind, row.Retryable, row.Detail = "contract", true, what
		return row, &ContractError{Arm: p.Arm, Item: p.Item.ID, What: what}
	}
	if att.ErrKind == "" {
		g, err := r.Suite.Grade(ctx, p.Item, att.Answer)
		if err != nil {
			return nil, err
		}
		row.Passed, row.Format, row.Detail = g.Passed, g.Format, g.Detail
		if g.Passed {
			row.Score = 1
		}
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
