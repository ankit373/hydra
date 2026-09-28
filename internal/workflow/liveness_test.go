// SPDX-License-Identifier: MIT

package workflow

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/testutil"
)

func runningWorkflow(t *testing.T, id string) Workflow {
	t.Helper()
	w := saved(t, id, []Step{{Prompt: "a"}, {Prompt: "b"}})
	w.Status = Running
	w.Steps[0].Status = Running
	if err := Save(w); err != nil {
		t.Fatal(err)
	}
	return w
}

// The symptom: a killed run keeps saying "running" and nothing tells anyone to
// resume it (#898).
func TestObserved_ARunWithNoHeartbeatIsInterrupted(t *testing.T) {
	testutil.NewSandbox(t)
	w := runningWorkflow(t, "dead")

	if got := w.Observed(); got != Interrupted {
		t.Errorf("Observed() = %q, want %q: the process that wrote `running` is gone", got, Interrupted)
	}
	if got := ObservedStep(w.Steps[0].Status, w.Observed()); got != Interrupted {
		t.Errorf("step status = %q, want %q: it is not running either", got, Interrupted)
	}
}

func TestObserved_ALiveHeartbeatIsStillRunning(t *testing.T) {
	testutil.NewSandbox(t)
	w := runningWorkflow(t, "live")

	stop := heartbeat(w.ID)
	if got := w.Observed(); got != Running {
		t.Errorf("Observed() = %q while the heartbeat is being written, want %q", got, Running)
	}
	stop()
	if got := w.Observed(); got != Interrupted {
		t.Errorf("Observed() = %q after the writer stopped, want %q", got, Interrupted)
	}
}

// A beat older than the timeout is a dead writer, not a slow one.
func TestObserved_AStaleHeartbeatIsInterrupted(t *testing.T) {
	testutil.NewSandbox(t)
	w := runningWorkflow(t, "stale")

	path, err := beatPath(w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if f, err := os.Create(path); err == nil {
		_ = f.Close()
	}
	old := time.Now().Add(-beatTimeout - time.Second)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	if got := w.Observed(); got != Interrupted {
		t.Errorf("Observed() = %q with a beat %v old, want %q", got, beatTimeout+time.Second, Interrupted)
	}
}

// One missed beat is a slow disk. The window has to be wider than the interval
// or an ordinary hiccup reports a live run as dead.
func TestObserved_ARecentBeatSurvivesOneMissedInterval(t *testing.T) {
	testutil.NewSandbox(t)
	w := runningWorkflow(t, "hiccup")

	path, err := beatPath(w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if f, err := os.Create(path); err == nil {
		_ = f.Close()
	}
	missed := time.Now().Add(-beatInterval - time.Second)
	if err := os.Chtimes(path, missed, missed); err != nil {
		t.Fatal(err)
	}

	if got := w.Observed(); got != Running {
		t.Errorf("Observed() = %q one missed beat in, want %q", got, Running)
	}
}

func TestObserved_LeavesAFinishedRunAlone(t *testing.T) {
	testutil.NewSandbox(t)
	for _, status := range []Status{Done, Failed, Pending} {
		w := saved(t, "wf-"+string(status), []Step{{Prompt: "a"}})
		w.Status = status
		if got := w.Observed(); got != status {
			t.Errorf("Observed() = %q for a stored %q, want it unchanged", got, status)
		}
	}
}

// A reader that repaired the stored status would race the writer it is trying
// to describe, so reading must not write.
func TestObserved_DoesNotTouchTheRecord(t *testing.T) {
	testutil.NewSandbox(t)
	w := runningWorkflow(t, "readonly")

	path, err := Path(w.ID)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	_ = w.Observed()
	loaded, err := Load(w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != Running {
		t.Errorf("stored status = %q, want it left at %q", loaded.Status, Running)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Error("Observed() rewrote the record")
	}
	afterInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !afterInfo.ModTime().Equal(beforeInfo.ModTime()) {
		t.Error("Observed() touched the record's mtime")
	}
}

type beatRouter struct {
	seen func()
}

func (b beatRouter) Route(context.Context, string, string) (StepResult, error) {
	if b.seen != nil {
		b.seen()
	}
	return StepResult{Output: "ok", Head: "stub", Model: "stub"}, nil
}

// While a run is in flight it must read as running, and once it ends its beat
// file must not be left behind claiming otherwise.
func TestRun_BeatsWhileItRunsAndClearsUpAfter(t *testing.T) {
	testutil.NewSandbox(t)
	w := saved(t, "beating", []Step{{Prompt: "a"}})

	var observedMidRun Status
	r := beatRouter{seen: func() {
		mid, err := Load(w.ID)
		if err != nil {
			t.Error(err)
			return
		}
		observedMidRun = mid.Observed()
	}}

	done, err := Run(context.Background(), w, r, func(x Workflow) error { return Save(x) })
	if err != nil {
		t.Fatal(err)
	}
	if observedMidRun != Running {
		t.Errorf("mid-run Observed() = %q, want %q", observedMidRun, Running)
	}
	if done.Status != Done {
		t.Fatalf("workflow status = %q", done.Status)
	}

	path, err := beatPath(w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("heartbeat file survived the run (%v); a finished run would read as live", err)
	}
}

func TestObservedStep(t *testing.T) {
	cases := []struct {
		step, run, want Status
	}{
		{Running, Interrupted, Interrupted},
		{Running, Running, Running},
		{Done, Interrupted, Done},
		{Pending, Interrupted, Pending},
		{Failed, Interrupted, Failed},
	}
	for _, c := range cases {
		if got := ObservedStep(c.step, c.run); got != c.want {
			t.Errorf("ObservedStep(%q, %q) = %q, want %q", c.step, c.run, got, c.want)
		}
	}
}

// The store directory is created by the first Save, which happens after Run
// starts beating. Writing the first beat into a directory that does not exist
// yet left a live run reading as interrupted until the first tick, seconds in.
func TestHeartbeat_BeatsBeforeAnythingHasBeenSaved(t *testing.T) {
	testutil.NewSandbox(t)

	stop := heartbeat("fresh")
	defer stop()

	if !alive("fresh") {
		t.Error("no heartbeat before the first Save, so a run that has just started reads as interrupted")
	}
}
