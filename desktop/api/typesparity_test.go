// SPDX-License-Identifier: MIT

package api

import (
	"go/ast"
	"go/parser"
	gotoken "go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/security"
)

// types.ts is a hand-written mirror and nothing was checking it, so it drifted:
// attestation, bom and privilege were computed, shipped on the wire, and
// invisible to the desktop because TypeScript had never heard of them (#435).
//
// dto_test.go guards the Go side of the wire. This guards the mirror.
//
// Everything below is *derived*, from reflection and from the Go source. The
// first cut listed the types and the union members by hand, so it could only
// catch drift its author had already thought to enumerate: security.Partial
// went missing from both this file and types.ts at once, and stayed green (#753).

var tsFieldRE = regexp.MustCompile(`(?m)^\s*([A-Za-z0-9_]+)\??:`)

// wireRoots are the payloads the API returns. Every other type is reached from
// these, never listed.
func wireRoots() []any {
	return []any{
		security.Report{},
		Model{}, Pool{}, ModelRegistry{}, GovernorPanel{}, Run{}, Fleet{},
		DiffLine{}, ReviewOutcome{}, MCPServer{}, MCPPanel{}, MCPSyncResult{},
		Head{}, HeadPanel{}, Span{}, Diff{}, Session{}, ChatReply{},
		PendingQuestion{}, QuestionQueue{}, HyctlStatus{}, InstallResult{},
	}
}

const modulePath = "github.com/ankit373/hydra"

// tsNames maps a Go type to its TypeScript name where the two differ, because
// a name that reads fine inside a Go package can be far too generic in one
// flat frontend namespace, or already taken there. Keyed by import path, since
// security.Action (a UI card) and ledger.Action (an enum) share a bare name.
//
// This is a rename table, not the enumeration this file used to be: a missing
// entry fails loudly with "declares no interface", so it can never make a
// check silently pass.
var tsNames = map[string]string{
	modulePath + "/internal/security.Report":   "SecurityReport",
	modulePath + "/internal/security.Count":    "SecurityCount",
	modulePath + "/internal/security.Boundary": "SecurityBoundary",
	modulePath + "/internal/ledger.Event":      "LedgerEvent",
	modulePath + "/internal/ledger.Action":     "LedgerAction",
}

func tsName(rt reflect.Type) string {
	if ts, ok := tsNames[rt.PkgPath()+"."+rt.Name()]; ok {
		return ts
	}
	return rt.Name()
}

// ours excludes stdlib structs such as time.Time, which marshal as a scalar
// and have no interface to mirror.
func ours(rt reflect.Type) bool {
	return rt.PkgPath() == modulePath || strings.HasPrefix(rt.PkgPath(), modulePath+"/")
}

// sourceDir maps a package's import path to its directory, relative to here.
func sourceDir(pkgPath string) string {
	return filepath.Join("../..", strings.TrimPrefix(pkgPath, modulePath+"/"))
}

// elem unwraps pointers, slices, arrays and maps down to the type a JSON field
// ultimately carries.
func elem(rt reflect.Type) reflect.Type {
	for {
		switch rt.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
			rt = rt.Elem()
		default:
			return rt
		}
	}
}

// wireStructs returns every struct reachable from the roots through fields
// that actually ship, keyed by type name.
//
// The hand-listed table this replaces only ever checked the roots, so a field
// on a *nested* type was unguarded. security.Coverage.partial was nested, which
// is how it shipped invisible to the desktop.
func wireStructs(t *testing.T) map[string]reflect.Type {
	t.Helper()
	out := map[string]reflect.Type{}
	var walk func(reflect.Type)
	walk = func(rt reflect.Type) {
		rt = elem(rt)
		// An anonymous struct has no name to mirror against an interface.
		if rt.Kind() != reflect.Struct || !ours(rt) || rt.Name() == "" {
			return
		}
		if prev, seen := out[rt.Name()]; seen {
			if prev != rt {
				t.Fatalf("two wire types are both named %s (%s and %s), "+
					"one TypeScript interface cannot mirror both",
					rt.Name(), prev.PkgPath(), rt.PkgPath())
			}
			return
		}
		out[rt.Name()] = rt
		eachWireField(rt, func(_ string, ft reflect.Type) { walk(ft) })
	}
	for _, r := range wireRoots() {
		walk(reflect.TypeOf(r))
	}
	return out
}

// eachWireField visits every field a struct puts on the wire, descending
// through embedded structs, whose fields JSON promotes into the parent.
func eachWireField(rt reflect.Type, fn func(name string, ft reflect.Type)) {
	for i := range rt.NumField() {
		f := rt.Field(i)
		tag := f.Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			if e := elem(f.Type); e.Kind() == reflect.Struct {
				eachWireField(e, fn)
			}
			continue
		}
		if name == "" || name == "-" {
			continue
		}
		fn(name, f.Type)
	}
}

// jsonFields returns the wire names of a Go struct.
func jsonFields(rt reflect.Type) map[string]bool {
	out := map[string]bool{}
	eachWireField(rt, func(name string, _ reflect.Type) { out[name] = true })
	return out
}

// tsInterface returns the field names declared on a TypeScript interface.
func tsInterface(t *testing.T, src, name string) map[string]bool {
	t.Helper()
	re := regexp.MustCompile(`(?s)export interface ` + regexp.QuoteMeta(name) + ` \{(.*?)\n\}`)
	m := re.FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("types.ts declares no interface %q", name)
	}
	out := map[string]bool{}
	for _, f := range tsFieldRE.FindAllStringSubmatch(m[1], -1) {
		out[f[1]] = true
	}
	return out
}

func readTypesTS(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../frontend/src/types.ts")
	if err != nil {
		t.Fatalf("cannot read the TypeScript mirror: %v", err)
	}
	return string(raw)
}

func TestTypesTS_MirrorsEveryFieldOnTheWire(t *testing.T) {
	src := readTypesTS(t)
	for name, rt := range wireStructs(t) {
		t.Run(name, func(t *testing.T) {
			ts := tsInterface(t, src, tsName(rt))
			for field := range jsonFields(rt) {
				if !ts[field] {
					t.Errorf("%s.%s ships on the wire but types.ts does not declare it, "+
						"the desktop cannot render a field it has never heard of", name, field)
				}
			}
		})
	}
}

// The reverse direction: a TypeScript field with no Go counterpart is always
// undefined at runtime, which typechecks and then renders nothing.
func TestTypesTS_DeclaresNoFieldTheBackendNeverSends(t *testing.T) {
	src := readTypesTS(t)
	for name, rt := range wireStructs(t) {
		t.Run(name, func(t *testing.T) {
			g := jsonFields(rt)
			for field := range tsInterface(t, src, tsName(rt)) {
				if !g[field] {
					t.Errorf("types.ts declares %s.%s but the backend never sends it, "+
						"it is permanently undefined", name, field)
				}
			}
		})
	}
}

// The tests above guard field *names*. They say nothing about the *values* a
// string enum carries, and a value is just as load-bearing: the Audit view
// looked two checks up by names internal/security never emitted, and both
// cards silently never rendered (#634).
//
// So: every named string type that ships must be declared in types.ts as a
// union holding exactly its members. Both sides are derived, the Go members
// from source and the shipping types from reflection, so neither can go stale
// the way the hand-written lists did.

var (
	tsUnionRE   = regexp.MustCompile(`^export type ([A-Za-z0-9_]+) =(.*)$`)
	tsLiteralRE = regexp.MustCompile(`'([^']*)'`)
)

// tsUnionValues returns the quoted members of a TypeScript string union,
// following the wrapped form (members on their own `|` lines) as well as the
// one-liner, so reformatting a long union cannot quietly unhook this check.
func tsUnionValues(t *testing.T, src, name string) (map[string]bool, bool) {
	t.Helper()
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		m := tsUnionRE.FindStringSubmatch(line)
		if m == nil || m[1] != name {
			continue
		}
		body := m[2]
		for _, next := range lines[i+1:] {
			if !strings.HasPrefix(strings.TrimSpace(next), "|") {
				break
			}
			body += next
		}
		out := map[string]bool{}
		for _, q := range tsLiteralRE.FindAllStringSubmatch(body, -1) {
			out[q[1]] = true
		}
		return out, true
	}
	return nil, false
}

// goUnionValues reads a named string type's members straight out of the Go
// source. Parsing beats a hand-kept list for one reason: a member is checked
// from the moment it is declared, with nothing left to remember.
func goUnionValues(t *testing.T, dir, typeName string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("cannot read %s to derive %s's members: %v", dir, typeName, err)
	}
	fset := gotoken.NewFileSet()
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, 0)
		if err != nil {
			t.Fatalf("cannot parse %s: %v", e.Name(), err)
		}
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != gotoken.CONST {
				continue
			}
			// Within one const block a spec with neither type nor value
			// repeats the previous line, so the type carries forward.
			var mine bool
			for _, s := range gd.Specs {
				vs, ok := s.(*ast.ValueSpec)
				if !ok {
					continue
				}
				switch {
				case vs.Type != nil:
					id, isIdent := vs.Type.(*ast.Ident)
					mine = isIdent && id.Name == typeName
				case len(vs.Values) > 0:
					mine = false // an untyped const sharing the block
				}
				if !mine {
					continue
				}
				for _, v := range vs.Values {
					lit, ok := v.(*ast.BasicLit)
					if !ok || lit.Kind != gotoken.STRING {
						continue
					}
					val, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatalf("cannot read %s member %s: %v", typeName, lit.Value, err)
					}
					out = append(out, val)
				}
			}
		}
	}
	return out
}

// wireStringTypes returns every named string type carried by a field that
// ships, keyed by type name.
func wireStringTypes(t *testing.T) map[string]reflect.Type {
	t.Helper()
	out := map[string]reflect.Type{}
	for _, rt := range wireStructs(t) {
		eachWireField(rt, func(_ string, ft reflect.Type) {
			if e := elem(ft); e.Kind() == reflect.String && ours(e) {
				out[e.Name()] = e
			}
		})
	}
	return out
}

func TestTypesTS_UnionValuesMatchGo(t *testing.T) {
	src := readTypesTS(t)
	checked := 0
	for name, rt := range wireStringTypes(t) {
		goVals := goUnionValues(t, sourceDir(rt.PkgPath()), name)
		if len(goVals) == 0 {
			continue // a free-form string type, nothing to mirror
		}
		checked++
		t.Run(name, func(t *testing.T) {
			ts, declared := tsUnionValues(t, src, tsName(rt))
			if !declared {
				t.Fatalf("Go ships %s as a %d-value enum but types.ts types it as a bare "+
					"string, so the frontend cannot branch on it and a typo'd comparison "+
					"still typechecks: declare `export type %s = %s`",
					name, len(goVals), tsName(rt), tsUnionLiteral(goVals))
			}
			for _, v := range goVals {
				if !ts[v] {
					t.Errorf("Go emits %s %q but types.ts does not list it, "+
						"a value the view can receive and never match", name, v)
				}
				delete(ts, v)
			}
			for extra := range ts {
				t.Errorf("types.ts lists %s %q, which Go never emits, "+
					"any branch on it is unreachable", name, extra)
			}
		})
	}
	// A derivation that finds nothing must fail rather than pass vacuously.
	if checked == 0 {
		t.Fatal("found no string enums on the wire, the derivation is broken")
	}
}

func tsUnionLiteral(vals []string) string {
	q := make([]string, len(vals))
	for i, v := range vals {
		q[i] = "'" + v + "'"
	}
	return strings.Join(q, " | ")
}
