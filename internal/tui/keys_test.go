// SPDX-License-Identifier: MIT

package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ankit373/hydra/internal/cost"
)

// The glossary renders FROM ckKeymap, so every documented binding appears and
// nothing can drift between the keys and their docs.
func TestGlossary_RendersEveryKeymapEntry(t *testing.T) {
	lines := stripANSI(strings.Join(ckGlossaryLines(), "\n"))
	for _, b := range ckKeymap {
		if b.group == "" {
			continue // bar-only refinement of a generic glossary row
		}
		if !strings.Contains(lines, b.keys) {
			t.Errorf("the glossary does not document %q", b.keys)
		}
	}
	for _, group := range ckGlossaryGroups {
		if !strings.Contains(lines, group) {
			t.Errorf("the glossary is missing the %s group", group)
		}
	}
	// Phase-1 honesty: keys that do not exist yet must not be documented.
	for _, ghost := range []string{"ctrl+r", "F1"} {
		if strings.Contains(lines, ghost) {
			t.Errorf("the glossary documents %q, which is not bound", ghost)
		}
	}
}

// Every view's status bar shows its table-declared keys plus "? shortcuts".
func TestStatusBar_KeysComeFromTheTable(t *testing.T) {
	m := testCockpit()
	for v := 0; v < ckViewCount(); v++ {
		m.view = v
		bar := stripANSI(m.statusBar())
		if !strings.Contains(bar, "shortcuts") {
			t.Errorf("view %d status bar lacks the ? shortcuts hint: %q", v, bar)
		}
		for _, b := range ckBarBindings(v) {
			if !strings.Contains(bar, b.does) {
				t.Errorf("view %d status bar lacks %q (%s): %q", v, b.does, b.keys, bar)
			}
		}
	}
}

// `?` opens the glossary everywhere; in chat only when the input is empty, so
// typing a task containing '?' still works (no existing binding overridden).
func TestGlossary_QuestionMarkSemantics(t *testing.T) {
	m := testCockpit()

	m.view = ckViewModels
	m = typed(m, "?")
	if !m.glossary {
		t.Fatal("? on a non-chat view did not open the glossary")
	}
	m = typed(m, "?")
	if m.glossary {
		t.Fatal("? did not toggle the glossary closed")
	}

	chat := testCockpit()
	chat.view = ckViewChat
	chat = typed(chat, "?")
	if !chat.glossary {
		t.Fatal("? on an empty chat input did not open the glossary")
	}
	chat = press(chat, tea.KeyEsc)
	if chat.glossary {
		t.Fatal("esc did not close the glossary")
	}

	chat = typed(chat, "what is 2")
	chat = typed(chat, "?")
	if chat.glossary {
		t.Error("? mid-sentence opened the glossary instead of typing")
	}
	if chat.input != "what is 2?" {
		t.Errorf("input = %q, want the ? appended", chat.input)
	}
}

// Digits jump between views everywhere except chat, where they type.
func TestDigits_JumpOutsideChatTypeInsideChat(t *testing.T) {
	m := testCockpit()
	m.view = ckViewUsage
	for digit, want := range map[string]int{"1": 0, "2": 1, "3": 2, "4": 3, "5": 4, "6": 5} {
		m.view = ckViewUsage
		m = typed(m, digit)
		if m.view != want {
			t.Errorf("digit %s jumped to view %d, want %d", digit, m.view, want)
		}
		m.audit = nil // keep the audit jump from re-reading files repeatedly
	}

	chat := testCockpit()
	chat.view = ckViewChat
	chat = typed(chat, "add 2 retries")
	if chat.view != ckViewChat {
		t.Fatal("typing a digit in chat switched views")
	}
	if chat.input != "add 2 retries" {
		t.Errorf("input = %q", chat.input)
	}
}

// Text editing must only affect the chat view — typing on other views is
// shortcuts, never invisible input edits (#506).
func TestInput_IsScopedToTheChatView(t *testing.T) {
	m := testCockpit()
	m.view = ckViewChat
	m = typed(m, "half a prompt")

	m.view = ckViewUsage
	m = typed(m, "x")
	m = press(m, tea.KeyBackspace)
	if m.input != "half a prompt" {
		t.Errorf("input on a non-chat view = %q, want untouched", m.input)
	}
	before := len(m.log)
	m, cmd := enter(m)
	if len(m.log) != before || cmd != nil {
		t.Error("enter on a non-chat view ran a chat submit")
	}

	m.view = ckViewChat
	m, cmd = enter(m)
	if len(m.log) <= before {
		t.Error("enter on chat did not submit the preserved input")
	}
	if cmd == nil {
		t.Error("submitting did not schedule the code stream")
	}
}

// esc means: close overlay first, then back (drill/detail), then clear input.
func TestEsc_Semantics(t *testing.T) {
	m := testCockpit()
	m.view = ckViewActivity
	m.actDrill = true
	m.glossary = true
	m = press(m, tea.KeyEsc)
	if m.glossary {
		t.Fatal("esc did not close the overlay first")
	}
	if !m.actDrill {
		t.Fatal("esc closed the drill together with the overlay")
	}
	m = press(m, tea.KeyEsc)
	if m.actDrill {
		t.Fatal("esc did not leave the drill")
	}

	mm := testCockpit()
	mm.view = ckViewModels
	mm.modelFocus = true
	mm = press(mm, tea.KeyEsc)
	if mm.modelFocus {
		t.Error("esc did not leave the model detail focus")
	}

	chat := testCockpit()
	chat.view = ckViewChat
	chat = typed(chat, "abc")
	chat = press(chat, tea.KeyEsc)
	if chat.input != "" {
		t.Errorf("esc did not clear the input: %q", chat.input)
	}
}

// j/k and arrows move the selection, clamped at both ends, and the rendered
// window always contains the selection.
func TestListSelection_MovesClampedAndStaysVisible(t *testing.T) {
	m := testCockpit()
	var many []ckRun
	for i := 0; i < 40; i++ {
		many = append(many, testRun(strings.Repeat("f", 4)+string(rune('a'+i%26)), "ok", strings.Repeat("task ", 3)))
	}
	many[35].task = "THE-SELECTED-ONE"
	m.runsToday = many
	m.view = ckViewActivity

	for i := 0; i < 100; i++ {
		m = typed(m, "j")
	}
	if m.actSel != len(many)-1 {
		t.Errorf("actSel = %d after running off the bottom, want %d", m.actSel, len(many)-1)
	}
	for i := 0; i < 200; i++ {
		m = typed(m, "k")
	}
	if m.actSel != 0 {
		t.Errorf("actSel = %d after running off the top", m.actSel)
	}

	// Selection 35 in a 20-row pane must be rendered (window follows).
	m.actSel = 35
	m.w, m.h, m.ready = 120, 24, true
	if out := stripANSI(m.View()); !strings.Contains(out, "THE-SELECTED-O") {
		t.Errorf("the selected row is not visible:\n%s", out)
	}

	// g/G jump to the ends.
	m = typed(m, "g")
	if m.actSel != 0 {
		t.Errorf("g left actSel = %d", m.actSel)
	}
	m = typed(m, "G")
	if m.actSel != len(many)-1 {
		t.Errorf("G left actSel = %d", m.actSel)
	}
}

// Page keys page the selection in list views and the offsets elsewhere.
func TestPageKeys_MoveSelectionAndOffsets(t *testing.T) {
	m := testCockpit()
	var many []ckRun
	for i := 0; i < 60; i++ {
		many = append(many, testRun("run", "ok", "t"))
	}
	m.runsToday = many
	m.view = ckViewActivity
	m.w, m.h, m.ready = 100, 24, true

	m = press(m, tea.KeyPgDown)
	if m.actSel == 0 {
		t.Error("pgdn did not page the selection")
	}
	down := m.actSel
	m = press(m, tea.KeyCtrlU)
	if m.actSel >= down {
		t.Error("ctrl+u did not page the selection back up")
	}
	m = press(m, tea.KeyEnd)
	if m.actSel != len(many)-1 {
		t.Errorf("end left actSel = %d", m.actSel)
	}
	m = press(m, tea.KeyHome)
	if m.actSel != 0 {
		t.Errorf("home left actSel = %d", m.actSel)
	}

	// In the usage view, page keys scroll the breakdown offset.
	u := testCockpit()
	u.view = ckViewUsage
	u.w, u.h, u.ready = 100, 24, true
	u.metrics.byModelToday = manyGroupRows(30)
	u = press(u, tea.KeyPgDown)
	if u.usageOff == 0 {
		t.Error("pgdn did not scroll the usage breakdown")
	}
	u = press(u, tea.KeyHome)
	if u.usageOff != 0 {
		t.Errorf("home left usageOff = %d", u.usageOff)
	}

	// The wheel scrolls too.
	w, _ := u.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	if w.(Cockpit).usageOff == 0 {
		t.Error("the wheel did not scroll the usage breakdown")
	}
}

// Chat scrollback: pgup anchors the window; new output must not yank it; end
// returns to live; sending your own message returns to live.
func TestChatScrollback_NoYankAndEndReturnsToLive(t *testing.T) {
	m := testCockpit()
	m.view = ckViewChat
	m.w, m.h, m.ready = 100, 24, true
	for i := 0; i < 80; i++ {
		m.log = append(m.log, strings.Repeat("history ", 2)+"line")
	}

	m = press(m, tea.KeyPgUp)
	if m.chatScroll == 0 {
		t.Fatal("pgup did not enter scrollback")
	}
	anchored := m.chatScroll
	m.log = append(m.log, "NEW OUTPUT")
	if m.chatScroll != anchored {
		t.Error("an append moved the scrollback anchor")
	}
	if out := stripANSI(m.View()); strings.Contains(out, "NEW OUTPUT") {
		t.Errorf("scrolled-up chat was yanked to the new output:\n%s", out)
	} else if !strings.Contains(out, "below · end → live") {
		t.Errorf("no new-output cue while scrolled up:\n%s", out)
	}

	m = press(m, tea.KeyEnd)
	if m.chatScroll != 0 {
		t.Error("end did not return to live")
	}
	if out := stripANSI(m.View()); !strings.Contains(out, "NEW OUTPUT") {
		t.Errorf("live view does not show the newest output:\n%s", out)
	}

	// Scroll up, then send a message: standard chat returns to live.
	m = press(m, tea.KeyPgUp)
	m = typed(m, "a new task")
	m, _ = enter(m)
	if m.chatScroll != 0 {
		t.Error("sending a message did not return the view to live")
	}
}

// Wheel scrolling in chat moves the log, not the input.
func TestChatWheel_ScrollsLog(t *testing.T) {
	m := testCockpit()
	m.view = ckViewChat
	m.w, m.h, m.ready = 100, 24, true
	for i := 0; i < 80; i++ {
		m.log = append(m.log, "line")
	}
	next, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	if next.(Cockpit).chatScroll == 0 {
		t.Error("wheel up did not scroll the chat log")
	}
	if next.(Cockpit).input != "" {
		t.Error("the wheel touched the input")
	}
}

// The glossary scrolls at short terminals and swallows every other key.
func TestGlossary_ScrollsAndSwallowsKeys(t *testing.T) {
	m := testCockpit()
	m.view = ckViewUsage
	m.glossary = true
	m.w, m.h, m.ready = 80, 12, true

	m = typed(m, "j")
	if m.glossOff == 0 {
		t.Error("j did not scroll the glossary")
	}
	out := stripANSI(m.View())
	if !strings.Contains(out, "↑") {
		t.Errorf("a scrolled glossary shows no position cue:\n%s", out)
	}
	// Other keys must not leak into the view underneath.
	before := m.usageGroup
	m = typed(m, "t")
	if m.usageGroup != before {
		t.Error("a key leaked through the glossary to the view below")
	}
	// tab still switches views (and closes the overlay).
	m = press(m, tea.KeyTab)
	if m.glossary {
		t.Error("tab did not close the glossary")
	}
}

// Ctrl+C quits from anywhere.
func TestCtrlC_Quits(t *testing.T) {
	for _, v := range []int{ckViewChat, ckViewModels, ckViewAudit} {
		m := testCockpit()
		m.view = v
		if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC}); cmd == nil {
			t.Errorf("ctrl+c on view %d did not quit", v)
		}
	}
}

// Enter on an agents row jumps to that run's trace in activity.
func TestAgentsEnter_JumpsToTrace(t *testing.T) {
	m := testCockpit()
	m.view = ckViewAgents
	m.agentSel = 0 // agentRows puts the live run first
	live := m.agentRows()[0]
	if !live.live {
		t.Fatal("fixture: first agent row is not the live run")
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Cockpit)
	if m.view != ckViewActivity {
		t.Fatalf("enter left view = %d, want activity", m.view)
	}
	if !m.actDrill {
		t.Error("the trace is not drilled in")
	}
	if got := m.activityRuns()[m.actSel].id; got != live.id {
		t.Errorf("selected run = %s, want %s", got, live.id)
	}
}

func manyGroupRows(n int) []cost.GroupRow {
	out := make([]cost.GroupRow, n)
	for i := range out {
		out[i] = cost.GroupRow{Key: "model-" + string(rune('a'+i%26)), Calls: i + 1, EstCostUSD: float64(i) * 0.001}
	}
	return out
}
