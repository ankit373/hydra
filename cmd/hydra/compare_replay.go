// SPDX-License-Identifier: MIT

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/ankit373/hydra/internal/checkgen"
	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/evalset"
	"github.com/ankit373/hydra/internal/ground"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/rank"
	"github.com/ankit373/hydra/internal/runid"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/stats"
	"github.com/ankit373/hydra/internal/trust"
)

// replayTask is a stored task whose checks can be rebuilt: ground needs only the
// prompt, a generated check needs the spec stored for the task.
type replayTask struct {
	Example evalset.Example
	Sources []string
	Specs   []checkgen.Spec
}

// replaySkips counts the stored tasks of a kind that could not be replayed.
type replaySkips struct {
	NoText  int `json:"no_text"`  // kept before the task text was
	NoCheck int `json:"no_check"` // a check that cannot be rebuilt from what is stored
}

// pickReplay takes up to n of the newest stored tasks of kind, one per task,
// whose every check can be rebuilt. The rest are counted, not run.
func pickReplay(examples []evalset.Example, kind string, n int, specs map[string][]checkgen.Spec) ([]replayTask, replaySkips) {
	var tasks []replayTask
	var skips replaySkips
	seen := map[string]bool{}
	for _, ex := range slices.Backward(examples) {
		if len(tasks) == n {
			break
		}
		if ex.Domain != trust.Domain(kind) || seen[ex.TaskHash] {
			continue
		}
		seen[ex.TaskHash] = true
		if ex.Task == "" {
			skips.NoText++
			continue
		}
		t := replayTask{Example: ex, Sources: strings.Split(ex.Source, ",")}
		if !t.rebuild(specs[ex.TaskHash]) {
			skips.NoCheck++
			continue
		}
		tasks = append(tasks, t)
	}
	return tasks, skips
}

func (t *replayTask) rebuild(stored []checkgen.Spec) bool {
	for _, src := range t.Sources {
		if src == "ground" {
			continue
		}
		i := slices.IndexFunc(stored, func(s checkgen.Spec) bool { return s.Check() == src })
		if !strings.HasPrefix(src, "gen:") || i < 0 {
			return false
		}
		t.Specs = append(t.Specs, stored[i])
	}
	return true
}

// replayRun is what a replay does, so a test can supply it: ask a head a task,
// judge the answer with the task's checks, and file the answer.
type replayRun struct {
	Heads    [2]provider.Head
	Ask      func(ctx context.Context, task string, head provider.Head) (output string, costUSD float64, err error)
	Estimate func(head provider.Head, task string) float64 // the most one call can cost
	// Check returns what its own model calls cost, so a check that asks a model counts toward MaxCost.
	Check   func(ctx context.Context, t replayTask, head provider.Head, answer string) (checked, passed bool, costUSD float64)
	File    func(t replayTask, head provider.Head, answer string, passed bool)
	MaxCost float64 // 0 for no cap
}

// replaySide is one head's score on the replayed sample.
type replaySide struct {
	Head       string  `json:"head"`
	Asked      int     `json:"asked"`
	Checked    int     `json:"checked"`
	Passed     int     `json:"passed"`
	NotChecked int     `json:"not_checked"`
	Errors     int     `json:"errors"`
	CostUSD    float64 `json:"cost_usd"`
}

// replayResult is a replay's sample and why it ended early, if it did.
type replayResult struct {
	Tasks   int           `json:"tasks"`
	Sides   [2]replaySide `json:"sides"`
	Skips   replaySkips   `json:"skipped"`
	Stopped string        `json:"stopped,omitempty"`
}

// worstCase is the most the whole replay can cost.
func (r replayRun) worstCase(tasks []replayTask) float64 {
	var sum float64
	for _, t := range tasks {
		for _, h := range r.Heads {
			sum += r.Estimate(h, t.Example.Task)
		}
	}
	return sum
}

// run sends each task to both heads, never starting one that could take the
// spend past MaxCost, so the two heads always see the same tasks.
func (r replayRun) run(ctx context.Context, tasks []replayTask) replayResult {
	res := replayResult{Sides: [2]replaySide{{Head: r.Heads[0].ID}, {Head: r.Heads[1].ID}}}
	// checksMax is the most one task's checks have cost so far, the best guess at the next task's.
	var spent, checksMax float64
	for _, t := range tasks {
		if err := ctx.Err(); err != nil {
			res.Stopped = "interrupted"
			break
		}
		next := r.Estimate(r.Heads[0], t.Example.Task) + r.Estimate(r.Heads[1], t.Example.Task) + checksMax
		if r.MaxCost > 0 && spent+next > r.MaxCost {
			res.Stopped = fmt.Sprintf("--max-cost $%.2f: $%.4f spent, the next task could cost $%.4f", r.MaxCost, spent, next)
			break
		}
		res.Tasks++
		var checksCost float64
		for i, h := range r.Heads {
			side := &res.Sides[i]
			side.Asked++
			out, cost, err := r.Ask(ctx, t.Example.Task, h)
			spent += cost
			side.CostUSD += cost
			if err != nil {
				side.Errors++
				continue
			}
			checked, passed, checkCost := r.Check(ctx, t, h, out)
			spent, checksCost = spent+checkCost, checksCost+checkCost
			side.CostUSD += checkCost
			if !checked {
				side.NotChecked++
				continue
			}
			side.Checked++
			if passed {
				side.Passed++
			}
			r.File(t, h, out, passed)
		}
		checksMax = max(checksMax, checksCost)
	}
	return res
}

// confirmReplay prints the worst case, then asks unless yes. Anything but y or yes is no.
func confirmReplay(in io.Reader, out io.Writer, worst float64, tasks, heads int, yes bool) bool {
	fmt.Fprintf(out, "\n  Worst case: $%.4f to send %d tasks to %d heads (a local head costs nothing).\n", worst, tasks, heads)
	fmt.Fprintln(out, "  A check that asks a model adds to that; --max-cost counts it, and the footer shows the total.")
	if yes {
		return true
	}
	fmt.Fprint(out, "  Continue? [y/N] ")
	line, _ := bufio.NewReader(in).ReadString('\n')
	return slices.Contains([]string{"y", "yes"}, strings.ToLower(strings.TrimSpace(line)))
}

// replayComparison is the table #1872 prints, over the replayed sample: both heads
// saw the same tasks, so the sample is the whole record and nothing is "since a change".
func replayComparison(kind string, res replayResult) comparison {
	c := comparison{Kind: kind}
	for _, s := range res.Sides {
		side := compareSide{Head: s.Head, Checked: s.Checked, Passed: s.Passed,
			SinceChecked: s.Checked, SincePassed: s.Passed}
		if s.Checked > 0 {
			side.PassRate = float64(s.Passed) / float64(s.Checked)
		}
		side.Lower, _, _ = stats.Wilson(s.Passed, s.Checked, stats.Z95)
		side.SinceLower, side.SinceUpper, _ = stats.Wilson(s.Passed, s.Checked, stats.Z95)
		c.Heads = append(c.Heads, side)
	}
	c.decide()
	return c
}

func replayFooter(r replayResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Both heads were sent the same %d tasks.", r.Tasks)
	for _, s := range r.Sides {
		if s.NotChecked+s.Errors > 0 {
			fmt.Fprintf(&b, " %s: %d not checked, %d failed to answer.", s.Head, s.NotChecked, s.Errors)
		}
	}
	if r.Skips.NoText+r.Skips.NoCheck > 0 {
		fmt.Fprintf(&b, " Skipped: %d stored without their text, %d whose check cannot be rebuilt.", r.Skips.NoText, r.Skips.NoCheck)
	}
	if r.Stopped != "" {
		fmt.Fprintf(&b, " Stopped early: %s.", r.Stopped)
	}
	fmt.Fprintf(&b, " Spent $%.4f.", r.Sides[0].CostUSD+r.Sides[1].CostUSD)
	return b.String()
}

// replayMaxTokens bounds each replayed answer, so the worst case is a real ceiling.
const replayMaxTokens = 1024

type replayArgs struct {
	kind       string
	heads      [2]string
	n          int
	yes        bool
	maxCost    float64
	maxCostSet bool
	jsonOut    bool
}

// compareReplay reruns stored tasks on both heads and prints the comparison table
// for that sample. Everything that reaches a model goes through the dispatcher.
func compareReplay(ctx context.Context, a replayArgs) error {
	if ctx == nil {
		ctx = context.Background()
	}
	d, err := dispatch.New(ctx)
	if err != nil {
		return fmt.Errorf("dispatcher init: %w", err)
	}
	defer d.Close()
	byID := map[string]provider.Head{}
	for _, h := range runnableHeads(d) {
		byID[h.ID] = h
	}
	var heads [2]provider.Head
	for i, id := range a.heads {
		h, ok := byID[id]
		if !ok {
			return fmt.Errorf("no runnable head %q; hyctl probe lists them", id)
		}
		heads[i] = h
	}
	examples, err := evalset.Load(evalset.DefaultPath())
	if err != nil {
		return err
	}
	specs, err := checkgen.LoadSpecs()
	if err != nil {
		return err
	}
	tasks, skips := pickReplay(examples, a.kind, a.n, specs)
	if len(tasks) == 0 {
		return fmt.Errorf("no stored task of kind %q can be replayed (%d stored without their text, %d whose check cannot be rebuilt)",
			a.kind, skips.NoText, skips.NoCheck)
	}
	domain := trust.Domain(a.kind)
	maxCost, _ := costCeiling(servePolicy(), a.maxCostSet, a.maxCost)
	runID := runid.New()
	// One spend for every check in the replay, so its calls stop at the cap together.
	checks := &genSpend{maxUSD: maxCost, chat: d.Dispatch}
	cal, err := trust.New(trust.DefaultPath())
	if err != nil {
		return err
	}
	rr := replayRun{
		Heads:   heads,
		MaxCost: maxCost,
		Estimate: func(h provider.Head, task string) float64 {
			return d.EstimateCost(rank.UITier(h), len(task)/4+1, replayMaxTokens)
		},
		Ask: func(ctx context.Context, task string, h provider.Head) (string, float64, error) {
			res, err := d.Dispatch(ctx, task, dispatch.Options{
				Head: h.ID, NoCache: true, Domain: domain, RunID: runID, MaxTokens: replayMaxTokens, MaxCostUSD: maxCost,
			})
			if err != nil {
				return "", 0, err
			}
			return res.Output, res.CostUSD, nil
		},
		Check: func(ctx context.Context, t replayTask, h provider.Head, answer string) (bool, bool, float64) {
			before := checks.spent
			checked, passed := replayCheck(ctx, d, byID, t, h, answer, domain, runID, maxCost, checks)
			return checked, passed, checks.spent - before
		},
		File: func(t replayTask, h provider.Head, answer string, passed bool) {
			fileExample(evalset.Example{
				Task: t.Example.Task, Domain: domain, Source: t.Example.Source, Head: h.ID,
				Enum: t.Example.Enum, Tier: rank.UITier(h), Candidate: answer, Passed: passed,
			})
			_ = cal.Update(h.ID, domain, true, trust.OutcomeFor(passed))
		},
	}
	if !confirmReplay(os.Stdin, stdout, rr.worstCase(tasks), len(tasks), 2, a.yes) {
		return errors.New("not confirmed; nothing was sent")
	}
	res := rr.run(ctx, tasks)
	res.Skips = skips
	c := replayComparison(a.kind, res)
	c.Basis = fmt.Sprintf("replay of %d stored tasks, 95%% bounds", res.Tasks)
	c.Replay = &res
	if a.jsonOut {
		return json.NewEncoder(os.Stdout).Encode(c)
	}
	printComparison(c)
	return nil
}

// replayCheck runs every check a task met. checked is false when any could not run,
// since a check that did not run is never a pass; passed needs all of them to.
func replayCheck(ctx context.Context, d *dispatch.Dispatcher, byID map[string]provider.Head, t replayTask, h provider.Head,
	answer, domain, runID string, maxCost float64, spend *genSpend) (checked, passed bool) {
	passed = true
	for _, src := range t.Sources {
		if src != "ground" {
			continue
		}
		v, err := (&ground.Oracle{Prompt: t.Example.Task}).Verify(ctx, answer, trust.Task{Domain: domain})
		if err != nil {
			return false, false
		}
		passed = passed && v.Passed
	}
	if len(t.Specs) > 0 {
		g := genRun{d: d, kind: domain, runID: runID, maxUSD: maxCost}
		_, env := g.checkEnv(byID, spend)
		results, _ := checkgen.Run(ctx, t.Specs, answer, h, runlog.Event{RunID: runID, Domain: domain}, env)
		for _, r := range results {
			if !r.Checked() {
				return false, false
			}
			passed = passed && r.Verdict.Passed
		}
	}
	return true, passed
}
