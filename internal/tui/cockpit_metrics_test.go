// SPDX-License-Identifier: MIT

package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ankit373/hydra/internal/cost"
	"github.com/ankit373/hydra/internal/graph"
)

// One fold of the cost rows feeds every consumer: per-model stats, the run
// join, the usage aggregates, and the local-model set.
func TestFold_PerModelStatsAndRunJoin(t *testing.T) {
	now := fixedNow()
	day := now.Format("2006-01-02")
	m := ckMetrics{stats: map[string]*ckModelStat{}, localModels: map[string]bool{}, runCost: map[string]ckRunCost{}}
	m.fold([]cost.Row{
		{TS: day + "T09:00:00Z", Model: "qwen (Ollama)", Executor: "local", Tier: 10,
			WallMS: 300, PromptTokens: 100, ResponseTokens: 50, TokensSource: "actual", RunID: "r1", Enum: "SIMPLE"},
		{TS: day + "T10:00:00Z", Model: "qwen (Ollama)", Executor: "local", Tier: 10,
			WallMS: 500, PromptTokens: 100, ResponseTokens: 50, TokensSource: "actual", RunID: "r1", SwarmMode: "best"},
		{TS: "2020-01-01T00:00:00Z", Model: "qwen (Ollama)", Executor: "local", Tier: 10, WallMS: 100},
	}, stubPricer{}, now)

	st := m.stats["qwen (Ollama)"]
	if st == nil {
		t.Fatal("no per-model stat folded")
	}
	if len(st.wall) != 3 {
		t.Errorf("wall samples = %d, want all 3 (p50 uses history)", len(st.wall))
	}
	if st.reqsToday != 2 {
		t.Errorf("reqsToday = %d, want 2 (the 2020 row is not today)", st.reqsToday)
	}
	if st.lastRunID != "r1" {
		t.Errorf("lastRunID = %q", st.lastRunID)
	}
	if !m.localModels["qwen (Ollama)"] {
		t.Error("a purely-local model is not marked local")
	}

	rc, ok := m.runCost["r1"]
	if !ok {
		t.Fatal("no run join folded")
	}
	if rc.enum != "SIMPLE" || rc.strategy != "best" {
		t.Errorf("run join = %+v", rc)
	}
	if rc.prompt != 200 || rc.resp != 100 || rc.actual != 300 || rc.est != 0 {
		t.Errorf("run tokens = %+v", rc)
	}
}

// A model that ever routed to a remote provider must not claim "local · free".
func TestFold_MixedProvidersAreNotLocal(t *testing.T) {
	now := fixedNow()
	day := now.Format("2006-01-02")
	m := ckMetrics{stats: map[string]*ckModelStat{}, localModels: map[string]bool{}, runCost: map[string]ckRunCost{}}
	m.fold([]cost.Row{
		{TS: day + "T09:00:00Z", Model: "m", Executor: "local"},
		{TS: day + "T10:00:00Z", Model: "m", Executor: "openrouter"},
	}, nil, now)
	if m.localModels["m"] {
		t.Error("a model with a remote row is marked local · free")
	}
}

func TestCkLocalExecutor(t *testing.T) {
	for e, want := range map[string]bool{"local": true, "ollama": true, "agy": false, "openai": false, "": false} {
		if got := ckLocalExecutor(e); got != want {
			t.Errorf("ckLocalExecutor(%q) = %v", e, got)
		}
	}
}

// ckStatFor matches tolerantly: cost.jsonl records the model as the executor
// reported it while the scan names it differently, so an exact match silently
// misses and a busy local head would show as never having run.
func TestCkStatFor_ToleratesTheNamingMismatch(t *testing.T) {
	m := ckMetrics{stats: map[string]*ckModelStat{
		"Qwen2.5-Coder:7b (Ollama)": {wall: []int64{120, 130, 118}, reqsToday: 3},
		"claude-opus":               {wall: []int64{2100, 1900}},
	}}
	tests := []struct {
		label    string
		name, id string
		wantN    int
	}{
		{"exact model name", "Qwen2.5-Coder:7b (Ollama)", "", 3},
		{"exact by id", "", "claude-opus", 2},
		{"head name contained in the model", "Ollama", "", 3},
		{"case-insensitive", "OLLAMA", "", 3},
		{"model contained in the id", "", "claude-opus-4-20250101", 2},
		{"no match at all", "gemini", "gemini", 0},
		{"both empty", "", "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.label, func(t *testing.T) {
			if got := m.ckStatFor(tt.name, tt.id); len(got.wall) != tt.wantN {
				t.Errorf("ckStatFor(%q, %q) = %d samples, want %d", tt.name, tt.id, len(got.wall), tt.wantN)
			}
		})
	}
	if got := (ckMetrics{}).ckStatFor("x", "y"); len(got.wall) != 0 {
		t.Errorf("empty metrics returned %v", got)
	}
}

// With no graph loaded there is no change impact to report — and reporting
// one anyway is exactly the bug #193 removed.
func TestBlastFor_NoGraphSaysNothing(t *testing.T) {
	var m ckMetrics
	if _, _, _, ok := m.ckBlastFor("internal/auth/token.go"); ok {
		t.Error("reported an impact with no graph loaded")
	}
	if _, _, _, ok := m.ckBlastFor(""); ok {
		t.Error("reported an impact for an empty path")
	}
}

// A graph that does contain the file yields real numbers — dependents and κ
// walked from the graph, never a literal.
func TestBlastFor_RealGraphYieldsRealNumbers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "graph.json")
	doc := `{"nodes":[
	  {"id":"hub","file":"hub.go"},
	  {"id":"a","file":"a.go"},
	  {"id":"b","file":"b.go"},
	  {"id":"c","file":"c.go"},
	  {"id":"leaf","file":"leaf.go"}
	],"edges":[
	  {"from":"a","to":"hub"},
	  {"from":"b","to":"hub"},
	  {"from":"c","to":"hub"}
	]}`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	g, err := graph.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	m := ckMetrics{graph: g}

	radius, deps, _, ok := m.ckBlastFor("hub.go")
	if !ok {
		t.Fatal("a file with real dependents reported no impact")
	}
	if deps != 3 {
		t.Errorf("dependents = %d, want 3", deps)
	}
	if radius <= 1.0 {
		t.Errorf("radius = %v, want >1", radius)
	}
	if _, _, _, ok := m.ckBlastFor("leaf.go"); ok {
		t.Error("a leaf file reported an impact")
	}
	if _, _, _, ok := m.ckBlastFor("not-in-graph.go"); ok {
		t.Error("a file absent from the graph reported an impact")
	}
}
