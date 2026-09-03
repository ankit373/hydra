// SPDX-License-Identifier: MIT

package dispatch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/ledger"
	"github.com/ankit373/hydra/internal/pending"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/testutil"
)

// askPolicy gates one head behind an Ask and allows everything else, which is
// the shape that makes the fall-through question interesting.
func askPolicy(t *testing.T, tool string) {
	t.Helper()
	writeLedgerPolicy(t, ledger.Policy{
		Rules:   []ledger.Rule{{Tool: tool, Decision: ledger.Ask}},
		Default: ledger.Allow,
	})
}

// An Ask must stop dispatch before the executor is invoked at all. This is
// Hydra's own gate, which is why it applies at any tier rather than only to
// heads clever enough to ask for themselves.
func TestDispatch_AskParksTheTask(t *testing.T) {
	s := testutil.NewSandbox(t)
	askPolicy(t, "gated")

	res, err := liveDispatcher(echoHead(t, s, "gated", 95)).
		Dispatch(context.Background(), "do the thing", Options{RunID: "run-ask", TaskID: "task-ask"})

	var parked *ParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("want ParkedError, got res=%v err=%v", res, err)
	}
	if parked.TaskID != "task-ask" || parked.Head != "gated" {
		t.Errorf("parked error names the wrong task/head: %+v", parked)
	}
	if parked.Question == "" {
		t.Error("a parked task with no question is unanswerable")
	}

	q, lErr := pending.Load("task-ask")
	if lErr != nil {
		t.Fatalf("the task was not parked durably: %v", lErr)
	}
	if q.Prompt != "do the thing" {
		t.Errorf("stored prompt = %q, want the original", q.Prompt)
	}
	if q.Head != "gated" {
		t.Errorf("stored head = %q, want gated", q.Head)
	}
}

// Session's Timeline already renders Detail on every entry, so the question has
// to travel in Detail for a parked task to be visible with no new view.
func TestDispatch_AskAppendsQuestionAskedEvent(t *testing.T) {
	s := testutil.NewSandbox(t)
	askPolicy(t, "gated")

	_, _ = liveDispatcher(echoHead(t, s, "gated", 95)).
		Dispatch(context.Background(), "do the thing", Options{RunID: "run-q", TaskID: "task-q"})

	events, err := runlog.Load("run-q")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Kind == runlog.KindQuestionAsked {
			if e.Detail == "" {
				t.Error("question_asked carries no question in Detail")
			}
			if e.Head != "gated" {
				t.Errorf("question_asked head = %q, want gated", e.Head)
			}
			return
		}
	}
	t.Error("no question_asked event — a parked task left no trail")
}

// The behaviour #582 changes, and the reason it is worth changing.
//
// An Ask used to fall through to the next candidate exactly like a deny, so a
// task with any fallback ran on a different head and the question was never
// asked. Where the operator's rule is tool-scoped that is merely the feature
// silently not working; where it is resource-scoped, the gated resource is
// reached anyway by another tool.
func TestDispatch_AskDoesNotFallThroughToAnotherHead(t *testing.T) {
	s := testutil.NewSandbox(t)
	askPolicy(t, "gated")

	// "gated" outranks "other", so it is tried first and "other" is the
	// fallback that must not silently pick the task up.
	res, err := liveDispatcher(echoHead(t, s, "gated", 95), echoHead(t, s, "other", 90)).
		Dispatch(context.Background(), "do the thing", Options{RunID: "run-ft", TaskID: "task-ft"})

	var parked *ParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("want ParkedError, got res=%v err=%v", res, err)
	}
	// These heads really execute, so a non-nil result would mean work ran.
	if res != nil {
		t.Fatalf("something ran while a question was outstanding: head=%s", res.Head.ID)
	}
	events, lErr := runlog.Load("run-ft")
	if lErr != nil {
		t.Fatal(lErr)
	}
	for _, e := range events {
		if e.Head == "other" {
			t.Fatalf("the fallback head was reached after gated asked: %s/%s", e.Kind, e.Detail)
		}
	}
}

// Approving one head must not authorize a different one, or a resume silently
// re-routes to a head the human was never shown.
func TestDispatch_ApprovalIsPerHead(t *testing.T) {
	s := testutil.NewSandbox(t)
	askPolicy(t, "gated")

	_, err := liveDispatcher(echoHead(t, s, "gated", 95)).
		Dispatch(context.Background(), "do the thing", Options{
			RunID: "run-ph", TaskID: "task-ph",
			AnsweredHead: "someone-else",
		})

	var parked *ParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("an answer for another head must not clear this one's gate; got %v", err)
	}
}

// The answered head does clear its own gate, or a resume would park forever.
func TestDispatch_AnsweredHeadRuns(t *testing.T) {
	s := testutil.NewSandbox(t)
	askPolicy(t, "gated")

	res, err := liveDispatcher(echoHead(t, s, "gated", 95)).
		Dispatch(context.Background(), "do the thing", Options{
			RunID: "run-cg", TaskID: "task-cg",
			AnsweredHead: "gated",
		})
	if err != nil {
		t.Fatalf("an answered head should run: %v", err)
	}
	if res.Head.ID != "gated" {
		t.Errorf("ran on %q, want gated", res.Head.ID)
	}
	if !strings.Contains(res.Output, "reviewed") {
		t.Errorf("the head did not actually execute: %q", res.Output)
	}
}

// Resume is the whole point: the parked task runs, on the head that was
// approved, with the answer folded into the prompt.
func TestResume_RunsTheApprovedHeadAndConsumesTheFile(t *testing.T) {
	s := testutil.NewSandbox(t)
	askPolicy(t, "gated")
	d := liveDispatcher(echoHead(t, s, "gated", 95))

	_, _ = d.Dispatch(context.Background(), "do the thing", Options{RunID: "r", TaskID: "t1"})
	if _, err := pending.Load("t1"); err != nil {
		t.Fatalf("precondition: task should be parked, got %v", err)
	}

	res, err := d.Resume(context.Background(), "t1", "yes, go ahead")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if res.Head.ID != "gated" {
		t.Errorf("resumed on %q, want the approved head", res.Head.ID)
	}
	if _, err := pending.Load("t1"); !errors.Is(err, pending.ErrNotFound) {
		t.Errorf("resume must consume the pending file, got %v", err)
	}
}

// Consuming the file before dispatching is what makes answering idempotent:
// double-clicking must not run the work twice.
func TestResume_SecondAnswerFindsNothing(t *testing.T) {
	s := testutil.NewSandbox(t)
	askPolicy(t, "gated")
	d := liveDispatcher(echoHead(t, s, "gated", 95))

	_, _ = d.Dispatch(context.Background(), "do the thing", Options{RunID: "r", TaskID: "t1"})
	if _, err := d.Resume(context.Background(), "t1", "yes"); err != nil {
		t.Fatalf("first resume: %v", err)
	}
	if _, err := d.Resume(context.Background(), "t1", "yes"); !errors.Is(err, pending.ErrNotFound) {
		t.Errorf("second resume should find nothing, got %v", err)
	}
}

// Dismissing or ignoring is not an answer. A parked task stays parked.
func TestResume_EmptyAnswerLeavesTheTaskParked(t *testing.T) {
	s := testutil.NewSandbox(t)
	askPolicy(t, "gated")
	d := liveDispatcher(echoHead(t, s, "gated", 95))

	_, _ = d.Dispatch(context.Background(), "do the thing", Options{RunID: "r", TaskID: "t1"})
	for _, empty := range []string{"", "   ", "\n\t"} {
		if _, err := d.Resume(context.Background(), "t1", empty); err == nil {
			t.Errorf("Resume with %q should be refused", empty)
		}
	}
	if _, err := pending.Load("t1"); err != nil {
		t.Errorf("the task must stay parked after a non-answer, got %v", err)
	}
}

// An unreadable question must fail the resume, not fall through to dispatching
// without the answer folded in.
func TestResume_CorruptPendingFileFailsLoudly(t *testing.T) {
	s := testutil.NewSandbox(t)
	askPolicy(t, "gated")
	if err := os.MkdirAll(pending.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pending.Dir(), "broken.json"), []byte("{oops"), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := liveDispatcher(echoHead(t, s, "gated", 95)).
		Resume(context.Background(), "broken", "yes")
	if err == nil {
		t.Fatal("resume on a corrupt pending file must fail")
	}
	if res != nil {
		t.Fatal("a corrupt question must not dispatch anything")
	}
	if !strings.Contains(err.Error(), "unreadable") {
		t.Errorf("the error should say the file is unreadable, got %v", err)
	}
}

// A refusal cannot be free text folded into the prompt — that still dispatches
// and leaves it to the head to notice. Decline never reaches an executor.
func TestDecline_ConsumesAndRecordsADenial(t *testing.T) {
	s := testutil.NewSandbox(t)
	askPolicy(t, "gated")
	d := liveDispatcher(echoHead(t, s, "gated", 95))

	_, _ = d.Dispatch(context.Background(), "do the thing", Options{RunID: "r", TaskID: "t1"})
	if err := Decline("t1", "not on production"); err != nil {
		t.Fatalf("Decline: %v", err)
	}
	if _, err := pending.Load("t1"); !errors.Is(err, pending.ErrNotFound) {
		t.Errorf("decline must consume the pending file, got %v", err)
	}

	events, err := ledger.Load(ledger.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Decision == ledger.Deny && strings.Contains(e.Reason, "not on production") {
			return
		}
	}
	t.Error("a declined task left no denial in the ledger")
}

func TestDecline_UnknownTaskIsNotFound(t *testing.T) {
	testutil.NewSandbox(t)
	if err := Decline("nope", ""); !errors.Is(err, pending.ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}
