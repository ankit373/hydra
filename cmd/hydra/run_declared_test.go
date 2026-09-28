// SPDX-License-Identifier: MIT

package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every command that opens a run declares what the run is for. The behavioural
// version of this test cannot be written: it would have to drive each command
// against real heads. So it is read off the source instead, which is also what
// would actually have caught the original, since the defect was three commands
// each not writing an event rather than one writing the wrong thing (#910).
//
// A heartbeat is the signal: starting one says "a run is live here", and that
// is exactly the claim a reader renders a row from.
func TestEveryCommandThatOpensARunDeclaresIt(t *testing.T) {
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}

	checked := 0
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatal(err)
		}

		ast.Inspect(f, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncLit)
			if !ok {
				return true
			}
			if !callsSelector(fn, "runlog", "StartHeartbeat") {
				return true
			}
			checked++
			if !callsSelector(fn, "runlog", "DeclareRun") {
				t.Errorf("%s: a run is opened here (runlog.StartHeartbeat) without runlog.DeclareRun; "+
					"the cockpit would fall back to task_started's routing enum for its label",
					fset.Position(fn.Pos()))
			}
			return true
		})
	}

	// A guard reading nothing passes for free, and this one reads a call
	// pattern that a refactor could rename out from under it.
	if checked == 0 {
		t.Fatal("found no command that opens a run; this guard is reading nothing")
	}
}

// callsSelector reports whether the body contains a pkg.Name(...) call.
func callsSelector(n ast.Node, pkg, name string) bool {
	found := false
	ast.Inspect(n, func(x ast.Node) bool {
		call, ok := x.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != name {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && id.Name == pkg {
			found = true
		}
		return true
	})
	return found
}
