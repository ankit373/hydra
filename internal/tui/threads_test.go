// SPDX-License-Identifier: MIT

package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ankit373/hydra/internal/testutil"
)

func altDigit(m Cockpit, d rune) Cockpit {
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{d}, Alt: true})
	return next.(Cockpit)
}

// ── registry & switching ─────────────────────────────────────────────────────

func TestThreads_CtrlTCreatesAndAltDigitJumps(t *testing.T) {
	m := testCockpit()
	m = press(m, tea.KeyCtrlT)
	if got := m.th().id; got != 2 {
		t.Fatalf("ctrl+t landed on thread %d, want 2", got)
	}
	if m.th().input != "" {
		t.Error("a fresh thread inherited input")
	}
	m.th().input = "draft for thread 2"

	m = altDigit(m, '1')
	if m.th().id != 1 {
		t.Fatalf("alt+1 landed on thread %d", m.th().id)
	}
	m = altDigit(m, '2')
	if m.th().id != 2 || m.th().input != "draft for thread 2" {
		t.Errorf("alt+2: id=%d input=%q, the draft must survive the switch", m.th().id, m.th().input)
	}
	// alt+9 with no thread 9: refused visibly, not a panic or a silent no-op.
	m = altDigit(m, '9')
	if m.th().id != 2 || m.flash == "" {
		t.Errorf("alt+9 on a missing thread: id=%d flash=%q", m.th().id, m.flash)
	}
}

func TestThreads_CapRefusesTheTenth(t *testing.T) {
	m := testCockpit()
	for i := 0; i < ckMaxThreads+2; i++ {
		m = press(m, tea.KeyCtrlT)
	}
	if len(m.threads) != ckMaxThreads {
		t.Fatalf("%d threads exist, cap is %d", len(m.threads), ckMaxThreads)
	}
	if !strings.Contains(m.flash, "thread limit") {
		t.Errorf("the cap was silent: flash=%q", m.flash)
	}
}

func TestThreads_CtrlArrowsCycleVisible(t *testing.T) {
	m := testCockpit()
	m = press(m, tea.KeyCtrlT)
	m = press(m, tea.KeyCtrlT) // threads 1,2,3, current 3
	m = press(m, tea.KeyCtrlRight)
	if m.th().id != 1 {
		t.Fatalf("ctrl+→ from 3 landed on %d, want 1 (wraps)", m.th().id)
	}
	m = press(m, tea.KeyCtrlLeft)
	if m.th().id != 3 {
		t.Fatalf("ctrl+← wrapped to %d, want 3", m.th().id)
	}
}

func TestThreads_TypingIsNeverBrokenByPlainDigitsOrLetters(t *testing.T) {
	m := testCockpit()
	m = press(m, tea.KeyCtrlT)
	m = typed(m, "add 19 tests to x.go")
	if got := m.th().input; got != "add 19 tests to x.go" {
		t.Errorf("typing into a thread mangled the input: %q", got)
	}
}

// ── status, strip, attention ─────────────────────────────────────────────────

func TestThreadStatus_GlyphsAndCounts(t *testing.T) {
	m := testCockpit()
	m = press(m, tea.KeyCtrlT)
	m = press(m, tea.KeyCtrlT)
	m = press(m, tea.KeyCtrlT)
	ts := m.threads
	ts[0].exec = &ckExecState{stage: "running", started: time.Now()}
	ts[1].planWait = &ckWait{task: ckTask{}}
	ts[2].queued = &ckQueued{reason: "queued behind 1 · both touch a.go"}
	ts[3].lastDone = &ckTask{}

	if got := ts[0].status(); got != "running" {
		t.Errorf("exec status = %q", got)
	}
	if got := ts[1].status(); got != "needs" {
		t.Errorf("planWait status = %q", got)
	}
	if got := ts[2].status(); got != "queued" {
		t.Errorf("queued status = %q", got)
	}
	if got := ts[3].status(); got != "done" {
		t.Errorf("done status = %q", got)
	}
	if got := newThread(9).status(); got != "idle" {
		t.Errorf("fresh thread status = %q", got)
	}

	running, needs, queued := m.threadCounts()
	if running != 1 || needs != 1 || queued != 1 {
		t.Errorf("counts = %d/%d/%d, want 1/1/1", running, needs, queued)
	}
	fact := m.attentionFact()
	for _, want := range []string{"1 running", "1 needs you", "1 queued"} {
		if !strings.Contains(fact, want) {
			t.Errorf("attention fact %q lacks %q", fact, want)
		}
	}
	// The chat status bar carries the tally.
	bar := stripANSI(m.statusBar())
	if !strings.Contains(bar, "needs you") {
		t.Errorf("status bar lacks the attention tally: %q", bar)
	}
}

func TestThreadStrip_ShowsChipsAndWorktreeTag(t *testing.T) {
	m := testCockpit()
	m.th().name = "first-task"
	m = press(m, tea.KeyCtrlT)
	m.th().name = "second"
	m.th().wt = &ckWorktree{tag: "t2-abc123"}

	strip := stripANSI(m.threadStrip(100))
	for _, want := range []string{"1", "first-task", "2", "second", "·wt"} {
		if !strings.Contains(strip, want) {
			t.Errorf("strip %q lacks %q", strip, want)
		}
	}
	if lipgloss.Width(strip) > 100 {
		t.Errorf("strip is %d cells wide", lipgloss.Width(strip))
	}
	// One thread → no strip line (it earns its row only with something to switch).
	solo := testCockpit()
	if solo.showStrip() {
		t.Error("a single thread grew a strip")
	}
}

func TestThreadStrip_OverflowWindowsAroundActive(t *testing.T) {
	m := testCockpit()
	for i := 0; i < 8; i++ {
		m = press(m, tea.KeyCtrlT)
		m.th().name = fmt.Sprintf("a-very-long-thread-name-%d", i+2)
	}
	m = altDigit(m, '5')
	strip := stripANSI(m.threadStrip(60))
	if lipgloss.Width(strip) > 60 {
		t.Fatalf("overflowed strip: %d cells: %q", lipgloss.Width(strip), strip)
	}
	if !strings.Contains(strip, "5") {
		t.Errorf("the active chip fell out of its own window: %q", strip)
	}
	if !strings.Contains(strip, "‹") && !strings.Contains(strip, "›") {
		t.Errorf("an overflowing strip shows no cues: %q", strip)
	}
}

func TestCtrlG_JumpsToTheNextThreadNeedingInput(t *testing.T) {
	m := testCockpit()
	m = press(m, tea.KeyCtrlT)
	m = press(m, tea.KeyCtrlT)
	m = altDigit(m, '1')
	m.threads[1].confirm = &ckWait{question: "write a.go? y/n"}
	m.threads[2].attn = true

	m = press(m, tea.KeyCtrlG)
	if m.th().id != 2 {
		t.Fatalf("ctrl+g landed on %d, want 2", m.th().id)
	}
	m = press(m, tea.KeyCtrlG)
	if m.th().id != 3 {
		t.Fatalf("second ctrl+g landed on %d, want 3", m.th().id)
	}
	if m.th().attn {
		t.Error("visiting the thread did not clear its attention mark")
	}
	m.threads[1].confirm = nil
	m = press(m, tea.KeyCtrlG)
	if !strings.Contains(m.flash, "nothing needs you") {
		t.Errorf("ctrl+g with nothing pending: flash=%q", m.flash)
	}
}

// ── split ────────────────────────────────────────────────────────────────────

func TestSplit_ToggleFocusAndNarrowRefusal(t *testing.T) {
	m := testCockpit()
	m.w = 120
	m = press(m, tea.KeyCtrlBackslash)
	if m.split {
		t.Fatal("split opened with only one thread")
	}
	m = press(m, tea.KeyCtrlT) // thread 2, current
	m = press(m, tea.KeyCtrlBackslash)
	if !m.split || m.splitID != 1 {
		t.Fatalf("split=%v pin=%d, want pin 1 (previously active)", m.split, m.splitID)
	}
	// ctrl+→ moves focus: the pinned side becomes active, the old active pins.
	m = press(m, tea.KeyCtrlRight)
	if m.th().id != 1 || !m.split || m.splitID != 2 {
		t.Fatalf("focus swap: cur=%d split=%v pin=%d", m.th().id, m.split, m.splitID)
	}
	m = press(m, tea.KeyCtrlBackslash)
	if m.split {
		t.Fatal("ctrl+\\ did not close the split")
	}

	// Too narrow: refuse with a note, render nothing broken.
	m.w = 80
	m = press(m, tea.KeyCtrlBackslash)
	if m.split || !strings.Contains(m.flash, "split needs") {
		t.Errorf("narrow split: split=%v flash=%q", m.split, m.flash)
	}

	// A live split degrades honestly when the terminal shrinks under it.
	m.w = 120
	m = press(m, tea.KeyCtrlBackslash)
	if !m.split {
		t.Fatal("split did not open at 120 cols")
	}
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(Cockpit)
	if m.split || !strings.Contains(m.flash, "split closed") {
		t.Errorf("shrink under a split: split=%v flash=%q", m.split, m.flash)
	}
}

func TestSplit_RendersBothThreadsAndScrollsIndependently(t *testing.T) {
	testutil.NewSandbox(t)
	m := testCockpit()
	m.w, m.h = 120, 40
	for i := 0; i < 60; i++ {
		m.th().log = append(m.th().log, fmt.Sprintf("one-line-%d", i))
	}
	m = press(m, tea.KeyCtrlT)
	for i := 0; i < 60; i++ {
		m.th().log = append(m.th().log, fmt.Sprintf("two-line-%d", i))
	}
	m = press(m, tea.KeyCtrlBackslash) // pin thread 1 beside thread 2

	out := stripANSI(m.View())
	if !strings.Contains(out, "two-line-59") || !strings.Contains(out, "one-line-59") {
		t.Fatalf("split does not show both live tails:\n%s", out)
	}
	if !strings.Contains(out, "watching") {
		t.Error("the pinned side is not labelled watch-only")
	}
	if !strings.Contains(out, "→ thread 2") {
		t.Error("the input bar does not label its target thread")
	}

	// Scroll the active side only; the pinned side stays live.
	m = press(m, tea.KeyPgUp)
	if m.threads[1].scroll == 0 {
		t.Fatal("pgup did not anchor the active side")
	}
	if m.threads[0].scroll != 0 {
		t.Fatal("pgup moved the pinned side too")
	}
	// Focus over, scroll there, come back: each side keeps its own position.
	m = press(m, tea.KeyCtrlLeft)
	m = press(m, tea.KeyPgUp)
	anchored := m.threads[0].scroll
	if anchored == 0 {
		t.Fatal("pgup did not anchor the other side")
	}
	m = press(m, tea.KeyCtrlRight)
	if m.threads[0].scroll != anchored {
		t.Error("switching focus lost the pinned side's scroll position")
	}
}

// Per-thread scrollback: anchored positions survive switching threads, and a
// result landing on thread B never yanks thread A's anchored reader.
func TestThreads_ScrollbackPreservedAcrossSwitches(t *testing.T) {
	testutil.NewSandbox(t)
	m := testCockpit()
	m.w, m.h = 100, 24
	for i := 0; i < 60; i++ {
		m.th().log = append(m.th().log, fmt.Sprintf("history %d", i))
	}
	m = press(m, tea.KeyPgUp)
	anchor := m.th().scroll
	if anchor == 0 {
		t.Fatal("pgup did not anchor")
	}
	m = press(m, tea.KeyCtrlT)
	m = altDigit(m, '1')
	if m.th().scroll != anchor {
		t.Fatalf("switching threads lost the scroll anchor: %d != %d", m.th().scroll, anchor)
	}

	// A worker finishing on thread 2 must not move thread 1's view.
	ex := &ckExecState{}
	m.threads[1].exec = ex
	m.threads[1].lastRunID = "r2"
	next, _ := m.Update(ckExecDoneMsg{exec: ex, task: ckTask{threadID: 2, runID: "r2", answer: strings.Repeat("thread2 answer\n", 40), elapsed: time.Second}})
	m = next.(Cockpit)
	if m.th().id != 1 || m.th().scroll != anchor {
		t.Errorf("thread 2's result yanked thread 1: id=%d scroll=%d", m.th().id, m.th().scroll)
	}
	if out := stripANSI(m.View()); strings.Contains(out, "thread2 answer") {
		t.Error("thread 2's output leaked into thread 1's pane")
	}
}

// ── message routing ──────────────────────────────────────────────────────────

// Concurrent tea.Cmd results must land on the thread that owns them, by id,
// never on whichever thread is current.
func TestExecDone_RoutesByThreadIDNotCurrent(t *testing.T) {
	testutil.NewSandbox(t)
	m := testCockpit()
	m = press(m, tea.KeyCtrlT)
	ex1, ex2 := &ckExecState{}, &ckExecState{}
	m.threads[0].exec = ex1
	m.threads[1].exec = ex2

	// Current is thread 2; thread 1's worker finishes first.
	next, _ := m.Update(ckExecDoneMsg{exec: ex1, task: ckTask{threadID: 1, runID: "r1", answer: "one done", elapsed: time.Second}})
	m = next.(Cockpit)
	if m.threads[0].exec != nil || m.threads[0].lastDone == nil {
		t.Fatal("thread 1's completion did not settle on thread 1")
	}
	if m.threads[1].exec == nil || m.threads[1].lastDone != nil {
		t.Fatal("thread 1's completion bled into thread 2")
	}
	if !strings.Contains(stripANSI(strings.Join(m.threads[0].log, "\n")), "one done") {
		t.Error("the answer landed in the wrong log")
	}
	// A completion for a thread finds it even when a stale exec pointer would
	// not match: superseded messages are dropped, not misrouted.
	stale, _ := m.Update(ckExecDoneMsg{exec: ex1, task: ckTask{threadID: 2, runID: "rX", answer: "stale"}})
	m = stale.(Cockpit)
	if m.threads[1].lastDone != nil {
		t.Error("a stale exec's completion settled anyway")
	}
}

func TestGate_OnAnotherThreadPingsChatAndArmsAttention(t *testing.T) {
	testutil.NewSandbox(t)
	m := testCockpit()
	m = press(m, tea.KeyCtrlT)
	m = altDigit(m, '1') // current: 1; the gate lands on 2
	ex := &ckExecState{}
	m.threads[1].exec = ex

	next, _ := m.Update(ckGateMsg{exec: ex, task: ckTask{threadID: 2, plan: "1. step", planSteps: 1, headName: "stub", mode: ckModeByName("plan")}, gate: 'p'})
	m = next.(Cockpit)
	if m.threads[1].planWait == nil {
		t.Fatal("the plan gate did not arm on its thread")
	}
	active := stripANSI(strings.Join(m.threads[0].log, "\n"))
	if !strings.Contains(active, "hydra ▸") || !strings.Contains(active, "thread 2") {
		t.Errorf("no ping in the active thread's chat: %q", active)
	}
	if m.threads[1].status() != "needs" {
		t.Errorf("gated thread status = %q", m.threads[1].status())
	}
}

// ── background (ctrl+b) ──────────────────────────────────────────────────────

func TestBackground_ReparentsToAgentsAndPingsOnCompletion(t *testing.T) {
	testutil.NewSandbox(t)
	m := testCockpit()
	m.th().name = "bg-task"
	ex := &ckExecState{stage: "running", started: time.Now()}
	m.th().exec = ex
	m.th().lastRunID = "20260904T100200Z-cccc" // the fixture's live run

	m = press(m, tea.KeyCtrlB)
	bg := m.threadByID(1)
	if !bg.bg {
		t.Fatal("ctrl+b did not background the thread")
	}
	if m.th().id == 1 {
		t.Fatal("the input still targets the backgrounded thread")
	}
	if m.showStrip() {
		t.Error("a lone foreground thread still renders a strip")
	}

	// ctrl+b reloads the run list from the (sandboxed, empty) run log; re-seed
	// the fixture rows the assertions join against.
	reseed := func() {
		m.runsToday = []ckRun{testRun("20260904T100200Z-cccc", "running", "write tests")}
	}
	reseed()

	// The agents view names it and enter pulls it back.
	m = m.jump(ckViewAgents)
	out := stripANSI(m.viewAgents(100, 24))
	if !strings.Contains(out, "thread 1") {
		t.Errorf("agents view does not name the backgrounded thread:\n%s", out)
	}
	// Its completion pings the active thread's chat.
	next, _ := m.Update(ckExecDoneMsg{exec: ex, task: ckTask{threadID: 1, runID: "r1", answer: "done in bg", elapsed: time.Second}})
	m = next.(Cockpit)
	ping := stripANSI(strings.Join(m.th().log, "\n"))
	if !strings.Contains(ping, "hydra ▸") || !strings.Contains(ping, "thread 1 done") {
		t.Errorf("no completion ping: %q", ping)
	}
	reseed() // settling reloads the run list too

	// Enter on its agents row re-foregrounds it.
	m = m.jump(ckViewAgents)
	rows := m.agentRows()
	for i, r := range rows {
		if r.id == "20260904T100200Z-cccc" {
			m.agentSel = i
		}
	}
	next2, _ := m.enterRow()
	m = next2.(Cockpit)
	if m.view != ckViewChat || m.th().id != 1 || m.th().bg {
		t.Errorf("enter did not foreground the thread: view=%d id=%d bg=%v", m.view, m.th().id, m.th().bg)
	}
}

// ── layout regression matrix (#598 additions) ────────────────────────────────

func TestThreadLayouts_NothingOverflowsAtAnySize(t *testing.T) {
	testutil.NewSandbox(t)
	long := strings.Repeat("wide thread content, no pane may leak past its column budget. ", 46) // ~3000 chars

	states := map[string]func(Cockpit) Cockpit{
		"four_threads_long_names": func(m Cockpit) Cockpit {
			for i := 0; i < 3; i++ {
				m = press(m, tea.KeyCtrlT)
			}
			for i, t := range m.threads {
				t.name = fmt.Sprintf("an-extremely-long-thread-name-that-must-truncate-%d", i+1)
			}
			return m
		},
		"split_both_streaming": func(m Cockpit) Cockpit {
			m = press(m, tea.KeyCtrlT)
			m.split, m.splitID = true, 1
			m.threads[0].exec = &ckExecState{stage: "verifying, go test ./...", started: time.Now()}
			m.threads[0].log = append(m.threads[0].log, strings.Split(long, ". ")...)
			m.threads[1].exec = &ckExecState{stage: "editing users.go", started: time.Now()}
			m.threads[1].log = append(m.threads[1].log, strings.Split(long, ". ")...)
			return m
		},
		"queued_chip_long_path": func(m Cockpit) Cockpit {
			m = press(m, tea.KeyCtrlT)
			m.th().queued = &ckQueued{reason: "queued behind 1 · both touch internal/very/deeply/nested/path/that/never/ends/handlers/users_endpoint_pagination.go"}
			m.th().name = "queued-one"
			return m
		},
		"huge_task_while_other_streams": func(m Cockpit) Cockpit {
			m = press(m, tea.KeyCtrlT)
			m.threads[0].exec = &ckExecState{stage: "streaming", started: time.Now()}
			m.threads[0].log = append(m.threads[0].log, "streamed line")
			m.th().input = long
			return m
		},
		"worktree_header_and_strip": func(m Cockpit) Cockpit {
			m = press(m, tea.KeyCtrlT)
			m.th().wt = &ckWorktree{tag: "t2-abc123", branch: "hydra/task-t2-abc123"}
			m.th().log = append(m.th().log, "an edit landed")
			return m
		},
	}

	sizes := []struct{ w, h int }{{60, 15}, {80, 24}, {100, 30}, {120, 40}}
	for name, build := range states {
		for _, sz := range sizes {
			t.Run(fmt.Sprintf("%s_%dx%d", name, sz.w, sz.h), func(t *testing.T) {
				m := build(testCockpit())
				m.w, m.h, m.ready = sz.w, sz.h, true
				out := m.View()
				lines := strings.Split(out, "\n")
				if len(lines) > sz.h {
					t.Errorf("%s at %dx%d renders %d lines, want <= %d", name, sz.w, sz.h, len(lines), sz.h)
				}
				for i, l := range lines {
					if got := lipgloss.Width(l); got > sz.w {
						t.Errorf("%s at %dx%d: line %d is %d cells wide", name, sz.w, sz.h, i, got)
					}
				}
				last := stripANSI(lines[len(lines)-1])
				if !strings.Contains(last, "shortcuts") {
					t.Errorf("%s at %dx%d: the status bar is not the final line: %q", name, sz.w, sz.h, last)
				}
			})
		}
	}
}

// A streamed REAL file (tab-indented, lines that fill the budget) must occupy
// exactly one panel row each. Found live: the panel's text budget ignored its
// own padding, so every full line wrapped in two and half the panel was lost,
// disclosed by ckFrame as "… N more lines", but still half a pane of content.
func TestCodePanel_RealFileLinesDoNotWrap(t *testing.T) {
	var lines []string
	for i := 0; i < 50; i++ {
		lines = append(lines, fmt.Sprintf("\tif err := doSomethingUseful(ctx, %d); err != nil {", i))
	}
	for _, sz := range []struct{ w, h int }{{60, 15}, {80, 24}, {100, 30}, {120, 40}} {
		m := testCockpit()
		m.w, m.h, m.ready = sz.w, sz.h, true
		m = m.addThread() // a strip exists, as it does in a real parallel session
		th := m.th()
		th.codeLang, th.codeLines, th.codeShown = "go", lines, len(lines)

		bodyH := sz.h - 3
		if m.showStrip() {
			bodyH--
		}
		for _, pw := range []int{22, 29, 40} {
			panel := m.codePanel(th, pw, bodyH)
			if got := lipgloss.Height(panel); got > bodyH {
				t.Errorf("%dx%d panel w=%d: height %d > %d, a line wrapped",
					sz.w, sz.h, pw, got, bodyH)
			}
		}
		if out := m.View(); strings.Contains(stripANSI(out), "more lines, enlarge") {
			t.Errorf("%dx%d: the frame had to clamp a streamed file", sz.w, sz.h)
		}
	}
}

// The glossary documents the THREADS group and every new key.
func TestGlossary_DocumentsThreadKeys(t *testing.T) {
	lines := stripANSI(strings.Join(ckGlossaryLines(), "\n"))
	for _, want := range []string{"THREADS", "ctrl+t", "alt+1-9", "ctrl+←/→", "ctrl+\\", "ctrl+b", "ctrl+g"} {
		if !strings.Contains(lines, want) {
			t.Errorf("glossary lacks %q", want)
		}
	}
}
