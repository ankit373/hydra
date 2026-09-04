// SPDX-License-Identifier: MIT

package tui

// styles.go — the cockpit's palette and lipgloss styles, mapped from the
// desktop design tokens (desktop/frontend/src/tokens.css). One place; no view
// declares its own colours. ck-prefixed to avoid clashing with splash/init.

import "github.com/charmbracelet/lipgloss"

var (
	ckAqua    = lipgloss.Color("#00E6C3")
	ckCyan    = lipgloss.Color("#2AF0E0")
	ckViolet  = lipgloss.Color("#8B5CF6")
	ckMagenta = lipgloss.Color("#E852C8")
	ckInk     = lipgloss.Color("#E7E9F5")
	ckDimc    = lipgloss.Color("#9AA0C4")
	ckFaint   = lipgloss.Color("#5A5F85")
	ckCheap   = lipgloss.Color("#3FD98A")
	ckMid     = lipgloss.Color("#E0A93A")
	ckExp     = lipgloss.Color("#FF5A6E")
	ckLineC   = lipgloss.Color("#2A2E52")
	ckBg      = lipgloss.Color("#06070F")

	ckAquaS    = lipgloss.NewStyle().Foreground(ckAqua)
	ckCyanS    = lipgloss.NewStyle().Foreground(ckCyan)
	ckVioletS  = lipgloss.NewStyle().Foreground(ckViolet)
	ckMagentaS = lipgloss.NewStyle().Foreground(ckMagenta)
	ckInkS     = lipgloss.NewStyle().Foreground(ckInk)
	ckDimS     = lipgloss.NewStyle().Foreground(ckDimc)
	ckFaintS   = lipgloss.NewStyle().Foreground(ckFaint)
	ckCheapS   = lipgloss.NewStyle().Foreground(ckCheap)
	ckMidS     = lipgloss.NewStyle().Foreground(ckMid)
	ckExpS     = lipgloss.NewStyle().Foreground(ckExp)
	ckLabelS   = lipgloss.NewStyle().Foreground(ckFaint).Bold(true)
	ckYouS     = lipgloss.NewStyle().Foreground(ckInk).Bold(true)
	ckBoxS     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(ckLineC).Padding(0, 1)
	ckChipS    = lipgloss.NewStyle().Background(ckAqua).Foreground(ckBg).Bold(true).Padding(0, 1)
	ckTabS     = lipgloss.NewStyle().Foreground(ckDimc).Padding(0, 1)
)

// ckTierColor maps a capability tier onto the cost ramp: cheap local heads
// green, mid cyan, expensive frontier heads violet.
func ckTierColor(tier int) lipgloss.Color {
	switch {
	case tier <= 2:
		return ckViolet
	case tier <= 6:
		return ckCyan
	default:
		return ckCheap
	}
}
