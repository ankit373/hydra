// SPDX-License-Identifier: MIT

package swarm

import (
	"context"
	"testing"

	"github.com/ankit373/hydra/internal/runlog"
)

// A verdict arrives on a span, and the evidence ledger that verdict would train
// is in trust.jsonl keyed by that span. If the span a run *reports* is not the
// span its samples were *logged* under, the two can never meet, which is #1144
// in a new place: ResolveTask mints a fresh id when the caller named none, so
// resolving it twice gives one run two spans.
func TestRunSPRT_ReportsTheSpanItsSamplesWereLoggedUnder(t *testing.T) {
	s := swarmSandbox(t)
	t.Setenv("HYDRA_TASK_ID", "")
	sw := newSwarm(t,
		swarmHead(t, s, "a", 90, "the same answer"),
		swarmHead(t, s, "b", 85, "the same answer"),
	)
	seedCalibration(t, "go", "a", "b")

	// No TaskID: the caller named none, which is the case that broke.
	res, err := sw.RunSPRT(context.Background(), "q", Options{
		RunID: "run-join", Confidence: 0.75, Domain: "go",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.SpanID == "" {
		t.Fatal("the run reports no span, so a verdict has nothing to join on")
	}

	events, err := runlog.Load("run-join")
	if err != nil {
		t.Fatalf("no run log: %v", err)
	}
	for _, e := range events {
		if e.SpanID == res.SpanID {
			return
		}
	}
	t.Fatalf("the run reports span %q and logged %v; a verdict on the reported span "+
		"can never find this run's ledger", res.SpanID, spanIDs(events))
}

func spanIDs(events []runlog.Event) []string {
	var out []string
	for _, e := range events {
		if e.SpanID != "" {
			out = append(out, e.SpanID)
		}
	}
	return out
}
