// SPDX-License-Identifier: MIT

package tui

// styles.go, the cockpit's styles over the generated token roles in
// tokens_gen.go (source: design/tokens). Body text has no colour at all and
// inherits the terminal's own foreground.

import (
	"image/color"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
)

// Local names for the token roles, kept so the views read unchanged. Lip
// Gloss v2 colours are values, so ckSetTheme resolves them for one terminal.
var (
	ckAqua, ckCyan, ckViolet, ckMagenta, ckDimc, ckFaint color.Color
	ckCheap, ckMid, ckExp, ckLineC                       color.Color

	ckAquaS, ckCyanS, ckVioletS, ckMagentaS, ckInkS, ckDimS, ckFaintS lipgloss.Style
	ckCheapS, ckMidS, ckExpS, ckLabelS, ckYouS, ckBoxS, ckChipS       lipgloss.Style
	ckTabS                                                            lipgloss.Style
)

// ckProfile and ckDark are the terminal the styles were last drawn for.
var (
	ckProfile colorprofile.Profile
	ckDark    bool
)

// Dark until an interactive program asks the terminal: nothing else may
// query it (TUI-31).
func init() { ckSetTheme(OutputProfile(), true) }

// ckSetTheme redraws every style for a terminal of profile p with a dark or
// light background. Call it before the first frame, never mid-render.
func ckSetTheme(p colorprofile.Profile, dark bool) {
	ckProfile, ckDark = p, dark
	at := func(c tokColor) color.Color { return c.at(p, dark) }
	ckAqua, ckCyan, ckViolet, ckMagenta = at(tokAccentDefault), at(tokAccentStrong), at(tokAccentSecondary), at(tokBrandEnd)
	ckDimc, ckFaint, ckLineC = at(tokTextMuted), at(tokTextSubtle), at(tokBorderDefault)
	ckCheap, ckMid, ckExp = at(tokStatusSuccess), at(tokStatusWarning), at(tokStatusDanger)

	ckAquaS = lipgloss.NewStyle().Foreground(ckAqua)
	ckCyanS = lipgloss.NewStyle().Foreground(ckCyan)
	ckVioletS = lipgloss.NewStyle().Foreground(ckViolet)
	ckMagentaS = lipgloss.NewStyle().Foreground(ckMagenta)
	ckInkS = lipgloss.NewStyle()
	ckDimS = lipgloss.NewStyle().Foreground(ckDimc)
	ckFaintS = lipgloss.NewStyle().Foreground(ckFaint)
	ckCheapS = lipgloss.NewStyle().Foreground(ckCheap)
	ckMidS = lipgloss.NewStyle().Foreground(ckMid)
	ckExpS = lipgloss.NewStyle().Foreground(ckExp)
	ckLabelS = lipgloss.NewStyle().Foreground(ckFaint).Bold(true)
	ckYouS = lipgloss.NewStyle().Bold(true)
	ckBoxS = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(ckLineC).Padding(0, tokSpace2)
	// Reverse video over the accent, so the chip is legible on either theme
	// without painting a background colour of our own.
	ckChipS = lipgloss.NewStyle().Foreground(ckAqua).Reverse(true).Bold(true).Padding(0, tokSpace2)
	ckTabS = lipgloss.NewStyle().Foreground(ckDimc).Padding(0, tokSpace2)
	setWizardTheme(dark)
}

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
func ckTierColor(tier int) color.Color {
	switch {
	case tier <= 2:
		return ckViolet
	case tier <= 6:
		return ckCyan
	default:
		return ckCheap
	}
}
