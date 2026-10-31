// SPDX-License-Identifier: MIT

package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// shift+tab cycles the basic modes in order and never lands on an advanced one.
func TestModes_ShiftTabCyclesBasicsOnly(t *testing.T) {
	m := testCockpit()
	m.mode = "ask"
	want := []string{"edit", "plan", "auto", "ask"}
	for _, w := range want {
		m = press(m, tea.KeyShiftTab)
		if m.mode != w {
			t.Fatalf("shift+tab left mode %q, want %q", m.mode, w)
		}
	}
	// From an advanced mode the cycle returns to the first basic.
	m.mode = "careful"
	m = press(m, tea.KeyShiftTab)
	if m.mode != "ask" {
		t.Errorf("shift+tab from careful = %q, want ask", m.mode)
	}
}

// `m` on an empty input opens the picker; with text it types. The picker
// selects with j/k+enter, closes on esc without changing the mode, and reaches
// the advanced modes shift+tab never offers.
func TestModes_PickerOpensSelectsAndCloses(t *testing.T) {
	m := testCockpit()
	m = typed(m, "m")
	if !m.modePick {
		t.Fatal("m on an empty input did not open the picker")
	}
	if ckModes[m.modeSel].name != "auto" {
		t.Errorf("the picker did not open on the current mode: %s", ckModes[m.modeSel].name)
	}
	m = press(m, tea.KeyEsc)
	if m.modePick || m.mode != "auto" {
		t.Fatal("esc did not close the picker unchanged")
	}

	m = typed(m, "m") // reopen
	for i := 0; i < len(ckModes); i++ {
		m = typed(m, "j") // clamp at the end
	}
	if m.modeSel != len(ckModes)-1 {
		t.Fatalf("j ran off the end: sel=%d", m.modeSel)
	}
	m, _ = enter(m)
	if m.mode != "unattended" || m.modePick {
		t.Errorf("enter selected %q, want unattended with the picker closed", m.mode)
	}

	// With text in the input, m types.
	m = typed(m, "make a helper for ")
	m = typed(m, "m")
	if m.modePick {
		t.Fatal("m typed mid-sentence opened the picker")
	}
	if m.th().input != "make a helper for m" {
		t.Errorf("input = %q, typing a word starting with m must pass through the picker intact", m.th().input)
	}
	if m.mode != "unattended" {
		t.Errorf("typing through the picker changed the mode to %q", m.mode)
	}
}

// Real terminals coalesce fast keystrokes into one multi-rune message; the
// handlers see them replayed one key at a time, found live when "jj" arrived
// as a single message and every modal dropped it (#597).
func TestKeys_CoalescedRunesReplayIndividually(t *testing.T) {
	m := testCockpit()
	m = press(m, tea.KeyCtrlO)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("jj")})
	m = next.(Cockpit)
	if m.ovSel != 2 {
		t.Fatalf("a coalesced jj moved the selection to %d, want 2", m.ovSel)
	}
	m, _ = enter(m)
	if m.override.kind != 'L' {
		t.Fatalf("the coalesced navigation did not land on local only: %+v", m.override)
	}
	// Typing coalesced into the input still types everything.
	chat := testCockpit()
	next, _ = chat.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("fix the bug")})
	if got := next.(Cockpit).th().input; got != "fix the bug" {
		t.Errorf("coalesced typing lost characters: %q", got)
	}
	// A bracketed paste is literal input, never a run of shortcuts, even when
	// it starts with a command letter on an empty input.
	next, _ = testCockpit().Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("make it so"), Paste: true})
	pasted := next.(Cockpit)
	if pasted.modePick || pasted.th().input != "make it so" {
		t.Errorf("paste triggered commands: pick=%v input=%q", pasted.modePick, pasted.th().input)
	}
}

// The picker is forgiving, not modal: a non-navigation rune closes it and
// types on, the opening m included, so "make…" is never eaten. Backspace
// un-types the m; space keeps it.
func TestModes_PickerForgivesTyping(t *testing.T) {
	m := typed(testCockpit(), "m")
	if !m.modePick {
		t.Fatal("picker did not open")
	}
	m = press(m, tea.KeyBackspace)
	if m.modePick || m.th().input != "" {
		t.Fatalf("backspace did not un-type the m: pick=%v input=%q", m.modePick, m.th().input)
	}
	m = typed(m, "m")
	m = press(m, tea.KeySpace)
	if m.modePick || m.th().input != "m " {
		t.Fatalf("space did not type through: pick=%v input=%q", m.modePick, m.th().input)
	}
}

// The picker documents every mode; the overlay respects the frame at the
// mandated sizes.
func TestModes_PickerRendersEveryMode(t *testing.T) {
	lines := stripANSI(strings.Join(ckModePickerLines(0), "\n"))
	for _, d := range ckModes {
		if !strings.Contains(lines, d.chip) {
			t.Errorf("the picker does not list %s", d.chip)
		}
	}
	if !strings.Contains(lines, "ADVANCED") {
		t.Error("the advanced group is not labelled")
	}
	// $0.50 cap is unattended's safety story, it must be visible up front.
	if !strings.Contains(lines, "$0.50") {
		t.Error("unattended's cost cap is not disclosed in the picker")
	}
}

func TestModes_LookupAndCommands(t *testing.T) {
	if got := ckModeByName("nonsense").name; got != "auto" {
		t.Errorf("unknown mode resolved to %q, want auto", got)
	}
	for _, name := range []string{"ask", "edit", "plan", "auto", "architect", "careful", "unattended"} {
		if !ckIsMode(name) {
			t.Errorf("ckIsMode(%q) = false", name)
		}
	}
	if ckIsMode("swarm") {
		t.Error("the phase-1 strategy names must not be modes")
	}
	// The advanced modes carry their distinguishing knobs.
	if d := ckModeByName("architect"); d.planTier != "2" || !d.cheapImpl {
		t.Errorf("architect knobs wrong: %+v", d)
	}
	if d := ckModeByName("careful"); !d.confirm || !d.verify {
		t.Errorf("careful knobs wrong: %+v", d)
	}
	if d := ckModeByName("unattended"); d.capUSD != ckUnattendedCapUSD || d.confirm {
		t.Errorf("unattended knobs wrong: %+v", d)
	}
	if d := ckModeByName("ask"); d.plan || d.verify || d.confirm {
		t.Errorf("ask must have no pipeline knobs: %+v", d)
	}
}
