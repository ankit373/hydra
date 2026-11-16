// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"slices"
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

// chatTurn sends one turn of a chat: the opening plus every later message.
func chatTurn(t *testing.T, d *dispatch.Dispatcher, id string, h string, msgs ...executor.Message) {
	t.Helper()
	_, err := serveRouter{d: d, defaultEnum: "MODERATE"}.Chat(context.Background(), serve.Request{
		RequestID: id, Messages: msgs, Conversation: h,
	})
	if err != nil {
		t.Fatal(err)
	}
}

// conversationOf is the key a run's task_started recorded, empty if none.
func conversationOf(t *testing.T, id string) string {
	t.Helper()
	events, err := runlog.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Kind == runlog.KindTaskStarted {
			c, _ := e.Meta[runlog.MetaConversation].(string)
			return c
		}
	}
	t.Fatalf("run %s has no task_started event", id)
	return ""
}

func TestServe_SameOpeningSameConversation(t *testing.T) {
	dispatchable(t, "answer")
	d, err := dispatch.New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	sys := executor.Message{Role: "system", Content: "You answer billing questions."}
	first := executor.Message{Role: "user", Content: "Why was I charged twice?"}
	reply := executor.Message{Role: "assistant", Content: "answer"}
	turns := []string{"20261115T100000Z-t1", "20261115T100100Z-t2", "20261115T100200Z-t3"}
	chatTurn(t, d, turns[0], "", sys, first)
	chatTurn(t, d, turns[1], "", sys, first, reply, executor.Message{Role: "user", Content: "Can you refund one?"})
	chatTurn(t, d, turns[2], "", sys, first, reply, executor.Message{Role: "user", Content: "Can you refund one?"},
		reply, executor.Message{Role: "user", Content: "Thanks, close it."})
	chatTurn(t, d, "20261115T100300Z-other", "", sys, executor.Message{Role: "user", Content: "Draft a refund email."})

	conv := conversationOf(t, turns[0])
	if conv == "" {
		t.Fatal("a served chat recorded no conversation key")
	}
	got, err := runlog.Conversation(conv)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, turns) {
		t.Fatalf("conversation %s holds %v, want the three turns %v", conv, got, turns)
	}
	if other := conversationOf(t, "20261115T100300Z-other"); other == conv {
		t.Fatal("a different chat got the same conversation key")
	}
}

func TestServe_HeaderOverridesDerivedConversation(t *testing.T) {
	dispatchable(t, "answer")
	d, err := dispatch.New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	// A client that rewrites its opening each turn still names one chat.
	chatTurn(t, d, "20261115T110000Z-h1", "chat-42", executor.Message{Role: "user", Content: "Outline the essay."})
	chatTurn(t, d, "20261115T110100Z-h2", "chat-42", executor.Message{Role: "user", Content: "Now the intro."})
	got, err := runlog.Conversation("chat-42")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"20261115T110000Z-h1", "20261115T110100Z-h2"}; !slices.Equal(got, want) {
		t.Fatalf("header chat-42 holds %v, want %v", got, want)
	}
}
