// SPDX-License-Identifier: MIT

package swarm

import (
	"fmt"

	"github.com/ankit373/hydra/internal/rank"
	"github.com/ankit373/hydra/internal/runid"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/trust"
)

// Node ids for the fan-out's own parent in the supervision tree.
//
// A named parent is emitted rather than reusing the task id: tree.Reconstruct
// links a child to whatever Parent names, so pointing at an id no event ever
// declares would materialise a phantom node labelled with a raw timestamp. A
// swarm and an ensemble are also different things to a reader, one is racing
// candidates, the other is accumulating evidence, so they get different roots.
const (
	swarmAgent = "swarm"
	sprtAgent  = "ensemble"
)

// logRunEvents appends one runlog event per attempt so a swarm reads as N heads
// working one task rather than a single opaque node.
//
// cost.jsonl already records the spend; this records the *shape*, which head,
// which tier, which won, how long, because that is what a supervision tree and
// a Fleet view are reconstructed from. Before #204 nothing in this package
// emitted, so a five-head swarm collapsed to one tree node.
//
// SPRT does not use this, see logSamples, which emits from the LLR ledger so
// each event carries the running confidence.
//
// Every append is best-effort. Losing an observability event must never fail the
// work being observed.
func logRunEvents(attempts []Attempt, mode SwarmMode, opts Options) {
	runID := runid.ResolveRun(opts.RunID)
	taskID := runid.ResolveTask(opts.TaskID)

	rl := runlog.New(runID)
	_ = rl.Append(runlog.Event{
		Kind: runlog.KindTaskStarted, TaskID: taskID,
		Agent:  swarmAgent,
		Detail: fmt.Sprintf("swarm · %s · %d heads", mode, len(attempts)),
	})

	for _, a := range attempts {
		if a.Status == StatusPending || a.Status == StatusCanceled {
			continue
		}
		detail := string(mode)
		if a.Status == StatusOK && a.Rank == 1 {
			detail = string(mode) + " · winner"
		}
		_ = rl.Append(runlog.Event{
			Kind:   runlog.KindAttempt,
			TaskID: taskID,
			// Keyed by head id, which is also how dispatch keys its own events,
			// so a head's selection, execution, and attempt collapse into one
			// node rather than three.
			Agent:      a.Head.ID,
			Parent:     swarmAgent,
			Head:       a.Head.ID,
			Model:      a.Head.Name,
			Tier:       rank.UITier(a.Head),
			Status:     string(a.Status),
			CostUSD:    a.EstCostUSD,
			DurationMS: a.Duration.Milliseconds(),
			Detail:     detail,
		})
	}

	_ = rl.Append(runlog.Event{Kind: runlog.KindTaskFinished, TaskID: taskID, Agent: swarmAgent})
}

// logSamples appends one runlog event per SPRT ledger entry, carrying the
// running confidence after that source was weighed.
//
// attempts is consulted only to recover the head's display name and tier, the
// ledger keys on source id alone.
func logSamples(ledger []trust.Evidence, attempts []Attempt, opts Options) {
	runID := runid.ResolveRun(opts.RunID)
	taskID := runid.ResolveTask(opts.TaskID)

	byID := make(map[string]Attempt, len(attempts))
	for _, a := range attempts {
		byID[a.Head.ID] = a
	}

	rl := runlog.New(runID)
	_ = rl.Append(runlog.Event{
		Kind: runlog.KindTaskStarted, TaskID: taskID,
		Agent:  sprtAgent,
		Detail: fmt.Sprintf("SPRT ensemble · %d samples", len(ledger)),
	})

	var final float64
	for _, ev := range ledger {
		final = ev.ConfidenceAfter
		e := runlog.Event{
			Kind:       runlog.KindSample,
			TaskID:     taskID,
			Agent:      ev.Source,
			Parent:     sprtAgent,
			Head:       ev.Source,
			CostUSD:    ev.CostUSD,
			Confidence: ev.ConfidenceAfter,
			Detail:     sampleDetail(ev),
		}
		if a, ok := byID[ev.Source]; ok {
			e.Model = a.Head.Name
			e.Tier = rank.UITier(a.Head)
			e.Status = string(a.Status)
			e.DurationMS = a.Duration.Milliseconds()
		}
		_ = rl.Append(e)
	}

	_ = rl.Append(runlog.Event{
		Kind: runlog.KindTaskFinished, TaskID: taskID,
		Agent: sprtAgent, Confidence: final,
	})
}

// sampleDetail says what the source did to the running log-odds, which is the
// one thing a reader cannot infer from the confidence number alone.
func sampleDetail(ev trust.Evidence) string {
	verdict := "disagreed"
	if ev.Agreed {
		verdict = "agreed"
	}
	return fmt.Sprintf("%s · LLR %+.3f → Λ %.3f", verdict, ev.LLR, ev.LambdaAfter)
}
