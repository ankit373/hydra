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
	}
	for _, n := range doc.Nodes {
		if n.File != "" {
			g.filesToNodes[n.File] = append(g.filesToNodes[n.File], n.ID)
		}
	}
	for _, e := range doc.Edges {
		// e.From depends on e.To → e.To has dependent e.From.
		g.dependents[e.To] = append(g.dependents[e.To], e.From)
	}
	return g
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

// BlastRadiusForFile returns a defect-cost multiplier for editing a file:
// 1 + log2(1 + transitive dependents) over all nodes declared in that file. A
// leaf (or an unknown file, or no graph) yields 1.0; a widely-depended-on file
// grows sub-linearly so one hub node doesn't produce an absurd multiplier.
func (g *Graph) BlastRadiusForFile(file string) float64 {
	if g == nil || len(g.filesToNodes) == 0 {
		return 1.0
	}
	seeds := g.filesToNodes[file]
	if len(seeds) == 0 {
		// Try a basename match so callers can pass absolute or relative paths.
		base := filepath.Base(file)
		for f, ids := range g.filesToNodes {
			if filepath.Base(f) == base {
				seeds = append(seeds, ids...)
			}
		}
	}
	if len(seeds) == 0 {
		return 1.0
	}
	count := len(g.transitiveDependents(seeds))
	return 1.0 + math.Log2(1.0+float64(count))
}

// Dependents returns the direct dependents of a node (for `hydra graph blast`).
func (g *Graph) Dependents(nodeID string) []string {
	if g == nil {
		return nil
	}
	return g.dependents[nodeID]
}

// NodesInFile returns the node IDs declared in a file.
func (g *Graph) NodesInFile(file string) []string {
	if g == nil {
		return nil
	}
	return g.filesToNodes[file]
}
