// SPDX-License-Identifier: MIT

package dispatch

import (
	"context"
	"testing"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/policy"
	"github.com/ankit373/hydra/internal/pricing"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/runlog"
)

// registryHead is executor.Supports()-eligible without any API key, so these
// tests exercise real selection rather than being filtered out.
func rlHead(id string, capScore int) provider.Head {
	return provider.Head{
		ID: id, Name: id, Provider: "agy", Source: "registry",
		CapScore: capScore, AuthReady: true,
	}
}

func rlDispatcher(heads ...provider.Head) *Dispatcher {
	return &Dispatcher{
		cfg:     &config.Config{},
		heads:   heads,
		policy:  policy.New(policy.DefaultRules(false)),
		pricing: pricing.Load(),
	}
}

// A dispatch must leave a reconstructable trail: which head was selected, and
// how the call ended. Nothing in cost.jsonl/dispatch.jsonl records the
// selection moment or a failed candidate.
func TestDispatch_WritesRunLog(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	d := rlDispatcher(rlHead("h1", 90))
	// The head is selectable but cannot actually execute in a test environment,
	// so this exercises the select-then-fail path, which is the one nothing
	// else in Hydra records.
	_, err := d.Dispatch(context.Background(), "hello", Options{
		RunID: "run-rl", TaskID: "task-rl",
	})
	if err == nil {
		t.Log("dispatch unexpectedly succeeded; the assertions below still hold")
	}

	events, loadErr := runlog.Load("run-rl")
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(events) == 0 {
		t.Fatal("dispatch wrote no run-log events")
	}

	var sawSelected bool
	for _, e := range events {
		if e.RunID != "run-rl" {
			t.Errorf("event run_id = %q, want run-rl", e.RunID)
		}
		if e.TaskID != "task-rl" {
			t.Errorf("event task_id = %q, want task-rl", e.TaskID)
		}
		if e.Kind == runlog.KindHeadSelected {
			sawSelected = true
			if e.Head != "h1" {
				t.Errorf("head_selected recorded head %q, want h1", e.Head)
			}
		}
	}
	if !sawSelected {
		t.Error("no head_selected event, the routing decision was not recorded")
	}
}

// Every candidate that fails must be recorded, because that is the only trace
// of why the fallback chain advanced.
func TestDispatch_RecordsFailedCandidates(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	// Three candidates, none of which can execute here.
	d := rlDispatcher(rlHead("h1", 95), rlHead("h2", 85), rlHead("h3", 75))
	_, _ = d.Dispatch(context.Background(), "hello", Options{RunID: "run-fb"})

	events, err := runlog.Load("run-fb")
	if err != nil {
		t.Fatal(err)
	}

	selected, failed := 0, 0
	for _, e := range events {
		switch e.Kind {
		case runlog.KindHeadSelected:
			selected++
		case runlog.KindError:
			failed++
		}
	}
	if selected == 0 {
		t.Fatal("no candidates recorded as selected")
	}
	// Each attempted candidate should produce a selection and, since none can
	// run here, a matching failure.
	if failed != selected {
		t.Errorf("%d selections but %d failures, every attempted candidate should record its outcome",
			selected, failed)
	}
	// Events must be in append order with monotonic sequence.
	for i := 1; i < len(events); i++ {
		if events[i].Seq <= events[i-1].Seq {
			t.Errorf("event %d seq %d not greater than previous %d", i, events[i].Seq, events[i-1].Seq)
		}
	}
}

// Two runs must never contaminate each other, the per-run-file design exists
// precisely so a reader of one run never sees another's events.
func TestDispatch_RunsAreIsolated(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	d := rlDispatcher(rlHead("h1", 90))
	_, _ = d.Dispatch(context.Background(), "a", Options{RunID: "run-a"})
	_, _ = d.Dispatch(context.Background(), "b", Options{RunID: "run-b"})

	for _, id := range []string{"run-a", "run-b"} {
		events, err := runlog.Load(id)
		if err != nil {
			t.Fatal(err)
		}
		if len(events) == 0 {
			t.Errorf("run %s has no events", id)
		}
		for _, e := range events {
			if e.RunID != id {
				t.Errorf("run %s contains an event from %s", id, e.RunID)
			}
		}
	}
}
