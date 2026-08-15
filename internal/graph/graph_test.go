// SPDX-License-Identifier: MIT

package graph

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// a ← b ← c   and   a ← d   (arrows point from dependency to dependent)
//
//	b depends on a; c depends on b; d depends on a.
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

// A garbage path whose final segment happens to collide with a real
// package's basename must not silently resolve to that package's data — the
// exact bug behind `hyctl graph blast zzz/made/up/dispatch` returning numbers
// identical to internal/dispatch (#503).
func TestSeedsForFile_BasenameFallbackRejectsAncestryMismatch(t *testing.T) {
	g := fromDoc(Doc{Nodes: []Node{{ID: "internal/dispatch", File: "internal/dispatch"}}})

	if g.Knows("zzz/made/up/dispatch") {
		t.Error("a garbage path matching only the final segment resolved to a real package")
	}
	if got := g.BlastRadiusForFile("zzz/made/up/dispatch"); got != 1.0 {
		t.Errorf("garbage path blast radius = %v, want the 1.0 unknown default", got)
	}
	if !g.Knows("internal/dispatch") {
		t.Error("the real package path itself must still resolve")
	}
}

// The ancestry check only applies when both sides actually have a parent
// segment to compare — a legitimate absolute-path lookup that preserves the
// real parent must still match, and one that swaps in an unrelated parent
// must not.
func TestSeedsForFile_BasenameFallbackAncestry(t *testing.T) {
	g := fromDoc(Doc{Nodes: []Node{{ID: "n", File: "src/foo/bar.go"}}})

	if !g.Knows("/home/dev/repo/src/foo/bar.go") {
		t.Error("an absolute path preserving the real parent segment should resolve")
	}
	if g.Knows("other/bar.go") {
		t.Error("same basename under an unrelated parent must not resolve")
	}
}

// graph generate (GenerateGo) indexes at Go package granularity — one node
// per package, keyed by its module-relative import path — not per file. A
// caller passing a specific file inside an indexed package must still resolve
// to that package's real measurement, not silently default to 1.0 (#448).
func TestBlastRadius_PackageGranularity(t *testing.T) {
	// Mirrors GenerateGo's output shape: ID == File == package import path.
	g := fromDoc(Doc{
		Nodes: []Node{
			{ID: "internal/dispatch", File: "internal/dispatch"},
			{ID: "internal/executor", File: "internal/executor"},
			{ID: "internal/policy", File: "internal/policy"},
		},
		Edges: []Edge{
			{From: "internal/executor", To: "internal/dispatch"},
			{From: "internal/policy", To: "internal/dispatch"},
		},
	})

	want := g.BlastRadiusForFile("internal/dispatch")
	if want == 1.0 {
		t.Fatal("precondition: package-level lookup should already be a real measurement")
	}
	if got := g.BlastRadiusForFile("internal/dispatch/dispatch.go"); got != want {
		t.Errorf("blast(internal/dispatch/dispatch.go) = %v, want %v (the package's measured radius)", got, want)
	}
	if !g.Knows("internal/dispatch/dispatch.go") {
		t.Error("Knows(internal/dispatch/dispatch.go) = false, want true — package is in the graph")
	}
	if got, want := len(g.NodesInFile("internal/dispatch/dispatch.go")), 1; got != want {
		t.Errorf("NodesInFile(internal/dispatch/dispatch.go) returned %d node(s), want %d", got, want)
	}

	// An absolute path into the same package must resolve identically.
	if got := g.BlastRadiusForFile("/repo/internal/dispatch/dispatch.go"); got != want {
		t.Errorf("blast(absolute path) = %v, want %v", got, want)
	}

	// A file in a package that genuinely is NOT in the graph must still report
	// as unrecognized — the fallback must not over-match.
	if g.Knows("internal/ledger/ledger.go") {
		t.Error("Knows(internal/ledger/ledger.go) = true, want false — package is not in the graph")
	}
	if got := g.BlastRadiusForFile("internal/ledger/ledger.go"); got != 1.0 {
		t.Errorf("blast(internal/ledger/ledger.go) = %v, want the 1.0 default", got)
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
	path := pathGraph(9) // κ → 2⁻
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

// Two files with the SAME transitive-dependent count must be priced
// identically, even when one has far more total degree than the other via its
// own outbound imports. Pre-#503 the lift was keyed on total (in+out) degree,
// so X (a fan-in hub with no imports of its own) scored higher than Y (same
// dependent count, reached via a chain) purely because X's raw degree was
// higher — the same mechanism that let cmd/hydra out-score other leaves.
func TestPercolation_EqualCountEqualFactorRegardlessOfDegree(t *testing.T) {
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
	if degX, degY := g.degree["X"], g.degree["Y"]; degX == degY {
		t.Fatalf("precondition: unequal total degree required, got X=%d Y=%d", degX, degY)
	}
	bx, by := g.BlastRadiusForFile("x.go"), g.BlastRadiusForFile("y.go")
	if bx != by {
		t.Errorf("equal-count files priced differently: x=%.4f y=%.4f — blast radius must depend "+
			"on dependent count, not on a file's own unrelated degree", bx, by)
	}
	if fx, fy := g.PercolationFactor("x.go"), g.PercolationFactor("y.go"); fx != fy {
		t.Errorf("equal-count PercolationFactor differs: x=%.4f y=%.4f", fx, fy)
	}
}

// The reported #503 case: a Go main package has 0 transitive dependents (a
// main package can't be imported) but plenty of outbound imports. It must
// float at the same neutral 1.0 floor as any other zero-dependent leaf, not
// score higher just because it happens to import a lot.
func TestPercolation_ZeroDependentLeafNeverLifted(t *testing.T) {
	nodes := []Node{{ID: "hub", File: "hub.go"}, {ID: "main", File: "main.go"}, {ID: "leaf", File: "leaf.go"}}
	var edges []Edge
	// hub: 6 direct dependents, gives the graph a supercritical core.
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("r%d", i)
		nodes = append(nodes, Node{ID: id, File: id + ".go"})
		edges = append(edges, Edge{From: id, To: "hub"})
	}
	// main: 0 dependents (nothing imports a main package), but imports 5
	// packages of its own — inflating its own total degree, not its reach.
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("imp%d", i)
		nodes = append(nodes, Node{ID: id, File: id + ".go"})
		edges = append(edges, Edge{From: "main", To: id})
	}
	// leaf: 0 dependents, 0 imports — the uncontroversial neutral case.
	g := fromDoc(Doc{Nodes: nodes, Edges: edges})

	if !g.Percolates() {
		t.Fatalf("test graph κ = %.3f, expected supercritical", g.Kappa())
	}
	if g.DependentCount("main") != 0 || g.DependentCount("leaf") != 0 {
		t.Fatalf("precondition: both main and leaf must have 0 dependents")
	}
	if f := g.PercolationFactor("main.go"); f != 1.0 {
		t.Errorf("main (0 dependents, heavy imports) PercolationFactor = %.4f, want the neutral 1.0 floor", f)
	}
	if got, want := g.BlastRadiusForFile("main.go"), g.BlastRadiusForFile("leaf.go"); got != want {
		t.Errorf("blast radius main=%.4f leaf=%.4f — a 0-dependent main package must not "+
			"out-score a 0-dependent leaf", got, want)
	}
}

// General regression guard for #503: a node's own outbound imports (which
// inflate its total degree) must never let it out-rank a node with genuinely
// more transitive dependents. specs are ordered by strictly increasing
// dependent count and strictly decreasing import count, the exact inverse
// correlation that produced the reported bug (dispatch's imports out-scoring
// provider's real dependents).
func TestPercolation_MonotonicInDependentCount(t *testing.T) {
	specs := []struct {
		id      string
		deps    int
		imports int
	}{
		{"few", 1, 12},
		{"mid", 4, 4},
		{"many", 9, 0},
	}
	var nodes []Node
	var edges []Edge
	for _, s := range specs {
		nodes = append(nodes, Node{ID: s.id, File: s.id + ".go"})
		prev := s.id
		for d := 0; d < s.deps; d++ {
			id := fmt.Sprintf("%s_dep%d", s.id, d)
			nodes = append(nodes, Node{ID: id, File: id + ".go"})
			edges = append(edges, Edge{From: id, To: prev})
			prev = id
		}
		for i := 0; i < s.imports; i++ {
			id := fmt.Sprintf("%s_imp%d", s.id, i)
			nodes = append(nodes, Node{ID: id, File: id + ".go"})
			edges = append(edges, Edge{From: s.id, To: id})
		}
	}
	g := fromDoc(Doc{Nodes: nodes, Edges: edges})
	if !g.Percolates() {
		t.Fatalf("test graph κ = %.3f, expected supercritical", g.Kappa())
	}

	prevID, prevCount, prevRadius := "", -1, -1.0
	for _, s := range specs {
		count := g.DependentCount(s.id)
		radius := g.BlastRadiusForFile(s.id + ".go")
		if prevCount >= 0 {
			if count <= prevCount {
				t.Fatalf("test precondition broken: %s count %d must exceed %s count %d", s.id, count, prevID, prevCount)
			}
			if radius < prevRadius {
				t.Errorf("%s: dependent count %d > %s's %d, but blast radius %.4f < %.4f — "+
					"not monotonic in dependent count", s.id, count, prevID, prevCount, radius, prevRadius)
			}
		}
		prevID, prevCount, prevRadius = s.id, count, radius
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

// A blast radius of 1.0 means either "nothing depends on this" or "I have no
// idea", and those are opposite conclusions. Before #251 they rendered
// identically: internal/a2a reported 6 dependents and 97.4% required confidence
// with a graph, and "subcritical — edits stay local" at 90.0% without one.
// Empty and Knows are what let a UI tell a default from a measurement.
func TestEmptyAndKnows_DistinguishNoDataFromNoDependents(t *testing.T) {
	missing, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("Load of a missing graph should not error: %v", err)
	}
	if !missing.Empty() {
		t.Error("a graph loaded from a missing file reports Empty() = false")
	}
	if missing.Knows("internal/a2a") {
		t.Error("an empty graph claims to know a file")
	}
	// The degraded radius itself is unchanged — that contract is deliberate.
	if got := missing.BlastRadiusForFile("internal/a2a"); got != 1.0 {
		t.Errorf("BlastRadiusForFile on an empty graph = %v, want 1.0", got)
	}

	populated := fromDoc(Doc{
		Nodes: []Node{{ID: "a", File: "a.go"}, {ID: "b", File: "b.go"}},
		Edges: []Edge{{From: "b", To: "a"}},
	})
	if populated.Empty() {
		t.Error("a graph with nodes reports Empty() = true")
	}
	if !populated.Knows("a.go") {
		t.Error("Knows(a.go) = false for an indexed file")
	}
	// An unindexed file in a real graph is still a default, not a measurement —
	// the case a typo or a stale index produces.
	if populated.Knows("typo.go") {
		t.Error("Knows(typo.go) = true for a file that is not in the graph")
	}
	if got := populated.BlastRadiusForFile("typo.go"); got != 1.0 {
		t.Errorf("unknown file radius = %v, want the 1.0 default", got)
	}
}
