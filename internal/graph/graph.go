// SPDX-License-Identifier: MIT

// Package graph reads a code dependency graph (graph.json, as produced by
// Graphify or any tree-sitter indexer) and computes a file's blast radius — how
// much other code transitively depends on it. Hydra feeds blast radius into the
// defect-cost model so an edit to a widely-depended-on file demands higher
// confidence than an edit to a leaf.
package graph

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// Node is one vertex in the dependency graph.
type Node struct {
	ID   string `json:"id"`
	File string `json:"file"`
}

// Edge is a directed dependency: From depends on To (From imports/calls To), so
// a change to To can break From.
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Doc is the on-disk graph.json schema.
type Doc struct {
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges"`
}

// Graph is an in-memory dependency graph with reverse adjacency for blast-radius
// queries. The zero value (and a nil *Graph) is a valid empty graph.
type Graph struct {
	// dependents[x] = nodes that directly depend on x (i.e. break if x changes).
	dependents map[string][]string
	// filesToNodes maps a file path to the node IDs declared in it.
	filesToNodes map[string][]string
	// degree[x] = total incident edges (in + out) on node x, over the undirected
	// projection of the dependency graph — the input to the percolation criterion.
	degree map[string]int
	// meanDeg is ⟨k⟩ and kappa is the Molloy–Reed ratio ⟨k²⟩/⟨k⟩, both computed
	// once at load. kappa ≥ 2 ⟹ a giant (cascade-capable) component exists.
	meanDeg float64
	kappa   float64
	// meanDependents is ⟨D⟩, the mean transitive-dependent count over all
	// nodes — separate from meanDeg/kappa because a node's own imports
	// inflate its total degree without saying anything about what depends on
	// it (#503). PercolationFactor keys its per-file lift off this instead.
	meanDependents float64
}

// Load reads graph.json from path. A missing file yields an empty graph and no
// error, so blast-radius queries degrade gracefully to 1.0.
func Load(path string) (*Graph, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Graph{}, nil
		}
		return nil, err
	}
	var doc Doc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	return fromDoc(doc), nil
}

// DefaultPath is where Hydra looks for the graph relative to a repo root.
func DefaultPath(repoRoot string) string {
	return filepath.Join(repoRoot, "graph.json")
}

func fromDoc(doc Doc) *Graph {
	g := &Graph{
		dependents:   make(map[string][]string),
		filesToNodes: make(map[string][]string),
		degree:       make(map[string]int),
	}
	// Every declared node exists in the degree map, even isolated ones — an
	// isolated node has degree 0 and correctly drags ⟨k⟩ down.
	nodes := make(map[string]bool)
	for _, n := range doc.Nodes {
		nodes[n.ID] = true
		if _, ok := g.degree[n.ID]; !ok {
			g.degree[n.ID] = 0
		}
		if n.File != "" {
			g.filesToNodes[n.File] = append(g.filesToNodes[n.File], n.ID)
		}
	}
	for _, e := range doc.Edges {
		// e.From depends on e.To → e.To has dependent e.From.
		g.dependents[e.To] = append(g.dependents[e.To], e.From)
		// Undirected projection for percolation: the edge adds one incident
		// endpoint to each of From and To. Endpoints not declared as nodes are
		// still counted so degree reflects the real edge set.
		g.degree[e.From]++
		g.degree[e.To]++
		nodes[e.From] = true
		nodes[e.To] = true
	}
	g.computeKappa(nodes)
	g.computeMeanDependents(nodes)
	return g
}

// computeKappa fills meanDeg = ⟨k⟩ and kappa = ⟨k²⟩/⟨k⟩ over the given node set.
// With no nodes or no edges (⟨k⟩ = 0) both stay 0, i.e. no percolation.
func (g *Graph) computeKappa(nodes map[string]bool) {
	n := len(nodes)
	if n == 0 {
		return
	}
	var sum, sumSq float64
	for id := range nodes {
		k := float64(g.degree[id])
		sum += k
		sumSq += k * k
	}
	g.meanDeg = sum / float64(n)
	if g.meanDeg > 0 {
		g.kappa = (sumSq / float64(n)) / g.meanDeg
	}
}

// computeMeanDependents fills meanDependents = ⟨D⟩ over the given node set,
// one BFS per node via DependentCount — cheap at package granularity, and the
// only way to get a per-node reach figure that means the same thing as the
// count BlastRadiusForFile already uses.
func (g *Graph) computeMeanDependents(nodes map[string]bool) {
	n := len(nodes)
	if n == 0 {
		return
	}
	var sum float64
	for id := range nodes {
		sum += float64(g.DependentCount(id))
	}
	g.meanDependents = sum / float64(n)
}

// transitiveDependents returns every node that transitively depends on any node
// in seeds (breadth-first over reverse edges), excluding the seeds themselves.
func (g *Graph) transitiveDependents(seeds []string) map[string]bool {
	seen := make(map[string]bool)
	if g == nil || g.dependents == nil {
		return seen
	}
	seedSet := make(map[string]bool, len(seeds))
	for _, s := range seeds {
		seedSet[s] = true
	}
	queue := append([]string(nil), seeds...)
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, dep := range g.dependents[cur] {
			if seen[dep] || seedSet[dep] {
				continue
			}
			seen[dep] = true
			queue = append(queue, dep)
		}
	}
	return seen
}

// DependentCount returns the number of nodes that transitively depend on the
// given node ID (how many things break if it changes).
func (g *Graph) DependentCount(nodeID string) int {
	return len(g.transitiveDependents([]string{nodeID}))
}

// DependentCountForFile returns the number of nodes that transitively depend
// on any node declared in file, using the same seed set as BlastRadiusForFile.
// Callers must use this instead of looping NodesInFile+DependentCount per
// node: that pattern runs one single-seed BFS per node in the file (wasteful)
// and double-counts any node reachable from more than one of the file's own
// seeds, so the displayed dependent count can disagree with the count that
// actually drove BlastRadiusForFile's multiplier.
func (g *Graph) DependentCountForFile(file string) int {
	seeds := g.seedsForFile(file)
	if len(seeds) == 0 {
		return 0
	}
	return len(g.transitiveDependents(seeds))
}

// seedsForFile returns the node IDs declared in a file, matching by exact path
// first, then basename, then (Go package-granularity graphs) the file's
// containing package — so callers may pass absolute paths, relative paths, or
// a specific .go file even though `graph generate` indexes at package
// granularity (#448).
func (g *Graph) seedsForFile(file string) []string {
	if g == nil || len(g.filesToNodes) == 0 {
		return nil
	}
	if seeds := g.filesToNodes[file]; len(seeds) > 0 {
		return seeds
	}
	if seeds := g.basenameSeeds(file); len(seeds) > 0 {
		return seeds
	}
	if pkg := g.packageIDForFile(file); pkg != "" {
		return g.filesToNodes[pkg]
	}
	return nil
}

// basenameSeeds matches file by its last path segment against every stored
// path's last segment, requiring the parent segment to also correspond
// whenever both paths actually have one. A bare filename (no directory, e.g.
// a flat non-Go index) has no ancestry to check and is accepted as before;
// once both sides have a directory, a matching basename alone is not enough —
// any garbage path whose final component happens to collide with a real
// package's would otherwise resolve to that package's full data (#503).
func (g *Graph) basenameSeeds(file string) []string {
	base := filepath.Base(file)
	fileDir := filepath.Dir(file)
	var seeds []string
	for f, ids := range g.filesToNodes {
		if filepath.Base(f) != base {
			continue
		}
		fDir := filepath.Dir(f)
		if fDir == "." || fileDir == "." || filepath.Base(fDir) == filepath.Base(fileDir) {
			seeds = append(seeds, ids...)
		}
	}
	return seeds
}

// packageIDForFile resolves a specific file to the package node ID that owns
// it under GenerateGo's granularity: one node per Go package, keyed by its
// module-relative import path (e.g. "internal/dispatch/dispatch.go" →
// "internal/dispatch"). It matches the containing directory verbatim first,
// then trims leading path segments so an absolute or otherwise-prefixed path
// still lines up with a package ID.
func (g *Graph) packageIDForFile(file string) string {
	dir := filepath.ToSlash(filepath.Dir(file))
	if _, ok := g.filesToNodes[dir]; ok {
		return dir
	}
	segs := strings.Split(dir, "/")
	for i := 1; i < len(segs); i++ {
		suffix := strings.Join(segs[i:], "/")
		if _, ok := g.filesToNodes[suffix]; ok {
			return suffix
		}
	}
	return ""
}

// BlastRadiusForFile returns a defect-cost multiplier for editing a file:
// (1 + log2(1 + transitive dependents)) × percolation factor, over all nodes
// declared in that file. A leaf (or an unknown file, or no graph) yields 1.0; a
// widely-depended-on file grows sub-linearly so one hub node doesn't produce an
// absurd multiplier. The percolation factor (≥1) additionally lifts files that
// sit in the graph's cascade-capable core — see PercolationFactor.
func (g *Graph) BlastRadiusForFile(file string) float64 {
	seeds := g.seedsForFile(file)
	if len(seeds) == 0 {
		return 1.0
	}
	count := len(g.transitiveDependents(seeds))
	base := 1.0 + math.Log2(1.0+float64(count))
	return base * g.percolationFactor(count)
}

// FileImpact bundles what BlastRadiusForFile, PercolationFactor,
// DependentCountForFile, and Knows each separately compute about one file.
// Every caller that wants more than one of these numbers (hyctl graph blast,
// the TUI dashboard, hyctl security's blast-radius check all want at least
// three) used to resolve seedsForFile once per call and run
// transitiveDependents' BFS twice over — Impact resolves the seed set once
// and derives all four from that single pass.
type FileImpact struct {
	Known       bool
	Dependents  int
	Radius      float64
	Percolation float64
}

// Impact is the single-resolution counterpart to calling BlastRadiusForFile,
// PercolationFactor, DependentCountForFile, and Knows separately for file.
func (g *Graph) Impact(file string) FileImpact {
	seeds := g.seedsForFile(file)
	if len(seeds) == 0 {
		return FileImpact{Radius: 1.0, Percolation: 1.0}
	}
	count := len(g.transitiveDependents(seeds))
	percolation := g.percolationFactor(count)
	return FileImpact{
		Known:       true,
		Dependents:  count,
		Radius:      (1.0 + math.Log2(1.0+float64(count))) * percolation,
		Percolation: percolation,
	}
}

// Kappa is the Molloy–Reed ratio κ = ⟨k²⟩/⟨k⟩ of the (undirected projection of
// the) dependency graph. A random graph has a giant connected component — the
// substrate for a breaking-change cascade — iff κ ≥ 2 (Molloy & Reed 1995). It
// is a single global property of the codebase: κ near 2 means edits stay local;
// κ ≫ 2 means the graph has a dense core where a change can ripple widely.
func (g *Graph) Kappa() float64 {
	if g == nil {
		return 0
	}
	return g.kappa
}

// Percolates reports whether the graph is supercritical (κ ≥ 2) — i.e. a change
// is topologically capable of cascading through a giant component.
func (g *Graph) Percolates() bool { return g.Kappa() >= 2.0 }

// percolationCap bounds the percolation factor's contribution so topology can
// meaningfully weight blast radius without ever dominating the dependent-count
// term: the factor stays in [1, 1+percolationCap].
const percolationCap = 2.0

// PercolationFactor returns a multiplier ≥ 1 that raises the blast radius of a
// file whose OWN transitive dependent count is above the graph's average, and
// only when the graph itself is supercritical (κ ≥ 2, a cascade-capable core
// exists at all). κ still comes from total (in+out) degree — the textbook
// Molloy-Reed criterion for a giant component — but the per-file excess is
// deliberately a different quantity: the same dependent count
// BlastRadiusForFile's base term uses. Keying the per-file lift off total
// degree let a file's own outbound imports (which inflate degree but say
// nothing about what depends on it) outscore a file with genuinely more
// dependents — cmd/hydra, a Go main package with 0 dependents, out-scored
// real leaves this way. Keying it off dependent count instead makes the
// factor, and therefore the final blast radius, monotonic non-decreasing in a
// node's own transitive dependent count (#503).
func (g *Graph) PercolationFactor(file string) float64 {
	seeds := g.seedsForFile(file)
	if len(seeds) == 0 {
		return 1.0
	}
	return g.percolationFactor(len(g.transitiveDependents(seeds)))
}

func (g *Graph) percolationFactor(count int) float64 {
	if g.meanDependents <= 0 || g.kappa < 2.0 {
		return 1.0
	}
	excess := float64(count)/g.meanDependents - 1.0 // how far above average this file's own reach is
	if excess <= 0 {
		return 1.0 // at or below average reach — periphery, no lift
	}
	// superMargin ∈ (0,1]: 0 at the κ=2 threshold, saturating to 1 by κ=4.
	superMargin := math.Min((g.kappa-2.0)/2.0, 1.0)
	lift := superMargin * excess
	if lift > percolationCap {
		lift = percolationCap
	}
	return 1.0 + lift
}

// Coordination-cost bounds for Coupling, mapping graph overlap → the k used by
// package optimal (independent work → kMin ⇒ n*≈6; fully-overlapping → kMax ⇒ n*≈2).
const (
	kMin = 0.02
	kMax = 0.30
)

// Coupling returns the per-agent coordination cost k for editing a set of files
// in parallel, derived from how much their impact sets (own nodes + transitive
// dependents) overlap. Disjoint files → kMin (safe to parallelize widely);
// files sharing the same subgraph → toward kMax (parallelism collapses). Fewer
// than two files, or no graph, → kMin.
func (g *Graph) Coupling(files []string) float64 {
	if g == nil || len(files) < 2 {
		return kMin
	}
	sets := make([]map[string]bool, len(files))
	for i, f := range files {
		sets[i] = g.affectedSet(f)
	}
	var sum float64
	var pairs int
	for i := 0; i < len(sets); i++ {
		for j := i + 1; j < len(sets); j++ {
			sum += jaccard(sets[i], sets[j])
			pairs++
		}
	}
	overlap := 0.0
	if pairs > 0 {
		overlap = sum / float64(pairs)
	}
	// Clamped because the interpolation overshoots on exact overlap:
	// 0.02 + 1×0.28 evaluates to 0.30000000000000004, four ulps above kMax. The
	// excess is numerically irrelevant to n*, but the documented range is either
	// true or it is not, and a caller is entitled to rely on it.
	k := kMin + overlap*(kMax-kMin)
	if k > kMax {
		return kMax
	}
	if k < kMin {
		return kMin
	}
	return k
}

// affectedSet is a file's own nodes plus everything that transitively depends on
// them — the region of the graph an edit could perturb.
func (g *Graph) affectedSet(file string) map[string]bool {
	seeds := g.seedsForFile(file)
	set := make(map[string]bool, len(seeds))
	for _, s := range seeds {
		set[s] = true
	}
	for n := range g.transitiveDependents(seeds) {
		set[n] = true
	}
	return set
}

// jaccard is |A∩B| / |A∪B|; two empty sets have no overlap (0).
func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	inter := 0
	for k := range a {
		if b[k] {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// Dependents returns the direct dependents of a node (for `hyctl graph blast`).
func (g *Graph) Dependents(nodeID string) []string {
	if g == nil {
		return nil
	}
	return g.dependents[nodeID]
}

// NodesInFile returns the node IDs associated with a file — resolved the same
// way as BlastRadiusForFile and Knows (exact path, basename, or containing
// package), so a dependents count shown alongside a blast radius always
// reflects the same node set.
func (g *Graph) NodesInFile(file string) []string {
	return g.seedsForFile(file)
}

// Empty reports whether the graph holds no nodes at all — which is what Load
// returns when graph.json is absent.
//
// Callers that *present* results need this. Degrading a missing graph to a
// blast radius of 1.0 is the right library behaviour, but a UI that prints that
// default as "subcritical — edits stay local" is making an affirmative safety
// claim from no data, and it silently lowered the confidence bar on files that
// are genuinely cascade risks (#251).
func (g *Graph) Empty() bool {
	return g == nil || len(g.degree) == 0
}

// Knows reports whether the graph contains the given file. A false here means a
// blast radius of 1.0 is a default, not a measurement — the file may be
// misspelled, outside the indexed tree, or simply not indexed yet.
func (g *Graph) Knows(file string) bool {
	return len(g.seedsForFile(file)) > 0
}
