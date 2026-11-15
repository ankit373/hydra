// SPDX-License-Identifier: MIT

package dispatch

import (
	"context"
	"os"
	"testing"
)

// act_prob is the first pick's probability, so a row whose answer came from a
// fallback must still name the first pick, or ope weights the wrong action.
func TestLogDispatch_FallbackRecordsFirstPick(t *testing.T) {
	d := streamDispatcher(t, refusingHead(t, "breaks-first"), answeringHead(t, "answers", "ok", 90))
	d.cfg.ExploreRate, d.rng = 0.2, newExploreRand(1)

	res, err := d.Dispatch(context.Background(), "hi", Options{RunID: "run-fp", TaskID: "task-fp"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Head.ID != "answers" {
		t.Fatalf("answered by %q, want the fallback", res.Head.ID)
	}
	assertFirstPick(t, "breaks-first", "answers", 1, SelectionProb(0, 2, 0.2))
}

func TestLogDispatch_FirstPickAnsweredHasStepZero(t *testing.T) {
	d := streamDispatcher(t, answeringHead(t, "answers", "ok", 95), answeringHead(t, "spare", "ok", 90))
	d.cfg.ExploreRate, d.rng = 0.2, newExploreRand(1)

	if _, err := d.Dispatch(context.Background(), "hi", Options{RunID: "run-fp0", TaskID: "task-fp0"}); err != nil {
		t.Fatal(err)
	}
	assertFirstPick(t, "answers", "answers", 0, SelectionProb(0, 2, 0.2))
}

// assertFirstPick checks both logs name the first pick beside the head that
// answered, with the first pick's probability.
func assertFirstPick(t *testing.T, first, answered string, step int, prob float64) {
	t.Helper()
	for _, name := range []string{"dispatch.jsonl", "cost.jsonl"} {
		rows := readLog(t, os.Getenv("HOME"), name)
		if len(rows) != 1 {
			t.Fatalf("%s: %d rows, want 1", name, len(rows))
		}
		r := rows[0]
		if r["head"] != answered || r["first_head"] != first {
			t.Errorf("%s: head %v first_head %v, want %s and %s", name, r["head"], r["first_head"], answered, first)
		}
		if got, ok := r["fallback_step"].(float64); !ok || int(got) != step {
			t.Errorf("%s: fallback_step %v, want %d", name, r["fallback_step"], step)
		}
		if r["act_prob"] != prob {
			t.Errorf("%s: act_prob %v, want the first pick's %v", name, r["act_prob"], prob)
		}
		for _, k := range []string{"first_model", "first_pool"} {
			if _, ok := r[k]; !ok {
				t.Errorf("%s: %s missing", name, k)
			}
		}
		if _, ok := r["first_tier"].(float64); !ok {
			t.Errorf("%s: first_tier %v, want a number", name, r["first_tier"])
		}
	}
}
