// SPDX-License-Identifier: MIT

package dispatch

import (
	"context"
	"testing"

	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/testutil"
	"github.com/ankit373/hydra/internal/waterfall"
)

const escalationReason = "verifier:cmd:schema failed: total must be a number"

// escalatedRun rejects h1's answer once, lets h2's stand, and returns the run's
// trace with the two attempt spans in the order they ran.
func escalatedRun(t *testing.T) (tr *waterfall.Trace, rejected, next *waterfall.Span) {
	t.Helper()
	testutil.NewSandbox(t)
	d := climbingDispatcher(t,
		climbing{answeringHead(t, "h1", "first", 90), 96, 0.01}, climbing{answeringHead(t, "h2", "second", 90), 98, 0.02})
	res, err := d.Dispatch(context.Background(), "extract the invoice total", Options{
		RunID: "run-escalated", TaskID: "task-escalated", Domain: climbClass,
		Escalate: rejectFirst(escalationReason),
	})
	if err != nil || res.Head.ID != "h2" {
		t.Fatalf("Dispatch = %v, %v; want h2 to answer after h1 was rejected", res, err)
	}
	events, err := runlog.Load("run-escalated")
	if err != nil {
		t.Fatal(err)
	}
	tr = waterfall.Build(events)
	for _, s := range tr.Flatten() {
		switch s.Head {
		case "h1":
			rejected = s
		case "h2":
			next = s
		}
	}
	if rejected == nil || next == nil {
		t.Fatalf("trace is missing an attempt: h1=%v h2=%v", rejected, next)
	}
	return tr, rejected, next
}

func TestEscalate_ScoresTheRejectedSpan(t *testing.T) {
	_, rejected, next := escalatedRun(t)
	if len(rejected.Scores) != 1 {
		t.Fatalf("rejected span scores = %+v, want one", rejected.Scores)
	}
	if sc := rejected.Scores[0]; sc.Name != "check" || sc.Value != 0 || sc.Comment != escalationReason {
		t.Errorf("score = %+v, want check=0 carrying the reason", sc)
	}
	if len(next.Scores) != 0 {
		t.Errorf("the accepted span was scored %+v; dispatch only scores what it rejected", next.Scores)
	}
}

func TestEscalate_NextAttemptNamesItsPredecessor(t *testing.T) {
	_, rejected, next := escalatedRun(t)
	if next.Meta["escalated_from"] != rejected.ID || next.Meta["reason"] != escalationReason {
		t.Errorf("next attempt meta = %v, want escalated_from %s and the reason", next.Meta, rejected.ID)
	}
	if _, ok := rejected.Meta["escalated_from"]; ok {
		t.Error("the first attempt claims to be an escalation")
	}
	if rejected.ParentID == "" || rejected.ParentID != next.ParentID {
		t.Errorf("attempts hang off %q and %q, want one task span", rejected.ParentID, next.ParentID)
	}
}

func TestVerdict_EscalatedSpanReadsFailed(t *testing.T) {
	_, rejected, next := escalatedRun(t)
	if passed, known := rejected.Verdict(); passed || !known {
		t.Errorf("rejected span verdict passed=%v known=%v, want a known fail", passed, known)
	}
	if _, known := next.Verdict(); known {
		t.Error("the accepted span reads as judged though nothing checked it here")
	}
}
