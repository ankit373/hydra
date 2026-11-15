// SPDX-License-Identifier: MIT

package policy

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const policyPkg = "github.com/ankit373/hydra/internal/policy"

type listedPkg struct {
	ImportPath string
	Dir        string
	GoFiles    []string
	Export     string
}

// The enforced tags are what `hyctl security` reports, so they must match the
// FilePolicy fields that non-test code in this module actually reads (#1754).
func TestEnforcedTag_MatchesCallers(t *testing.T) {
	pkgs := goList(t)
	exports := map[string]string{}
	for _, p := range pkgs {
		exports[p.ImportPath] = p.Export
	}
	fset := token.NewFileSet()
	imp := importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
		return os.Open(exports[path])
	})

	// field -> functions that read it ("" for code outside any function).
	readers := map[string][]string{}
	called := map[string]bool{}
	for _, p := range pkgs {
		// Every module package, since a caller can reach a FilePolicy without importing policy.
		if !strings.HasPrefix(p.ImportPath, "github.com/ankit373/hydra/") {
			continue
		}
		scanPackage(t, fset, imp, p, readers, called)
	}
	// A read inside a function nothing calls enforces nothing.
	used := map[string]bool{}
	for field, fns := range readers {
		used[field] = slices.ContainsFunc(fns, func(fn string) bool { return fn == "" || called[fn] })
	}

	var read, keys []string
	rt := reflect.TypeFor[FilePolicy]()
	for i := range rt.NumField() {
		f := rt.Field(i)
		if key := f.Tag.Get("yaml"); key != "-" {
			keys = append(keys, key)
			if used[f.Name] {
				read = append(read, key)
			}
		}
	}
	if got := EnforcedKeys(); !slices.Equal(got, read) {
		t.Errorf("EnforcedKeys() = %v, but callers read %v; fix the enforced tags", got, read)
	}
	if got := Keys(); !slices.Equal(got, keys) {
		t.Errorf("Keys() = %v, want %v", got, keys)
	}
}

// scanPackage type-checks p, adding each FilePolicy field its non-test files
// select to readers, and each function they reference to called.
func scanPackage(t *testing.T, fset *token.FileSet, imp types.Importer, p listedPkg,
	readers map[string][]string, called map[string]bool) {
	t.Helper()
	var files []*ast.File
	for _, name := range p.GoFiles {
		f, err := parser.ParseFile(fset, filepath.Join(p.Dir, name), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	info := &types.Info{
		Selections: map[*ast.SelectorExpr]*types.Selection{},
		Uses:       map[*ast.Ident]types.Object{},
		Defs:       map[*ast.Ident]types.Object{},
	}
	conf := types.Config{Importer: imp}
	if _, err := conf.Check(p.ImportPath, fset, files, info); err != nil {
		t.Fatalf("type-check %s: %v", p.ImportPath, err)
	}
	for _, obj := range info.Uses {
		if fn, ok := obj.(*types.Func); ok {
			called[fn.FullName()] = true
		}
	}
	for _, f := range files {
		for _, decl := range f.Decls {
			fn := ""
			if fd, ok := decl.(*ast.FuncDecl); ok && fd.Name.Name != "main" && fd.Name.Name != "init" {
				fn = info.Defs[fd.Name].(*types.Func).FullName()
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				if se, ok := n.(*ast.SelectorExpr); ok {
					if field := filePolicyField(info.Selections[se]); field != "" {
						readers[field] = append(readers[field], fn)
					}
				}
				return true
			})
		}
	}
}

// filePolicyField names the FilePolicy field sel reads, or "" if it reads another.
func filePolicyField(sel *types.Selection) string {
	if sel == nil || sel.Kind() != types.FieldVal {
		return ""
	}
	recv := sel.Recv()
	if ptr, isPtr := recv.(*types.Pointer); isPtr {
		recv = ptr.Elem()
	}
	named, ok := types.Unalias(recv).(*types.Named)
	if ok && named.Obj().Name() == "FilePolicy" && named.Obj().Pkg().Path() == policyPkg {
		return sel.Obj().Name()
	}
	return ""
}

// goList lists every package in this module with export data for its imports.
func goList(t *testing.T) []listedPkg {
	t.Helper()
	cmd := exec.Command("go", "list", "-export", "-deps", "-json", "./...")
	cmd.Dir = filepath.Join("..", "..")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, stderr.String())
	}
	var pkgs []listedPkg
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var p listedPkg
		if err := dec.Decode(&p); err != nil {
			t.Fatal(err)
		}
		pkgs = append(pkgs, p)
	}
	return pkgs
}
