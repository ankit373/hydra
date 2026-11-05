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
	in := "\x1b[2K\x1b[1;38;5;214mwarn\x1b[0m \x1b[48;5;236;38;2;0;112;122mcode\x1b[m \x1b[4:3;38;5;196mx\x1b[m"
	was := lipgloss.Writer.Profile
	t.Cleanup(func() { lipgloss.Writer.Profile = was })
	t.Setenv("NO_COLOR", "")
	t.Setenv("CLICOLOR", "")
	// Bytes with no escape are written as they are, a raw C1 CSI byte
	// (0x9B) in non-UTF-8 data included, as v1 wrote them.
	lipgloss.Writer.Profile = colorprofile.NoTTY
	if raw := "a\x9b31mb\n"; captureStdout(t, func() { fmt.Fprint(stdout, raw) }) != raw {
		t.Error("data with no escape sequence was altered")
	}
	for p, want := range map[colorprofile.Profile]string{
		colorprofile.NoTTY:     "\x1b[2Kwarn code x",
		colorprofile.ASCII:     "\x1b[2Kwarn code x",
		colorprofile.ANSI:      "\x1b[2K\x1b[1;93mwarn\x1b[0m \x1b[40;94mcode\x1b[m \x1b[4:3;91mx\x1b[m",
		colorprofile.TrueColor: in,
	} {
		lipgloss.Writer.Profile = p
		if got := captureStdout(t, func() { fmt.Fprint(stdout, in) }); got != want {
			t.Errorf("at %v:\n got %q\nwant %q", p, got, want)
		}
	}
}

// Every line hyctl prints goes through stdout or stderr: a print that reaches
// os.Stdout directly ignores NO_COLOR and puts colour codes in pipes, which
// Lip Gloss v1 used to prevent at render time and v2 does not.
func TestOutput_EveryPrintGoesThroughTheProfileWriter(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") || name == "output.go" {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		// The stream itself is fine where no text is written to it: its Fd,
		// and JSON, which escapes every control byte.
		allowed := map[ast.Node]bool{}
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.SelectorExpr:
				if inner, ok := n.X.(*ast.SelectorExpr); ok && n.Sel.Name == "Fd" {
					allowed[inner] = true
				}
				if id, ok := n.X.(*ast.Ident); ok && id.Name == "os" && (n.Sel.Name == "Stdout" || n.Sel.Name == "Stderr") && !allowed[n] {
					t.Errorf("%s: os.%s written directly; print through stdout or stderr", fset.Position(n.Pos()), n.Sel.Name)
				}
			case *ast.CallExpr:
				fun, _ := n.Fun.(*ast.SelectorExpr)
				if fun == nil {
					return true
				}
				pkg, _ := fun.X.(*ast.Ident)
				switch {
				case pkg != nil && pkg.Name == "json" && fun.Sel.Name == "NewEncoder":
					for _, a := range n.Args {
						allowed[a] = true
					}
				case pkg != nil && pkg.Name == "fmt" && strings.HasPrefix(fun.Sel.Name, "Print"):
					t.Errorf("%s: fmt.%s writes to os.Stdout directly; use fmt.F%s(stdout, …)",
						fset.Position(n.Pos()), fun.Sel.Name, strings.ToLower(fun.Sel.Name[:1])+fun.Sel.Name[1:])
				case fun.Sel.Name == "OutOrStdout" || fun.Sel.Name == "OutOrStderr" || fun.Sel.Name == "ErrOrStderr":
					t.Errorf("%s: %s is os.Stdout or os.Stderr unwrapped", fset.Position(n.Pos()), fun.Sel.Name)
				}
			}
			return true
		})
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
