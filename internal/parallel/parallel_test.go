// SPDX-License-Identifier: MIT

package parallel

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/tree"
)

func TestRun_EmptyBatch(t *testing.T) {
	_, err := Run(context.Background(), nil, Options{})
	if err == nil {
		t.Fatal("Run(nil) = nil error, want error")
	}
}

// Run must reject a batch where two edit tasks target the same file, before
// dispatching anything.
func TestRun_DuplicateFileConflict(t *testing.T) {
	tasks := []Task{
		{Label: "a", Enum: "SIMPLE", File: "/abs/path/foo.go", Prompt: "x"},
		{Label: "b", Enum: "SIMPLE", File: "/abs/path/foo.go", Prompt: "y"},
	}
	_, err := Run(context.Background(), tasks, Options{})
	if err == nil {
		t.Fatal("Run(duplicate file) = nil error, want conflict error")
	}
	if !strings.Contains(err.Error(), "conflict") {
		t.Errorf("error = %q, want it to mention conflict", err.Error())
	}
}

func TestExtractContent(t *testing.T) {
	body := markerStart + "\nhello\nworld\n" + markerEnd
	if got := extractContent("prose\n" + body + "\ntrailing"); got != "hello\nworld" {
		t.Errorf("extractContent(both) = %q, want %q", got, "hello\nworld")
	}
	if got := extractContent("no markers here"); got != "" {
		t.Errorf("extractContent(none) = %q, want empty", got)
	}
}

func TestStripOuterFence(t *testing.T) {
	if got := stripOuterFence("```ts\nconst x = 1\n```"); got != "const x = 1" {
		t.Errorf("stripOuterFence() = %q, want %q", got, "const x = 1")
	}
}

func TestFileExt(t *testing.T) {
	if got := fileExt("main.go"); got != "go" {
		t.Errorf("fileExt = %q, want go", got)
	}
	if got := fileExt("Makefile"); got != "" {
		t.Errorf("fileExt(Makefile) = %q, want empty", got)
	}
}

// Result marshals to its raw JSON verbatim.
func TestResult_MarshalJSON(t *testing.T) {
	r := Result{raw: json.RawMessage(`{"label":"a","status":"ok"}`)}
	out, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"label":"a","status":"ok"}` {
		t.Errorf("Marshal = %s, want raw passthrough", out)
	}
	if string(r.Raw()) != `{"label":"a","status":"ok"}` {
		t.Errorf("Raw() mismatch")
	}
}

// A batch is a fan-out and must reconstruct as one: a batch root with each task
// as a child. Before #204 this package emitted nothing, so an N-task batch had
// no structure in the run log at all.
func TestRun_EmitsBatchTree(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HYDRA_RUN_ID", "")
	t.Setenv("HYDRA_TASK_ID", "")

	tasks := []Task{
		{Label: "alpha", Enum: "SIMPLE", Prompt: "a"},
		{Label: "beta", Enum: "SIMPLE", Prompt: "b"},
		{Label: "gamma", Enum: "SIMPLE", Prompt: "c"},
	}
	// Dispatch cannot succeed in a test environment; the tree shape is what
	// matters and it is written either way.
	if _, err := Run(context.Background(), tasks, Options{RunID: "run-batch"}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	events, err := runlog.Load("run-batch")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("a parallel batch wrote no run-log events")
	}

	tr, _ := tree.Reconstruct(events)
	rows := tr.Rows()
	if len(rows) != len(tasks)+1 {
		t.Fatalf("%d rows, want %d (batch root + one per task)", len(rows), len(tasks)+1)
	}
	if rows[0].Node.ID != batchAgent {
		t.Errorf("root = %q, want %q", rows[0].Node.ID, batchAgent)
	}

	seen := map[string]bool{}
	for _, r := range rows[1:] {
		if r.Depth != 1 {
			t.Errorf("task %q at depth %d, want 1", r.Node.ID, r.Depth)
		}
		seen[r.Node.ID] = true
	}
	for _, task := range tasks {
		if !seen[task.Label] {
			t.Errorf("task %q missing from the tree", task.Label)
		}
	}
}

// Every task in a batch shares the batch's run but gets its own task id — that
// is what lets a reader tell "one batch of three" from "three unrelated runs".
func TestRun_TasksShareRunAndGetDistinctTaskIDs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	tasks := []Task{
		{Label: "one", Enum: "SIMPLE", Prompt: "a"},
		{Label: "two", Enum: "SIMPLE", Prompt: "b"},
	}
	if _, err := Run(context.Background(), tasks, Options{RunID: "run-ids"}); err != nil {
		t.Fatal(err)
	}

	events, err := runlog.Load("run-ids")
	if err != nil {
		t.Fatal(err)
	}

	taskIDs := map[string]string{} // label → task id
	for _, e := range events {
		if e.RunID != "run-ids" {
			t.Errorf("event carries run_id %q, want run-ids", e.RunID)
		}
		if e.Agent == batchAgent || e.TaskID == "" {
			continue
		}
		if prev, ok := taskIDs[e.Agent]; ok && prev != e.TaskID {
			t.Errorf("task %q has two task ids: %q and %q", e.Agent, prev, e.TaskID)
		}
		taskIDs[e.Agent] = e.TaskID
	}
	if len(taskIDs) != len(tasks) {
		t.Fatalf("saw %d tasks, want %d", len(taskIDs), len(tasks))
	}
	seen := map[string]bool{}
	for label, id := range taskIDs {
		if seen[id] {
			t.Errorf("task %q reused task id %q — tasks must be distinguishable", label, id)
		}
		seen[id] = true
	}
}
