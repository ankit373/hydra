// SPDX-License-Identifier: MIT

package tui

// override.go, the ctrl+o routing override modal: where the NEXT task runs
// (auto / force tier / local only / best of 3 / consensus check). The mode
// says what a task does; this says where it runs, one task only, then reset.

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ckOverride is the routing override applied to the next task.
type ckOverride struct {
	kind byte    // 0 auto · 'T' force tier · 'L' local only · 'B' best of 3 · 'C' consensus
	tier int     // for 'T'
	conf float64 // for 'C', in (0,1)
}

// strategy is the route line's strategy word for this override.
func (o ckOverride) strategy() string {
	switch o.kind {
	case 'B':
		return "best of 3"
	case 'C':
		return fmt.Sprintf("consensus ≥%s", ckPct(o.conf))
	default:
		return "single"
	}
}

// label names the override for the status bar; auto reads "route auto".
func (o ckOverride) label() string {
	switch o.kind {
	case 'T':
		return fmt.Sprintf("route T%d forced", o.tier)
	case 'L':
		return "route local only"
	case 'B':
		return "route best of 3"
	case 'C':
		return fmt.Sprintf("route consensus ≥%s", ckPct(o.conf))
	default:
		return "route auto"
	}
}

// ckPct renders a confidence target without fabricating precision: 0.999
// reads "99.9%", 0.95 reads "95%".
func ckPct(c float64) string {
	s := fmt.Sprintf("%.1f", c*100)
	return strings.TrimSuffix(s, ".0") + "%"
}

// ckOvRow is one modal option.
type ckOvRow struct {
	name, desc string
	kind       byte
	sub        byte // non-zero: selecting it opens this sub-stage first
}

var ckOvRows = []ckOvRow{
	{"auto (recommended)", "the router decides, classification picks the tier", 0, 0},
	{"force tier…", "pin the next task to a tier, 1 strongest, 10 cheapest", 'T', 'T'},
	{"local only", "nothing leaves this machine", 'L', 0},
	{"best of 3", "three heads answer, a judge picks the best", 'B', 0},
	{"consensus check…", "sample heads until they agree at a target confidence", 'C', 'C'},
}

// ckOvConfChoices are the consensus targets on offer (the design's 90-99.9%).
var ckOvConfChoices = []float64{0.90, 0.95, 0.99, 0.999}

// overrideKey handles keys while the ctrl+o modal is open.
func (m Cockpit) overrideKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.ovStage {
	case 'T':
		return m.overrideTierKey(msg)
	case 'C':
		return m.overrideConfKey(msg)
	}
	switch msg.Type {
	case tea.KeyEsc:
		m.ovOpen = false
		return m, nil
	case tea.KeyUp:
		return m.moveOvSel(-1), nil
	case tea.KeyDown:
		return m.moveOvSel(1), nil
	case tea.KeyEnter:
		row := ckOvRows[m.ovSel]
		if row.sub != 0 {
			m.ovStage = row.sub
			return m, nil
		}
		m.override = ckOverride{kind: row.kind}
		m.ovOpen = false
		m.flash = m.override.label() + ", next task"
		return m, nil
	case tea.KeyRunes:
		if len(msg.Runes) != 1 {
			return m, nil
		}
		switch msg.Runes[0] {
		case 'j':
			return m.moveOvSel(1), nil
		case 'k':
			return m.moveOvSel(-1), nil
		case 'q':
			m.ovOpen = false
		}
	}
	return m, nil
}

// overrideTierKey reads one digit: 1-9 → T1-T9, 0 → T10.
func (m Cockpit) overrideTierKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.ovStage = 0
		return m, nil
	case tea.KeyRunes:
		if len(msg.Runes) != 1 {
			return m, nil
		}
		r := msg.Runes[0]
		if r < '0' || r > '9' {
			return m, nil
		}
		tier := int(r - '0')
		if tier == 0 {
			tier = 10
		}
		m.override = ckOverride{kind: 'T', tier: tier}
		m.ovOpen, m.ovStage = false, 0
		m.flash = m.override.label() + ", next task"
	}
	return m, nil
}

// overrideConfKey picks a consensus target: j/k + enter, or digits 1-4.
func (m Cockpit) overrideConfKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	apply := func(i int) (tea.Model, tea.Cmd) {
		m.override = ckOverride{kind: 'C', conf: ckOvConfChoices[i]}
		m.ovOpen, m.ovStage, m.ovConfSel = false, 0, 0
		m.flash = m.override.label() + ", next task"
		return m, nil
	}
	switch msg.Type {
	case tea.KeyEsc:
		m.ovStage, m.ovConfSel = 0, 0
		return m, nil
	case tea.KeyUp:
		m.ovConfSel = ckClampOff(m.ovConfSel-1, len(ckOvConfChoices)-1)
		return m, nil
	case tea.KeyDown:
		m.ovConfSel = ckClampOff(m.ovConfSel+1, len(ckOvConfChoices)-1)
		return m, nil
	case tea.KeyEnter:
		return apply(m.ovConfSel)
	case tea.KeyRunes:
		if len(msg.Runes) != 1 {
			return m, nil
		}
		switch r := msg.Runes[0]; {
		case r == 'j':
			m.ovConfSel = ckClampOff(m.ovConfSel+1, len(ckOvConfChoices)-1)
		case r == 'k':
			m.ovConfSel = ckClampOff(m.ovConfSel-1, len(ckOvConfChoices)-1)
		case r >= '1' && r <= '4':
			return apply(int(r - '1'))
		}
	}
	return m, nil
}

func (m Cockpit) moveOvSel(delta int) Cockpit {
	m.ovSel += delta
	if m.ovSel < 0 {
		m.ovSel = 0
	}
	if m.ovSel >= len(ckOvRows) {
		m.ovSel = len(ckOvRows) - 1
	}
	return m
}

// ckOverrideLines renders the modal rows for the current stage.
func (m Cockpit) ckOverrideLines() []string {
	lines := []string{
		ckLabelS.Render("ROUTING OVERRIDE, next task only"),
		ckFaintS.Render("the mode says what to do; this says where it runs"),
	}
	switch m.ovStage {
	case 'T':
		lines = append(lines, "",
			ckInkS.Render(" force tier"),
			ckDimS.Render(" press 1-9 for T1-T9, 0 for T10 · esc back"))
	case 'C':
		lines = append(lines, "", ckInkS.Render(" consensus target"))
		for i, c := range ckOvConfChoices {
			marker := "  "
			style := ckDimS
			if i == m.ovConfSel {
				marker = ckAquaS.Render("▸ ")
				style = ckInkS
			}
			lines = append(lines, marker+style.Render(fmt.Sprintf("%d. ≥%s", i+1, ckPct(c))))
		}
		lines = append(lines, ckFaintS.Render(" enter picks · esc back"))
	default:
		lines = append(lines, "")
		for i, row := range ckOvRows {
			marker := "  "
			name := ckDimS.Render(ckCell(row.name, 20))
			if i == m.ovSel {
				marker = ckAquaS.Render("▸ ")
				name = ckInkS.Bold(true).Render(ckCell(row.name, 20))
			}
			lines = append(lines, marker+name+ckFaintS.Render(row.desc))
		}
	}
	return lines
}

// viewOverride renders the modal centred, or as a scroll list on short
// terminals, the same degradation rule as every other overlay.
func (m Cockpit) viewOverride(w, h int) string {
	lines := m.ckOverrideLines()
	box := ckBoxS.BorderForeground(ckViolet).Render(strings.Join(lines, "\n"))
	if lipgloss.Width(box) <= w && lipgloss.Height(box) <= h {
		return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, box)
	}
	sel := m.ovSel + 3
	if m.ovStage != 0 {
		sel = 0
	}
	return strings.Join(ckSelScroll(lines, sel, h), "\n")
}
