// SPDX-License-Identifier: MIT

package graph

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeGoModule builds a real, tiny Go module on disk: a <- b, and a third
// package (skip) that also imports a, so GenerateGo runs against the real
// `go` toolchain rather than a hand-built fixture.
func writeGoModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/testmod\n\ngo 1.21\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pkgs := map[string]string{
		"a/a.go":    "package a\n",
		"b/b.go":    "package b\n\nimport _ \"example.com/testmod/a\"\n",
		"skip/s.go": "package skip\n\nimport _ \"example.com/testmod/a\"\n",
	}
	for rel, body := range pkgs {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestGenerateGo_BuildsTheRealImportGraph(t *testing.T) {
	dir := writeGoModule(t)

	doc, err := GenerateGo(dir, nil)
	if err != nil {
		t.Fatal(err)
	}

	ids := map[string]bool{}
	for _, n := range doc.Nodes {
		ids[n.ID] = true
	}
	for _, want := range []string{"a", "b", "skip"} {
		if !ids[want] {
			t.Errorf("nodes = %v, missing %q", ids, want)
		}
	}

	var sawBToA bool
	for _, e := range doc.Edges {
		if e.From == "b" && e.To == "a" {
			sawBToA = true
		}
	}
	if !sawBToA {
		t.Errorf("edges = %+v, want an edge b -> a", doc.Edges)
	}
}

func TestGenerateGo_ExcludeSkipsMatchingPrefixes(t *testing.T) {
	dir := writeGoModule(t)

	doc, err := GenerateGo(dir, []string{"skip"})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range doc.Nodes {
		if n.ID == "skip" {
			t.Errorf("excluded package still present in nodes: %+v", doc.Nodes)
		}
	}
	for _, e := range doc.Edges {
		if e.From == "skip" || e.To == "skip" {
			t.Errorf("excluded package still present in an edge: %+v", e)
		}
	}
}

// GenerateGo's whole purpose is feeding internal/graph.Load, its output must
// actually be loadable, not merely well-formed JSON.
func TestGenerateGo_OutputRoundTripsThroughLoad(t *testing.T) {
	dir := writeGoModule(t)
	doc, err := GenerateGo(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "graph.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	g, err := Load(path)
	if err != nil {
		t.Fatalf("internal/graph could not load GenerateGo's own output: %v", err)
	}
	if g.Empty() {
		t.Error("a graph with real nodes/edges loaded as empty")
	}
	if !g.Knows("b") {
		t.Error("Load did not recognize a node GenerateGo produced")
	}
}

func TestGenerateGo_NotAGoModuleIsAnError(t *testing.T) {
	dir := t.TempDir() // no go.mod
	if _, err := GenerateGo(dir, nil); err == nil {
		t.Error("GenerateGo succeeded outside any Go module")
	}
}
