// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/testutil"
	"github.com/ankit373/hydra/internal/workflow"
)

// capturedDispatch records the options a step really dispatched with. Asserting
// on the builder alone would leave the call site free to rebuild them by hand,
// which is the defect rather than a variant of it (#996).
func capturedDispatch(opts *dispatch.Options, ctxOut *context.Context) func(context.Context, string, dispatch.Options) (*dispatch.Result, error) {
	return func(ctx context.Context, _ string, o dispatch.Options) (*dispatch.Result, error) {
		*opts, *ctxOut = o, ctx
		return &dispatch.Result{Output: "ok"}, nil
	}
}

const workflowCapPolicy = `version: "1.0"
defaults:
  max_cost_usd: 0.25
  max_wall_seconds: 30
`

// hyctl workflow is the one command designed to fire N dispatches in sequence
// and it was the only dispatch path with no wallet guard at all (#1161).
func TestWorkflowStep_CarriesThePolicyCeilingAndDeadline(t *testing.T) {
	s := testutil.NewSandbox(t)
	writeWorkflowPolicy(t, s.HydraHome, workflowCapPolicy)

	var got dispatch.Options
	var seen context.Context
	r := dispatchRouter{dispatchFn: capturedDispatch(&got, &seen)}
	if _, err := r.Route(context.Background(), "write the fix", "SIMPLE"); err != nil {
		t.Fatal(err)
	}

	if got.MaxCostUSD != 0.25 {
		t.Errorf("MaxCostUSD = %v, want 0.25 from policy.yaml: a workflow spends a dispatch's money once per step", got.MaxCostUSD)
	}
	if got.MaxCostSource != "policy.yaml max_cost_usd" {
		t.Errorf("MaxCostSource = %q, want the policy file named", got.MaxCostSource)
	}
	if _, ok := seen.Deadline(); !ok {
		t.Error("the step ran with no deadline; policy.yaml max_wall_seconds is inert")
	}
}

// A deadline is the policy refusing rather than the head failing, and the two
// want different answers from whoever reads the step.
func TestWorkflowStep_WallCeilingIsReportedAsThePolicyRefusing(t *testing.T) {
	s := testutil.NewSandbox(t)
	writeWorkflowPolicy(t, s.HydraHome, "version: \"1.0\"\ndefaults:\n  max_wall_seconds: 1\n")

	r := dispatchRouter{dispatchFn: func(ctx context.Context, _ string, _ dispatch.Options) (*dispatch.Result, error) {
		// What a killed CLI head surfaces as: no deadline for errors.Is to find.
		// The timer bounds a run where no deadline was set at all, so that case
		// fails rather than hanging out the suite's own timeout.
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}
		return nil, errors.New("signal: killed")
	}}
	_, err := r.Route(context.Background(), "p", "SIMPLE")
	if err == nil || !strings.Contains(err.Error(), "max_wall_seconds_exceeded") {
		t.Fatalf("error = %v, want the policy's own wall-clock refusal", err)
	}
}

// --local and --max-cost are stored on the record, so they reach every step of
// a resumed run and not only the one the flag was typed on.
func TestWorkflowStep_StoredRunSettingsReachTheDispatch(t *testing.T) {
	s := testutil.NewSandbox(t)
	writeWorkflowPolicy(t, s.HydraHome, workflowCapPolicy)
	ceiling := 0.01

	r := routerFor(nil, workflow.Workflow{
		System: "be terse", LocalOnly: true, MaxCostUSD: &ceiling,
	})
	got, _, err := r.options("p", "SIMPLE")
	if err != nil {
		t.Fatal(err)
	}
	if !got.LocalOnly {
		t.Error("LocalOnly did not reach the step; a --local workflow would route to paid heads")
	}
	if got.System != "be terse" {
		t.Errorf("System = %q, want the stored prompt", got.System)
	}
	if got.MaxCostUSD != ceiling || got.MaxCostSource != "--max-cost" {
		t.Errorf("ceiling = %v from %q, want %v from the flag", got.MaxCostUSD, got.MaxCostSource, ceiling)
	}
}

// --max-cost 0 lifts a policy ceiling for one run, the case a zero check gets
// wrong. Absent means nil, which is what lets policy.yaml decide.
func TestWorkflowStep_ExplicitZeroCeilingLiftsThePolicy(t *testing.T) {
	s := testutil.NewSandbox(t)
	writeWorkflowPolicy(t, s.HydraHome, workflowCapPolicy)

	zero := 0.0
	got, _, err := routerFor(nil, workflow.Workflow{MaxCostUSD: &zero}).options("p", "SIMPLE")
	if err != nil {
		t.Fatal(err)
	}
	if got.MaxCostUSD != 0 || got.MaxCostSource != "--max-cost" {
		t.Errorf("ceiling = %v from %q, want 0 from the flag", got.MaxCostUSD, got.MaxCostSource)
	}
}

// `run --id` of a stored id overwrote the record and corrupted its trace; the
// id is how `resume` finds work somebody is waiting on (#1168).
func TestWorkflowRun_RefusesAnIDAlreadyStored(t *testing.T) {
	testutil.NewSandbox(t)
	storedWorkflow(t, "wf1")

	_, _, err := run(t, "workflow", "run", "--id", "wf1", "--step", "something else")
	if err == nil {
		t.Fatal("run --id of a stored workflow was accepted")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error does not say the id is taken: %v", err)
	}

	back, lerr := workflow.Load("wf1")
	if lerr != nil {
		t.Fatal(lerr)
	}
	if len(back.Steps) != 2 || back.Steps[0].Output != "TestFoo is flaky" {
		t.Error("the stored workflow was overwritten by the refused run")
	}
}

// The same hole by another door: run --id of a workflow still running would
// start a second runner over it (#1162).
func TestWorkflowRun_RefusesAnIDThatIsStillRunning(t *testing.T) {
	testutil.NewSandbox(t)
	markWorkflowRunning(t, "wf1")

	_, _, err := run(t, "workflow", "run", "--id", "wf1", "--step", "x")
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("error = %v, want run --id of a live workflow refused", err)
	}
}

// Resuming a live workflow double-dispatched every remaining step. Refused
// before a dispatcher is opened or a run declared, so nothing claims to have
// started (#1162).
func TestWorkflowResume_RefusesALiveWorkflow(t *testing.T) {
	testutil.NewSandbox(t)
	markWorkflowRunning(t, "wf1")

	out, _, err := run(t, "workflow", "resume", "wf1")
	if !errors.Is(err, workflow.ErrAlreadyRunning) {
		t.Fatalf("error = %v, want %v", err, workflow.ErrAlreadyRunning)
	}
	if strings.Contains(stripANSICodes(out), "▶ WORKFLOW") {
		t.Errorf("a refused resume announced a run it never started:\n%s", out)
	}
}

// An all-steps-done record whose status says running read as interrupted for
// ever, and resume would not clear it; only `rm` did (#1168).
func TestWorkflowResume_FinishesARecordWithNothingLeft(t *testing.T) {
	testutil.NewSandbox(t)
	w := storedWorkflow(t, "wf1")
	for i := range w.Steps {
		w.Steps[i].Status = workflow.Done
		w.Steps[i].Output = "done"
	}
	w.Status = workflow.Running // what the last writer knew, and it was killed
	if err := workflow.Save(w); err != nil {
		t.Fatal(err)
	}

	if _, _, err := run(t, "workflow", "resume", "wf1"); err != nil {
		t.Fatal(err)
	}
	back, err := workflow.Load("wf1")
	if err != nil {
		t.Fatal(err)
	}
	if back.Observed() != workflow.Done {
		t.Errorf("status reads %q after a resume with nothing left, want %q", back.Observed(), workflow.Done)
	}
}

// A resumed workflow must run under the instructions it started with, and a
// --system given to resume overrides them (#1168).
//
// Read back off the record the run itself wrote before its first step, which
// is the copy every later step is dispatched from.
func TestWorkflowResume_KeepsOrOverridesTheStoredSystemPrompt(t *testing.T) {
	s := testutil.NewSandbox(t)
	writeWorkflowConfig(t, s.HydraHome)
	w := storedWorkflow(t, "wf1")
	w.System = "answer in French"
	if err := workflow.Save(w); err != nil {
		t.Fatal(err)
	}

	// The step itself fails: the sandbox has no heads. What is asserted is
	// what the run recorded on its way into that step.
	_, _, _ = run(t, "workflow", "resume", "wf1")
	back, err := workflow.Load("wf1")
	if err != nil {
		t.Fatal(err)
	}
	if back.System != "answer in French" {
		t.Errorf("System = %q after a resume with no --system, want the stored one", back.System)
	}

	_, _, _ = run(t, "workflow", "resume", "wf1", "--system", "answer in German")
	if back, err = workflow.Load("wf1"); err != nil {
		t.Fatal(err)
	}
	if back.System != "answer in German" {
		t.Errorf("System = %q, want resume's own --system to win", back.System)
	}
}

// --dry-run prints the routing chain before any money moves, and must leave
// nothing behind: a preview that stored a record would leave a workflow nobody
// started.
func TestWorkflowRun_DryRunStoresNothing(t *testing.T) {
	s := testutil.NewSandbox(t)
	writeWorkflowConfig(t, s.HydraHome)
	// Both fail for want of a head; what is asserted is what each left behind.
	_, _, _ = run(t, "workflow", "run", "--dry-run", "--id", "wf-dry", "--step", "a")
	if ok, err := workflow.Exists("wf-dry"); err != nil || ok {
		t.Error("--dry-run stored a workflow nobody started")
	}

	_, _, _ = run(t, "workflow", "run", "--id", "wf-wet", "--step", "a")
	if ok, err := workflow.Exists("wf-wet"); err != nil || !ok {
		t.Fatal("a real run recorded nothing, so the dry-run assertion above proves nothing")
	}
}

// %-10s counts an ANSI escape as width, so a styled STATUS pushed every column
// after it left on a colour terminal (#1168).
func TestWorkflowRow_AlignsWhenCellsAreStyled(t *testing.T) {
	const green, reset = "\x1b[32m", "\x1b[0m"
	header := workflowHeader()
	row := stripANSICodes(workflowRow(1, "triage", green+"done"+reset, "T10", green+"qwen3:8b"+reset, "5.8s"))

	for _, c := range []struct{ head, cell string }{
		{"STATUS", "done"}, {"TIER", "T10"}, {"HEAD", "qwen3:8b"},
	} {
		want, got := strings.Index(header, c.head), strings.Index(row, c.cell)
		if want != got {
			t.Errorf("%s column starts at %d in the header and %d in the row:\n%s\n%s", c.head, want, got, header, row)
		}
	}

	listRow := stripANSICodes(workflowListRow("wf1", green+"done"+reset, "2/2", "$0.0010", "fix it"))
	if want, got := strings.Index(workflowListHeader(), "STEPS"), strings.Index(listRow, "2/2"); want != got {
		t.Errorf("list STEPS column starts at %d in the header and %d in the row:\n%s", want, got, listRow)
	}
}

func TestPadCell_MeasuresDisplayCellsNotBytes(t *testing.T) {
	styled := "\x1b[32mdone\x1b[0m"
	if got := len(stripANSICodes(padCell(styled, 10))); got != 10 {
		t.Errorf("padCell rendered %d display cells, want 10", got)
	}
	if got := len(stripANSICodes(rpadCell(styled, 10))); got != 10 {
		t.Errorf("rpadCell rendered %d display cells, want 10", got)
	}
	// Never truncates: cutting is not ANSI-aware and a half-cut escape
	// corrupts the frame, so callers truncate the raw value first.
	if got := padCell("abcdef", 3); got != "abcdef" {
		t.Errorf("padCell(%q, 3) = %q, want it left whole", "abcdef", got)
	}
}

func writeWorkflowPolicy(t *testing.T, home, body string) {
	t.Helper()
	dir := filepath.Join(home, "registry")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "policy.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// markWorkflowRunning stores a workflow and beats its heartbeat file, which is
// what Observed() reads. The path is derived from workflow.Path rather than
// written down, so it cannot drift from the package's own.
func markWorkflowRunning(t *testing.T, id string) {
	t.Helper()
	w := storedWorkflow(t, id)
	w.Status = workflow.Running
	w.Steps[1].Status = workflow.Running
	if err := workflow.Save(w); err != nil {
		t.Fatal(err)
	}
	path, err := workflow.Path(id)
	if err != nil {
		t.Fatal(err)
	}
	beat := strings.TrimSuffix(path, ".json") + ".alive"
	f, err := os.Create(beat)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	now := time.Now()
	if err := os.Chtimes(beat, now, now); err != nil {
		t.Fatal(err)
	}
}

// writeWorkflowConfig is what makes dispatch.New succeed in a sandbox, so a
// run reaches the runner rather than stopping at "run hyctl init".
func writeWorkflowConfig(t *testing.T, home string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[openrouter]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}
