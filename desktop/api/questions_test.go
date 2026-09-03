// SPDX-License-Identifier: MIT

package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/pending"
)

func park(t *testing.T, taskID, question string, asked time.Time) {
	t.Helper()
	if err := pending.Save(pending.Question{
		TaskID: taskID, RunID: "run-" + taskID, Question: question,
		Prompt: "do the thing", Head: "gated", AskedAt: asked,
	}); err != nil {
		t.Fatal(err)
	}
}

// An empty queue must be an empty list, not null: a consumer doing .length on
// null fails differently than one reading an empty array.
func TestGetPendingQuestions_EmptyIsAList(t *testing.T) {
	sandbox(t)

	got := New().GetPendingQuestions()
	if got.Questions == nil {
		t.Error("Questions is nil — the bridge must send [] for an empty queue")
	}
	if len(got.Questions) != 0 || got.Error != "" {
		t.Errorf("expected a clean empty queue, got %+v", got)
	}
}

func TestGetPendingQuestions_OldestFirstWithEverythingNeededToAnswer(t *testing.T) {
	sandbox(t)
	now := time.Now().UTC()
	park(t, "newer", "Allow gated for B?", now)
	park(t, "older", "Allow gated for A?", now.Add(-2*time.Hour))

	got := New().GetPendingQuestions()
	if len(got.Questions) != 2 {
		t.Fatalf("want 2 questions, got %d", len(got.Questions))
	}
	if got.Questions[0].TaskID != "older" {
		t.Errorf("oldest question should come first, got %q", got.Questions[0].TaskID)
	}
	q := got.Questions[0]
	if q.Question == "" || q.Head == "" || q.RunID == "" {
		t.Errorf("a question missing its text, head or run cannot be acted on: %+v", q)
	}
	if q.AskedAtMS <= 0 {
		t.Error("AskedAtMs is unset, so the UI cannot say how long it has waited")
	}
}

// One unreadable file must not hide the answerable ones, or a parked task is
// forgotten because an unrelated one is corrupt.
func TestGetPendingQuestions_ReportsCorruptWithoutHidingTheRest(t *testing.T) {
	sandbox(t)
	park(t, "good", "Allow gated?", time.Now().UTC())
	if err := os.WriteFile(filepath.Join(pending.Dir(), "bad.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := New().GetPendingQuestions()
	if got.Error == "" {
		t.Error("an unreadable question must be reported")
	}
	if len(got.Questions) != 1 || got.Questions[0].TaskID != "good" {
		t.Errorf("the readable question must still be returned, got %+v", got.Questions)
	}
}

// Answering twice must not run the work twice, and the second answer must not
// read as a crash — it is an ordinary double click or a second window.
func TestAnswerQuestion_AlreadyAnsweredIsNotAlarming(t *testing.T) {
	sandbox(t)

	r, err := New().AnswerQuestion("never-parked", "yes")
	if err != nil {
		t.Fatalf("AnswerQuestion must report on the reply, not as a second error: %v", err)
	}
	if !strings.Contains(r.Error, "already answered") {
		t.Errorf("want an already-answered message, got %q", r.Error)
	}
}

// Dismissing is not answering. An empty answer leaves the task parked.
func TestAnswerQuestion_EmptyAnswerLeavesItParked(t *testing.T) {
	sandbox(t)
	park(t, "t1", "Allow gated?", time.Now().UTC())

	r, err := New().AnswerQuestion("t1", "   ")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Error, "decline it") {
		t.Errorf("want the refuse-or-decline message, got %q", r.Error)
	}
	if _, lErr := pending.Load("t1"); lErr != nil {
		t.Errorf("the task must stay parked after a non-answer: %v", lErr)
	}
}

// The run id is only recorded in the pending file, and Resume consumes it, so
// the reply has to capture it before resuming or a parked task loses its link
// into Session the moment it is answered.
func TestAnswerQuestion_ReplyKeepsTheRunLink(t *testing.T) {
	sandbox(t)
	park(t, "t1", "Allow gated?", time.Now().UTC())

	r, err := New().AnswerQuestion("t1", "yes, go ahead")
	if err != nil {
		t.Fatal(err)
	}
	if r.RunID != "run-t1" {
		t.Errorf("RunID = %q, want run-t1 — the answered task must stay linked to its run", r.RunID)
	}
	if r.TaskID != "t1" {
		t.Errorf("TaskID = %q, want t1", r.TaskID)
	}
}

// Declining needs no config: a machine that parked a task must be able to
// refuse it even when dispatch.New would fail.
func TestDeclineQuestion_ConsumesTheQuestion(t *testing.T) {
	sandbox(t)
	park(t, "t1", "Allow gated?", time.Now().UTC())

	if err := New().DeclineQuestion("t1", "not on production"); err != nil {
		t.Fatalf("DeclineQuestion: %v", err)
	}
	if _, err := pending.Load("t1"); err == nil {
		t.Error("decline left the question parked")
	}
	if got := New().GetPendingQuestions(); len(got.Questions) != 0 {
		t.Errorf("the declined question is still queued: %+v", got.Questions)
	}
}

func TestDeclineQuestion_UnknownTaskErrors(t *testing.T) {
	sandbox(t)
	if err := New().DeclineQuestion("nope", ""); err == nil {
		t.Error("declining an unknown task should error")
	}
}
