// SPDX-License-Identifier: MIT

package dispatch

import (
	"context"
	"testing"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/recipe"
	"github.com/ankit373/hydra/internal/runlog"
)

// taskRecipe is the recipe the task span carries, as a reader of the log sees it.
func taskRecipe(t *testing.T, events []runlog.Event, taskID string) map[string]any {
	t.Helper()
	for _, e := range events {
		if e.Kind == runlog.KindTaskStarted && e.SpanID == runlog.SpanIDFor(taskID) {
			r, ok := e.Meta["recipe"].(map[string]any)
			if !ok {
				t.Fatalf("task span meta %v has no recipe", e.Meta)
			}
			return r
		}
	}
	t.Fatal("no task span")
	return nil
}

func TestDispatch_LogsTheDefaultRecipeOnTheTaskSpan(t *testing.T) {
	r := taskRecipe(t, dispatchOnce(t, "run-rcp", "task-rcp"), "task-rcp")
	if r["name"] != "single" || r["samples"] != float64(1) || r["stop"] != "none" {
		t.Errorf("task span recipe %v, want the one-call recipe", r)
	}
}

func TestDispatch_LogsTheRecipeItWasGiven(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	rcp := recipe.Default([]string{"ground"})
	d := captureDispatcher(t, &config.Config{})
	if _, err := d.Dispatch(context.Background(), "hello", Options{
		RunID: "run-rcp2", TaskID: "task-rcp2", Recipe: &rcp,
	}); err != nil {
		t.Fatal(err)
	}
	events, err := runlog.Load("run-rcp2")
	if err != nil {
		t.Fatal(err)
	}
	r := taskRecipe(t, events, "task-rcp2")
	checks, _ := r["checks"].([]any)
	if r["stop"] != "first_pass" || len(checks) != 1 || checks[0] != "ground" {
		t.Errorf("task span recipe %v, want first_pass on the ground check", r)
	}
}

func TestDispatch_RefusesARecipeThatCannotWork(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	bad := recipe.Recipe{Name: "vote", Samples: 5, Stop: recipe.StopAll}
	d := captureDispatcher(t, &config.Config{})
	if _, err := d.Dispatch(context.Background(), "hello", Options{
		RunID: "run-rcp3", TaskID: "task-rcp3", Recipe: &bad,
	}); err == nil {
		t.Fatal("a recipe with 5 samples and no check was dispatched")
	}
	if events, _ := runlog.Load("run-rcp3"); len(events) != 0 {
		t.Errorf("a refused recipe still logged %d events", len(events))
	}
}
