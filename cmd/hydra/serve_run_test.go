// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"testing"

	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/serve"
)

// askServe routes one request through the adapter under the given request id.
func askServe(t *testing.T, d *dispatch.Dispatcher, id, prompt string) {
	t.Helper()
	_, err := serveRouter{d: d, defaultEnum: "MODERATE"}.Chat(context.Background(), serve.Request{
		RequestID: id,
		Messages:  []executor.Message{{Role: "user", Content: prompt}},
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A server's unit of work is the request. One run per process gave a long-lived
// server a single ever-growing run, so `hyctl trace view` with no argument
// showed the session rather than the last request (#1149).
func TestServeRouter_EachRequestIsItsOwnRun(t *testing.T) {
	dispatchable(t, "answer")

	ctx := context.Background()
	d, err := dispatch.New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	askServe(t, d, "req-one", "first question")
	askServe(t, d, "req-two", "second question")

	// Load answers nil for a run that was never written, so the events are
	// what says the run exists: a guard on the error alone passes under the
	// bug it is here to catch.
	for _, id := range []string{"req-one", "req-two"} {
		events, err := runlog.Load(id)
		if err != nil {
			t.Fatalf("run %q: %v", id, err)
		}
		if len(events) == 0 {
			t.Errorf("run %q holds no events, so a client holding that id has nothing to trace", id)
		}
	}
}

// A run opened by serve recorded no subject at all, so a reader fell back to
// task_started, whose Detail is the routing enum: a real question rendered as
// "MODERATE". The same shape as #910, in the one path that pass did not cover.
func TestServeRouter_RecordsWhatTheRequestWasFor(t *testing.T) {
	dispatchable(t, "answer")

	ctx := context.Background()
	d, err := dispatch.New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	askServe(t, d, "req-subject", "rotate the signing key")

	events, err := runlog.Load("req-subject")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("the request recorded no events at all")
	}
	for _, e := range events {
		if e.Kind == runlog.KindRunStarted {
			if e.Detail == "" {
				t.Fatal("the run was declared with no subject, so a reader falls back to the routing enum")
			}
			return
		}
	}
	t.Fatal("no run_started event: the run records nothing about what it was for")
}
