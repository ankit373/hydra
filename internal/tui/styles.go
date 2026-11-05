// SPDX-License-Identifier: MIT

package tui

// styles.go, the cockpit's styles over the generated token roles in
// tokens_gen.go (source: design/tokens). Body text has no colour at all and
// inherits the terminal's own foreground.

import "github.com/charmbracelet/lipgloss"

var (
	// Local names for the token roles, kept so the views read unchanged; the
	// values, contrast floors and ANSI picks all come from design/tokens.
	ckAqua    = tokAccentDefault
	ckCyan    = tokAccentStrong
	ckViolet  = tokAccentSecondary
	ckMagenta = tokBrandEnd
	ckDimc    = tokTextMuted
	ckFaint   = tokTextSubtle
	ckCheap   = tokStatusSuccess
	ckMid     = tokStatusWarning
	ckExp     = tokStatusDanger
	ckLineC   = tokBorderDefault

	ckAquaS    = lipgloss.NewStyle().Foreground(ckAqua)
	ckCyanS    = lipgloss.NewStyle().Foreground(ckCyan)
	ckVioletS  = lipgloss.NewStyle().Foreground(ckViolet)
	ckMagentaS = lipgloss.NewStyle().Foreground(ckMagenta)
	ckInkS     = lipgloss.NewStyle().Foreground(tokTextDefault)
	ckDimS     = lipgloss.NewStyle().Foreground(ckDimc)
	ckFaintS   = lipgloss.NewStyle().Foreground(ckFaint)
	ckCheapS   = lipgloss.NewStyle().Foreground(ckCheap)
	ckMidS     = lipgloss.NewStyle().Foreground(ckMid)
	ckExpS     = lipgloss.NewStyle().Foreground(ckExp)
	ckLabelS   = lipgloss.NewStyle().Foreground(ckFaint).Bold(true)
	ckYouS     = lipgloss.NewStyle().Bold(true)
	ckBoxS     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(ckLineC).Padding(0, 1)
	// Reverse video over the accent, so the chip is legible on either theme
	// without painting a background colour of our own.
	ckChipS = lipgloss.NewStyle().Foreground(ckAqua).Reverse(true).Bold(true).Padding(0, 1)
	ckTabS  = lipgloss.NewStyle().Foreground(ckDimc).Padding(0, 1)
)

// ckActiveTab marks the selected tab or thread with brackets as well as the
// chip, because NO_COLOR strips reverse and bold and left no cue (TUI-06).
// Same width as an unselected ckTabS label, so the bar never shifts.
func ckActiveTab(name string) string {
	return ckChipS.UnsetPadding().Render("[" + name + "]")
}

// ckFocusBox is a pane with keyboard focus: a heavier border carries the
// focus where colour cannot (NO_COLOR, monochrome), the accent where it can.
func ckFocusBox(box lipgloss.Style) lipgloss.Style {
	return box.Border(lipgloss.ThickBorder()).BorderForeground(ckAqua)
}

// ckTierColor maps a capability tier onto the cost ramp: cheap local heads
// green, mid cyan, expensive frontier heads violet.
func ckTierColor(tier int) lipgloss.CompleteAdaptiveColor {
	switch {
	case tier <= 2:
		return ckViolet
	case tier <= 6:
		return ckCyan
	default:
		return ckCheap
	}
}
