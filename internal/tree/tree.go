// SPDX-License-Identifier: MIT

// Package tree turns a run's event log into the two shapes a UI renders: a
// supervision tree and a chronological timeline.
//
// It is deliberately free of any framework: no lipgloss, no Bubble Tea, no
// assumptions about a frontend's data shape. The terminal cockpit calls it from
// a tea.Cmd and wraps the result in a tea.Msg; a desktop app calls it from a
// bound method or pushes the result over an event channel. Both sit above this
// package; neither is visible to it.
//
// # Two overlaid graphs
//
// Ownership (who spawned whom) is a strict tree and governs navigation. A2A
// handoffs (who handed context to whom) form a DAG overlay on the same nodes.
// Keeping them apart is the load-bearing decision: a node has exactly one
// owner, so drill-down stays unambiguous, while collaboration stays as rich as
// a DAG. Conflating the two collapses navigation.
//
// # Reconstruct and Apply
//
// Reconstruct rebuilds everything from scratch; Apply folds a single event into
// an existing tree. They are equivalent by construction — Reconstruct is Apply
// in a loop — and a property test pins that. Both exist because their callers
// differ: a terminal redrawing a handful of nodes each tick can afford a full
// rebuild, while a long-running desktop session accumulating thousands of
// events needs to fold only what is new.
package tree

import (
	"time"

	"github.com/ankit373/hydra/internal/runlog"
)

// State is where a node is in its lifecycle.
type State string

const (
	StatePending State = "pending"
	StateRunning State = "running"
	StateOK      State = "ok"
	StateFailed  State = "failed"
)

// Handoff is one A2A context transfer — an edge in the collaboration overlay,
// never an ownership edge.
type Handoff struct {
	To     string
	Seq    uint64
	TS     time.Time
	Detail string
}

// Node is one agent/head in the run.
type Node struct {
	ID     string
	Parent string // ownership edge; "" at a root
	TaskID string

	Head  string
	Model string
	Tier  int
	State State

	CostUSD    float64
	Confidence float64
	DurationMS int64

	StartedAt  time.Time
	FinishedAt time.Time

	// Children are ownership edges, in the order they first appeared.
	Children []string
	// Handoffs are the A2A overlay — kept separate from Children on purpose.
	Handoffs []Handoff

	// Detail is a short human string (an error, a note). Never bulk content.
	Detail string
}

// Tree is a run's supervision tree.
type Tree struct {
	RunID string
	Roots []string
	Nodes map[string]*Node

	// Order is node-creation order, so a renderer can lay nodes out stably
	// rather than at the mercy of map iteration.
	Order []string

	// Skipped counts events that could not be attributed to a node. Surfaced
	// rather than hidden: silently dropping events renders a partial run as a
	// complete one.
	Skipped int
}

// Entry is one timeline row.
type Entry struct {
	Seq        uint64
	TS         time.Time
	Kind       runlog.Kind
	NodeID     string
	TaskID     string
	Head       string
	Model      string
	Tier       int
	Status     string
	CostUSD    float64
	DurationMS int64
	Confidence float64
	Detail     string
}

// Timeline is the run's events in order, for a chronological view.
type Timeline struct {
	RunID   string
	Entries []Entry
}

// NewTree returns an empty tree ready for Apply.
func NewTree(runID string) *Tree {
	return &Tree{RunID: runID, Nodes: map[string]*Node{}}
}

// Reconstruct builds a tree and timeline from a run's events. Events are taken
// in the order given — runlog.Load returns them in append order, which is the
// authoritative ordering (wall-clock timestamps can tie or invert between
// concurrent goroutines).
func Reconstruct(events []runlog.Event) (*Tree, *Timeline) {
	var runID string
	if len(events) > 0 {
		runID = events[0].RunID
	}
	t := NewTree(runID)
	tl := &Timeline{RunID: runID, Entries: make([]Entry, 0, len(events))}

	for _, e := range events {
		Apply(t, e)
		tl.Entries = append(tl.Entries, entryOf(e))
	}
	return t, tl
}

// Apply folds one event into t and returns it, so calls can be chained. A nil
// tree is created on demand. Unknown or malformed events are counted in
// Skipped rather than dropped silently or panicking — a live UI must survive a
// log written by a newer Hydra than itself.
func Apply(t *Tree, e runlog.Event) *Tree {
	if t == nil {
		t = NewTree(e.RunID)
	}
	if t.RunID == "" {
		t.RunID = e.RunID
	}

	// Run-level events describe the invocation, not a node in it, so they are
	// timeline-only. This is keyed on the kind rather than on whether nodeID
	// happened to come back empty: a run_started that carries a TaskID — which
	// is legitimate metadata — would otherwise fall through nodeID's TaskID
	// branch and materialise a phantom root labelled with a raw id (#204).
	if e.Kind == runlog.KindRunStarted || e.Kind == runlog.KindRunFinished {
		return t
	}

	id := nodeID(e)
	if id == "" {
		t.Skipped++
		return t
	}

	n := t.node(id)
	if e.TaskID != "" {
		n.TaskID = e.TaskID
	}
	if e.Head != "" {
		n.Head = e.Head
	}
	if e.Model != "" {
		n.Model = e.Model
	}
	if e.Tier != 0 {
		n.Tier = e.Tier
	}
	if e.CostUSD != 0 {
		n.CostUSD = e.CostUSD
	}
	if e.Confidence != 0 {
		n.Confidence = e.Confidence
	}
	if e.DurationMS != 0 {
		n.DurationMS = e.DurationMS
	}
	if e.Parent != "" && e.Parent != id {
		t.link(e.Parent, id)
	}

	ts := parseTS(e.TS)

	switch e.Kind {
	case runlog.KindHeadSelected, runlog.KindDispatchStarted, runlog.KindTaskStarted:
		n.State = StateRunning
		if n.StartedAt.IsZero() {
			n.StartedAt = ts
		}
	case runlog.KindDispatchFinished, runlog.KindTaskFinished, runlog.KindAttempt, runlog.KindSample:
		n.FinishedAt = ts
		n.State = stateFor(e.Status, StateOK)
		if n.StartedAt.IsZero() {
			n.StartedAt = ts
		}
	case runlog.KindError:
		n.FinishedAt = ts
		n.State = StateFailed
		if e.Detail != "" {
			n.Detail = e.Detail
		}
	case runlog.KindHandoff:
		// Collaboration edge, not ownership — this is why Handoffs is a
		// separate field from Children.
		if e.Ref != "" || e.Detail != "" {
			n.Handoffs = append(n.Handoffs, Handoff{
				To: e.Ref, Seq: e.Seq, TS: ts, Detail: e.Detail,
			})
		}
	case runlog.KindEdit:
		if n.State == "" {
			n.State = StateRunning
		}
		if e.Detail != "" {
			n.Detail = e.Detail
		}
	default:
		// A kind this build does not know about. The node still exists and
		// carries whatever fields came with it; only the lifecycle is unknown.
		if n.State == "" {
			n.State = StatePending
		}
	}
	return t
}

// node returns the node for id, creating it (and recording creation order) if new.
func (t *Tree) node(id string) *Node {
	if n, ok := t.Nodes[id]; ok {
		return n
	}
	n := &Node{ID: id, State: StatePending}
	t.Nodes[id] = n
	t.Order = append(t.Order, id)
	t.Roots = append(t.Roots, id) // provisional; link() promotes it to a child
	return n
}

// link records an ownership edge, creating either endpoint if needed. A node
// keeps its first owner: ownership is single by definition, so a later claim is
// a collaboration relationship, not a re-parenting.
func (t *Tree) link(parent, child string) {
	p := t.node(parent)
	c := t.node(child)
	if c.Parent != "" {
		return
	}
	c.Parent = parent
	for _, existing := range p.Children {
		if existing == child {
			return
		}
	}
	p.Children = append(p.Children, child)
	// No longer a root.
	for i, r := range t.Roots {
		if r == child {
			t.Roots = append(t.Roots[:i], t.Roots[i+1:]...)
			break
		}
	}
}

// nodeID picks the identity an event belongs to: the explicit agent if given,
// else the head, else the task. Dispatch emits head-scoped events today; a
// future spawning runtime will emit agent-scoped ones.
func nodeID(e runlog.Event) string {
	switch {
	case e.Agent != "":
		return e.Agent
	case e.Head != "":
		return e.Head
	default:
		return e.TaskID
	}
}

func stateFor(status string, dflt State) State {
	switch status {
	case "ok", "success":
		return StateOK
	case "failed", "error", "timeout", "canceled":
		return StateFailed
	case "":
		return dflt
	default:
		return State(status)
	}
}

func parseTS(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if ts, err := time.Parse(layout, s); err == nil {
			return ts
		}
	}
	return time.Time{}
}

func entryOf(e runlog.Event) Entry {
	return Entry{
		Seq: e.Seq, TS: parseTS(e.TS), Kind: e.Kind,
		NodeID: nodeID(e), TaskID: e.TaskID,
		Head: e.Head, Model: e.Model, Tier: e.Tier, Status: e.Status,
		CostUSD: e.CostUSD, DurationMS: e.DurationMS, Confidence: e.Confidence,
		Detail: e.Detail,
	}
}

// ── read helpers a renderer needs ────────────────────────────────────────────

// Row is one line of a flattened tree, with the depth a renderer needs to
// indent. Producing this here keeps every consumer from re-deriving it.
type Row struct {
	Node  *Node
	Depth int
	Last  bool // last child of its parent — for box-drawing glyphs
}

// Rows flattens the ownership tree depth-first in creation order. Cycles cannot
// occur (link refuses to re-parent), but a visited set guards anyway: a
// malformed log must not hang a UI.
func (t *Tree) Rows() []Row {
	var rows []Row
	visited := map[string]bool{}

	var walk func(id string, depth int, last bool)
	walk = func(id string, depth int, last bool) {
		n, ok := t.Nodes[id]
		if !ok || visited[id] {
			return
		}
		visited[id] = true
		rows = append(rows, Row{Node: n, Depth: depth, Last: last})
		for i, child := range n.Children {
			walk(child, depth+1, i == len(n.Children)-1)
		}
	}
	for i, r := range t.Roots {
		walk(r, 0, i == len(t.Roots)-1)
	}
	// Any node unreachable from a root (a log that lost its parent event) is
	// still shown, rather than vanishing.
	for _, id := range t.Order {
		if !visited[id] {
			walk(id, 0, true)
		}
	}
	return rows
}

// TotalCost sums every node's cost.
func (t *Tree) TotalCost() float64 {
	var sum float64
	for _, n := range t.Nodes {
		sum += n.CostUSD
	}
	return sum
}

// Span returns the run's wall-clock extent.
func (t *Tree) Span() (start, end time.Time) {
	for _, n := range t.Nodes {
		if !n.StartedAt.IsZero() && (start.IsZero() || n.StartedAt.Before(start)) {
			start = n.StartedAt
		}
		if !n.FinishedAt.IsZero() && n.FinishedAt.After(end) {
			end = n.FinishedAt
		}
	}
	return start, end
}

// CountByState tallies nodes per lifecycle state, for a status summary.
func (t *Tree) CountByState() map[State]int {
	counts := map[State]int{}
	for _, n := range t.Nodes {
		counts[n.State]++
	}
	return counts
}
