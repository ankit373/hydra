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
