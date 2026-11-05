// SPDX-License-Identifier: MIT

package budget

import (
	"bufio"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Every shape state.json is found in must read as the quotas it states, with
// the legacy key winning for the scope the orchestrator protocol still writes.
func TestQuotas_ReadsEveryShape(t *testing.T) {
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
		{"both keys, alias wins for claude-code",
			`{"claude_pct":70,"quotas":{"session:claude-code":{"context_frac":0.1},"session:codex":{"context_frac":0.4}}}`,
			[]Quota{
				{Scope: SessionClaudeCode, ContextFrac: 0.70, History: nil},
				{Scope: "session:codex", ContextFrac: 0.4},
			}},
		{"negative claude_pct", `{"claude_pct":-5}`,
			[]Quota{{Scope: SessionClaudeCode, ContextFrac: -0.05}}},
		{"claude_pct over 100", `{"claude_pct":140}`,
			[]Quota{{Scope: SessionClaudeCode, ContextFrac: 1.4}}},
		{"non-numeric context_frac skips only that entry",
			`{"quotas":{"session:a":{"context_frac":"high"},"session:b":{"context_frac":0.2}}}`,
			[]Quota{{Scope: "session:b", ContextFrac: 0.2}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Quotas([]byte(tc.state))
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Quotas(%s)\n got %+v\nwant %+v", tc.state, got, tc.want)
			}
		})
	}
}

// A value written by hand outside 0..100 is clamped by the type, so no caller
// can be handed a percentage that indexes past a bar or under a band.
func TestQuota_PctClamps(t *testing.T) {
	for frac, want := range map[float64]int{-0.05: 0, 0: 0, 0.52: 52, 0.666: 67, 1.4: 100} {
		if got := (Quota{ContextFrac: frac}).Pct(); got != want {
			t.Errorf("Quota{ContextFrac: %v}.Pct() = %d, want %d", frac, got, want)
		}
	}
}

func TestGoverning_PicksTheMostUrgent(t *testing.T) {
	if _, ok := Governing(nil); ok {
		t.Error("Governing(nil) reported a quota; none means normal, not a zero-value scope")
	}
	qs := []Quota{
		{Scope: "session:a", ContextFrac: 0.10},
		{Scope: "session:b", ContextFrac: 0.77},
		{Scope: "session:c", ContextFrac: 0.72},
	}
	if q, _ := Governing(qs); q.Scope != "session:b" {
		t.Errorf("Governing = %s, want session:b, the critical one", q.Scope)
	}
	// Equal modes tie on scope, so the same file always names the same quota.
	tied := []Quota{{Scope: "session:z", ContextFrac: 0.76}, {Scope: "session:m", ContextFrac: 0.78}}
	if q, _ := Governing(tied); q.Scope != "session:m" {
		t.Errorf("Governing on a tie = %s, want session:m, the first by scope", q.Scope)
	}
}

// The writer half of the alias: the history keeps growing, bounded, and a
// level of 0 (unknown) adds no observation.
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
}

// G2 (RFC 0001 T1): the provider-neutral path must not read the legacy key.
// Only internal/budget may, through LegacySessionQuota and MirrorLegacyHistory.
func TestT1_NoLegacyKeyOutsideBudget(t *testing.T) {
	for _, dir := range []string{"../dispatch", "../serve", "../../cmd/hydra"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("no Go files under %s (%v); the guard would pass vacuously", dir, err)
		}
		for _, path := range files {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
			for n := 1; sc.Scan(); n++ {
				for _, key := range []string{"claude_pct", "claudeMode"} {
					if strings.Contains(sc.Text(), key) {
						t.Errorf("%s:%d names %q; only internal/budget may read the legacy key (RFC 0001 T1)", path, n, key)
					}
				}
			}
			if err := sc.Err(); err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			_ = f.Close()
		}
	}
}

// asInt / asIntSlice bridge JSON's float64 numbers back to ints. A wrong answer
// here silently zeroes the governor's history.
func TestAsIntAndAsIntSlice(t *testing.T) {
	if got := asInt(float64(52)); got != 52 {
		t.Errorf("asInt(float64) = %d, want 52, JSON numbers arrive as float64", got)
	}
	if got := asInt(52); got != 52 {
		t.Errorf("asInt(int) = %d, want 52", got)
	}
	for _, v := range []any{nil, "52", true, []any{1}} {
		if got := asInt(v); got != 0 {
			t.Errorf("asInt(%#v) = %d, want 0", v, got)
		}
	}

	got := asIntSlice([]any{float64(1), float64(2), float64(3)})
	if len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Errorf("asIntSlice = %v, want [1 2 3]", got)
	}
	if got := asIntSlice("not an array"); got != nil {
		t.Errorf("asIntSlice(string) = %v, want nil", got)
	}
	// A mixed array must not drop elements, the history's length is its rate
	// signal, so a silently shortened slice changes the risk estimate.
	if got := asIntSlice([]any{float64(1), "x", float64(3)}); len(got) != 3 || got[1] != 0 {
		t.Errorf("asIntSlice with a bad element = %v, want [1 0 3]", got)
	}
}
