// SPDX-License-Identifier: MIT

package main

import (
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/testutil"
	"github.com/ankit373/hydra/internal/workflow"
)

func storedWorkflow(t *testing.T, id string) workflow.Workflow {
	t.Helper()
	w, err := workflow.New(id, "fix the flaky test", []workflow.Step{
		{Prompt: "list the failing tests", Enum: "SIMPLE"},
		{Prompt: "write the fix", Enum: "EXPERT"},
	})
	if err != nil {
		t.Fatal(err)
	}
	w.Steps[0].Status = workflow.Done
	w.Steps[0].Output = "TestFoo is flaky"
	w.Steps[0].Model = "Qwen2.5-Coder:7b (Ollama)"
	w.Steps[0].Tier = 10
	w.Steps[0].CostUSD = 0.0012
	w.Steps[0].DurationMS = 5800
	if err := workflow.Save(w); err != nil {
		t.Fatal(err)
	}
	return w
}

// The step table is the whole surface of a workflow run: which step, its
// status, which head answered and at what tier.
func TestPrintWorkflow_ShowsPerStepHeadAndTier(t *testing.T) {
	w := workflow.Workflow{
		ID: "wf1", Task: "t", Status: workflow.Failed,
		Steps: []workflow.Step{
			{N: 1, Title: "triage", Status: workflow.Done, Model: "Qwen2.5-Coder:7b", Tier: 10, DurationMS: 5800, CostUSD: 0.001},
			{N: 2, Title: "fix it", Status: workflow.Failed, Err: "no head could answer\nsecond line"},
			{N: 3, Title: "verify", Status: workflow.Pending},
		},
	}
	out := stripANSICodes(captureStdout(t, func() { printWorkflow(w) }))

	for _, want := range []string{"triage", "done", "T10", "5.8s",
		"fix it", "failed", "no head could answer", "verify", "pending", "1/3 steps", "$0.0010"} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
	// A multi-line error must not break the table.
	if strings.Contains(out, "second line") {
		t.Errorf("a step error was printed over multiple lines:\n%s", out)
	}
	// A step that never ran must not claim a head or a duration.
	if strings.Count(out, "Qwen2.5-Coder:7b") != 1 {
		t.Errorf("a head is shown for a step that did not run:\n%s", out)
	}
}

// The workflow's answer is its last completed step's output; printing every
// step would bury it.
func TestLastOutput_IsTheFinalCompletedStep(t *testing.T) {
	w := workflow.Workflow{Steps: []workflow.Step{
		{Status: workflow.Done, Output: "first"},
		{Status: workflow.Done, Output: "second"},
		{Status: workflow.Failed, Output: ""},
	}}
	if got := lastOutput(w); got != "second" {
		t.Errorf("lastOutput = %q, want the last completed step's output", got)
	}
	if got := lastOutput(workflow.Workflow{Steps: []workflow.Step{{Status: workflow.Pending}}}); got != "" {
		t.Errorf("lastOutput = %q on a workflow that produced nothing", got)
	}
}

func TestStatusLabel_CoversEveryStatus(t *testing.T) {
	for _, s := range []workflow.Status{workflow.Pending, workflow.Running, workflow.Done, workflow.Failed} {
		got := stripANSICodes(statusLabel(s))
		if got == "" {
			t.Errorf("status %q renders as empty", s)
		}
		if !strings.Contains(string(s), strings.TrimSpace(got)) {
			t.Errorf("status %q renders as %q, which does not name it", s, got)
		}
	}
}

func TestOneLine_CollapsesAndTruncates(t *testing.T) {
	if got := oneLine("first\nsecond"); got != "first" {
		t.Errorf("oneLine = %q, want the first line only", got)
	}
	if got := oneLine(strings.Repeat("x", 200)); len(got) > 64 {
		t.Errorf("a 200-char string rendered %d chars wide", len(got))
	}
	if got := oneLine("  spaced  "); got != "spaced" {
		t.Errorf("oneLine = %q, want it trimmed", got)
	}
}

// A timestamp that cannot be parsed is shown, not hidden: the raw value is more
// use than a blank column.
func TestCreatedAge(t *testing.T) {
	recent := time.Now().UTC().Add(-90 * time.Second).Format(time.RFC3339Nano)
	if got := createdAge(recent); !strings.Contains(got, "ago") {
		t.Errorf("createdAge(%q) = %q, want an age", recent, got)
	}
	if got := createdAge("not a timestamp"); got != "not a timestamp" {
		t.Errorf("createdAge = %q, want the raw value back", got)
	}
}

// A mismatch would silently route a step at a tier meant for a different one.
func TestWorkflowRun_RefusesMismatchedAndUnknownEnums(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		args       []string
	}{
		{"no steps", "at least one --step", []string{"workflow", "run", "--task", "x"}},
		{"enum count mismatch", "one --enum per --step",
			[]string{"workflow", "run", "--step", "a", "--step", "b", "--enum", "SIMPLE"}},
		{"unknown enum", `unknown --enum "NONSENSE"`,
			[]string{"workflow", "run", "--step", "a", "--enum", "NONSENSE"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testutil.NewSandbox(t)
			_, _, err := run(t, tc.args...)
			if err == nil {
				t.Fatalf("%s was accepted", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error does not say %q: %v", tc.want, err)
			}
		})
	}
}

func TestWorkflowList_EmptyAndPopulated(t *testing.T) {
	testutil.NewSandbox(t)
	out, _, err := run(t, "workflow", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no workflows yet") {
		t.Errorf("an empty store does not say so:\n%s", out)
	}

	storedWorkflow(t, "wf1")
	out, _, err = run(t, "workflow", "list")
	if err != nil {
		t.Fatal(err)
	}
	out = stripANSICodes(out)
	for _, want := range []string{"wf1", "1/2", "fix the flaky test"} {
		if !strings.Contains(out, want) {
			t.Errorf("list is missing %q:\n%s", want, out)
		}
	}
}

func TestWorkflowShow_AndJSON(t *testing.T) {
	testutil.NewSandbox(t)
	storedWorkflow(t, "wf1")

	out, _, err := run(t, "workflow", "show", "wf1")
	if err != nil {
		t.Fatal(err)
	}
	out = stripANSICodes(out)
	for _, want := range []string{"wf1", "list the failing tests", "write the fix", "1/2 steps"} {
		if !strings.Contains(out, want) {
			t.Errorf("show is missing %q:\n%s", want, out)
		}
	}

	jsonOut, _, err := run(t, "workflow", "show", "wf1", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(jsonOut, `"id":"wf1"`) || !strings.Contains(jsonOut, `"enum":"EXPERT"`) {
		t.Errorf("--json did not emit the workflow:\n%s", jsonOut)
	}
}

func TestWorkflowShow_UnknownAndTraversingID(t *testing.T) {
	testutil.NewSandbox(t)
	if _, _, err := run(t, "workflow", "show", "nope"); err == nil {
		t.Error("an unknown id was accepted")
	}
	// The id lands in a filesystem path.
	_, _, err := run(t, "workflow", "show", "../../etc/passwd")
	if err == nil {
		t.Fatal("a traversing id was accepted")
	}
	if !strings.Contains(err.Error(), "invalid workflow id") {
		t.Errorf("error does not name the problem: %v", err)
	}
}

func TestWorkflowRemove(t *testing.T) {
	testutil.NewSandbox(t)
	storedWorkflow(t, "wf1")
	if _, _, err := run(t, "workflow", "rm", "wf1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, "workflow", "show", "wf1"); err == nil {
		t.Error("the workflow is still readable after rm")
	}
}

// Resuming a finished workflow must not dispatch again. It reports and stops,
// which is why this needs no head on the machine.
func TestWorkflowResume_FinishedWorkflowDoesNothing(t *testing.T) {
	testutil.NewSandbox(t)
	w := storedWorkflow(t, "wf1")
	for i := range w.Steps {
		w.Steps[i].Status = workflow.Done
		w.Steps[i].Output = "done"
	}
	w.Status = workflow.Done
	if err := workflow.Save(w); err != nil {
		t.Fatal(err)
	}
	out, _, err := run(t, "workflow", "resume", "wf1")
	if err != nil {
		t.Fatalf("resuming a finished workflow errored: %v", err)
	}
	if !strings.Contains(stripANSICodes(out), "nothing left to run") {
		t.Errorf("output does not say it had nothing to do:\n%s", out)
	}
}

func TestWorkflowResume_UnknownID(t *testing.T) {
	testutil.NewSandbox(t)
	if _, _, err := run(t, "workflow", "resume", "nope"); err == nil {
		t.Error("resuming an unknown id was accepted")
	}
}

// The adapter must refuse a bad enum rather than routing unrestricted, the #501
// rule applied to this caller of the map.
func TestDispatchRouter_RefusesAnUnknownEnum(t *testing.T) {
	testutil.NewSandbox(t)
	r := dispatchRouter{}
	if _, err := r.Route(t.Context(), "p", "NONSENSE"); err == nil {
		t.Fatal("an unknown enum was passed through to the router")
	}
}
