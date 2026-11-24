// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/checkgen"
	"github.com/ankit373/hydra/internal/evalset"
	"github.com/ankit373/hydra/internal/provider"
)

func replayExample(task, source, domain string) evalset.Example {
	return evalset.Example{Task: task, TaskHash: evalset.TaskHashFor(task), Domain: domain, Source: source, Passed: true}
}

func TestCompareReplay_UsesOnlyRebuildableChecks(t *testing.T) {
	gen := checkgen.Spec{ID: "s1", Kind: "claims", Variant: "ground", TaskHash: evalset.TaskHashFor("summarise the contract")}
	specs := map[string][]checkgen.Spec{gen.TaskHash: {gen}}
	examples := []evalset.Example{
		replayExample("summarise the memo", "ground", "summary"),
		replayExample("summarise the contract", "gen:claims-ground", "summary"),
		replayExample("summarise the audit", "cmd:go test", "summary"),
		replayExample("", "ground", "summary"),
		replayExample("summarise the policy", "gen:rubric-code", "summary"),
		replayExample("summarise the memo", "ground", "summary"),
		replayExample("translate the memo", "ground", "translation"),
		replayExample("summarise the minutes", "ground,gen:claims-ground", "summary"),
	}
	// Newest first: the minutes name a spec that was never stored, so they are skipped with the others.
	tasks, skips := pickReplay(examples, "summary", 10, specs)
	var got []string
	for _, tk := range tasks {
		got = append(got, tk.Example.Task)
	}
	if want := "summarise the memo,summarise the contract"; strings.Join(got, ",") != want {
		t.Fatalf("replayed %q, want %q: newest first, one per task, other kinds left out", strings.Join(got, ","), want)
	}
	if len(tasks[1].Specs) != 1 || tasks[1].Specs[0].ID != "s1" {
		t.Fatalf("the generated check was not rebuilt from its stored spec: %+v", tasks[1].Specs)
	}
	if skips.NoText != 1 || skips.NoCheck != 3 {
		t.Fatalf("skips %+v, want 1 without text and 3 whose check cannot be rebuilt (cmd, a missing spec, a missing spec beside ground)", skips)
	}
	if one, _ := pickReplay(examples, "summary", 1, specs); len(one) != 1 || one[0].Example.Task != "summarise the memo" {
		t.Fatalf("--replay 1 took %+v, want only the newest rebuildable task", one)
	}
}

func TestCompareReplay_AsksBeforeSpending(t *testing.T) {
	var out strings.Builder
	if confirmReplay(strings.NewReader("n\n"), &out, 0.1234, 20, 2, false) {
		t.Fatal("a no was read as a yes")
	}
	text := out.String()
	if i, j := strings.Index(text, "Worst case: $0.1234 to send 20 tasks to 2 heads"), strings.Index(text, "Continue?"); i < 0 || j < i {
		t.Fatalf("output %q, want the worst case printed before the question", text)
	}
	for _, in := range []string{"y\n", "YES\n"} {
		if !confirmReplay(strings.NewReader(in), &strings.Builder{}, 1, 1, 2, false) {
			t.Errorf("%q was not a yes", in)
		}
	}
	if confirmReplay(strings.NewReader(""), &strings.Builder{}, 1, 1, 2, false) {
		t.Fatal("no answer was read as a yes")
	}
	out.Reset()
	if !confirmReplay(strings.NewReader(""), &out, 1, 1, 2, true) || strings.Contains(out.String(), "Continue?") || !strings.Contains(out.String(), "Worst case") {
		t.Fatalf("--yes must still print the worst case, and not ask: %q", out.String())
	}
}

func replayHeads() [2]provider.Head {
	return [2]provider.Head{{ID: "api/strong"}, {ID: "ollama/small", LocalOnly: true}}
}

func fakeRun(maxCost float64) (replayRun, *[]string) {
	var filed []string
	return replayRun{
		Heads:    replayHeads(),
		MaxCost:  maxCost,
		Estimate: func(provider.Head, string) float64 { return 0.01 },
		Ask: func(_ context.Context, task string, h provider.Head) (string, float64, error) {
			if h.ID == "ollama/small" && task == "t3" {
				return "", 0, errors.New("server down")
			}
			return h.ID + " answers " + task, 0.01, nil
		},
		Check: func(_ context.Context, tk replayTask, h provider.Head, answer string) (bool, bool) {
			return tk.Example.Task != "t2", h.ID == "api/strong"
		},
		File: func(tk replayTask, h provider.Head, answer string, passed bool) {
			filed = append(filed, h.ID+":"+tk.Example.Task)
		},
	}, &filed
}

func replayTasks(names ...string) []replayTask {
	var out []replayTask
	for _, n := range names {
		out = append(out, replayTask{Example: evalset.Example{Task: n}})
	}
	return out
}

func TestCompareReplay_StopsAtMaxCost(t *testing.T) {
	rr, filed := fakeRun(0.05)
	res := rr.run(context.Background(), replayTasks("t1", "t4", "t5", "t6", "t7"))
	if res.Tasks != 2 || res.Sides[0].Asked != 2 || res.Sides[1].Asked != 2 || !strings.Contains(res.Stopped, "--max-cost $0.05") {
		t.Fatalf("result %+v, want both heads sent the same 2 tasks and a stop before the third could pass $0.05", res)
	}
	if spent := res.Sides[0].CostUSD + res.Sides[1].CostUSD; spent > 0.05 {
		t.Fatalf("spent $%.2f, over the cap", spent)
	}
	if len(*filed) != 4 {
		t.Fatalf("filed %v, want each checked answer filed", *filed)
	}
	rr, _ = fakeRun(0)
	if res := rr.run(context.Background(), replayTasks("t1", "t4", "t5")); res.Tasks != 3 || res.Stopped != "" {
		t.Fatalf("with no cap: %+v", res)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if res := rr.run(ctx, replayTasks("t1")); res.Tasks != 0 || res.Stopped != "interrupted" {
		t.Fatalf("a cancelled replay: %+v", res)
	}
	if got := rr.worstCase(replayTasks("a", "b", "c")); got < 0.0599 || got > 0.0601 {
		t.Fatalf("worst case %v, want 3 tasks x 2 heads x $0.01", got)
	}
}

func TestCompareReplay_CountsWhatDidNotRun(t *testing.T) {
	rr, _ := fakeRun(0)
	res := rr.run(context.Background(), replayTasks("t1", "t2", "t3"))
	strong, small := res.Sides[0], res.Sides[1]
	if strong.Checked != 2 || strong.Passed != 2 || strong.NotChecked != 1 {
		t.Fatalf("strong %+v, want t2 not checked rather than failed", strong)
	}
	if small.Checked != 1 || small.Passed != 0 || small.NotChecked != 1 || small.Errors != 1 {
		t.Fatalf("small %+v, want one fail, one not checked and one that could not answer", small)
	}
	c := replayComparison("summary", res)
	if c.Heads[0].Checked != 2 || c.Heads[1].SinceChecked != 1 || c.Verdict != "no_clear_difference" {
		t.Fatalf("comparison %+v", c)
	}
	// Twenty tasks each, one head clean and the other failing most, is a clear lead.
	clear := replayComparison("summary", replayResult{Tasks: 20, Sides: [2]replaySide{
		{Head: "a", Checked: 20, Passed: 20}, {Head: "b", Checked: 20, Passed: 5}}})
	if clear.Verdict != "ahead" || clear.Ahead != "a" {
		t.Fatalf("comparison %+v, want a ahead", clear)
	}
	foot := replayFooter(replayResult{Tasks: 3, Sides: res.Sides, Skips: replaySkips{NoText: 2, NoCheck: 1}, Stopped: "--max-cost"})
	for _, want := range []string{"same 3 tasks", "2 stored without their text", "1 whose check cannot be rebuilt", "Stopped early", "Spent $"} {
		if !strings.Contains(foot, want) {
			t.Errorf("footer %q missing %q", foot, want)
		}
	}
}

func TestCompareReplay_RefusesBadInput(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())
	if _, _, err := run(t, "compare", "a", "b", "--kind", "summary", "--replay", "-1"); err == nil {
		t.Fatal("a negative --replay ran")
	}
}
