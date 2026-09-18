// SPDX-License-Identifier: MIT

package executor

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The behavioural version cannot be written portably: it needs a clock coarse
// enough that time.Since returns 0, which is a Windows tick and not reachable
// from a test here. So the guard is structural instead, and it is the one that
// would have caught this: every executor set Duration from a bare time.Since,
// and CI failed on Windows with "Duration was not measured" for a call that
// really happened (#875).
func TestEveryExecutor_FloorsTheDurationItReports(t *testing.T) {
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
		f, err := parser.ParseFile(token.NewFileSet(), path, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			kv, ok := n.(*ast.KeyValueExpr)
			if !ok {
				return true
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok || key.Name != "Duration" {
				return true
			}
			checked++
			if !callsMeasured(kv.Value) {
				t.Errorf("%s: Duration is set from %s, not through measured(); a call "+
					"that completes inside one clock tick will report itself as instant",
					path, exprText(src, kv.Value))
			}
			return true
		})
	}

	if checked == 0 {
		t.Fatal("found no Duration assignments; this guard is reading nothing")
	}
}

func callsMeasured(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	fn, ok := call.Fun.(*ast.Ident)
	return ok && fn.Name == "measured"
}

func exprText(src []byte, e ast.Expr) string {
	return string(src[e.Pos()-1 : e.End()-1])
}
