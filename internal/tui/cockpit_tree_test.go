// SPDX-License-Identifier: MIT

package tui

import (
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/tree"
)

// The old view stored a box-drawing prefix per node, which is why it could only
// ever render one fixed shape. Deriving it from depth/last is what makes
// arbitrary depth and branching work.
func TestTreePrefix_DerivedFromDepthAndLast(t *testing.T) {
	tests := []struct {
		name  string
		depth int
		last  bool
		want  string
	}{
		{"root", 0, false, ""},
		{"root last", 0, true, ""},
		{"child", 1, false, "├─ "},
		{"last child", 1, true, "└─ "},
		{"grandchild", 2, false, "│  ├─ "},
		{"last grandchild", 2, true, "│  └─ "},
		{"depth 4", 4, false, "│  │  │  ├─ "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := treePrefix(tree.Row{Depth: tt.depth, Last: tt.last})
			if got != tt.want {
				t.Errorf("treePrefix(depth=%d,last=%v) = %q, want %q", tt.depth, tt.last, got, tt.want)
			}
		})
	}
}

func TestNodeLabel_PrefersHeadOverID(t *testing.T) {
	if got := nodeLabel(&tree.Node{ID: "abc123", Head: "claude"}); got != "claude" {
		t.Errorf("nodeLabel = %q, want the head name", got)
	}
	if got := nodeLabel(&tree.Node{ID: "abc123"}); got != "abc123" {
		t.Errorf("nodeLabel with no head = %q, want the id", got)
	}
}

// With nothing recorded the view must say so and point at how to produce a run
// — never fall back to a fictional example, which is what it used to show.
func TestTreeView_EmptyStateIsHonest(t *testing.T) {
	m := Cockpit{w: 100, h: 30, ready: true}
	out := m.tree(100, 30)

	if !strings.Contains(out, "no runs recorded") {
		t.Errorf("empty tree view does not say it is empty:\n%s", out)
	}
	if !strings.Contains(out, "hyctl dispatch") {
		t.Error("empty state does not tell the user how to produce a run")
	}
	// The fictional run must be gone for good.
	for _, ghost := range []string{"token-rotation", "worker-1", "orchestrator"} {
		if strings.Contains(out, ghost) {
			t.Errorf("empty view still shows the old fictional node %q", ghost)
		}
	}
}

// A real run must render with correct structure at arbitrary depth.
func TestTreeView_RendersRealRun(t *testing.T) {
	events := []runlog.Event{
		{V: 1, Seq: 1, RunID: "r", Kind: runlog.KindTaskStarted, Agent: "root"},
		{V: 1, Seq: 2, RunID: "r", Kind: runlog.KindTaskStarted, Agent: "a", Parent: "root"},
		{V: 1, Seq: 3, RunID: "r", Kind: runlog.KindTaskStarted, Agent: "deep", Parent: "a"},
		{V: 1, Seq: 4, RunID: "r", Kind: runlog.KindDispatchFinished, Agent: "deep",
			Head: "claude", Model: "M", Tier: 3, Status: "ok", CostUSD: 0.02, DurationMS: 1200},
		{V: 1, Seq: 5, RunID: "r", Kind: runlog.KindTaskStarted, Agent: "b", Parent: "root"},
	}
	tr, _ := tree.Reconstruct(events)

	m := Cockpit{w: 110, h: 30, ready: true, runID: "r", treeRows: tr.Rows()}
	out := m.tree(110, 30)

	// Depth-2 node must carry the nested prefix, which the old fixed-shape
	// renderer could not produce for an arbitrary tree.
	if !strings.Contains(out, "│  ") {
		t.Errorf("no nested prefix rendered — depth is not being derived:\n%s", out)
	}
	for _, want := range []string{"root", "claude", "ok"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered tree missing %q:\n%s", want, out)
		}
	}
	// Real cost from the event, not an invented figure.
	if !strings.Contains(out, "0.0200") {
		t.Errorf("real cost not rendered:\n%s", out)
	}
	if strings.Contains(out, "run r") == false {
		t.Errorf("run id not shown in the header:\n%s", out)
	}
}

// Confidence is shown only when recorded — the previous view printed one for
// every node regardless.
func TestTreeView_OmitsUnrecordedConfidence(t *testing.T) {
	events := []runlog.Event{
		{V: 1, Seq: 1, RunID: "r", Kind: runlog.KindDispatchFinished, Agent: "n1", Status: "ok"},
	}
	tr, _ := tree.Reconstruct(events)
	m := Cockpit{w: 100, h: 30, ready: true, runID: "r", treeRows: tr.Rows()}

	if strings.Contains(m.tree(100, 30), "confidence") {
		t.Error("a confidence line was rendered for a node that never recorded one")
	}
}

// A selection cursor beyond the row count must not panic — rows come from a
// log whose length the view does not control.
func TestTreeView_SelectionOutOfRangeIsSafe(t *testing.T) {
	events := []runlog.Event{
		{V: 1, Seq: 1, RunID: "r", Kind: runlog.KindTaskStarted, Agent: "only"},
	}
	tr, _ := tree.Reconstruct(events)

	for _, sel := range []int{-5, 1, 99} {
		m := Cockpit{w: 100, h: 30, ready: true, runID: "r", treeRows: tr.Rows(), treeSel: sel}
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("treeSel=%d panicked: %v", sel, r)
				}
			}()
			if out := m.tree(100, 30); out == "" {
				t.Errorf("treeSel=%d rendered nothing", sel)
			}
		}()
	}
}

// A2A handoffs must render as an overlay, distinct from ownership children.
func TestTreeView_ShowsHandoffOverlay(t *testing.T) {
	events := []runlog.Event{
		{V: 1, Seq: 1, RunID: "r", Kind: runlog.KindTaskStarted, Agent: "a"},
		{V: 1, Seq: 2, RunID: "r", Kind: runlog.KindTaskStarted, Agent: "b"},
		{V: 1, Seq: 3, RunID: "r", Kind: runlog.KindHandoff, Agent: "a", Ref: "b", Detail: "ctx"},
	}
	tr, _ := tree.Reconstruct(events)
	m := Cockpit{w: 100, h: 30, ready: true, runID: "r", treeRows: tr.Rows()}
	out := m.tree(100, 30)

	if !strings.Contains(out, "┄") {
		t.Errorf("handoff overlay glyph not rendered:\n%s", out)
	}
}
