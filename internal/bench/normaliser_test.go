// SPDX-License-Identifier: MIT

package bench

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// splitKeyFunc is the one function allowed to fold case: the split key, not a grader.
const splitKeyFunc = "splitText"

// caseFolders are the calls a second label normaliser would be built from.
var caseFolders = map[string][]string{
	"strings": {"ToLower", "ToUpper", "EqualFold"},
	"unicode": {"ToLower", "ToUpper"},
}

// TestBench_NoSecondNormaliser: grading calls oracle.NormaliseLabel, and nothing under
// internal/bench folds case except splitText, the leakage key, which grades nothing.
func TestBench_NoSecondNormaliser(t *testing.T) {
	found := scanCaseFolds(t, ".")
	if len(found) > 0 {
		t.Fatalf("internal/bench folds case outside oracle.NormaliseLabel:\n  %s", strings.Join(found, "\n  "))
	}
}

func scanCaseFolds(t *testing.T, root string) []string {
	t.Helper()
	var found []string
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return err
		}
		local := map[string]string{} // local name → import path
		for _, imp := range f.Imports {
			ip, _ := strconv.Unquote(imp.Path.Value)
			name := path.Base(ip)
			if imp.Name != nil {
				name = imp.Name.Name
			}
			local[name] = ip
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if fd, ok := n.(*ast.FuncDecl); ok && fd.Name.Name == splitKeyFunc && filepath.Base(p) == "split.go" {
				return false
			}
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			for _, fn := range caseFolders[local[pkg.Name]] {
				if sel.Sel.Name == fn {
					found = append(found, fset.Position(sel.Pos()).String()+": "+local[pkg.Name]+"."+fn)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

// The scan must find a fold when one is there, or the guard above guards nothing.
func TestBench_NoSecondNormaliser_SeesAnAliasedFold(t *testing.T) {
	dir := t.TempDir()
	src := "package x\nimport s \"strings\"\nvar _ = s.ToLower(\"A\")\n"
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := scanCaseFolds(t, dir); len(got) != 1 {
		t.Fatalf("scan found %v, want the aliased strings.ToLower", got)
	}
	// The exemption is the function in split.go, not the name anywhere.
	src = "package x\nimport \"strings\"\nfunc splitText(s string) string { return strings.ToLower(s) }\n"
	os.WriteFile(filepath.Join(dir, "x.go"), []byte(src), 0o644)
	if got := scanCaseFolds(t, dir); len(got) != 1 {
		t.Fatalf("a splitText outside split.go was exempted: %v", got)
	}
}

// splitText is unexported, so nothing outside this package can grade with it; inside
// it, only split.go may call it.
func TestBench_SplitKeyIsOnlyUsedForSplits(t *testing.T) {
	fset := token.NewFileSet()
	entries, _ := os.ReadDir(".")
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "split.go" {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == splitKeyFunc {
				t.Errorf("%s uses %s outside the split code", fset.Position(id.Pos()), splitKeyFunc)
			}
			return true
		})
	}
}
