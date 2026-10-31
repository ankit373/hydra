// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/pending"
	"github.com/ankit373/hydra/internal/testutil"
)

// hyctl is the whole interface for anyone not running the desktop app, so a
// policy that asks must not be able to park a task the CLI cannot resume.
func TestCLI_AskList_EmptyIsNotAnError(t *testing.T) {
	testutil.NewSandbox(t)

	out, cobraOut, err := run(t, "ask", "list")
	if err != nil {
		t.Fatalf("ask list with nothing parked should succeed: %v (%s)", err, cobraOut)
	}
	if !strings.Contains(out, "nothing is waiting on you") {
		t.Errorf("output does not say the queue is empty: %q", out)
	}
}

// A machine-readable empty queue must be [], not null: a consumer doing
// `.length` on null gets a different failure than an empty list.
func TestCLI_AskList_JSONEmptyIsAnArray(t *testing.T) {
	testutil.NewSandbox(t)

	out, _, err := run(t, "ask", "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got []pending.Question
	if uErr := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); uErr != nil {
		t.Fatalf("output is not a JSON array: %v (%q)", uErr, out)
	}
	if len(got) != 0 {
		t.Errorf("want an empty array, got %d entries", len(got))
	}
}

func TestCLI_AskList_ShowsTheQuestionAndHowToAnswer(t *testing.T) {
	testutil.NewSandbox(t)
	if err := pending.Save(pending.Question{
		TaskID: "task-42", Question: "Allow gated to run this task?",
		Prompt: "do the thing", Head: "gated",
		AskedAt: time.Now().Add(-90 * time.Minute).UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	out, _, err := run(t, "ask", "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"task-42", "gated", "Allow gated to run this task?", "1h ago", "hyctl ask answer"} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
}

// Answering something that is not parked must say so rather than resolving to
// an empty question and dispatching.
func TestCLI_AskAnswer_UnknownTaskFails(t *testing.T) {
	testutil.NewSandbox(t)

	if _, _, err := run(t, "ask", "answer", "no-such-task", "yes"); err == nil {
		t.Error("answering an unknown task should fail")
	}
}

func TestCLI_AskDecline_ConsumesTheQuestion(t *testing.T) {
	testutil.NewSandbox(t)
	if err := pending.Save(pending.Question{
		TaskID: "task-9", Question: "Allow gated?", Prompt: "p", Head: "gated",
	}); err != nil {
		t.Fatal(err)
	}

	if _, cobraOut, err := run(t, "ask", "decline", "task-9", "not today"); err != nil {
		t.Fatalf("decline: %v (%s)", err, cobraOut)
	}
	if _, err := pending.Load("task-9"); err == nil {
		t.Error("decline left the question parked")
	}
}

func TestHumanAge(t *testing.T) {
	cases := map[time.Duration]string{
		30 * time.Second: "just now",
		5 * time.Minute:  "5m ago",
		3 * time.Hour:    "3h ago",
		50 * time.Hour:   "2d ago",
		90 * time.Minute: "1h ago",
	}
	for d, want := range cases {
		if got := humanAge(d); got != want {
			t.Errorf("humanAge(%v) = %q, want %q", d, got, want)
		}
	}
}
