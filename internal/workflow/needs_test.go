// SPDX-License-Identifier: MIT

package workflow

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/testutil"
)

// funcRouter answers by step prompt and is safe to call from several steps at once.
type funcRouter struct {
	mu    sync.Mutex
	asked []string
	fn    func(ctx context.Context, prompt string) (StepResult, error)
}

func (f *funcRouter) Route(ctx context.Context, prompt string, _ Step) (StepResult, error) {
	f.mu.Lock()
	f.asked = append(f.asked, prompt)
	f.mu.Unlock()
	return f.fn(ctx, prompt)
}

// last line of a step prompt is the step's own prompt.
func own(prompt string) string {
	return prompt[strings.LastIndex(prompt, "\n")+1:]
}

func newWorkflow(t *testing.T, steps ...Step) Workflow {
	t.Helper()
	w, err := New("wf-needs", "handle the support tickets", steps)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func nopSave(Workflow) error { return nil }

// peak counts calls in flight and keeps the most seen at once.
type peak struct{ now, most atomic.Int32 }

func (p *peak) enter() func() {
	n := p.now.Add(1)
	for m := p.most.Load(); n > m && !p.most.CompareAndSwap(m, n); m = p.most.Load() {
	}
	return func() { p.now.Add(-1) }
}

// Steps 2 and 3 both need only step 1: each blocks until the other has started,
// which only returns if the two are in flight together.
func TestRun_IndependentStepsRunTogether(t *testing.T) {
	w := newWorkflow(t,
		Step{Prompt: "extract product and complaint"},
		Step{Prompt: "summarise the top issues", Needs: []int{1}, Enum: "MODERATE"},
		Step{Prompt: "draft a reply for each", Needs: []int{1}, Enum: "STANDARD"},
	)
	var started sync.WaitGroup
	started.Add(2)
	both := make(chan struct{})
	go func() { started.Wait(); close(both) }()
	r := &funcRouter{fn: func(ctx context.Context, prompt string) (StepResult, error) {
		if own(prompt) == "extract product and complaint" {
			return StepResult{Output: "rows"}, nil
		}
		started.Done()
		select {
		case <-both:
			return StepResult{Output: "ok"}, nil
		case <-time.After(10 * time.Second):
			return StepResult{}, errors.New("the other step never started: the two ran one after the other")
		}
	}}
	done, err := Run(context.Background(), w, r, nopSave)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != Done {
		t.Errorf("status %q, want done", done.Status)
	}
}

func TestRun_StepWaitsForItsNeeds(t *testing.T) {
	w := newWorkflow(t,
		Step{Prompt: "find the cause"},
		Step{Prompt: "read the changelog", Needs: []int{}},
		Step{Prompt: "write the fix", Needs: []int{1, 2}},
	)
	var mu sync.Mutex
	finished := map[string]bool{}
	r := &funcRouter{fn: func(_ context.Context, prompt string) (StepResult, error) {
		p := own(prompt)
		mu.Lock()
		defer mu.Unlock()
		if p == "write the fix" && (!finished["find the cause"] || !finished["read the changelog"]) {
			return StepResult{}, errors.New("step 3 started before both of its needs were done")
		}
		finished[p] = true
		return StepResult{Output: "out of " + p}, nil
	}}
	if _, err := Run(context.Background(), w, r, nopSave); err != nil {
		t.Fatal(err)
	}
	for _, p := range r.asked {
		if own(p) == "write the fix" && (!strings.Contains(p, "out of find the cause") || !strings.Contains(p, "out of read the changelog")) {
			t.Errorf("step 3 does not carry both outputs it needs:\n%s", p)
		}
	}
}

// A record stored before steps had needs runs exactly as it did: one step at a
// time, each seeing every earlier output.
func TestRun_EmptyNeedsMeansAllEarlierSteps(t *testing.T) {
	old := Workflow{ID: "wf-old", Task: "t", Status: Pending, Steps: []Step{
		{N: 1, Title: "a", Prompt: "a", Status: Pending},
		{N: 2, Title: "b", Prompt: "b", Status: Pending},
		{N: 3, Title: "c", Prompt: "c", Status: Pending},
	}}
	if got := old.Needs(2); !slices.Equal(got, []int{1, 2}) {
		t.Fatalf("Needs of an old step 3 = %v, want [1 2]", got)
	}
	var p peak
	r := &funcRouter{fn: func(_ context.Context, prompt string) (StepResult, error) {
		defer p.enter()()
		return StepResult{Output: "out-" + own(prompt)}, nil
	}}
	if _, err := Run(context.Background(), old, r, nopSave); err != nil {
		t.Fatal(err)
	}
	if p.most.Load() != 1 {
		t.Errorf("%d steps of an old record ran at once, want 1", p.most.Load())
	}
	if p := r.asked[2]; !strings.Contains(p, "out-a") || !strings.Contains(p, "out-b") {
		t.Errorf("old step 3 does not see every earlier output:\n%s", p)
	}
}

// Step 2 fails while step 3 runs beside it. Step 3 is still recorded, step 4
// (which needs 2) stays pending, and a resume runs only 2 and 4.
func TestRun_FailedStepLeavesDependentsPending(t *testing.T) {
	testutil.NewSandbox(t)
	w := newWorkflow(t,
		Step{Prompt: "extract"},
		Step{Prompt: "summarise", Needs: []int{1}},
		Step{Prompt: "draft replies", Needs: []int{1}},
		Step{Prompt: "send the summary", Needs: []int{2}},
	)
	if err := Save(w); err != nil {
		t.Fatal(err)
	}
	failing := &funcRouter{fn: func(_ context.Context, prompt string) (StepResult, error) {
		if own(prompt) == "summarise" {
			return StepResult{}, errors.New("head refused")
		}
		return StepResult{Output: "ok"}, nil
	}}
	got, err := Run(context.Background(), w, failing, Save)
	if err == nil || !strings.Contains(err.Error(), "step 2") {
		t.Fatalf("err = %v, want the failure at step 2", err)
	}
	want := []Status{Done, Failed, Done, Pending}
	for i, s := range got.Steps {
		if s.Status != want[i] {
			t.Errorf("step %d is %q, want %q", i+1, s.Status, want[i])
		}
	}

	back, err := Load(w.ID)
	if err != nil {
		t.Fatal(err)
	}
	passing := &funcRouter{fn: func(context.Context, string) (StepResult, error) {
		return StepResult{Output: "ok"}, nil
	}}
	if _, err := Run(context.Background(), back, passing, Save); err != nil {
		t.Fatal(err)
	}
	var ran []string
	for _, p := range passing.asked {
		ran = append(ran, own(p))
	}
	slices.Sort(ran)
	if !slices.Equal(ran, []string{"send the summary", "summarise"}) {
		t.Errorf("resume ran %v, want only the failed step and its dependent", ran)
	}
}

func TestRun_SavesAreNeverInterleaved(t *testing.T) {
	steps := []Step{{Prompt: "root"}}
	for range 6 {
		steps = append(steps, Step{Prompt: "leaf", Needs: []int{1}})
	}
	w := newWorkflow(t, steps...)
	var saving, overlaps atomic.Int32
	var last Workflow
	save := func(x Workflow) error {
		if saving.Add(1) > 1 {
			overlaps.Add(1)
		}
		defer saving.Add(-1)
		last = x
		return nil
	}
	var p peak
	r := &funcRouter{fn: func(context.Context, string) (StepResult, error) {
		defer p.enter()()
		return StepResult{Output: "ok"}, nil
	}}
	if _, err := Run(context.Background(), w, r, save); err != nil {
		t.Fatal(err)
	}
	if overlaps.Load() != 0 {
		t.Errorf("%d saves overlapped another", overlaps.Load())
	}
	if last.Status != Done {
		t.Errorf("last save says %q, want done", last.Status)
	}
	if p.most.Load() > maxParallel {
		t.Errorf("%d steps ran at once, over the cap of %d", p.most.Load(), maxParallel)
	}
}

func TestStepPrompt_OnlyNeededOutputs(t *testing.T) {
	w := Workflow{Task: "fix the flaky test", Steps: []Step{
		{N: 1, Title: "cause", Output: "it races on a map", Needs: []int{}},
		{N: 2, Title: "fix", Output: "a mutex", Needs: []int{1}},
		{N: 3, Title: "changelog", Prompt: "update the changelog", Needs: []int{2}},
	}}
	got := w.stepPrompt(2)
	if !strings.Contains(got, "a mutex") {
		t.Errorf("step 3 lacks the output it needs:\n%s", got)
	}
	if strings.Contains(got, "it races on a map") {
		t.Errorf("step 3 carries step 1's output, which it does not need:\n%s", got)
	}
}

func TestNew_RefusesBadNeeds(t *testing.T) {
	for name, needs := range map[string][]int{
		"itself":       {2},
		"a later step": {3},
		"no such step": {9},
		"step zero":    {0},
	} {
		_, err := New("wf", "t", []Step{{Prompt: "a"}, {Prompt: "b", Needs: needs}, {Prompt: "c"}})
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	w, err := New("wf", "t", []Step{{Prompt: "a"}, {Prompt: "b", Needs: []int{1, 1}}, {Prompt: "c", Needs: []int{}}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(w.Steps[1].Needs, []int{1}) || w.Steps[2].Needs == nil || len(w.Steps[2].Needs) != 0 {
		t.Errorf("needs stored as %v and %v, want [1] and an empty list", w.Steps[1].Needs, w.Steps[2].Needs)
	}
}

// No needs and every earlier step are different instructions, and both must
// survive the record, or a resume reads "none" as "all".
func TestSaveLoad_KeepsNoNeedsApartFromAllEarlier(t *testing.T) {
	testutil.NewSandbox(t)
	w := saved(t, "wf-none", []Step{{Prompt: "a"}, {Prompt: "b", Needs: []int{}}, {Prompt: "c"}})
	back, err := Load(w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := back.Needs(1); len(got) != 0 {
		t.Errorf("step 2 needs %v after a reload, want none", got)
	}
	if got := back.Needs(2); !slices.Equal(got, []int{1, 2}) {
		t.Errorf("step 3 needs %v after a reload, want [1 2]", got)
	}
}
