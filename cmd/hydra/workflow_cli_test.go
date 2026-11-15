// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
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

// show is the audit surface: every step's output, head, tier and cost, not
// only the last answer and the total (#1177).
func TestWorkflowShow_EveryStepsOutputAndCost(t *testing.T) {
	testutil.NewSandbox(t)
	w := storedWorkflow(t, "wf1")
	w.Steps[1].Status = workflow.Done
	w.Steps[1].Output = "the fix is a retry"
	w.Steps[1].Model = "claude-sonnet"
	w.Steps[1].Tier = 3
	w.Steps[1].CostUSD = 0.0345
	if err := workflow.Save(w); err != nil {
		t.Fatal(err)
	}

	out, _, err := run(t, "workflow", "show", "wf1")
	if err != nil {
		t.Fatal(err)
	}
	out = stripANSICodes(out)
	wants := []string{
		// Every step's output, not only the last.
		"TestFoo is flaky",
		"the fix is a retry",
		// Each step's cost, then the total.
		"$0.0012",
		"$0.0345",
		"$0.0357",
		// Each step's head and tier.
		"Qwen2.5-Coder:7b (Ollama) · T10",
		"claude-sonnet · T3",
	}
	for _, want := range wants {
		if !strings.Contains(out, want) {
			t.Errorf("show is missing %q:\n%s", want, out)
		}
	}

	// --json is the same record, so it carries every field the view shows.
	raw, _, err := run(t, "workflow", "show", "wf1", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got workflow.Workflow
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	s := got.Steps[0]
	if s.Output != "TestFoo is flaky" || s.CostUSD != 0.0012 || s.Tier != 10 || s.Model == "" {
		t.Errorf("--json step 1 lost a field: %+v", s)
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
	if _, err := r.Route(t.Context(), "p", workflow.Step{Enum: "NONSENSE"}); err == nil {
		t.Fatal("an unknown enum was passed through to the router")
	}
}

// A saved step naming a gen check is refused, never routed with the check silently dropped.
func TestDispatchRouter_RefusesAGenCheck(t *testing.T) {
	testutil.NewSandbox(t)
	if _, err := (dispatchRouter{}).Route(t.Context(), "p", workflow.Step{Check: []string{"gen"}}); err == nil {
		t.Fatal("a step's gen check was dropped and the step routed")
	}
}

// --needs N belongs to --step N, the same pairing as --enum, and "" is no
// needs rather than the default of every earlier step.
func TestWorkflowRun_NeedsPairsWithStep(t *testing.T) {
	s := testutil.NewSandbox(t)
	writeWorkflowConfig(t, s.HydraHome)
	// The run fails for want of a head; what is asserted is the stored record.
	_, _, _ = run(t, "workflow", "run", "--id", "wf-needs",
		"--step", "extract", "--needs", "",
		"--step", "summarise", "--needs", "1",
		"--step", "draft replies", "--needs", " 1 ,")
	w, err := workflow.Load("wf-needs")
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range [][]int{{}, {1}, {1}} {
		if got := w.Steps[i].Needs; !slices.Equal(got, want) {
			t.Errorf("step %d needs %v, want %v", i+1, got, want)
		}
	}
	if got := stripANSICodes(needsLabel(w, 0)); got != "needs nothing" {
		t.Errorf("step 1 is labelled %q, want needs nothing", got)
	}

	for _, tc := range []struct {
		name, want string
		args       []string
	}{
		{"count mismatch", "one --needs per --step",
			[]string{"workflow", "run", "--step", "a", "--step", "b", "--needs", "1"}},
		{"not a number", "not a step number",
			[]string{"workflow", "run", "--step", "a", "--needs", "first"}},
		{"a later step", "comes after it",
			[]string{"workflow", "run", "--step", "a", "--step", "b", "--needs", "2", "--needs", "1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testutil.NewSandbox(t)
			_, _, err := run(t, tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to say %q", err, tc.want)
			}
		})
	}
}

// A plain chain prints no needs line; one with side-by-side steps names each.
func TestNeedsLabel_OnlyWhenNotAChain(t *testing.T) {
	chain := workflow.Workflow{Steps: []workflow.Step{{N: 1}, {N: 2}}}
	if got := needsLabel(chain, 1); got != "" {
		t.Errorf("a chain step is labelled %q, want nothing", got)
	}
	fan := workflow.Workflow{Steps: []workflow.Step{{N: 1}, {N: 2, Needs: []int{1}}, {N: 3, Needs: []int{1}}}}
	if got := needsLabel(fan, 2); got != "needs 1" {
		t.Errorf("step 3 is labelled %q, want needs 1", got)
	}
}

// --writes N belongs to --step N, comma separated, and show prints it.
func TestWorkflowRun_WritesPairsWithStep(t *testing.T) {
	s := testutil.NewSandbox(t)
	writeWorkflowConfig(t, s.HydraHome)
	// The run fails for want of a head; what is asserted is the stored record.
	_, _, _ = run(t, "workflow", "run", "--id", "wf-writes",
		"--step", "reply to ticket 12", "--writes", "doc:replies#ticket-12, record:tickets/12",
		"--step", "update the changelog", "--writes", "CHANGELOG.md")
	w, err := workflow.Load("wf-writes")
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range [][]string{{"doc:replies#ticket-12", "record:tickets/12"}, {"CHANGELOG.md"}} {
		if got := w.Steps[i].Writes; !slices.Equal(got, want) {
			t.Errorf("step %d writes %v, want %v", i+1, got, want)
		}
	}
	out := stripANSICodes(captureStdout(t, func() { printWorkflow(w) }))
	if !strings.Contains(out, "writes doc:replies#ticket-12, record:tickets/12") {
		t.Errorf("the table does not say what step 1 writes:\n%s", out)
	}

	_, _, err = run(t, "workflow", "run", "--step", "a", "--step", "b", "--writes", "x")
	if err == nil || !strings.Contains(err.Error(), "one --writes per --step") {
		t.Errorf("a short --writes list: err = %v", err)
	}
}

// The coupling is read only when a step writes a file, and from the working
// directory's graph.json.
func TestCoupleFiles_OnlyForFileWrites(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	graphJSON := `{"nodes":[{"id":"a","file":"a.go"},{"id":"b","file":"b.go"}],"edges":[{"from":"a","to":"b"}]}`
	if err := os.WriteFile(filepath.Join(dir, "graph.json"), []byte(graphJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	docs := workflow.Workflow{Steps: []workflow.Step{{Writes: []string{"doc:replies"}}}}
	coupleFiles(&docs)
	if docs.Coupled != nil {
		t.Error("a workflow writing no file loaded the code graph")
	}
	code := workflow.Workflow{Steps: []workflow.Step{{Writes: []string{"a.go"}}}}
	coupleFiles(&code)
	if code.Coupled == nil || !code.Coupled("a.go", "b.go") {
		t.Error("a workflow writing a.go did not see a.go and b.go as coupled")
	}
}
