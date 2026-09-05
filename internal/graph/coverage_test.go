// SPDX-License-Identifier: MIT

package graph

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultPath_IsGraphJSONAtTheRepoRoot(t *testing.T) {
	root := filepath.Join("a", "b")
	if got, want := DefaultPath(root), filepath.Join(root, "graph.json"); got != want {
		t.Errorf("DefaultPath(%q) = %q, want %q", root, got, want)
	}
}

// Load's degradation is deliberate and load-bearing: a missing graph must yield
// an empty graph and no error, so blast-radius queries fall back to 1.0 rather
// than failing a dispatch. Callers tell the default from a measurement with
// Empty/Knows (#251).
func TestLoad_MissingFileIsAnEmptyGraphNotAnError(t *testing.T) {
	g, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("a missing graph errored: %v", err)
	}
	if g == nil {
		t.Fatal("Load returned a nil graph")
	}
	if !g.Empty() {
		t.Error("a missing graph did not report Empty()")
	}
	if g.BlastRadiusForFile("anything.go") != 1.0 {
		t.Error("a missing graph did not degrade to a blast radius of 1.0")
	}
}

// Malformed JSON is different: the file exists and says something we cannot
// read, which is a problem the operator needs told about rather than silently
// treated as "no dependencies".
func TestLoad_MalformedJSONIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "graph.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("malformed graph.json loaded without error, it would be " +
			"indistinguishable from a codebase with no dependencies")
	}
}

func TestLoad_ReadsNodesAndEdges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "graph.json")
	body := `{"nodes":[{"id":"a","file":"a.go"},{"id":"b","file":"b.go"}],
	          "edges":[{"from":"b","to":"a"}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	g, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if g.Empty() {
		t.Fatal("a graph with two nodes reported Empty()")
	}
	if !g.Knows("a.go") {
		t.Error("Knows(a.go) = false for a file the graph declares")
	}
	if g.Knows("nowhere.go") {
		t.Error("Knows returned true for a file the graph does not contain")
	}
	// b depends on a, so changing a breaks b.
	if got := g.DependentCount("a"); got != 1 {
		t.Errorf("DependentCount(a) = %d, want 1", got)
	}
	if got := g.DependentCount("b"); got != 0 {
		t.Errorf("DependentCount(b) = %d, want 0, nothing depends on a leaf", got)
	}
}

// Kappa and the percolation factor must be well-defined on the degenerate
// graphs, since those are what a fresh repo and a missing file produce.
func TestKappaAndPercolation_OnDegenerateGraphs(t *testing.T) {
	var nilGraph *Graph
	if got := nilGraph.Kappa(); got != 0 {
		t.Errorf("nil graph Kappa = %v, want 0", got)
	}
	if nilGraph.Percolates() {
		t.Error("a nil graph reported as cascade-capable")
	}
	if got := nilGraph.PercolationFactor("x.go"); got != 1.0 {
		t.Errorf("nil graph PercolationFactor = %v, want 1.0", got)
	}

	empty := &Graph{}
	if got := empty.Kappa(); got != 0 {
		t.Errorf("empty graph Kappa = %v, want 0", got)
	}
	if got := empty.PercolationFactor("x.go"); got != 1.0 {
		t.Errorf("empty graph PercolationFactor = %v, want 1.0", got)
	}
	if !empty.Empty() {
		t.Error("the zero-value Graph did not report Empty()")
	}
}

// A subcritical graph must never lift a file's blast radius: the factor exists
// to weight hubs inside a cascade-capable core, and there is no such core when
// κ < 2.
func TestPercolationFactor_SubcriticalGraphNeverLifts(t *testing.T) {
	// A path: a → b → c. Sparse, so κ stays below 2.
	g := fromDoc(Doc{
		Nodes: []Node{{ID: "a", File: "a.go"}, {ID: "b", File: "b.go"}, {ID: "c", File: "c.go"}},
		Edges: []Edge{{From: "b", To: "a"}, {From: "c", To: "b"}},
	})
	if g.Percolates() {
		t.Skip("this fixture percolates; the subcritical branch needs a sparser graph")
	}
	for _, f := range []string{"a.go", "b.go", "c.go"} {
		if got := g.PercolationFactor(f); got != 1.0 {
			t.Errorf("PercolationFactor(%s) = %v in a subcritical graph, want 1.0", f, got)
		}
	}
}

// Nodes and edges may name things the other does not. Neither may panic, and
// an edge endpoint with no node declaration must still count toward degree,
// otherwise κ is computed over a graph that is not the one on disk.
func TestFromDoc_HandlesUndeclaredEndpointsAndIsolatedNodes(t *testing.T) {
	g := fromDoc(Doc{
		Nodes: []Node{{ID: "isolated", File: "iso.go"}},
		Edges: []Edge{{From: "ghost-a", To: "ghost-b"}},
	})
	if g.Empty() {
		t.Fatal("a graph with a node and an edge reported Empty()")
	}
	// The isolated node drags the mean degree down, as it should.
	if got := g.DependentCount("isolated"); got != 0 {
		t.Errorf("an isolated node has %d dependents", got)
	}
	if got := g.DependentCount("ghost-b"); got != 1 {
		t.Errorf("an undeclared edge endpoint has %d dependents, want 1", got)
	}
}

// Coupling drives the optimal parallel-agent count. Disjoint files must yield
// the low bound and identical files the high one, or `graph parallel` advises
// the wrong fan-out.
func TestCoupling_BoundsAndDegenerateInputs(t *testing.T) {
	g := fromDoc(Doc{
		Nodes: []Node{{ID: "a", File: "a.go"}, {ID: "b", File: "b.go"}, {ID: "shared", File: "shared.go"}},
		Edges: []Edge{{From: "a", To: "shared"}, {From: "b", To: "shared"}},
	})

	if got := g.Coupling(nil); got != kMin {
		t.Errorf("Coupling(nil) = %v, want kMin", got)
	}
	if got := g.Coupling([]string{"a.go"}); got != kMin {
		t.Errorf("Coupling of one file = %v, want kMin", got)
	}

	var nilGraph *Graph
	if got := nilGraph.Coupling([]string{"a.go", "b.go"}); got != kMin {
		t.Errorf("nil graph Coupling = %v, want kMin", got)
	}

	same := g.Coupling([]string{"shared.go", "shared.go"})
	disjoint := g.Coupling([]string{"a.go", "b.go"})
	if same < disjoint {
		t.Errorf("identical files coupled %v, less than disjoint files at %v, "+
			"parallelism advice would be inverted", same, disjoint)
	}
	for _, c := range []float64{same, disjoint} {
		if c < kMin || c > kMax {
			t.Errorf("coupling %v outside [%v, %v]", c, kMin, kMax)
		}
	}
}

// A file the graph does not know yields the neutral blast radius, and the
// caller can tell that apart with Knows, which is the #251 distinction.
func TestBlastRadiusForFile_UnknownFileIsNeutralButKnowable(t *testing.T) {
	g := fromDoc(Doc{Nodes: []Node{{ID: "a", File: "a.go"}}})

	if got := g.BlastRadiusForFile("unknown.go"); got != 1.0 {
		t.Errorf("unknown file blast radius = %v, want 1.0", got)
	}
	if g.Knows("unknown.go") {
		t.Error("Knows returned true for an unknown file, a caller could not tell " +
			"the 1.0 default from a measured leaf")
	}
	if !g.Knows("a.go") {
		t.Error("Knows returned false for a declared file")
	}
}

func TestNodesInFileAndDependents_NilSafe(t *testing.T) {
	var g *Graph
	if got := g.NodesInFile("x.go"); got != nil {
		t.Errorf("nil graph NodesInFile = %v", got)
	}
	if got := g.Dependents("x"); got != nil {
		t.Errorf("nil graph Dependents = %v", got)
	}
	if !g.Empty() {
		t.Error("nil graph did not report Empty()")
	}
	if g.Knows("x.go") {
		t.Error("nil graph claimed to know a file")
	}
}
