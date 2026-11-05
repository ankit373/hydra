// SPDX-License-Identifier: MIT

package budget

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Every shape state.json is found in must read as the quotas it states.
func TestParseState_ReadsEveryShape(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state string
		want  []Quota
	}{
		{"no file", "", nil},
		{"malformed JSON", "{truncated", nil},
		{"legacy keys only", `{"claude_pct":52,"claude_pct_history":[40,52]}`,
			[]Quota{{Scope: SessionClaudeCode, ContextFrac: 0.52, History: []int{40, 52}}}},
		{"quotas only", `{"quotas":{"session:codex":{"context_frac":0.3,"history":[20,30]}}}`,
			[]Quota{{Scope: "session:codex", ContextFrac: 0.3, History: []int{20, 30}}}},
		{"alias fills claude-code, quotas the rest",
			`{"claude_pct":70,"quotas":{"session:codex":{"context_frac":0.4}}}`,
			[]Quota{{Scope: SessionClaudeCode, ContextFrac: 0.70}, {Scope: "session:codex", ContextFrac: 0.4}}},
		{"the alias wins over a quotas entry for claude-code",
			`{"claude_pct":70,"quotas":{"session:claude-code":{"context_frac":0.1}}}`,
			[]Quota{{Scope: SessionClaudeCode, ContextFrac: 0.7}}},
		{"negative claude_pct clamps to 0", `{"claude_pct":-5}`,
			[]Quota{{Scope: SessionClaudeCode, ContextFrac: 0}}},
		{"claude_pct over 100 clamps to 100", `{"claude_pct":140}`,
			[]Quota{{Scope: SessionClaudeCode, ContextFrac: 1}}},
		{"string claude_pct is absent", `{"claude_pct":"fifty"}`, nil},
		{"non-numeric context_frac skips only that entry",
			`{"quotas":{"session:a":{"context_frac":"high"},"session:b":{"context_frac":0.2}}}`,
			[]Quota{{Scope: "session:b", ContextFrac: 0.2}}},
		{"a negative or missing context_frac skips the entry",
			`{"quotas":{"session:a":{"history":[10]},"session:b":{"context_frac":-0.1}}}`, nil},
		{"a malformed quotas key leaves the alias", `{"claude_pct":30,"quotas":"x"}`,
			[]Quota{{Scope: SessionClaudeCode, ContextFrac: 0.3}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseState([]byte(tc.state)).Quotas
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ParseState(%s)\n got %+v\nwant %+v", tc.state, got, tc.want)
			}
		})
	}
}

// One bad point used to read as 0, a 60-point drop and recovery that faked a
// burn; the whole series is unknown instead, so the level band governs alone.
func TestParseState_NonNumericHistoryIsNoHistory(t *testing.T) {
	for _, hist := range []string{`[60,null,62]`, `[60,"x",62]`, `"60,62"`} {
		qs := ParseState([]byte(`{"claude_pct":62,"claude_pct_history":` + hist + `}`)).Quotas
		if len(qs) != 1 || qs[0].History != nil || qs[0].Mode() != ModeCompact {
			t.Errorf("history %s: got %+v, want no history and mode compact", hist, qs)
		}
	}
}

// A fraction above 1 is usually a percent in the wrong field. Reading it as
// calm would be the unsafe mistake, so it fails closed and says so, once.
func TestParseState_OverFullFractionFailsClosedWithOneWarning(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	for _, tc := range []struct {
		scope string
		frac  string
	}{{"session:overfull-40", "40"}, {"session:overfull-1.04", "1.04"}} {
		buf.Reset()
		state := []byte(`{"quotas":{"` + tc.scope + `":{"context_frac":` + tc.frac + `}}}`)
		for range 3 {
			qs := ParseState(state).Quotas
			if len(qs) != 1 || qs[0].ContextFrac != 1 || qs[0].Mode() != ModeEmergency {
				t.Errorf("context_frac %s: got %+v, want it clamped to 1 and governing as emergency", tc.frac, qs)
			}
		}
		if n := strings.Count(buf.String(), tc.scope+" has context_frac "+tc.frac); n != 1 {
			t.Errorf("context_frac %s warned %d times over three reads, want once naming the scope and value:\n%s",
				tc.frac, n, buf.String())
		}
	}
}

// The table is decoded only on demand; a non-object entry is skipped rather
// than rendered as an empty row, and a malformed table costs nothing else.
func TestParseState_Budget(t *testing.T) {
	got := ParseState([]byte(`{"budget":{"m":{"pct":40},"bad":"x","gone":null}}`)).Budget()
	if len(got) != 1 || got["m"]["pct"] != float64(40) {
		t.Errorf("Budget() = %v, want only m at 40", got)
	}
	if s := ParseState([]byte(`{"claude_pct":30,"budget":"x"}`)); len(s.Budget()) != 0 || len(s.Quotas) != 1 {
		t.Errorf("a malformed budget key cost the quotas or read as a table: %+v", s)
	}
}

func TestGoverning_PicksTheMostUrgent(t *testing.T) {
	if _, _, ok := Governing(nil); ok {
		t.Error("Governing(nil) reported a quota; none means normal, not a zero-value scope")
	}
	qs := []Quota{
		{Scope: "session:a", ContextFrac: 0.10},
		{Scope: "session:b", ContextFrac: 0.77},
		{Scope: "session:c", ContextFrac: 0.72},
	}
	if q, mode, _ := Governing(qs); q.Scope != "session:b" || mode != ModeCritical {
		t.Errorf("Governing = %s (%s), want session:b, the critical one", q.Scope, mode)
	}
	// Equal modes tie on scope, so the same file always names the same quota.
	tied := []Quota{{Scope: "session:z", ContextFrac: 0.76}, {Scope: "session:m", ContextFrac: 0.78}}
	if q, _, _ := Governing(tied); q.Scope != "session:m" {
		t.Errorf("Governing on a tie = %s, want session:m, the first by scope", q.Scope)
	}
}

// The writer half of the alias: the history keeps growing, bounded, a level
// of 0 (unknown) adds no observation, and a fractional level rounds the way
// the reader rounds it, so the level and its newest point agree.
func TestMirrorLegacyHistory(t *testing.T) {
	state := map[string]any{"claude_pct": float64(40), "claude_pct_history": []any{float64(20), float64(30)}}
	MirrorLegacyHistory(state)
	if got := state["claude_pct_history"]; !reflect.DeepEqual(got, []int{20, 30, 40}) {
		t.Errorf("history = %v, want [20 30 40]", got)
	}
	empty := map[string]any{"claude_pct": float64(0)}
	MirrorLegacyHistory(empty)
	if _, ok := empty["claude_pct_history"]; ok {
		t.Error("an unknown level of 0 started a history")
	}

	frac := map[string]any{"claude_pct": 79.6}
	MirrorLegacyHistory(frac)
	hist, _ := frac["claude_pct_history"].([]int)
	level := ParseState([]byte(`{"claude_pct":79.6}`)).Quotas[0].Pct()
	if level != 80 || len(hist) != 1 || hist[0] != 80 {
		t.Errorf("claude_pct 79.6: level %d, newest history point %v; want both 80", level, hist)
	}
}

// G2 (RFC 0001 T1): no Go outside internal/budget names the legacy key in
// code. The frozen readers below are the only exceptions (RFC §6).
func TestT1_NoLegacyKeyOutsideBudget(t *testing.T) {
	allowed := map[string]bool{
		"internal/budget": true, // the alias and its mirror
		"internal/tui":    true, // frozen legacy reader
		"desktop/api":     true, // frozen legacy reader
	}
	root := filepath.Join("..", "..")
	scanned := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && (strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata" ||
				d.Name() == "node_modules" || allowed[rel]) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		scanned++
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.BasicLit:
				if x.Kind == token.STRING && strings.Contains(x.Value, "claude_pct") {
					t.Errorf("%s names %q; only internal/budget may read the legacy key (RFC 0001 T1)",
						fset.Position(x.Pos()), "claude_pct")
				}
			case *ast.Ident:
				if x.Name == "claudeMode" {
					t.Errorf("%s names %q; only internal/budget may read the legacy key (RFC 0001 T1)",
						fset.Position(x.Pos()), "claudeMode")
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 100 {
		t.Fatalf("scanned only %d Go files from %s; the guard would pass vacuously", scanned, root)
	}
}
