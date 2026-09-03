// SPDX-License-Identifier: MIT

package api

import (
	"sort"
	"time"

	"github.com/ankit373/hydra/internal/pending"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/tree"
)

// GroupThreshold is the agent count past which the frontend collapses a run's
// agents behind a summary instead of rendering every card.
//
// Set here rather than in the view so both surfaces agree on when a fan-out
// stops being readable. A 20-head swarm reaches it on the first run, which is
// why this exists before the problem is visible rather than after.
const GroupThreshold = 16

// Fleet is every run this machine knows about, live first.
type Fleet struct {
	// HasRuns is false when no run has ever been logged. The view says so
	// rather than rendering an empty list that looks like a failed load.
	HasRuns bool `json:"hasRuns"`

	LiveCount int   `json:"liveCount"`
	Runs      []Run `json:"runs"`

	// WaitingCount is runs parked on a question. Counted separately from
	// LiveCount because they are opposites: one is working, the other has
	// stopped and only a person can restart it.
	WaitingCount int `json:"waitingCount"`

	// GroupThreshold is served to the frontend so the collapse point is defined
	// once, in Go, rather than duplicated as a magic number in the view.
	GroupThreshold int `json:"groupThreshold"`
}

// Run is one invocation and the agents inside it.
type Run struct {
	ID   string `json:"id"`
	Live bool   `json:"live"`

	// Waiting is true while a question for this run is still parked. Taken
	// from the pending store, not from the run log: the log records that a
	// question was asked (KindQuestionAsked) but never that it was answered,
	// so only the presence of the file distinguishes "still parked" from
	// "asked and since resumed".
	Waiting bool `json:"waiting"`

	StartedAt string `json:"startedAt"`
	ElapsedMS int64  `json:"elapsedMs"`

	CostUSD float64 `json:"costUsd"`
	// Confidence is the run's final confidence, non-zero only for an SPRT
	// ensemble. Zero means "not a confidence run", not "no confidence".
	Confidence float64 `json:"confidence"`

	Agents []Agent `json:"agents"`

	// Counts by state, so a collapsed run still says what is in it.
	Running  int `json:"running"`
	OK       int `json:"ok"`
	Failed   int `json:"failed"`
	Pending  int `json:"pending"`
	AllCount int `json:"allCount"`

	// Skipped is events the reconstruction could not attribute to an agent.
	// Surfaced rather than hidden: silently dropping them renders a partial run
	// as a complete one.
	Skipped int `json:"skipped"`

	// Goal is what was asked, in the requester's words — the run-started
	// event's own detail. It is the only human-readable identifier a run has;
	// without it a list of runs is a list of timestamps. Empty when the run
	// recorded no prompt (an external orchestrator can supply the id and
	// nothing else), and a preview rather than the full text, because the log
	// stores a preview.
	Goal string `json:"goal,omitempty"`

	// Error is set when this run's log could not be read. The run still appears
	// as a row — a bad file must not blank the whole view.
	Error string `json:"error,omitempty"`
}

// Agent is one head or task inside a run.
type Agent struct {
	ID     string `json:"id"`
	Parent string `json:"parent,omitempty"`
	Depth  int    `json:"depth"`

	Head  string `json:"head,omitempty"`
	Model string `json:"model,omitempty"`
	State string `json:"state"`

	// Tier and Confidence are deliberately not omitempty: the view compares
	// them numerically, and an omitted key arrives as undefined rather than 0.
	// A tier of 0 also means something — "unknown", the same bucket
	// `hyctl stats --tier` uses.
	Tier       int     `json:"tier"`
	CostUSD    float64 `json:"costUsd"`
	Confidence float64 `json:"confidence"`
	DurationMS int64   `json:"durationMs"`

	Detail string `json:"detail,omitempty"`
}

// GetFleet lists every run, live ones first.
//
// Reconstruction goes through tree.Reconstruct — the same call the terminal
// cockpit makes — so the two surfaces cannot disagree about the same run.
func (a *API) GetFleet() (*Fleet, error) {
	ids, err := runlog.Runs()
	if err != nil {
		return nil, err
	}

	// Runs is initialised rather than left nil: a nil slice marshals to null,
	// and types.ts declares runs as Run[], so the type would be lying to every
	// future caller on a machine that has never dispatched (#230). Session
	// initialises its slices for the same reason.
	f := &Fleet{GroupThreshold: GroupThreshold, Runs: []Run{}}
	if len(ids) == 0 {
		return f, nil
	}
	f.HasRuns = true

	live := make(map[string]bool, len(ids))
	if liveIDs, err := runlog.LiveRuns(); err == nil {
		for _, id := range liveIDs {
			live[id] = true
		}
	}

	// Errors ignored deliberately: a queue that cannot be read must not fail
	// the whole Activity list. GetPendingQuestions is where an unreadable
	// question is reported.
	// List returns what it could read alongside any error, and the partial
	// answer is the right one to use here: an unreadable question must not
	// blank the whole Activity list. GetPendingQuestions reports the error.
	waiting := map[string]bool{}
	parked, _ := pending.List()
	for _, q := range parked {
		if q.RunID != "" {
			waiting[q.RunID] = true
		}
	}

	now := time.Now()
	for _, id := range ids {
		r := buildRun(id, live[id], now)
		r.Waiting = waiting[id]
		if r.Live {
			f.LiveCount++
		}
		if r.Waiting {
			f.WaitingCount++
		}
		// A finished run with zero agents and no error carries no information —
		// dispatch never reached a head, most commonly a --dry-run preview
		// (before #390 fixed dry-run writing these in the first place; this
		// machine's own history still has 16 of them from before that fix).
		// Sorted live-first-then-most-recent, a pile of these outranks real
		// history and makes Fleet look empty even when hasRuns is technically
		// true. A live run is never filtered — it may not have picked a head
		// yet, and it is the one the user opened the app to watch.
		// A parked run has exactly this shape — nothing executed, so no agents,
		// and no error because nothing failed. Without the exemption the one
		// run that actually needs a person would be the one filtered out.
		if !r.Live && !r.Waiting && r.AllCount == 0 && r.Error == "" {
			continue
		}
		f.Runs = append(f.Runs, r)
	}
	f.HasRuns = len(f.Runs) > 0

	// Live first, then most recent. Run ids are timestamp-prefixed, so a
	// reverse lexical compare is a reverse chronological one — no parsing, and
	// it stays correct for ids minted by an external orchestrator that followed
	// the same format.
	// Waiting first, then live, then most recent. A parked run outranks a
	// running one because it is the only kind that cannot progress on its own.
	sort.SliceStable(f.Runs, func(i, j int) bool {
		if f.Runs[i].Waiting != f.Runs[j].Waiting {
			return f.Runs[i].Waiting
		}
		if f.Runs[i].Live != f.Runs[j].Live {
			return f.Runs[i].Live
		}
		return f.Runs[i].ID > f.Runs[j].ID
	})

	return f, nil
}

func buildRun(id string, live bool, now time.Time) Run {
	r := Run{ID: id, Live: live}

	events, err := runlog.Load(id)
	if err != nil {
		// A row with an error beats a blank view: the run existed, and saying
		// why it cannot be read is more useful than pretending it does not.
		r.Error = err.Error()
		return r
	}
	if len(events) == 0 {
		return r
	}

	// tree.Reconstruct drops run-level events by design — they describe the
	// invocation, not a node in it — so the prompt has to be read here, before
	// the events are handed over, or it is lost to the view entirely.
	r.Goal = runGoal(events)

	t, _ := tree.Reconstruct(events)
	r.Skipped = t.Skipped

	for _, row := range t.Rows() {
		n := row.Node
		r.Agents = append(r.Agents, Agent{
			ID: n.ID, Parent: n.Parent, Depth: row.Depth,
			Head: n.Head, Model: n.Model, Tier: n.Tier,
			State:      string(n.State),
			CostUSD:    n.CostUSD,
			Confidence: n.Confidence,
			DurationMS: n.DurationMS,
			Detail:     n.Detail,
		})
		r.CostUSD += n.CostUSD
		// The run's confidence is whichever agent carries one — only an SPRT
		// ensemble does, and its root records the final value.
		if n.Confidence > r.Confidence {
			r.Confidence = n.Confidence
		}
		switch n.State {
		case tree.StateRunning:
			r.Running++
		case tree.StateOK:
			r.OK++
		case tree.StateFailed:
			r.Failed++
		default:
			r.Pending++
		}
	}
	r.AllCount = len(r.Agents)

	first, last := eventSpan(t)
	if !first.IsZero() {
		r.StartedAt = first.UTC().Format(time.RFC3339)
		// A live run is still accruing, so it is measured to now; a finished one
		// is measured to its last event. Using now for both would make every
		// historical run appear to still be growing.
		end := last
		if live || end.Before(first) {
			end = now
		}
		r.ElapsedMS = end.Sub(first).Milliseconds()
	}

	return r
}

// eventSpan returns the earliest start and latest finish across a run's nodes.
// runGoal returns what the run was asked to do, from the first run-started
// event carrying a detail. Later events are ignored: a run has one goal, and
// the first statement of it is the one that was actually requested.
func runGoal(events []runlog.Event) string {
	for _, e := range events {
		if e.Kind == runlog.KindRunStarted && e.Detail != "" {
			return e.Detail
		}
	}
	return ""
}

func eventSpan(t *tree.Tree) (first, last time.Time) {
	for _, id := range t.Order {
		n := t.Nodes[id]
		if !n.StartedAt.IsZero() && (first.IsZero() || n.StartedAt.Before(first)) {
			first = n.StartedAt
		}
		for _, ts := range []time.Time{n.StartedAt, n.FinishedAt} {
			if !ts.IsZero() && ts.After(last) {
				last = ts
			}
		}
	}
	return first, last
}
