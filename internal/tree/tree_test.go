// SPDX-License-Identifier: MIT

package tree

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/runlog"
)

func ev(seq uint64, kind runlog.Kind, mods ...func(*runlog.Event)) runlog.Event {
	e := runlog.Event{
		V: runlog.SchemaVersion, Seq: seq, RunID: "run-1",
		TS: time.Now().UTC().Format(time.RFC3339Nano), Kind: kind,
	}
	for _, m := range mods {
		m(&e)
	}
	return e
}

func agent(id string) func(*runlog.Event)  { return func(e *runlog.Event) { e.Agent = id } }
func parent(id string) func(*runlog.Event) { return func(e *runlog.Event) { e.Parent = id } }
func head(h string) func(*runlog.Event)    { return func(e *runlog.Event) { e.Head = h } }
func status(s string) func(*runlog.Event)  { return func(e *runlog.Event) { e.Status = s } }
func cost(c float64) func(*runlog.Event)   { return func(e *runlog.Event) { e.CostUSD = c } }
func ref(r string) func(*runlog.Event)     { return func(e *runlog.Event) { e.Ref = r } }

func TestReconstruct_BuildsOwnershipTree(t *testing.T) {
	events := []runlog.Event{
		ev(1, runlog.KindTaskStarted, agent("root")),
		ev(2, runlog.KindTaskStarted, agent("child-a"), parent("root")),
		ev(3, runlog.KindTaskStarted, agent("child-b"), parent("root")),
		ev(4, runlog.KindTaskStarted, agent("grandchild"), parent("child-a")),
		ev(5, runlog.KindDispatchFinished, agent("grandchild"), status("ok"), cost(0.01)),
	}

	tr, tl := Reconstruct(events)

	if tr.RunID != "run-1" {
		t.Errorf("RunID = %q, want run-1", tr.RunID)
	}
	if len(tr.Nodes) != 4 {
		t.Fatalf("got %d nodes, want 4", len(tr.Nodes))
	}
	if len(tr.Roots) != 1 || tr.Roots[0] != "root" {
		t.Fatalf("Roots = %v, want [root]", tr.Roots)
	}
	if got := tr.Nodes["child-a"].Parent; got != "root" {
		t.Errorf("child-a parent = %q, want root", got)
	}
	if got := tr.Nodes["grandchild"].State; got != StateOK {
		t.Errorf("grandchild state = %q, want ok", got)
	}
	if len(tl.Entries) != len(events) {
		t.Errorf("timeline has %d entries, want %d", len(tl.Entries), len(events))
	}
}

// The load-bearing decision from AGENT_TREE_MODEL.md: a handoff is a
// collaboration edge, never an ownership edge. If a handoff ever became a
// child, drill-down navigation would stop being a tree.
func TestApply_HandoffIsOverlayNotOwnership(t *testing.T) {
	events := []runlog.Event{
		ev(1, runlog.KindTaskStarted, agent("a")),
		ev(2, runlog.KindTaskStarted, agent("b")),
		ev(3, runlog.KindHandoff, agent("a"), ref("b"), func(e *runlog.Event) { e.Detail = "context" }),
	}
	tr, _ := Reconstruct(events)

	a := tr.Nodes["a"]
	if len(a.Children) != 0 {
		t.Errorf("handoff created ownership children %v, it must be overlay only", a.Children)
	}
	if len(a.Handoffs) != 1 || a.Handoffs[0].To != "b" {
		t.Fatalf("handoff overlay = %+v, want one edge to b", a.Handoffs)
	}
	if tr.Nodes["b"].Parent != "" {
		t.Errorf("handoff re-parented b to %q, ownership must be untouched", tr.Nodes["b"].Parent)
	}
	// Both remain roots: neither owns the other.
	if len(tr.Roots) != 2 {
		t.Errorf("Roots = %v, want both a and b", tr.Roots)
	}
}

// Ownership is single by definition. A second claim is a collaboration
// relationship, not a re-parenting, otherwise the "tree" silently becomes a
// graph and drill-down breaks.
func TestApply_NodeKeepsFirstOwner(t *testing.T) {
	events := []runlog.Event{
		ev(1, runlog.KindTaskStarted, agent("p1")),
		ev(2, runlog.KindTaskStarted, agent("p2")),
		ev(3, runlog.KindTaskStarted, agent("c"), parent("p1")),
		ev(4, runlog.KindTaskStarted, agent("c"), parent("p2")), // second claim
	}
	tr, _ := Reconstruct(events)

	if got := tr.Nodes["c"].Parent; got != "p1" {
		t.Errorf("c parent = %q, want p1 (first owner wins)", got)
	}
	if len(tr.Nodes["p2"].Children) != 0 {
		t.Errorf("p2 gained children %v from a second ownership claim", tr.Nodes["p2"].Children)
	}
}

// The property that makes shipping both functions safe: folding events one at a
// time must equal reconstructing them in bulk. If these diverge, the desktop
// app's incremental path and the terminal's full-rebuild path show different
// trees for the same run.
func TestApply_EqualsReconstruct(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	kinds := []runlog.Kind{
		runlog.KindTaskStarted, runlog.KindHeadSelected, runlog.KindDispatchFinished,
		runlog.KindError, runlog.KindAttempt, runlog.KindSample, runlog.KindEdit,
	}
	agents := []string{"a", "b", "c", "d"}

	for trial := 0; trial < 50; trial++ {
		var events []runlog.Event
		for i := 0; i < 40; i++ {
			mods := []func(*runlog.Event){agent(agents[rng.Intn(len(agents))])}
			if rng.Intn(3) == 0 {
				mods = append(mods, parent(agents[rng.Intn(len(agents))]))
			}
			if rng.Intn(4) == 0 {
				mods = append(mods, status("ok"), cost(rng.Float64()))
			}
			events = append(events, ev(uint64(i+1), kinds[rng.Intn(len(kinds))], mods...))
		}

		bulk, _ := Reconstruct(events)

		folded := NewTree(events[0].RunID)
		for _, e := range events {
			folded = Apply(folded, e)
		}

		if len(bulk.Nodes) != len(folded.Nodes) {
			t.Fatalf("trial %d: bulk has %d nodes, folded has %d", trial, len(bulk.Nodes), len(folded.Nodes))
		}
		if fmt.Sprint(bulk.Roots) != fmt.Sprint(folded.Roots) {
			t.Fatalf("trial %d: roots differ: bulk=%v folded=%v", trial, bulk.Roots, folded.Roots)
		}
		if fmt.Sprint(bulk.Order) != fmt.Sprint(folded.Order) {
			t.Fatalf("trial %d: creation order differs", trial)
		}
		for id, bn := range bulk.Nodes {
			fn, ok := folded.Nodes[id]
			if !ok {
				t.Fatalf("trial %d: folded missing node %q", trial, id)
			}
			if bn.Parent != fn.Parent || bn.State != fn.State ||
				fmt.Sprint(bn.Children) != fmt.Sprint(fn.Children) ||
				bn.CostUSD != fn.CostUSD {
				t.Fatalf("trial %d: node %q differs:\n bulk=%+v\n fold=%+v", trial, id, bn, fn)
			}
		}
	}
}

// A live UI must survive a log written by a newer Hydra than itself.
func TestApply_UnknownKindDoesNotPanic(t *testing.T) {
	tr := NewTree("run-x")
	tr = Apply(tr, ev(1, runlog.Kind("kind_from_the_future"), agent("a")))
	if len(tr.Nodes) != 1 {
		t.Fatalf("unknown kind produced %d nodes, want 1", len(tr.Nodes))
	}
	if tr.Nodes["a"].State != StatePending {
		t.Errorf("unknown kind set state %q, want pending", tr.Nodes["a"].State)
	}
}

// Events that belong to no node are counted, not silently dropped, a partial
// run must not render as a complete one.
func TestApply_UnattributableEventsAreCounted(t *testing.T) {
	tr := NewTree("run-x")
	tr = Apply(tr, ev(1, runlog.KindAttempt)) // no agent/head/task
	if tr.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", tr.Skipped)
	}
	// Run-level events legitimately have no node and must not count as skipped.
	tr = Apply(tr, ev(2, runlog.KindRunStarted))
	tr = Apply(tr, ev(3, runlog.KindRunFinished))
	if tr.Skipped != 1 {
		t.Errorf("Skipped = %d after run-level events, want still 1", tr.Skipped)
	}
}

func TestApply_NilTreeIsCreated(t *testing.T) {
	tr := Apply(nil, ev(1, runlog.KindTaskStarted, agent("a")))
	if tr == nil || len(tr.Nodes) != 1 {
		t.Fatalf("Apply(nil) = %+v, want a tree with one node", tr)
	}
	if tr.RunID != "run-1" {
		t.Errorf("RunID = %q, want run-1", tr.RunID)
	}
}

func TestReconstruct_EmptyIsUsable(t *testing.T) {
	tr, tl := Reconstruct(nil)
	if tr == nil || tl == nil {
		t.Fatal("Reconstruct(nil) returned nil")
	}
	if len(tr.Rows()) != 0 || len(tl.Entries) != 0 {
		t.Error("empty run produced rows or entries")
	}
}

func TestRows_DepthAndLastFlags(t *testing.T) {
	events := []runlog.Event{
		ev(1, runlog.KindTaskStarted, agent("root")),
		ev(2, runlog.KindTaskStarted, agent("a"), parent("root")),
		ev(3, runlog.KindTaskStarted, agent("b"), parent("root")),
		ev(4, runlog.KindTaskStarted, agent("a1"), parent("a")),
	}
	tr, _ := Reconstruct(events)
	rows := tr.Rows()

	if len(rows) != 4 {
		t.Fatalf("got %d rows, want 4", len(rows))
	}
	want := []struct {
		id    string
		depth int
		last  bool
	}{
		{"root", 0, true},
		{"a", 1, false},
		{"a1", 2, true},
		{"b", 1, true},
	}
	for i, w := range want {
		if rows[i].Node.ID != w.id || rows[i].Depth != w.depth || rows[i].Last != w.last {
			t.Errorf("row %d = {%s d=%d last=%v}, want {%s d=%d last=%v}",
				i, rows[i].Node.ID, rows[i].Depth, rows[i].Last, w.id, w.depth, w.last)
		}
	}
}

// A log that lost a parent event must still show its orphans, not silently
// hide work that happened.
func TestRows_OrphansAreStillShown(t *testing.T) {
	tr := NewTree("run-x")
	// Child references a parent that never got its own event... which link()
	// creates. Simulate a true orphan by adding a node with a parent that is
	// removed from Roots but unreachable: easiest is a self-consistent tree
	// plus a node created directly.
	tr = Apply(tr, ev(1, runlog.KindTaskStarted, agent("visible")))
	tr = Apply(tr, ev(2, runlog.KindTaskStarted, agent("orphan")))
	// Manually break reachability the way a corrupt log could.
	tr.Roots = []string{"visible"}

	rows := tr.Rows()
	var sawOrphan bool
	for _, r := range rows {
		if r.Node.ID == "orphan" {
			sawOrphan = true
		}
	}
	if !sawOrphan {
		t.Error("an unreachable node vanished from Rows, work would be hidden")
	}
}

func TestTree_Aggregates(t *testing.T) {
	events := []runlog.Event{
		ev(1, runlog.KindDispatchFinished, agent("a"), status("ok"), cost(0.02)),
		ev(2, runlog.KindDispatchFinished, agent("b"), status("ok"), cost(0.03)),
		ev(3, runlog.KindError, agent("c"), status("failed")),
	}
	tr, _ := Reconstruct(events)

	if got := tr.TotalCost(); got < 0.049 || got > 0.051 {
		t.Errorf("TotalCost = %v, want ~0.05", got)
	}
	counts := tr.CountByState()
	if counts[StateOK] != 2 {
		t.Errorf("ok count = %d, want 2", counts[StateOK])
	}
	if counts[StateFailed] != 1 {
		t.Errorf("failed count = %d, want 1", counts[StateFailed])
	}
	start, end := tr.Span()
	if start.IsZero() || end.IsZero() {
		t.Errorf("Span() = (%v, %v), want a real interval", start, end)
	}
}

// One run legitimately has several writers, the CLI logs the run's lifecycle
// while dispatch logs the routing inside it, so seq values interleave and
// repeat across them. runlog's own doc says seq "is not the sort key"; file
// order is. A timeline that re-sorted by seq would put a run's end before a
// handoff that happened first (#204).
func TestTimeline_PreservesFileOrderAcrossWriters(t *testing.T) {
	// Exactly the interleaving a real dispatch produces: writer A is the CLI
	// (run lifecycle), writer B is dispatch (routing), each counting from 1.
	events := []runlog.Event{
		ev(1, runlog.KindRunStarted),
		ev(1, runlog.KindHeadSelected, agent("h")),
		ev(2, runlog.KindDispatchFinished, agent("h"), status("ok")),
		ev(3, runlog.KindHandoff, agent("h"), ref("next")),
		ev(2, runlog.KindRunFinished),
	}
	_, tl := Reconstruct(events)

	var kinds []runlog.Kind
	for _, e := range tl.Entries {
		kinds = append(kinds, e.Kind)
	}
	want := []runlog.Kind{
		runlog.KindRunStarted, runlog.KindHeadSelected,
		runlog.KindDispatchFinished, runlog.KindHandoff, runlog.KindRunFinished,
	}
	for i := range want {
		if i >= len(kinds) || kinds[i] != want[i] {
			t.Fatalf("timeline order = %v, want %v, sorting by seq reorders across writers", kinds, want)
		}
	}
}

// The head-scoped events dispatch emits today must reconstruct sensibly, since
// that is the only producer wired up so far.
func TestReconstruct_FromRealDispatchShape(t *testing.T) {
	events := []runlog.Event{
		ev(1, runlog.KindHeadSelected, head("h1"), func(e *runlog.Event) { e.TaskID = "t1"; e.Tier = 3 }),
		ev(2, runlog.KindError, head("h1"), status("failed"), func(e *runlog.Event) { e.Detail = "timeout" }),
		ev(3, runlog.KindHeadSelected, head("h2"), func(e *runlog.Event) { e.TaskID = "t1"; e.Tier = 5 }),
		ev(4, runlog.KindDispatchFinished, head("h2"), status("ok"), cost(0.01)),
	}
	tr, _ := Reconstruct(events)

	if len(tr.Nodes) != 2 {
		t.Fatalf("got %d nodes, want 2 (one per head tried)", len(tr.Nodes))
	}
	if tr.Nodes["h1"].State != StateFailed {
		t.Errorf("h1 state = %q, want failed", tr.Nodes["h1"].State)
	}
	if tr.Nodes["h1"].Detail != "timeout" {
		t.Errorf("h1 detail = %q, want the failure reason", tr.Nodes["h1"].Detail)
	}
	if tr.Nodes["h2"].State != StateOK {
		t.Errorf("h2 state = %q, want ok", tr.Nodes["h2"].State)
	}
	if tr.Nodes["h2"].TaskID != "t1" && tr.Nodes["h1"].TaskID != "t1" {
		t.Error("task id was not carried onto the nodes")
	}
}

// A run-level event describes the invocation, not a node in it. It carries a
// TaskID as legitimate metadata, and nodeID falls back to TaskID, so without an
// explicit kind check the run's own start would appear in the tree as a node
// named after a raw timestamp id (#204).
func TestApply_RunLevelEventsNeverCreateNodes(t *testing.T) {
	events := []runlog.Event{
		{V: 1, Seq: 1, RunID: "r", TaskID: "task-abc", Kind: runlog.KindRunStarted},
		{V: 1, Seq: 2, RunID: "r", TaskID: "task-abc", Kind: runlog.KindHeadSelected, Head: "agy"},
		{V: 1, Seq: 3, RunID: "r", TaskID: "task-abc", Kind: runlog.KindDispatchFinished,
			Head: "agy", Status: "ok"},
		{V: 1, Seq: 4, RunID: "r", TaskID: "task-abc", Kind: runlog.KindRunFinished},
	}
	tr, tl := Reconstruct(events)

	if _, exists := tr.Nodes["task-abc"]; exists {
		t.Error("run-level event created a node keyed by its TaskID")
	}
	rows := tr.Rows()
	if len(rows) != 1 {
		t.Fatalf("%d rows, want 1 (just the head): %+v", len(rows), rows)
	}
	if rows[0].Node.ID != "agy" {
		t.Errorf("row 0 is %q, want the head %q", rows[0].Node.ID, "agy")
	}
	if tr.Skipped != 0 {
		t.Errorf("Skipped = %d; run-level events are expected, not malformed", tr.Skipped)
	}
	// They must still appear on the timeline, that is where a run's start and
	// end belong.
	if len(tl.Entries) != len(events) {
		t.Errorf("%d timeline entries, want %d, run-level events belong on the timeline",
			len(tl.Entries), len(events))
	}
}

// A KindEdit event carries no identity of its own, it records which file
// changed, not who changed it, so nodeID falls back to TaskID. Before #434,
// Agent held the edited file path instead (a since-removed overload), which
// took priority over that TaskID fallback and split the run into two
// disconnected nodes: the real agent, and a phantom one named after the file,
// stuck at StatePending forever since nothing else ever touched it.
func TestApply_EditEventJoinsItsTasksNodeNotAPhantomOne(t *testing.T) {
	events := []runlog.Event{
		{V: 1, Seq: 1, RunID: "r", TaskID: "task-abc", Kind: runlog.KindHeadSelected, Head: "flash-med"},
		{V: 1, Seq: 2, RunID: "r", TaskID: "task-abc", Kind: runlog.KindDispatchFinished,
			Head: "flash-med", Status: "ok"},
		{V: 1, Seq: 3, RunID: "r", TaskID: "task-abc", Kind: runlog.KindEdit,
			File: "/private/tmp/scratch/greet.py", Ref: "000001", Detail: "+2/-0"},
	}
	tr, _ := Reconstruct(events)

	if len(tr.Nodes) != 1 {
		t.Fatalf("got %d nodes, want 1, the edit must join flash-med, not mint its own: %+v",
			len(tr.Nodes), tr.Nodes)
	}
	n := tr.Nodes["flash-med"]
	if n == nil {
		t.Fatal("no node keyed by the real head \"flash-med\"")
	}
	if n.State != StateOK {
		t.Errorf("state = %q, want ok, the edit event must not downgrade a finished node", n.State)
	}
	if n.Detail != "+2/-0" {
		t.Errorf("detail = %q, want the edit's line counts", n.Detail)
	}
}

// Apply refuses to create a node for a run-level event; entryOf must agree, or
// the timeline names a node the tree does not have. nodeID falls back to
// TaskID, which a run_started legitimately carries.
func TestTimeline_RunLevelEntriesNameNoNode(t *testing.T) {
	events := []runlog.Event{
		{V: 1, Seq: 1, RunID: "r", TaskID: "task-abc", Kind: runlog.KindRunStarted},
		{V: 1, Seq: 2, RunID: "r", TaskID: "task-abc", Kind: runlog.KindHeadSelected, Head: "agy"},
		{V: 1, Seq: 3, RunID: "r", TaskID: "task-abc", Kind: runlog.KindRunFinished},
	}
	tr, tl := Reconstruct(events)

	for _, e := range tl.Entries {
		switch e.Kind {
		case runlog.KindRunStarted, runlog.KindRunFinished:
			if e.NodeID != "" {
				t.Errorf("%s names node %q; run-level events belong to no node", e.Kind, e.NodeID)
			}
			// The task id is still carried, it is real metadata, just not a node.
			if e.TaskID != "task-abc" {
				t.Errorf("%s dropped its TaskID", e.Kind)
			}
		default:
			if e.NodeID == "" {
				t.Errorf("%s has no NodeID", e.Kind)
			}
			if _, ok := tr.Nodes[e.NodeID]; !ok {
				t.Errorf("%s names node %q, which the tree does not have", e.Kind, e.NodeID)
			}
		}
	}
}

// editOnlyEvents builds n KindEdit events, each its own distinct task with no
// Agent/Head, the shape that made nodeByTaskID's linear scan over Order pay
// O(n) per event, O(n^2) for the run (#523).
func editOnlyEvents(n int) []runlog.Event {
	events := make([]runlog.Event, n)
	for i := range events {
		events[i] = runlog.Event{
			V: runlog.SchemaVersion, Seq: uint64(i + 1), RunID: "run-edits",
			TS: time.Now().UTC().Format(time.RFC3339Nano), Kind: runlog.KindEdit,
			TaskID: fmt.Sprintf("task-%d", i), File: "/scratch/f.py", Detail: "+1/-0",
		}
	}
	return events
}

// A run of many distinct edit-only tasks must still reconstruct into one node
// per task, correctly attributed, the byTaskID index changes the lookup's
// cost, not its result.
func TestReconstruct_ManyDistinctEditTasks(t *testing.T) {
	const n = 5000
	events := editOnlyEvents(n)

	tr, tl := Reconstruct(events)

	if len(tr.Nodes) != n {
		t.Fatalf("got %d nodes, want %d, one per distinct edit-only task", len(tr.Nodes), n)
	}
	if len(tr.Order) != n {
		t.Fatalf("Order has %d entries, want %d", len(tr.Order), n)
	}
	if len(tr.Roots) != n {
		t.Fatalf("Roots has %d entries, want %d, no Parent was ever set", len(tr.Roots), n)
	}
	if len(tl.Entries) != n {
		t.Fatalf("timeline has %d entries, want %d", len(tl.Entries), n)
	}
	if tr.Skipped != 0 {
		t.Errorf("Skipped = %d, want 0", tr.Skipped)
	}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("task-%d", i)
		node := tr.Nodes[id]
		if node == nil {
			t.Fatalf("no node for %s", id)
		}
		if node.TaskID != id {
			t.Errorf("node %s TaskID = %q, want %q", id, node.TaskID, id)
		}
		// node() seeds a fresh node at StatePending, and KindEdit's own
		// "if n.State == \"\"" guard never fires on it, so it stays pending.
		if node.State != StatePending {
			t.Errorf("node %s state = %q, want pending", id, node.State)
		}
		if node.Detail != "+1/-0" {
			t.Errorf("node %s detail = %q, want the edit's line counts", id, node.Detail)
		}
	}

	// Folding one at a time must agree with the bulk reconstruction (the same
	// invariant TestApply_EqualsReconstruct pins for smaller, mixed-kind runs).
	folded := NewTree(events[0].RunID)
	for _, e := range events {
		folded = Apply(folded, e)
	}
	if len(folded.Nodes) != len(tr.Nodes) {
		t.Fatalf("folded %d nodes, bulk %d", len(folded.Nodes), len(tr.Nodes))
	}
}

// BenchmarkReconstruct tracks the cost of folding a run of distinct edit-only
// tasks. Before the byTaskID index (#523), ns/op per event grew with n
// (O(n^2) total); after it, ns/op per event should stay roughly flat.
func BenchmarkReconstruct(b *testing.B) {
	for _, n := range []int{2000, 8000, 32000} {
		events := editOnlyEvents(n)
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				Reconstruct(events)
			}
		})
	}
}
