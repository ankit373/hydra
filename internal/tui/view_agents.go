// SPDX-License-Identifier: MIT

package tui

// view_agents.go — view 1, minimal in this phase: live runs plus today's
// finished ones, enter opens the run's trace. Fleet machinery lands in a later
// phase; nothing here pretends it already exists.

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// ckAgentTaskW is the task column budget — fixed so the right-hand facts align.
const ckAgentTaskW = 30

func (m Cockpit) viewAgents(w, h int) string {
	rows := m.agentRows()
	sel := m.agentSel
	if sel < 0 || sel >= len(rows) {
		sel = 0
	}
	var b strings.Builder
	live, done := m.agentCounts()
	b.WriteString(ckLabelS.Render("AGENTS") +
		ckDimS.Render(fmt.Sprintf(" · %d live · %d finished today", live, done)) + "\n\n")

	if len(rows) == 0 {
		b.WriteString(ckFaintS.Render(" no agents running") + "\n\n" +
			ckDimS.Render(" This view lists live runs and today's finished ones.") + "\n" +
			ckDimS.Render(" Start one from chat, or `hyctl dispatch \"…\"` — enter opens its trace."))
		return lipgloss.NewStyle().Width(w).Height(h).Padding(0, 1).Render(b.String())
	}

	avail := h - 5
	if avail < 3 {
		avail = 3
	}
	lines := make([]string, len(rows))
	for i, r := range rows {
		section := ""
		if r.live {
			section = ckCheapS.Render("live ")
		} else {
			section = ckFaintS.Render("done ")
		}
		marker := "  "
		if i == sel {
			marker = ckAquaS.Render("▸ ")
		}
		task := r.task
		if task == "" {
			task = "(task not recorded)"
		}
		line := marker + ckStatusGlyph(r.status) + " " + section + ckFaintS.Render(ckCell(ckShortID(r.id), 8)) + " " +
			ckInkS.Bold(i == sel).Render(ckCell(ckSafe(task), ckAgentTaskW)) + " "
		if r.live {
			line += ckDimS.Render("started " + ckAgo(r.start))
		} else {
			line += ckDimS.Render(ckRCell(ckFmtMS(r.durMS), 7) + fmt.Sprintf("  $%.4f est", m.runCostUSD(r)))
		}
		lines[i] = line
	}
	b.WriteString(strings.Join(ckSelScroll(lines, sel, avail), "\n"))
	b.WriteString("\n\n" + ckFaintS.Render(" enter opens the run's trace in activity"))
	return lipgloss.NewStyle().Width(w).Height(h).Padding(0, 1).Render(b.String())
}

// ckAgo renders how long ago t was, coarsely.
func ckAgo(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t).Round(time.Second)
	if d < 0 {
		d = 0
	}
	if d > time.Hour {
		return d.Round(time.Minute).String() + " ago"
	}
	return d.String() + " ago"
}
