package graph

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

// a ← b ← c   and   a ← d   (arrows point from dependency to dependent)
//   b depends on a; c depends on b; d depends on a.
func sampleGraph() *Graph {
	return fromDoc(Doc{
		Nodes: []Node{
			{ID: "a", File: "a.go"},
			{ID: "b", File: "b.go"},
			{ID: "c", File: "c.go"},
			{ID: "d", File: "d.go"},
		},
		Edges: []Edge{
			{From: "b", To: "a"},
			{From: "c", To: "b"},
			{From: "d", To: "a"},
		},
	})
}

func TestDependentCount_Transitive(t *testing.T) {
	g := sampleGraph()
	if got := g.DependentCount("a"); got != 3 { // b, c (via b), d
		t.Errorf("DependentCount(a) = %d, want 3", got)
	}
	if got := g.DependentCount("b"); got != 1 { // c
		t.Errorf("DependentCount(b) = %d, want 1", got)
	}
	if got := g.DependentCount("c"); got != 0 { // leaf
		t.Errorf("DependentCount(c) = %d, want 0", got)
	}
}

func TestBlastRadiusForFile(t *testing.T) {
	g := sampleGraph()
	// a.go: 3 transitive dependents → 1 + log2(4) = 3.0
	if got := g.BlastRadiusForFile("a.go"); math.Abs(got-3.0) > 1e-9 {
		t.Errorf("blast(a.go) = %v, want 3.0", got)
	}
	// c.go: leaf → 1 + log2(1) = 1.0
	if got := g.BlastRadiusForFile("c.go"); math.Abs(got-1.0) > 1e-9 {
		t.Errorf("blast(c.go) = %v, want 1.0", got)
	}
	// a hub is riskier than a leaf.
	if g.BlastRadiusForFile("a.go") <= g.BlastRadiusForFile("c.go") {
		t.Error("hub file should have larger blast radius than a leaf")
	}
}

func TestBlastRadius_BasenameMatch(t *testing.T) {
	g := sampleGraph()
	if got := g.BlastRadiusForFile("/repo/pkg/a.go"); math.Abs(got-3.0) > 1e-9 {
		t.Errorf("basename match blast = %v, want 3.0", got)
	}
}

func TestBlastRadius_UnknownAndEmpty(t *testing.T) {
	g := sampleGraph()
	if got := g.BlastRadiusForFile("nonexistent.go"); got != 1.0 {
		t.Errorf("unknown file blast = %v, want 1.0", got)
	}
	var empty *Graph
	if got := empty.BlastRadiusForFile("a.go"); got != 1.0 {
		t.Errorf("nil graph blast = %v, want 1.0", got)
	}
}

func TestCycleSafety(t *testing.T) {
	g := fromDoc(Doc{
		Nodes: []Node{{ID: "a", File: "a.go"}, {ID: "b", File: "b.go"}},
		Edges: []Edge{{From: "a", To: "b"}, {From: "b", To: "a"}}, // mutual
	})
	// Must terminate; each depends on the other, so one transitive dependent each.
	if got := g.DependentCount("a"); got != 1 {
		t.Errorf("DependentCount(a) with cycle = %d, want 1", got)
	}
}

func TestLoad_MissingFileIsEmpty(t *testing.T) {
	g, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("missing graph should not error: %v", err)
	}
	if got := g.BlastRadiusForFile("a.go"); got != 1.0 {
		t.Errorf("empty graph blast = %v, want 1.0", got)
	}
}

func TestCoupling(t *testing.T) {
	// Two files whose impact sets are disjoint → minimal coordination cost.
	disjoint := fromDoc(Doc{
		Nodes: []Node{{ID: "x", File: "x.go"}, {ID: "y", File: "y.go"}},
		Edges: nil, // no dependents, no overlap
	})
	if got := disjoint.Coupling([]string{"x.go", "y.go"}); math.Abs(got-kMin) > 1e-9 {
		t.Errorf("disjoint Coupling = %v, want kMin=%v", got, kMin)
	}

	// Two files that share their entire dependent set → maximal coupling.
	// hub1, hub2 both depended on by the same {a,b,c}.
	shared := fromDoc(Doc{
		Nodes: []Node{
			{ID: "h1", File: "h1.go"}, {ID: "h2", File: "h2.go"},
			{ID: "a", File: "a.go"}, {ID: "b", File: "b.go"}, {ID: "c", File: "c.go"},
		},
		Edges: []Edge{
			{From: "a", To: "h1"}, {From: "b", To: "h1"}, {From: "c", To: "h1"},
			{From: "a", To: "h2"}, {From: "b", To: "h2"}, {From: "c", To: "h2"},
		},
	})
	k := shared.Coupling([]string{"h1.go", "h2.go"})
	if k <= disjoint.Coupling([]string{"x.go", "y.go"}) {
		t.Errorf("shared-subgraph coupling (%v) should exceed disjoint (%v)", k, disjoint.Coupling([]string{"x.go", "y.go"}))
	}
	if k < 0.10 { // {a,b,c} shared out of union {h1,a,b,c}∪{h2,a,b,c} → strong overlap
		t.Errorf("shared coupling = %v, expected substantial overlap", k)
	}

	// Fewer than two files → kMin (nothing to coordinate).
	if got := shared.Coupling([]string{"h1.go"}); got != kMin {
		t.Errorf("single-file Coupling = %v, want kMin", got)
	}
}

func TestLoad_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "graph.json")
	data := `{"nodes":[{"id":"a","file":"a.go"},{"id":"b","file":"b.go"}],"edges":[{"from":"b","to":"a"}]}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	g, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := g.DependentCount("a"); got != 1 {
		t.Errorf("loaded DependentCount(a) = %d, want 1", got)
	}
}
