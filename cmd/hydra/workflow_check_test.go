// SPDX-License-Identifier: MIT

package main

import (
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/workflow"
)

// Extract then summarise: step 1's answer misses a field, its check fails, and
// the step reruns once on the stronger head before step 2 sees its output.
func TestWorkflowStep_FailedCheckRerunsOnce(t *testing.T) {
	s, f := escalating(t, "extraction", `{"invoice": "A-17"}`, `{"invoice": "A-17", "total": 120}`)
	containsChecker(t, s)

	out, cobraOut, err := run(t, "workflow", "run", "--id", "wf-rerun",
		"--step", "pull the invoice number and total as JSON", "--step", "summarise the invoice",
		"--step-kind", "1=extraction", "--step-check", "1=cmd:contains total {file}")
	if err != nil {
		t.Fatalf("workflow failed: %v (%s)", err, cobraOut)
	}
	if !strings.Contains(out, "rerun after a failed check on litellm/a") {
		t.Errorf("the run does not show the rerun:\n%s", out)
	}
	w, err := workflow.Load("wf-rerun")
	if err != nil {
		t.Fatal(err)
	}
	if w.Status != workflow.Done {
		t.Fatalf("workflow is %s, want done", w.Status)
	}
	if !w.Steps[0].Checked || w.Steps[1].Checked {
		t.Errorf("checked = %v, %v; want step 1 checked and step 2, which has no check, not", w.Steps[0].Checked, w.Steps[1].Checked)
	}
	if s1 := w.Steps[0]; s1.Head != "litellm/b" || !s1.Escalated || s1.FirstHead != "litellm/a" {
		t.Errorf("step 1 = head %s escalated %v from %q, want b after a", s1.Head, s1.Escalated, s1.FirstHead)
	}
	// Step 2 has no check, so it runs once, given step 1's passing answer.
	step2 := strings.TrimPrefix(w.Steps[1].Head, "litellm/")
	if f.called("a")+f.called("b") != 3 || !strings.Contains(f.prompt(step2), `"total": 120`) {
		t.Errorf("calls a=%d b=%d, want 3; step 2 was sent:\n%s", f.called("a"), f.called("b"), f.prompt(step2))
	}
}

// A step still failing after its rerun stops the workflow, so its output never
// becomes the next step's context.
func TestWorkflowStep_StillFailingStopsTheWorkflow(t *testing.T) {
	s, f := escalating(t, "extraction", `{"invoice": "A-17"}`, `{"invoice": "A-18"}`)
	containsChecker(t, s)

	_, _, err := run(t, "workflow", "run", "--id", "wf-stop",
		"--step", "pull the invoice number and total as JSON", "--step", "summarise the invoice",
		"--step-kind", "1=extraction", "--step-check", "1=cmd:contains total {file}")
	if err == nil || !strings.Contains(err.Error(), "check failed on litellm/a and again on litellm/b") {
		t.Fatalf("err = %v, want the step stopped after both heads failed", err)
	}
	w, lerr := workflow.Load("wf-stop")
	if lerr != nil {
		t.Fatal(lerr)
	}
	if w.Steps[0].Status != workflow.Failed || w.Steps[1].Status != workflow.Pending {
		t.Errorf("steps = %s, %s; want step 1 failed and step 2 never run", w.Steps[0].Status, w.Steps[1].Status)
	}
	if f.called("a") != 1 || f.called("b") != 1 {
		t.Errorf("calls a=%d b=%d, want one each", f.called("a"), f.called("b"))
	}
}

func TestWorkflowRun_RefusesABadStepCheckOrKind(t *testing.T) {
	for _, tc := range []struct{ name, flag, value, want string }{
		{"no step number", "--step-check", "ground", "N=<value>"},
		{"step out of range", "--step-check", "3=ground", "from 1 to 1"},
		{"unknown check", "--step-check", "1=tests", "unknown check"},
		{"unknown kind", "--step-kind", "1=poetry", "--step-kind"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cliSandbox(t)
			_, _, err := run(t, "workflow", "run", "--step", "a", tc.flag, tc.value)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to say %q", err, tc.want)
			}
		})
	}
}

// A check that could not run lets the step finish, but never marks it checked.
func TestWorkflowStep_CheckThatCannotRunIsNotChecked(t *testing.T) {
	dispatchable(t, "The total is 450 dollars.")
	if _, cobraOut, err := run(t, "workflow", "run", "--id", "wf-unchecked",
		"--step", "summarise the invoice", "--step-check", "1=ground"); err != nil {
		t.Fatalf("workflow failed: %v (%s)", err, cobraOut)
	}
	w, err := workflow.Load("wf-unchecked")
	if err != nil {
		t.Fatal(err)
	}
	if s := w.Steps[0]; s.Status != workflow.Done || s.Checked {
		t.Errorf("step = %s checked %v, want done and not checked", s.Status, s.Checked)
	}
}
