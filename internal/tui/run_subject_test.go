// SPDX-License-Identifier: MIT

package tui

import (
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/runlog"
)

// A workflow's run: no run_started, because nothing wrote one, and a
// task_started carrying the step's routing enum. This is the exact event shape
// read off disk from a real `hyctl workflow run` (#910).
func workflowRunEvents() []runlog.Event {
	return []runlog.Event{
		{Kind: runlog.KindTaskStarted, TaskID: "t1", Detail: "GRUNT", TS: "2026-09-18T17:38:41Z"},
		{Kind: runlog.KindDispatchFinished, TaskID: "t1", Status: "ok", TS: "2026-09-18T17:38:45Z"},
	}
}

// The routing key is not a description, and rendering it as one told the user
// their workflow was called GRUNT.
func TestRunRow_ARoutingEnumIsNeverTheTask(t *testing.T) {
	r := ckRunFromEvents("20260918T173841Z-7012569f", workflowRunEvents(), false)

	if r.task == "GRUNT" {
		t.Fatal("the run is labelled with its routing enum")
	}
	if r.task != "" {
		t.Errorf("task = %q, want empty: this run declared no subject", r.task)
	}
}

// And with nothing recorded, the view says so rather than inventing a label.
func TestAgentsView_ARunWithNoSubjectSaysSo(t *testing.T) {
	m := Cockpit{runsToday: []ckRun{{
		id: "20260918T173841Z-7012569f", status: "ok", task: "", durMS: 4774,
	}}}

	out := m.viewAgents(100, 20)
	if strings.Contains(out, "GRUNT") {
		t.Errorf("view rendered a routing key:\n%s", out)
	}
	if !strings.Contains(out, "task not recorded") {
		t.Errorf("view does not say the subject is missing:\n%s", out)
	}
}

// The subject a command declares is what the row shows.
func TestRunRow_TheDeclaredSubjectIsTheTask(t *testing.T) {
	events := append([]runlog.Event{{
		Kind: runlog.KindRunStarted, TaskID: "t0",
		Detail: "check the tiny local model answers twice", TS: "2026-09-18T17:38:41Z",
	}}, workflowRunEvents()...)

	r := ckRunFromEvents("20260918T173841Z-7012569f", events, false)
	if r.task != "check the tiny local model answers twice" {
		t.Errorf("task = %q, want the declared subject", r.task)
	}
}
