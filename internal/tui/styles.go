// SPDX-License-Identifier: MIT

package tui

// styles.go, the cockpit's palette: one role per colour, each with a light and
// a dark value at every colour depth, so the TUI reads on any terminal theme.
// Body text has no colour at all and inherits the terminal's own foreground.

import "github.com/charmbracelet/lipgloss"

// ckRole is a colour role: truecolor, ANSI-256 and ANSI-16 values for a dark
// and a light background. Explicit lower depths, because termenv's nearest
// match turned body text magenta at 16 colours (TUI-03).
func ckRole(darkHex, dark256, darkANSI, lightHex, light256, lightANSI string) lipgloss.CompleteAdaptiveColor {
	return lipgloss.CompleteAdaptiveColor{
		Dark:  lipgloss.CompleteColor{TrueColor: darkHex, ANSI256: dark256, ANSI: darkANSI},
		Light: lipgloss.CompleteColor{TrueColor: lightHex, ANSI256: light256, ANSI: lightANSI},
	}
}

var (
	// Every text role clears 4.5:1 on #1E1E1E (dark) and on #FFFFFF and
	// Solarized Light #FDF6E3 (light), at truecolor and at ANSI-256.
	ckAqua    = ckRole("#00E6C3", "43", "6", "#007A68", "23", "6")
	ckCyan    = ckRole("#2AF0E0", "50", "6", "#00707A", "24", "6")
	ckViolet  = ckRole("#A78BFA", "141", "5", "#6D28D9", "56", "5")
	ckMagenta = ckRole("#E852C8", "170", "5", "#A21CAF", "127", "5")
	ckDimc    = ckRole("#9AA0C4", "146", "7", "#4B5170", "239", "8")
	ckFaint   = ckRole("#8088B0", "103", "8", "#5A5F85", "60", "8")
	ckCheap   = ckRole("#3FD98A", "78", "2", "#13804A", "22", "2")
	ckMid     = ckRole("#E0A93A", "179", "3", "#8A5A00", "94", "3")
	ckExp     = ckRole("#FF5A6E", "203", "1", "#C0243A", "160", "1")
	ckLineC   = ckRole("#2A2E52", "238", "8", "#C9CCE0", "252", "8")

	ckAquaS    = lipgloss.NewStyle().Foreground(ckAqua)
	ckCyanS    = lipgloss.NewStyle().Foreground(ckCyan)
	ckVioletS  = lipgloss.NewStyle().Foreground(ckViolet)
	ckMagentaS = lipgloss.NewStyle().Foreground(ckMagenta)
	ckInkS     = lipgloss.NewStyle()
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
