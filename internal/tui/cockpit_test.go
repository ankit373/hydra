// SPDX-License-Identifier: MIT

package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/rank"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/testutil"
)

// ── shared fixtures ─────────────────────────────────────────────────────────────

func typed(m Cockpit, s string) Cockpit {
	for _, r := range s {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(Cockpit)
	}
	return m
}

func enter(m Cockpit) (Cockpit, tea.Cmd) {
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	return next.(Cockpit), cmd
}

func press(m Cockpit, k tea.KeyType) Cockpit {
	next, _ := m.Update(tea.KeyMsg{Type: k})
	return next.(Cockpit)
}

// testHeads is a fixed roster so routing assertions do not depend on what
// happens to be installed on the machine running the test.
func testHeads() []ckHead {
	return []ckHead{
		{id: "opus", name: "anthropic · claude-opus", tier: 1, price: 15, up: true},
		{id: "sonnet", name: "anthropic · claude-sonnet", tier: 3, price: 3, up: true},
		{id: "qwen", name: "ollama · qwen2.5-coder", tier: 10, price: 0, up: true, local: true},
	}
}

// testRun builds one recorded run for list fixtures.
func testRun(id, status, task string) ckRun {
	r := ckRun{id: id, status: status, task: task, durMS: 1200}
	if status == "running" {
		r.live = true
	}
	if status == "failed" {
		r.fails = 1
	}
	r.events = []runlog.Event{{Kind: runlog.KindRunStarted, TS: "2026-09-04T10:00:00Z", Detail: task}}
	return r
}

// testCockpit is a deterministic model with fixtures on every view, built
// without touching the machine.
func testCockpit() Cockpit {
	m := Cockpit{
		mode:         "dispatch",
		heads:        testHeads(),
		collapsed:    map[string]bool{},
		auditIgnored: map[string]bool{},
		usageGroup:   'm',
		scannedAt:    time.Now(),
		w:            100, h: 30, ready: true,
	}
	m.groups = ckGroupHeads(testProbedHeads())
	m.runsToday = []ckRun{
		testRun("20260904T100000Z-aaaa", "ok", "add pagination"),
		testRun("20260904T100100Z-bbbb", "failed", "rotate signing key"),
		testRun("20260904T100200Z-cccc", "running", "write tests"),
	}
	m.log = []string{"welcome"}
	return m
}

func testProbedHeads() []provider.Head {
	return []provider.Head{
		{ID: "claude", Name: "Claude Code", Provider: "anthropic", Source: "cli", CapScore: 95, AuthReady: true},
		{ID: "ollama/qwen2.5-coder:7b", Name: "qwen2.5-coder:7b (Ollama)", Provider: "local",
			Source: "port", Endpoint: "http://localhost:11434", CapScore: 60, LocalOnly: true, AuthReady: true},
		{ID: "ollama/nomic-embed-text", Name: "nomic-embed-text (Ollama)", Provider: "local",
			Source: "port", Endpoint: "http://localhost:11434", CapScore: 0, LocalOnly: true, AuthReady: true,
			Meta: map[string]string{"embedding_only": "true"}},
		{ID: "ollama", Name: "Ollama", Provider: "local", Source: "cli", CapScore: 60, LocalOnly: true},
	}
}

// ── view table ──────────────────────────────────────────────────────────────────

func TestViewTable_SixViews(t *testing.T) {
	want := []string{"chat", "agents", "models", "activity", "usage", "audit"}
	if len(ckViewNames) != len(want) {
		t.Fatalf("ckViewNames = %v, want %v", ckViewNames, want)
	}
	for i, name := range want {
		if ckViewNames[i] != name {
			t.Errorf("view %d = %q, want %q", i, ckViewNames[i], name)
		}
	}
	if got := ckViewName(-1); got != "chat" {
		t.Errorf("ckViewName(-1) = %q, want the default", got)
	}
	if got := ckViewName(len(ckViewNames)); got != "chat" {
		t.Errorf("ckViewName(out of range) = %q, want the default", got)
	}
}

func TestValidSnapshotView(t *testing.T) {
	for i := range ckViewNames {
		if ok, _ := ValidSnapshotView(i); !ok {
			t.Errorf("ValidSnapshotView(%d) = false", i)
		}
	}
	for _, bad := range []int{-1, len(ckViewNames), 99} {
		if ok, _ := ValidSnapshotView(bad); ok {
			t.Errorf("ValidSnapshotView(%d) = true", bad)
		}
	}
	_, names := ValidSnapshotView(0)
	if len(names) != len(ckViewNames) {
		t.Fatalf("returned %d names", len(names))
	}
	names[0] = "mutated"
	if ckViewNames[0] == "mutated" {
		t.Error("ValidSnapshotView leaked the package view table to the caller")
	}
}

// ── layout regression: the shell's hard invariants ─────────────────────────────
//
// For every view (plus the glossary) at every mandated size: the output is at
// most h lines, no line exceeds w cells, and the status bar is the final line
// — even with pathological content (a 3000-char task, embedded newlines).

func TestEveryView_LayoutInvariantsAtEverySize(t *testing.T) {
	testutil.NewSandbox(t)

	long := strings.Repeat("x", 3000)
	base := testCockpit()
	base.runsToday = append(base.runsToday,
		testRun("20260904T100300Z-dddd", "ok", long),
		testRun("20260904T100400Z-eeee", "failed", "first\nsecond\nthird lines embedded"),
	)
	base.pctKnown, base.claudePct, base.pctHist = true, 52, []int{40, 45, 52}
	base = typed(base, "add pagination to the users endpoint")
	next, _ := enter(base)
	base = next
	base.log = append(base.log, long, "tail\nwith\nnewlines")

	sizes := []struct{ w, h int }{{60, 15}, {80, 24}, {100, 30}, {120, 40}}
	for view := 0; view < ckViewCount(); view++ {
		for _, sz := range sizes {
			t.Run(fmt.Sprintf("view%d_%dx%d", view, sz.w, sz.h), func(t *testing.T) {
				m := base.jump(view)
				m.w, m.h, m.ready = sz.w, sz.h, true
				out := m.View()
				lines := strings.Split(out, "\n")
				if len(lines) > sz.h {
					t.Errorf("view %d at %dx%d renders %d lines, want <= %d",
						view, sz.w, sz.h, len(lines), sz.h)
				}
				for i, l := range lines {
					if got := lipgloss.Width(l); got > sz.w {
						t.Errorf("view %d at %dx%d: line %d is %d cells wide:\n%q",
							view, sz.w, sz.h, i, got, l)
					}
				}
				last := stripANSI(lines[len(lines)-1])
				if !strings.Contains(last, "shortcuts") {
					t.Errorf("view %d at %dx%d: the status bar is not the final line: %q",
						view, sz.w, sz.h, last)
				}
			})
		}
	}

	// The glossary overlay obeys the same frame.
	for _, sz := range sizes {
		g := base
		g.glossary = true
		g.w, g.h, g.ready = sz.w, sz.h, true
		out := g.View()
		lines := strings.Split(out, "\n")
		if len(lines) > sz.h {
			t.Errorf("glossary at %dx%d renders %d lines", sz.w, sz.h, len(lines))
		}
		for i, l := range lines {
			if got := lipgloss.Width(l); got > sz.w {
				t.Errorf("glossary at %dx%d: line %d is %d cells", sz.w, sz.h, i, got)
			}
		}
	}
}

// Before the first WindowSizeMsg the model is not ready; it must still render
// something rather than a blank terminal.
func TestView_RendersBeforeFirstResize(t *testing.T) {
	fresh := Cockpit{}
	if strings.TrimSpace(fresh.View()) == "" {
		t.Error("the cockpit renders nothing before its first resize")
	}
}

// Tab cycles through every view and always lands in range; entering the audit
// view builds its data.
func TestTabCycle_VisitsEveryViewInRange(t *testing.T) {
	testutil.NewSandbox(t)
	m := testCockpit()
	seen := map[int]bool{}
	for i := 0; i < ckViewCount()*2; i++ {
		m = press(m, tea.KeyTab)
		if !ckValidView(m.view) {
			t.Fatalf("after %d tabs view = %d", i+1, m.view)
		}
		seen[m.view] = true
	}
	if len(seen) != ckViewCount() {
		t.Errorf("tab visited %d views, want %d", len(seen), ckViewCount())
	}
}

// ── snapshot ───────────────────────────────────────────────────────────────────

// The snapshot is what `hyctl tui --snapshot` prints. All six views plus the
// glossary must render and be labelled, or the output is unreadable in a bug
// report.
func TestCockpitSnapshot_RendersAllViewsAndGlossary(t *testing.T) {
	testutil.NewSandbox(t)
	got := CockpitSnapshot()
	if strings.TrimSpace(got) == "" {
		t.Fatal("CockpitSnapshot rendered nothing")
	}
	n := len(ckViewNames)
	for i := 1; i <= n; i++ {
		want := fmt.Sprintf("VIEW %d/%d", i, n)
		if !strings.Contains(got, want) {
			t.Errorf("the snapshot is missing %q", want)
		}
	}
	if !strings.Contains(got, "GLOSSARY") {
		t.Error("the snapshot is missing the glossary frame")
	}
}

// `--view N` renders exactly the requested frame, never panicking on an
// out-of-range value — the CLI validates, but the function must be total.
func TestCockpitSnapshotView_AllViewsAndOutOfRange(t *testing.T) {
	testutil.NewSandbox(t)
	for view, name := range ckViewNames {
		out := CockpitSnapshotView(view)
		if out == "" {
			t.Fatalf("view %d (%s) rendered nothing", view, name)
		}
		if !strings.Contains(stripANSI(out), name) {
			t.Errorf("view %d frame does not name %q in its header", view, name)
		}
	}
	for _, view := range []int{-1, -100, len(ckViewNames), 99} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("--view %d panicked: %v", view, r)
				}
			}()
			if CockpitSnapshotView(view) == "" {
				t.Errorf("--view %d rendered nothing", view)
			}
		}()
	}
}

// ── shell plumbing ─────────────────────────────────────────────────────────────

func TestResizeAndInit(t *testing.T) {
	next, _ := (Cockpit{}).Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m := next.(Cockpit)
	if m.w != 120 || m.h != 40 || !m.ready {
		t.Errorf("resize left w=%d h=%d ready=%v", m.w, m.h, m.ready)
	}
	if (Cockpit{}).Init() != nil {
		t.Error("Init() returned a command")
	}
}

// ckHeadsFrom must rank heads the way dispatch does, mark unroutable ones
// down, and never fabricate a price for a head whose cost is unknown (#189).
func TestCkHeadsFrom_MirrorsRoutingAndDoesNotFabricatePrices(t *testing.T) {
	heads := testProbedHeads()
	rows := ckHeadsFrom(heads, nil)
	if len(rows) != len(heads) {
		t.Fatalf("got %d rows for %d heads", len(rows), len(heads))
	}
	byID := map[string]ckHead{}
	for _, r := range rows {
		if r.price != 0 {
			t.Errorf("%s priced at %v with no pricing DB loaded", r.id, r.price)
		}
		byID[r.id] = r
	}
	if !byID["claude"].up {
		t.Error("a routable head is shown as down")
	}
	// The ollama binary alone is not routable (#248); the embedding-only model
	// never is (#532).
	if byID["ollama"].up {
		t.Error("the binary-only head is shown as up")
	}
	if byID["ollama/nomic-embed-text"].up {
		t.Error("an embedding-only model is shown as routable")
	}
	if got := byID["ollama/qwen2.5-coder:7b"].tier; got != rank.UITier(heads[1]) {
		t.Errorf("tier %d disagrees with routing", got)
	}
	if got := ckHeadsFrom(nil, nil); len(got) != 0 {
		t.Errorf("ckHeadsFrom(nil) = %v", got)
	}
}

// The code-stream tick carries its generation so a superseded stream cannot
// double-speed the current one.
func TestCodeTick_GenerationGuard(t *testing.T) {
	cmd := ckCodeTick(7)
	if cmd == nil {
		t.Fatal("ckCodeTick returned no command")
	}
	if tick, ok := cmd().(ckCodeTickMsg); !ok || tick.gen != 7 {
		t.Fatalf("tick = %#v, want gen 7", cmd())
	}

	m := testCockpit()
	m.codeLines = []string{"a", "b", "c"}
	m.codeGen = 3
	next, cmd2 := m.Update(ckCodeTickMsg{gen: 3})
	if got := next.(Cockpit).codeShown; got != 1 {
		t.Errorf("codeShown = %d after one tick", got)
	}
	if cmd2 == nil {
		t.Error("mid-stream tick scheduled nothing")
	}
	stale, _ := next.Update(ckCodeTickMsg{gen: 2})
	if got := stale.(Cockpit).codeShown; got != 1 {
		t.Errorf("a stale tick advanced the stream to %d", got)
	}
}

// state.json readers: absent or corrupt state renders as unknown, never as a
// number the user would act on (#189).
func TestCkClaudePct_AbsentIsUnknownNotZero(t *testing.T) {
	testutil.NewSandbox(t)

	if _, _, ok := ckClaudePct(); ok {
		t.Error("no state.json read as known")
	}
	dir := filepath.Join(config.Dir(), "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeState := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeState("{truncated")
	if _, _, ok := ckClaudePct(); ok {
		t.Error("corrupt state.json read as known")
	}
	writeState(`{"claude_pct":73,"claude_pct_history":[60,66,73]}`)
	pct, hist, ok := ckClaudePct()
	if !ok || pct != 73 || len(hist) != 3 {
		t.Errorf("got pct=%d hist=%v ok=%v, want 73/[60 66 73]/true", pct, hist, ok)
	}
	// A state.json without the field is unknown, not 0%.
	writeState(`{"other":1}`)
	if _, _, ok := ckClaudePct(); ok {
		t.Error("state.json without claude_pct read as known")
	}
}
