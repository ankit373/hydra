// SPDX-License-Identifier: MIT

package tui

// modes.go — the chat modes (what a task DOES: ask/edit/plan/auto + advanced)
// and the `m` picker overlay. Where a task RUNS is the ctrl+o override
// (override.go); the two are deliberately separate axes.

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ckUnattendedCapUSD is unattended mode's hard per-task cost cap. Refusal to
// continue past it is the mode's whole safety story, so it is a visible
// constant, not a config knob buried somewhere.
const ckUnattendedCapUSD = 0.50

// ckModeDef is one chat mode: its pipeline knobs plus the picker copy.
type ckModeDef struct {
	name      string // key: "auto"
	chip      string // input-bar chip: "Auto"
	desc      string
	basic     bool    // shift+tab cycles basics only
	plan      bool    // run a plan stage first
	verify    bool    // run the oracle verify loop after an edit
	confirm   bool    // pause y/n before a file write lands
	planTier  string  // "" = cheap default (ckPlanTierDefault)
	cheapImpl bool    // implement on the cheap tier regardless of classification
	capUSD    float64 // hard per-task cost cap; 0 = none
}

// ckPlanTierDefault is where plan drafts route: cheap, per the design — a plan
// is a numbered list, not the implementation.
const ckPlanTierDefault = "8"

var ckModes = []ckModeDef{
	{name: "ask", chip: "Ask", basic: true,
		desc: "answer only — never writes files"},
	{name: "edit", chip: "Edit", basic: true,
		desc: "direct change when the prompt names an existing file — no plan step"},
	// plan carries verify: an approved plan proceeds as auto (edit → verify).
	{name: "plan", chip: "Plan", basic: true, plan: true, verify: true,
		desc: "draft numbered steps and wait — enter/y runs them, esc discards"},
	{name: "auto", chip: "Auto", basic: true, plan: true, verify: true,
		desc: "plan → edit → verify with the workspace tests, fixing failures (max 2 tries)"},
	{name: "architect", chip: "Architect", plan: true, verify: true, planTier: "2", cheapImpl: true,
		desc: "plan on a strong tier, implement on a cheap one"},
	{name: "careful", chip: "Careful", plan: true, verify: true, confirm: true,
		desc: "auto, but every file write needs a y/n confirm first"},
	{name: "unattended", chip: "Unattended", plan: true, verify: true, capUSD: ckUnattendedCapUSD,
		desc: "auto with no confirms — hard $0.50 cost cap per task, stops visibly at it"},
}

// ckModeDef looks a mode up by name; an unknown name reads as auto so a stale
// saved mode can never wedge the chat.
func ckModeByName(name string) ckModeDef {
	for _, d := range ckModes {
		if d.name == name {
			return d
		}
	}
	return ckModeByName("auto")
}

// ckNextBasicMode is the shift+tab cycle: ask → edit → plan → auto → ask.
// From an advanced mode it returns to the first basic.
func ckNextBasicMode(cur string) string {
	var basics []string
	for _, d := range ckModes {
		if d.basic {
			basics = append(basics, d.name)
		}
	}
	for i, n := range basics {
		if n == cur {
			return basics[(i+1)%len(basics)]
		}
	}
	return basics[0]
}

// ckIsMode reports whether name is a defined mode (for the /mode commands).
func ckIsMode(name string) bool {
	for _, d := range ckModes {
		if d.name == name {
			return true
		}
	}
	return false
}

// ── picker overlay ────────────────────────────────────────────────────────────

// modePickerKey is forgiving, not modal: the picker opened on a bare 'm', so
// typing a word that starts with m ("make a helper…") closes it and keeps
// typing, the m included. Only j/k/arrows/enter/esc/m are picker keys.
func (m Cockpit) modePickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.modePick = false
		return m, nil
	case tea.KeyBackspace:
		m.modePick = false // un-types the m that opened it
		return m, nil
	case tea.KeySpace:
		m.modePick = false
		m.input = "m "
		return m, nil
	case tea.KeyUp:
		return m.moveModeSel(-1), nil
	case tea.KeyDown:
		return m.moveModeSel(1), nil
	case tea.KeyEnter:
		m.mode = ckModes[m.modeSel].name
		m.modePick = false
		m.flash = "mode → " + m.mode
		return m, nil
	case tea.KeyRunes:
		if len(msg.Runes) == 1 {
			switch msg.Runes[0] {
			case 'j':
				return m.moveModeSel(1), nil
			case 'k':
				return m.moveModeSel(-1), nil
			case 'm':
				m.modePick = false
				return m, nil
			}
		}
		// Anything else — a typed letter or a paste — falls through to input.
		m.modePick = false
		m.input = "m" + string(msg.Runes)
	}
	return m, nil
}

func (m Cockpit) moveModeSel(delta int) Cockpit {
	m.modeSel += delta
	if m.modeSel < 0 {
		m.modeSel = 0
	}
	if m.modeSel >= len(ckModes) {
		m.modeSel = len(ckModes) - 1
	}
	return m
}

// openModePicker opens the overlay with the current mode selected.
func (m Cockpit) openModePicker() Cockpit {
	m.modePick = true
	m.modeSel = 0
	for i, d := range ckModes {
		if d.name == m.mode {
			m.modeSel = i
		}
	}
	return m
}

// ckModePickerLines renders the picker rows; sel marks the highlighted mode.
func ckModePickerLines(sel int) []string {
	lines := []string{
		ckLabelS.Render("MODE — what the task does"),
		ckFaintS.Render("shift+tab cycles the basics · where it runs is ctrl+o"),
	}
	lastBasic := true
	for i, d := range ckModes {
		if lastBasic && !d.basic {
			lines = append(lines, "", ckCyanS.Render("ADVANCED"))
			lastBasic = false
		}
		marker := "  "
		name := ckDimS.Render(ckCell(d.chip, 11))
		if i == sel {
			marker = ckAquaS.Render("▸ ")
			name = ckInkS.Bold(true).Render(ckCell(d.chip, 11))
		}
		lines = append(lines, marker+name+ckDimS.Render(d.desc))
	}
	return lines
}

// viewModePicker renders the picker as a centred overlay, degrading to a
// selection-following scroll list on short terminals (same rule as the
// glossary).
func (m Cockpit) viewModePicker(w, h int) string {
	lines := ckModePickerLines(m.modeSel)
	box := ckBoxS.BorderForeground(ckAqua).Render(strings.Join(lines, "\n"))
	if lipgloss.Width(box) <= w && lipgloss.Height(box) <= h {
		return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, box)
	}
	return strings.Join(ckSelScroll(lines, m.modeSel+2, h), "\n")
}
