// SPDX-License-Identifier: MIT

package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
)

// What each colour depth leaves of a styled line with a cursor move in it:
// no colour drops SGR but keeps the move, 16 colours take v1's picks, and a
// depth that shows the colour gets the bytes as written.
func TestProfileWriter_DrawsWhatV1Drew(t *testing.T) {
	in := "\x1b[2K\x1b[1;38;5;214mwarn\x1b[0m \x1b[48;5;236;38;2;0;112;122mcode\x1b[m"
	was := lipgloss.Writer.Profile
	t.Cleanup(func() { lipgloss.Writer.Profile = was })
	for p, want := range map[colorprofile.Profile]string{
		colorprofile.NoTTY:     "\x1b[2Kwarn code",
		colorprofile.ASCII:     "\x1b[2Kwarn code",
		colorprofile.ANSI:      "\x1b[2K\x1b[1;93mwarn\x1b[0m \x1b[40;94mcode\x1b[m",
		colorprofile.TrueColor: in,
	} {
		lipgloss.Writer.Profile = p
		if got := captureStdout(t, func() { fmt.Fprint(stdout, in) }); got != want {
			t.Errorf("at %v:\n got %q\nwant %q", p, got, want)
		}
	}
}

// TUI-31: only a full-screen program may ask the terminal for its background,
// because the reply is read as input. Every query lives in tui.OwnTerminal,
// and every call to that follows a requireTerminal in the same function.
func TestTerminalQuery_OnlyInteractiveCommandsAsk(t *testing.T) {
	banned := map[string]bool{"charm.land/lipgloss/v2/compat": true, "github.com/muesli/termenv": true}
	fset := token.NewFileSet()
	for _, root := range []string{"../../cmd", "../../internal"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			for _, im := range f.Imports {
				if p, _ := strconv.Unquote(im.Path.Value); banned[p] {
					t.Errorf("%s imports %s, which queries the terminal at package init", path, p)
				}
			}
			ast.Inspect(f, func(n ast.Node) bool {
				switch fn := n.(type) {
				case *ast.FuncDecl:
					if fn.Body != nil {
						checkQueries(t, fset, fn.Body, fn.Name.Name == "OwnTerminal")
					}
				case *ast.FuncLit:
					checkQueries(t, fset, fn.Body, false)
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// checkQueries walks one function body, not the closures inside it: a
// background query is allowed only in OwnTerminal, and OwnTerminal only after
// requireTerminal has refused a non-terminal.
func checkQueries(t *testing.T, fset *token.FileSet, body *ast.BlockStmt, own bool) {
	t.Helper()
	required := false
	ast.Inspect(body, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "requireTerminal" {
			required = true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, _ := sel.X.(*ast.Ident)
		switch {
		case pkg == nil:
		case (pkg.Name == "lipgloss" || pkg.Name == "tea") && strings.Contains(sel.Sel.Name, "BackgroundColor") && !own,
			(pkg.Name == "lipgloss" && sel.Sel.Name == "HasDarkBackground" && !own):
			t.Errorf("%s: %s.%s asks the terminal outside tui.OwnTerminal (TUI-31)", fset.Position(call.Pos()), pkg.Name, sel.Sel.Name)
		case pkg.Name == "tui" && sel.Sel.Name == "OwnTerminal" && !required:
			t.Errorf("%s: OwnTerminal with no requireTerminal before it, so a non-interactive run would query", fset.Position(call.Pos()))
		}
		return true
	})
}
