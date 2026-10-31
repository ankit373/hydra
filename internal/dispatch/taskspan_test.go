// SPDX-License-Identifier: MIT

package dispatch

import (
	"context"
	"testing"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/tree"
	"github.com/ankit373/hydra/internal/waterfall"
)

func dispatchOnce(t *testing.T, runID, taskID string) []runlog.Event {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	d := captureDispatcher(t, &config.Config{})
	if _, err := d.Dispatch(context.Background(), "hello", Options{
		RunID: runID, TaskID: taskID,
	}); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	events, err := runlog.Load(runID)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

// The defect: every dispatch event named runlog.SpanIDFor(taskID) as its parent
// and no event ever declared it. waterfall promotes a span whose parent was
// never declared to a root, so the attempts read as unrelated dispatches and no
// run had a tree at all. Measured before the fix: 74 exported spans, 0 with a
// resolvable parent.
func TestDispatch_DeclaresTheSpanItsAttemptsNameAsParent(t *testing.T) {
	events := dispatchOnce(t, "run-ts", "task-ts")

	declared := map[string]bool{}
	for _, e := range events {
		if e.SpanID != "" {
			declared[e.SpanID] = true
		}
	}
	if !declared[runlog.SpanIDFor("task-ts")] {
		t.Fatalf("no event declares the task span %s, so every attempt naming it "+
			"as a parent is a dangling reference", runlog.SpanIDFor("task-ts"))
	}

	// The general invariant, not just the one id: nothing may name a parent the
	// log does not declare.
	for _, e := range events {
		if p := e.ParentSpan(); p != "" && !declared[p] {
			t.Errorf("%s event names parent %s, which no event declares", e.Kind, p)
		}
	}
}

// What the declaration is for: the run has to reconstruct as a tree.
func TestDispatch_ReconstructsAsATreeNotAFlatList(t *testing.T) {
	events := dispatchOnce(t, "run-tree2", "task-tree2")

	tr := waterfall.Build(events)
	if len(tr.Roots) != 1 {
		t.Fatalf("%d roots, want 1 (the task): a run whose attempts each root "+
			"is what a collector shows as unrelated spans", len(tr.Roots))
	}
	root := tr.Roots[0]
	if root.ID != runlog.SpanIDFor("task-tree2") {
		t.Errorf("root span is %s, want the task span %s", root.ID, runlog.SpanIDFor("task-tree2"))
	}
	if len(root.Children) == 0 {
		t.Error("the task span has no children, so the attempts did not nest under it")
	}
	for _, c := range root.Children {
		if c.Depth != 1 {
			t.Errorf("child %s at depth %d, want 1", c.ID, c.Depth)
		}
	}
}

// The task span exists for the waterfall. A supervision tree answers a
// different question and deliberately collapses these, so declaring the span
// must not add a node there, least of all one labelled with a raw task id.
func TestDispatch_TaskSpanIsInvisibleToTheSupervisionTree(t *testing.T) {
	events := dispatchOnce(t, "run-tree3", "task-tree3")

	tr, _ := tree.Reconstruct(events)
	for _, r := range tr.Rows() {
		if r.Node.ID == "task-tree3" {
			t.Errorf("the supervision tree grew a node labelled with the raw task id; " +
				"the task span is a waterfall concept and says nothing about ownership")
		}
	}
	if len(tr.Rows()) == 0 {
		t.Error("the supervision tree is empty, so the skip removed more than the task span")
	}
}

// A task that ends has to close its span, or the bar runs to whichever attempt
// logged last rather than to when the task actually finished.
func TestDispatch_ClosesTheTaskSpan(t *testing.T) {
	events := dispatchOnce(t, "run-ts2", "task-ts2")

	span := runlog.SpanIDFor("task-ts2")
	var started, finished bool
	for _, e := range events {
		if e.SpanID != span {
			continue
		}
		switch e.Kind {
		case runlog.KindTaskStarted:
			started = true
		case runlog.KindTaskFinished:
			finished = true
		}
	}
	if !started {
		t.Error("the task span is never opened")
	}
	if !finished {
		t.Error("the task span is never closed, so its bar ends at whatever logged last")
	}
}
