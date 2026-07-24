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

// starGraph: m leaves each depending on one center → highly supercritical.
func starGraph(m int) *Graph {
	nodes := []Node{{ID: "center", File: "center.go"}}
	var edges []Edge
	for i := 0; i < m; i++ {
		id := "leaf" + string(rune('A'+i))
		nodes = append(nodes, Node{ID: id, File: id + ".go"})
		edges = append(edges, Edge{From: id, To: "center"})
	}
	return fromDoc(Doc{Nodes: nodes, Edges: edges})
}

// pathGraph: a directed chain 0→1→…→(n-1). κ approaches 2 from below.
func pathGraph(n int) *Graph {
	var nodes []Node
	var edges []Edge
	for i := 0; i < n; i++ {
		id := "n" + string(rune('0'+i))
		nodes = append(nodes, Node{ID: id, File: id + ".go"})
		if i > 0 {
			edges = append(edges, Edge{From: "n" + string(rune('0'+i-1)), To: id})
		}
	}
	return fromDoc(Doc{Nodes: nodes, Edges: edges})
}

func TestKappa_TopologyOrdering(t *testing.T) {
	star := starGraph(9) // κ = (m+1)/2 = 5
	path := pathGraph(9)  // κ → 2⁻
	disc := fromDoc(Doc{Nodes: []Node{{ID: "x", File: "x.go"}, {ID: "y", File: "y.go"}}})

	if !star.Percolates() {
		t.Errorf("star κ = %.3f, expected supercritical (≥2)", star.Kappa())
	}
	if star.Kappa() <= path.Kappa() {
		t.Errorf("star κ (%.3f) should exceed path κ (%.3f)", star.Kappa(), path.Kappa())
	}
	if path.Kappa() >= 2.0 || path.Kappa() < 1.5 {
		t.Errorf("path κ = %.3f, want in [1.5, 2.0)", path.Kappa())
	}
	if disc.Kappa() != 0 || disc.Percolates() {
		t.Errorf("edgeless graph κ = %.3f, want 0 and non-percolating", disc.Kappa())
	}
}

// A hub-core file and a peripheral file with the SAME transitive-dependent count
// must be priced differently once the graph is supercritical.
func TestPercolation_EqualCountDifferentDegree(t *testing.T) {
	nodes := []Node{{ID: "X", File: "x.go"}, {ID: "Y", File: "y.go"}}
	var edges []Edge
	// X: a fan-in hub with 6 direct dependents (degree 6, count 6).
	for _, d := range []string{"a", "b", "c", "g", "h", "i"} {
		nodes = append(nodes, Node{ID: d, File: d + ".go"})
		edges = append(edges, Edge{From: d, To: "X"})
	}
	// Y: a 6-long chain of dependents (degree 1 at Y, count 6).
	chain := []string{"d", "e", "f", "p", "q", "r"}
	prev := "Y"
	for _, c := range chain {
		nodes = append(nodes, Node{ID: c, File: c + ".go"})
		edges = append(edges, Edge{From: c, To: prev})
		prev = c
	}
	g := fromDoc(Doc{Nodes: nodes, Edges: edges})

	if !g.Percolates() {
		t.Fatalf("test graph κ = %.3f, expected supercritical", g.Kappa())
	}
	if cx, cy := g.DependentCount("X"), g.DependentCount("Y"); cx != cy {
		t.Fatalf("precondition: equal counts required, got X=%d Y=%d", cx, cy)
	}
	bx, by := g.BlastRadiusForFile("x.go"), g.BlastRadiusForFile("y.go")
	if bx <= by {
		t.Errorf("hub file blast (%.3f) should exceed peripheral file blast (%.3f) at equal count", bx, by)
	}
	// The peripheral file (below-mean degree) gets no lift.
	if f := g.PercolationFactor("y.go"); f != 1.0 {
		t.Errorf("peripheral PercolationFactor = %.3f, want 1.0", f)
	}
	if f := g.PercolationFactor("x.go"); f <= 1.0 {
		t.Errorf("hub PercolationFactor = %.3f, want > 1.0", f)
	}
}

func TestPercolation_SubcriticalIsNeutral(t *testing.T) {
	// The canonical sample graph is subcritical (κ≈1.67) → factor 1.0 everywhere,
	// so blast radius is unchanged from the pre-percolation formula.
	g := sampleGraph()
	if g.Percolates() {
		t.Fatalf("sample graph unexpectedly supercritical (κ=%.3f)", g.Kappa())
	}
	if f := g.PercolationFactor("a.go"); f != 1.0 {
		t.Errorf("subcritical PercolationFactor = %.3f, want exactly 1.0", f)
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
