// SPDX-License-Identifier: MIT

package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/graph"
)

// runPeak runs w with every step answering at once, and returns the most
// steps that were ever in flight together.
func runPeak(t *testing.T, w Workflow) int32 {
	t.Helper()
	var p peak
	r := &funcRouter{fn: func(context.Context, string) (StepResult, error) {
		defer p.enter()()
		time.Sleep(20 * time.Millisecond) // gives a wrongly launched second step time to overlap
		return StepResult{Output: "ok"}, nil
	}}
	if _, err := Run(context.Background(), w, r, nopSave); err != nil {
		t.Fatal(err)
	}
	return p.most.Load()
}

func TestRun_SameWriteKeyRunsInSequence(t *testing.T) {
	w := newWorkflow(t,
		Step{Prompt: "reply to ticket 12", Needs: []int{}, Writes: []string{"doc:replies#ticket-12"}},
		Step{Prompt: "fix the tone of ticket 12", Needs: []int{}, Writes: []string{"doc:replies#ticket-12"}},
	)
	if got := runPeak(t, w); got != 1 {
		t.Errorf("%d steps writing doc:replies#ticket-12 ran at once, want 1", got)
	}
}

// runTogether runs a two-step w where each step waits until the other has
// started, so it only succeeds if the two are in flight at once.
func runTogether(t *testing.T, w Workflow) {
	t.Helper()
	var started sync.WaitGroup
	started.Add(2)
	both := make(chan struct{})
	go func() { started.Wait(); close(both) }()
	r := &funcRouter{fn: func(context.Context, string) (StepResult, error) {
		started.Done()
		select {
		case <-both:
			return StepResult{Output: "ok"}, nil
		case <-time.After(10 * time.Second):
			return StepResult{}, errors.New("the other step never started")
		}
	}}
	if _, err := Run(context.Background(), w, r, nopSave); err != nil {
		t.Fatal(err)
	}
}

func TestRun_DifferentRecordsRunTogether(t *testing.T) {
	runTogether(t, newWorkflow(t,
		Step{Prompt: "update ticket 12", Needs: []int{}, Writes: []string{"record:tickets/12"}},
		Step{Prompt: "update ticket 13", Needs: []int{}, Writes: []string{"record:tickets/13"}},
	))
}

func TestRun_CoupledFilesRunInSequence(t *testing.T) {
	doc := graph.Doc{
		Nodes: []graph.Node{{ID: "flaky", File: "flaky_test.go"}, {ID: "clock", File: "clock.go"}, {ID: "log", File: "CHANGELOG.md"}},
		Edges: []graph.Edge{{From: "flaky", To: "clock"}},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "graph.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	g, err := graph.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	w := newWorkflow(t,
		Step{Prompt: "fix the test", Needs: []int{}, Writes: []string{"flaky_test.go"}},
		Step{Prompt: "fix the clock", Needs: []int{}, Writes: []string{"file:clock.go"}},
	)
	w.Coupled = g.Coupled
	if got := runPeak(t, w); got != 1 {
		t.Errorf("edits to two coupled files ran %d at once, want 1", got)
	}

	w = newWorkflow(t,
		Step{Prompt: "fix the test", Needs: []int{}, Writes: []string{"flaky_test.go"}},
		Step{Prompt: "update the changelog", Needs: []int{}, Writes: []string{"CHANGELOG.md"}},
	)
	w.Coupled = g.Coupled
	runTogether(t, w)
}
