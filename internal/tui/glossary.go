// SPDX-License-Identifier: MIT

package tui

// glossary.go — the `?` shortcut overlay. Rendered FROM ckKeymap (keys.go),
// grouped EVERYWHERE / CHAT / LISTS, so it can only ever document keys that
// are actually declared.

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var ckGlossaryGroups = []string{"EVERYWHERE", "THREADS", "CHAT", "LISTS"}

// ckGlossaryLines builds the glossary rows from ckKeymap — the single source
// of every binding, so the overlay cannot document a key that does not exist.
func ckGlossaryLines() []string {
	lines := []string{ckLabelS.Render("SHORTCUTS")}
	for _, group := range ckGlossaryGroups {
		lines = append(lines, "", ckCyanS.Render(group))
		for _, b := range ckKeymap {
			if b.group != group {
				continue
			}
			// Long key lists (the /mode and :view commands) overflow the
			// column rather than truncate — a glossary must never cut a key.
			k := ckCell(b.keys, 18)
			if lipgloss.Width(b.keys) > 18 {
				k = b.keys + "  "
			}
			lines = append(lines, " "+ckAquaS.Render(k)+ckDimS.Render(b.does))
		}
	}
	return lines
}

// viewGlossary renders the shortcut glossary as a centred overlay; at short
// terminals it becomes a scrollable list instead of a cropped box.
func (m Cockpit) viewGlossary(w, h int) string {
	lines := ckGlossaryLines()
	box := ckBoxS.Render(strings.Join(lines, "\n"))
	if m.glossOff == 0 && lipgloss.Width(box) <= w && lipgloss.Height(box) <= h {
		return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, box)
	}
	out, _ := ckScrollLines(lines, m.glossOff, h)
	return strings.Join(out, "\n")
}
