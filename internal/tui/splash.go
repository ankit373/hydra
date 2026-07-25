// SPDX-License-Identifier: MIT

package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ── Color palette ─────────────────────────────────────────────────────────────

var (
	cHead1  = lipgloss.Color("82")  // left head   — bright lime
	cHead2  = lipgloss.Color("46")  // center head — vivid green
	cHead3  = lipgloss.Color("77")  // right head  — medium green
	cNeck   = lipgloss.Color("34")  // necks joining body
	cBody   = lipgloss.Color("22")  // body — dark green
	cCortex = lipgloss.Color("205") // CORTEX label — magenta/pink
	cArrow  = lipgloss.Color("213") // arrow accent
	cDim    = lipgloss.Color("240") // muted text
	cWhite  = lipgloss.Color("255")
	cGold   = lipgloss.Color("220") // stat highlights
)

// ── Head shape ────────────────────────────────────────────────────────────────
//
// Each head is identical in shape; only colour differs.
// Adjust this constant to restyle all three heads at once.

const headShape = `  .~~~~~.
 / %s   %s \
( >> ^ << )
 \  ~~~  /
  |     |
  |_____|   `

// eye characters per head — subtle variation
var headEyes = [3][2]string{
	{"o", "o"}, // left head
	{"*", "*"}, // center head (brighter)
	{"o", "o"}, // right head
}

var headColors = []lipgloss.Color{cHead1, cHead2, cHead3}

func renderHead(idx int) string {
	eyes := headEyes[idx]
	shape := fmt.Sprintf(headShape, eyes[0], eyes[1])
	return lipgloss.NewStyle().Foreground(headColors[idx]).Render(shape)
}

// ── Neck + body ───────────────────────────────────────────────────────────────

const neckLine = `      \               |               /      `
const bodyLine = `       \_____________=_____________/       `
const scaleLine = `                  |||||||                  `
const tailLine = `              ~~~~~~~~~~~                  `

func renderBody() string {
	s := lipgloss.NewStyle().Foreground(cNeck)
	b := lipgloss.NewStyle().Foreground(cBody)
	return lipgloss.JoinVertical(lipgloss.Left,
		s.Render(neckLine),
		b.Render(bodyLine),
		b.Render(scaleLine),
		b.Render(tailLine),
	)
}

// ── CORTEX banner ─────────────────────────────────────────────────────────────

func renderCortex(name string) string {
	label := lipgloss.NewStyle().
		Bold(true).
		Foreground(cCortex).
		Padding(0, 1).
		Render("⚡ CORTEX")

	arrow := lipgloss.NewStyle().Foreground(cArrow).Render(" ──▶ ")

	value := lipgloss.NewStyle().
		Bold(true).
		Foreground(cWhite).
		Render(name)

	return "  " + label + arrow + value
}

// ── Stats bar ─────────────────────────────────────────────────────────────────

type Stats struct {
	Heads   int
	Tasks   int
	CostUSD float64
	SavePct int
}

func renderStats(s Stats) string {
	statStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(cBody).
		Padding(0, 2)

	labelStyle := lipgloss.NewStyle().Foreground(cDim)
	valueStyle := lipgloss.NewStyle().Bold(true).Foreground(cGold)

	stat := func(label, value string) string {
		return statStyle.Render(
			labelStyle.Render(label+"\n") + valueStyle.Render(value),
		)
	}

	heads := stat("Heads", fmt.Sprintf("%d", s.Heads))
	tasks := stat("Tasks", fmt.Sprintf("%d", s.Tasks))
	cost := stat("Cost", fmt.Sprintf("$%.2f", s.CostUSD))
	saved := stat("Saved", fmt.Sprintf("%d%%", s.SavePct))

	return "  " + lipgloss.JoinHorizontal(lipgloss.Top, heads, " ", tasks, " ", cost, " ", saved)
}

// ── Tagline ───────────────────────────────────────────────────────────────────

func renderTagline() string {
	return lipgloss.NewStyle().
		Foreground(cDim).
		Italic(true).
		Render("  Multi-model AI orchestration — one Cortex, many Heads")
}

// ── Public API ────────────────────────────────────────────────────────────────

// Logo returns the full Hydra ASCII logo with three coloured heads.
func Logo() string {
	heads := lipgloss.JoinHorizontal(lipgloss.Top,
		renderHead(0), "  ", renderHead(1), "  ", renderHead(2),
	)
	return lipgloss.JoinVertical(lipgloss.Left,
		heads,
		renderBody(),
	)
}

// Splash renders the full startup screen: logo + cortex label + tagline.
func Splash(cortexName string) string {
	title := lipgloss.NewStyle().
		Bold(true).
		Foreground(cCortex).
		Render("H Y D R A")

	divider := lipgloss.NewStyle().Foreground(cBody).
		Render("  " + strings.Repeat("─", 46))

	return lipgloss.JoinVertical(lipgloss.Left,
		"",
		"  "+title,
		divider,
		Logo(),
		"",
		renderCortex(cortexName),
		"",
		renderTagline(),
		"",
	)
}

// Dashboard renders the logo + live stats panel.
func Dashboard(cortexName string, s Stats) string {
	return lipgloss.JoinVertical(lipgloss.Left,
		Splash(cortexName),
		renderStats(s),
		"",
	)
}
