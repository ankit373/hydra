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

// caseFolders are the calls a second label normaliser would be built from.
var caseFolders = map[string][]string{
	"strings": {"ToLower", "ToUpper", "EqualFold"},
	"unicode": {"ToLower", "ToUpper"},
}

// TestBench_NoSecondNormaliser: grading and the split key call oracle.NormaliseLabel,
// and nothing under internal/bench folds case on its own (scope E7.2-4; E1's G19 is the other half).
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
}
