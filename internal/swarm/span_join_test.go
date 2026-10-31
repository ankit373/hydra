// SPDX-License-Identifier: MIT

package swarm

import (
	"testing"

	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/trust"
)

// cost.Row.SpanID exists so spend joins the span that spent it, which a task-id
// join cannot do when one task made several attempts. That is exactly the
// fan-out case, and it was the one writer that left the field empty (#794).
//
// Asserted by performing the join, not by checking the field is non-empty: a
// span id derived independently on each side would pass that check and still
// join to nothing, which is the failure worth catching.
func TestLogAttempts_CostRowsJoinTheRunLogSpans(t *testing.T) {
	for _, c := range []struct {
		name string
		mode SwarmMode
		emit func(attempts []Attempt, opts Options)
	}{
		{
			name: "swarm",
			mode: ModeBest,
			emit: func(a []Attempt, o Options) { logRunEvents(a, ModeBest, o) },
		},
		{
			// SPRT emits from the LLR ledger rather than the attempts, and keys
			// its spans on a different root ("ensemble"), so a cost row that
			// assumed the swarm root would join to nothing here.
			name: "sprt",
			mode: ModeSPRT,
			emit: func(a []Attempt, o Options) { logSamples(sprtLedger(a), a, o) },
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			attempts := []Attempt{idAttempt("alpha"), idAttempt("beta")}
			opts := Options{RunID: "run-" + c.name, TaskID: "task-" + c.name}

			rows := costRows(t, func() {
				c.emit(attempts, opts)
				logAttempts(attempts, c.mode, opts, "p")
			})
			if len(rows) != len(attempts) {
				t.Fatalf("wrote %d cost rows, want %d", len(rows), len(attempts))
			}

			events, err := runlog.Load(opts.RunID)
			if err != nil {
				t.Fatal(err)
			}
			spanHead := map[string]string{}
			for _, e := range events {
				if e.SpanID != "" && e.Head != "" {
					spanHead[e.SpanID] = e.Head
				}
			}

			for _, r := range rows {
				span, _ := r["span_id"].(string)
				head, _ := r["head"].(string)
				if span == "" {
					t.Fatalf("cost row for %q carries no span_id, so its spend "+
						"cannot be attributed to the attempt that incurred it", head)
				}
				got, ok := spanHead[span]
				if !ok {
					t.Errorf("cost row for %q names span %s, which no run-log event "+
						"declares: the two sides derive it separately", head, span)
					continue
				}
				if got != head {
					t.Errorf("span %s is %q in the run log and %q in cost.jsonl", span, got, head)
				}
			}
		})
	}
}

// sprtLedger is the ledger shape logSamples reads: one entry per head that
// voted, keyed by the same id the attempt carries.
func sprtLedger(attempts []Attempt) []trust.Evidence {
	out := make([]trust.Evidence, 0, len(attempts))
	for _, a := range attempts {
		out = append(out, trust.Evidence{Source: a.Head.ID, ConfidenceAfter: 0.8, Agreed: true})
	}
	return out
}
