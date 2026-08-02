// SPDX-License-Identifier: MIT

package api

import (
	"time"

	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/tree"
)

// Session is one run in detail: its timeline, its agents, and its A2A edges.
type Session struct {
	RunID string `json:"runId"`
	Live  bool   `json:"live"`

	// Found is false when the run id names no log. The view says so rather than
	// rendering an empty session that looks like a run which did nothing.
	Found bool   `json:"found"`
	Error string `json:"error,omitempty"`

	Timeline []TimelineEntry `json:"timeline"`
	Agents   []Agent         `json:"agents"`
	Edges    []Edge          `json:"edges"`

	// NonLinear reports whether this run has structure a list cannot show —
	// a fan-out or an A2A cross-edge. The Graph tab is offered only then;
	// drawing a graph of a straight line is worse than drawing a list.
	NonLinear bool `json:"nonLinear"`

	// Skipped is events that could not be attributed to an agent. Surfaced, not
	// hidden: a partial run rendered as a complete one is the worse failure.
	Skipped int `json:"skipped"`
}

// TimelineEntry is one row of the chronological view.
type TimelineEntry struct {
	Kind   string `json:"kind"`
	TS     string `json:"ts"`
	NodeID string `json:"nodeId,omitempty"`

	Head  string `json:"head,omitempty"`
	Model string `json:"model,omitempty"`
	Tier  int    `json:"tier"`

	Status     string  `json:"status,omitempty"`
	CostUSD    float64 `json:"costUsd"`
	DurationMS int64   `json:"durationMs"`
	Confidence float64 `json:"confidence"`

	// Detail is the run log's short human string. For an SPRT sample #205
	// writes the verifiable part here — "agreed · LLR +1.203 → Λ 1.203" — which
	// is deliberately what the row leads with rather than narrated intent.
	Detail string `json:"detail,omitempty"`
}

// Edge is one A2A handoff: a collaboration edge, distinct from the ownership
// edges that Agent.Parent describes. Keeping them apart is the whole reason
// tree.Node has both Children and Handoffs.
type Edge struct {
	From   string `json:"from"`
	To     string `json:"to"`
	TS     string `json:"ts"`
	Detail string `json:"detail,omitempty"`
}

// GetSession returns one run in full.
//
// Reconstruction goes through tree.Reconstruct, the same call Fleet and the
// terminal cockpit make, so no two surfaces can disagree about one run.
func (a *API) GetSession(runID string) (*Session, error) {
	// Slices start empty, never nil: a nil slice marshals to null, and the
	// frontend iterates these directly. "No edges" and "no session" are
	// different facts, and Found already carries the second one.
	s := &Session{
		RunID: runID, Live: runlog.IsAlive(runID),
		Timeline: []TimelineEntry{}, Agents: []Agent{}, Edges: []Edge{},
	}
	if runID == "" {
		return s, nil
	}

	events, err := runlog.Load(runID)
	if err != nil {
		// Saying why beats a blank pane: the run may well exist and simply be
		// unreadable, which is a different problem from it not existing.
		s.Error = err.Error()
		return s, nil
	}
	if len(events) == 0 {
		return s, nil
	}
	s.Found = true

	t, tl := tree.Reconstruct(events)
	s.Skipped = t.Skipped

	// Entries in file order. runlog's own doc says Seq "is not the sort key" —
	// several writers share one run and each counts from 1, so re-sorting by it
	// puts a run's end before a handoff that happened first (#205).
	for _, e := range tl.Entries {
		s.Timeline = append(s.Timeline, TimelineEntry{
			Kind: string(e.Kind), TS: formatTS(e.TS), NodeID: e.NodeID,
			Head: e.Head, Model: e.Model, Tier: e.Tier,
			Status: e.Status, CostUSD: e.CostUSD,
			DurationMS: e.DurationMS, Confidence: e.Confidence,
			Detail: e.Detail,
		})
	}

	for _, row := range t.Rows() {
		n := row.Node
		s.Agents = append(s.Agents, Agent{
			ID: n.ID, Parent: n.Parent, Depth: row.Depth,
			Head: n.Head, Model: n.Model, Tier: n.Tier,
			State:      string(n.State),
			CostUSD:    n.CostUSD,
			Confidence: n.Confidence,
			DurationMS: n.DurationMS,
			Detail:     n.Detail,
		})
		for _, h := range n.Handoffs {
			s.Edges = append(s.Edges, Edge{
				From: n.ID, To: h.To, TS: formatTS(h.TS), Detail: h.Detail,
			})
		}
	}

	s.NonLinear = isNonLinear(t, len(s.Edges))
	return s, nil
}

// isNonLinear reports whether a run has structure a list cannot convey: an A2A
// cross-edge, or any node with more than one child. Everything else is a chain,
// and a chain reads better as a timeline.
func isNonLinear(t *tree.Tree, edges int) bool {
	if edges > 0 {
		return true
	}
	if len(t.Roots) > 1 {
		return true
	}
	for _, n := range t.Nodes {
		if len(n.Children) > 1 {
			return true
		}
	}
	return false
}

// formatTS renders a timestamp for display, or empty when there is none —
// never a zero-time string, which reads as a real measurement from year 1.
func formatTS(ts time.Time) string {
	if ts.IsZero() {
		return ""
	}
	return ts.UTC().Format(time.RFC3339)
}
