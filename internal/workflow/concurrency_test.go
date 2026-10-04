// SPDX-License-Identifier: MIT

package workflow

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/testutil"
)

// The reported repro: resume a workflow that is still running and a second
// runner dispatches every remaining step a second time (#1162).
func TestRun_RefusesASecondRunnerWhileTheFirstIsMidStep(t *testing.T) {
	testutil.NewSandbox(t)
	w := saved(t, "wf-conc", []Step{{Prompt: "a"}, {Prompt: "b"}})

	second := &stubRouter{}
	var secondErr error
	first := &stubRouter{answer: func(n int, _, _ string) (StepResult, error) {
		if n == 1 {
			_, secondErr = Run(context.Background(), w, second, func(Workflow) error { return nil })
		}
		return StepResult{Output: "ok", Head: "stub", Model: "stub"}, nil
	}}

	if _, err := Run(context.Background(), w, first, Save); err != nil {
		t.Fatalf("the first runner failed: %v", err)
	}
	if !errors.Is(secondErr, ErrAlreadyRunning) {
		t.Fatalf("the second runner returned %v, want %v", secondErr, ErrAlreadyRunning)
	}
	if len(second.calls) != 0 {
		t.Errorf("the second runner dispatched %d step(s); every remaining step would have run twice", len(second.calls))
	}
	if len(first.calls) != 2 {
		t.Errorf("the first runner made %d calls, want 2", len(first.calls))
	}
}

// The refusal must not outlive the process it refuses for: a killed run leaves
// its beat file behind, and a workflow nothing is running must stay resumable.
func TestRun_TakesOverABeatWhoseWriterIsGone(t *testing.T) {
	testutil.NewSandbox(t)
	w := saved(t, "wf-stale", []Step{{Prompt: "a"}})

	path, err := beatPath(w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if f, cerr := os.Create(path); cerr == nil {
		_ = f.Close()
	}
	old := time.Now().Add(-beatTimeout - time.Second)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	r := &stubRouter{}
	if _, err := Run(context.Background(), w, r, Save); err != nil {
		t.Fatalf("a workflow whose runner is gone is not resumable: %v", err)
	}
	if len(r.calls) != 1 {
		t.Errorf("made %d calls, want 1", len(r.calls))
	}
}

// The command's own three-step example needs step 3 to see step 1: it explains
// why the first failing test fails, which only step 1 listed (#1168).
func TestStepPrompt_CarriesEveryPriorOutput(t *testing.T) {
	w := Workflow{
		Task: "fix the flaky test",
		Steps: []Step{
			{N: 1, Title: "list", Prompt: "list the failing tests", Output: "TestFoo is flaky"},
			{N: 2, Title: "explain", Prompt: "explain the first", Output: "it races on a map"},
			{N: 3, Title: "fix", Prompt: "write the fix"},
		},
	}
	got := w.stepPrompt(2)

	for _, want := range []string{
		"BEGIN STEP 1 OUTPUT (list)", "TestFoo is flaky",
		"BEGIN STEP 2 OUTPUT (explain)", "it races on a map",
		"write the fix",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("step 3's prompt is missing %q:\n%s", want, got)
		}
	}
	// Order matters: a step reads its chain forwards.
	if strings.Index(got, "TestFoo is flaky") > strings.Index(got, "it races on a map") {
		t.Errorf("prior outputs are not in step order:\n%s", got)
	}
	// A step that produced nothing contributes no empty fence.
	if n := strings.Count(got, "BEGIN STEP"); n != 2 {
		t.Errorf("got %d fenced outputs, want 2", n)
	}
}

// The record is what a resume reads its settings back out of; without them a
// resumed workflow runs under different instructions than it started with.
func TestSaveLoad_KeepsTheRunSettings(t *testing.T) {
	testutil.NewSandbox(t)
	w := saved(t, "wf-opts", []Step{{Prompt: "a"}})
	ceiling := 0.25
	w.System, w.LocalOnly, w.MaxCostUSD = "be terse", true, &ceiling
	if err := Save(w); err != nil {
		t.Fatal(err)
	}

	back, err := Load(w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if back.System != "be terse" {
		t.Errorf("System = %q, want it persisted", back.System)
	}
	if !back.LocalOnly {
		t.Error("LocalOnly did not survive; a resumed --local workflow would route to paid heads")
	}
	if back.MaxCostUSD == nil || *back.MaxCostUSD != ceiling {
		t.Errorf("MaxCostUSD = %v, want %v", back.MaxCostUSD, ceiling)
	}
}

// nil is "no explicit ceiling", which policy.yaml then decides. Stored as a
// zero it would read as --max-cost 0, which lifts the policy ceiling instead.
func TestSaveLoad_NoExplicitCeilingStaysAbsent(t *testing.T) {
	testutil.NewSandbox(t)
	w := saved(t, "wf-noceil", []Step{{Prompt: "a"}})
	back, err := Load(w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if back.MaxCostUSD != nil {
		t.Errorf("MaxCostUSD = %v, want nil for a run that named none", *back.MaxCostUSD)
	}
}

func TestExists(t *testing.T) {
	testutil.NewSandbox(t)
	if ok, err := Exists("nope"); err != nil || ok {
		t.Errorf("Exists(nope) = %v, %v; want false, nil", ok, err)
	}
	saved(t, "wf-there", []Step{{Prompt: "a"}})
	if ok, err := Exists("wf-there"); err != nil || !ok {
		t.Errorf("Exists(wf-there) = %v, %v; want true, nil", ok, err)
	}
	if _, err := Exists("../../etc/passwd"); err == nil {
		t.Error("a traversing id was accepted")
	}
}
