// SPDX-License-Identifier: MIT

package graph

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// GenerateGo builds a Doc from `go list -json ./...` rooted at dir, a Go
// package-import graph (dependency edges at package granularity), not a
// general tree-sitter indexer. exclude skips any package whose short
// (module-relative) import path has one of these prefixes.
func GenerateGo(dir string, exclude []string) (*Doc, error) {
	modOut, err := runIn(dir, "go", "list", "-m")
	if err != nil {
		return nil, fmt.Errorf("go list -m failed (not a Go module?): %w", err)
	}
	modulePath := strings.TrimSpace(modOut)
	prefix := modulePath + "/"

	listOut, err := runIn(dir, "go", "list", "-json", "./...")
	if err != nil {
		return nil, fmt.Errorf("go list -json failed: %w", err)
	}

	type pkg struct {
		ImportPath string
		Imports    []string
	}
	short := func(importPath string) string {
		if importPath == modulePath {
			return "."
		}
		return strings.TrimPrefix(importPath, prefix)
	}
	skip := func(importPath string) bool {
		s := short(importPath)
		for _, e := range exclude {
			if e != "" && strings.HasPrefix(s, e) {
				return true
			}
		}
		return false
	}

	dec := json.NewDecoder(strings.NewReader(listOut))
	var pkgs []pkg
	for dec.More() {
		var p pkg
		if err := dec.Decode(&p); err != nil {
			return nil, fmt.Errorf("decode failed: %w", err)
		}
		pkgs = append(pkgs, p)
	}

	d := &Doc{}
	for _, p := range pkgs {
		if skip(p.ImportPath) {
			continue
		}
		id := short(p.ImportPath)
		d.Nodes = append(d.Nodes, Node{ID: id, File: id})
	}
	for _, p := range pkgs {
		if skip(p.ImportPath) {
			continue
		}
		from := short(p.ImportPath)
		for _, imp := range p.Imports {
			if !strings.HasPrefix(imp, prefix) || imp == p.ImportPath || skip(imp) {
				continue
			}
			d.Edges = append(d.Edges, Edge{From: from, To: short(imp)})
		}
	}
	return d, nil
}

func runIn(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}
