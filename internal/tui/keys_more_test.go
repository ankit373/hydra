// SPDX-License-Identifier: MIT

package tui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/trust"
)

func providerHead(id, prov string) provider.Head {
	return provider.Head{ID: id, Provider: prov}
}

// Every list view moves its own selection on j/k; space collapses only in
// models; the per-view letters land where the table says they do.
func TestViewKeys_PerViewRouting(t *testing.T) {
	m := testCockpit()

	m.view = ckViewAgents
	m = typed(m, "j")
	if m.agentSel != 1 {
		t.Errorf("agents j → %d", m.agentSel)
	}
	m = typed(m, "k")
	if m.agentSel != 0 {
		t.Errorf("agents k → %d", m.agentSel)
	}

	m.view = ckViewModels
	m = typed(m, "j")
	if m.modelSel != 1 {
		t.Errorf("models j → %d", m.modelSel)
	}
	before := len(m.flatRows())
	m.modelSel = 0 // a group header
	m = press(m, tea.KeySpace)
	if len(m.flatRows()) >= before {
		t.Error("space did not collapse the selected group")
	}
	m = press(m, tea.KeySpace)

	// p pins from the models view via the key path.
	for i, f := range m.flatRows() {
		if f.r >= 0 {
			m.modelSel = i
			break
		}
	}
	m = typed(m, "p")
	if m.pinnedTier == 0 {
		t.Error("p did not pin")
	}

	// usage m/t/d via the key path.
	m.view = ckViewUsage
	m = typed(m, "t")
	if m.usageGroup != 't' {
		t.Errorf("t → %q", m.usageGroup)
	}
	m = typed(m, "d")
	if m.usageGroup != 'd' {
		t.Errorf("d → %q", m.usageGroup)
	}
	m = typed(m, "m")
	if m.usageGroup != 'm' {
		t.Errorf("m → %q", m.usageGroup)
	}

	// The audit queue selection moves with j/k; i ignores the selected item.
	m.view = ckViewAudit
	m.audit = testAudit(nil, []ckRun{
		testRun("f1", "failed", "a"), testRun("f2", "failed", "b"),
	}) // two items: the failed-runs signal + the provisional postmark server
	m = typed(m, "j")
	if m.auditSel != 1 {
		t.Errorf("audit j → %d", m.auditSel)
	}
	before2 := len(m.auditItems())
	m = typed(m, "i") // ignore the selected item via the key path
	if len(m.auditItems()) != before2-1 {
		t.Errorf("i did not ignore: %d items, want %d", len(m.auditItems()), before2-1)
	}

	// Arrow keys are the same motion.
	m.view = ckViewAgents
	m.agentSel = 0
	m = press(m, tea.KeyDown)
	if m.agentSel != 1 {
		t.Errorf("down → %d", m.agentSel)
	}
	m = press(m, tea.KeyUp)
	if m.agentSel != 0 {
		t.Errorf("up → %d", m.agentSel)
	}
}

// Enter on a model row focuses the detail via the key path.
func TestViewKeys_EnterFocusesModelDetail(t *testing.T) {
	m := testCockpit()
	m.view = ckViewModels
	for i, f := range m.flatRows() {
		if f.r >= 0 {
			m.modelSel = i
			break
		}
	}
	m, _ = enter(m)
	if !m.modelFocus {
		t.Error("enter did not focus the detail")
	}
}

// Drilled trace scrolling moves traceOff via j/k, clamped at 0.
func TestViewKeys_DrilledTraceScrolls(t *testing.T) {
	m := testCockpit()
	m.view = ckViewActivity
	m.actDrill = true
	m = typed(m, "j")
	m = typed(m, "j")
	if m.traceOff != 2 {
		t.Errorf("traceOff = %d", m.traceOff)
	}
	for i := 0; i < 10; i++ {
		m = typed(m, "k")
	}
	if m.traceOff != 0 {
		t.Errorf("traceOff underflowed to %d", m.traceOff)
	}
}

// runCostUSD prefers the runlog figure and falls back to the cost.jsonl join.
func TestRunCostUSD_Fallback(t *testing.T) {
	m := testCockpit()
	r := testRun("rx", "ok", "t")
	r.costUSD = 0
	m.metrics.runCost = map[string]ckRunCost{"rx": {costUSD: 0.042}}
	if got := m.runCostUSD(r); got != 0.042 {
		t.Errorf("fallback = %v", got)
	}
	r.costUSD = 0.01
	if got := m.runCostUSD(r); got != 0.01 {
		t.Errorf("runlog figure not preferred: %v", got)
	}
}

// ckScorecardFor pulls a model's rows out of a real calibrator, most-observed
// first, matched tolerantly by id or name.
func TestCkScorecardFor_FromRealCalibrator(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trust.jsonl")
	cal, err := trust.New(path)
	if err != nil {
		t.Fatal(err)
	}
	rec := func(source, domain string, n int) {
		for i := 0; i < n; i++ {
			if err := cal.Update(source, domain, true, trust.OutcomeCorrect); err != nil {
				t.Fatal(err)
			}
		}
	}
	rec("ollama/qwen2.5-coder:7b", "go", 3)
	rec("ollama/qwen2.5-coder:7b", "ts", 8)
	rec("claude", "go", 5)

	m := ckMetrics{calibrator: cal}
	got := m.ckScorecardFor("ollama/qwen2.5-coder:7b", "qwen2.5-coder:7b (Ollama)")
	if len(got) != 2 {
		t.Fatalf("got %d rows, want the model's 2 domains: %+v", len(got), got)
	}
	if got[0].Domain != "ts" {
		t.Errorf("rows not most-observed first: %+v", got)
	}
	for _, s := range got {
		if strings.Contains(s.Source, "claude") {
			t.Errorf("another model's calibration leaked in: %+v", s)
		}
	}
	if rows := m.ckScorecardFor("nothing", "matches"); len(rows) != 0 {
		t.Errorf("a no-match query returned %+v", rows)
	}
}

func TestCkAgo(t *testing.T) {
	if got := ckAgo(time.Time{}); got != "—" {
		t.Errorf("zero time = %q", got)
	}
	if got := ckAgo(time.Now().Add(-30 * time.Second)); !strings.Contains(got, "s ago") {
		t.Errorf("seconds = %q", got)
	}
	if got := ckAgo(time.Now().Add(-2 * time.Hour)); !strings.Contains(got, "h") || !strings.Contains(got, "ago") {
		t.Errorf("hours = %q", got)
	}
}

// ckServerFor: the two binaries become headers of their server groups.
func TestCkServerFor(t *testing.T) {
	for _, tt := range []struct {
		id, provider string
		wantGroup    string
		wantBinary   bool
	}{
		{"ollama", "local", "Ollama", true},
		{"llamafile", "local", "Llamafile", true},
		{"ollama/qwen", "local", "Ollama", false},
		{"lmstudio/phi", "local", "LM Studio", false},
		{"claude", "anthropic", "anthropic", false},
	} {
		g, b := ckServerFor(providerHead(tt.id, tt.provider))
		if g != tt.wantGroup || b != tt.wantBinary {
			t.Errorf("ckServerFor(%s) = (%s, %v), want (%s, %v)", tt.id, g, b, tt.wantGroup, tt.wantBinary)
		}
	}
}
