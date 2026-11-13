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

// Local names for the token roles, named for what they mean. Status styles
// (ok, warn, bad) are for states only, never for cost or tiers. Lip Gloss v2
// colours are values, so ckSetTheme resolves them for one terminal.
var (
	ckAccent, ckAccentStrong, ckSecondary, ckBrand, ckDimc, ckFaint color.Color
	ckOk, ckWarn, ckBad, ckLineC                                    color.Color

	ckAccentS, ckAccentStrongS, ckSecondaryS, ckBrandS, ckInkS, ckDimS, ckFaintS lipgloss.Style
	ckOkS, ckWarnS, ckBadS, ckLabelS, ckYouS, ckBoxS, ckChipS                    lipgloss.Style
	ckTabS, ckMoneyS                                                             lipgloss.Style
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
	ckAccent, ckAccentStrong, ckSecondary, ckBrand = at(tokAccentDefault), at(tokAccentStrong), at(tokAccentSecondary), at(tokBrandEnd)
	ckDimc, ckFaint, ckLineC = at(tokTextMuted), at(tokTextSubtle), at(tokBorderDefault)
	ckOk, ckWarn, ckBad = at(tokStatusSuccess), at(tokStatusWarning), at(tokStatusDanger)

	ckAccentS = lipgloss.NewStyle().Foreground(ckAccent)
	ckAccentStrongS = lipgloss.NewStyle().Foreground(ckAccentStrong)
	ckSecondaryS = lipgloss.NewStyle().Foreground(ckSecondary)
	ckBrandS = lipgloss.NewStyle().Foreground(ckBrand)
	ckInkS = lipgloss.NewStyle()
	ckDimS = lipgloss.NewStyle().Foreground(ckDimc)
	ckFaintS = lipgloss.NewStyle().Foreground(ckFaint)
	ckOkS = lipgloss.NewStyle().Foreground(ckOk)
	ckWarnS = lipgloss.NewStyle().Foreground(ckWarn)
	ckBadS = lipgloss.NewStyle().Foreground(ckBad)
	ckLabelS = lipgloss.NewStyle().Foreground(ckFaint).Bold(true)
	ckYouS = lipgloss.NewStyle().Bold(true)
	ckBoxS = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(ckLineC).Padding(0, tokSpace2)
	// Reverse video over the accent, so the chip is legible on either theme
	// without painting a background colour of our own.
	ckChipS = lipgloss.NewStyle().Foreground(ckAccent).Reverse(true).Bold(true).Padding(0, tokSpace2)
	ckTabS = lipgloss.NewStyle().Foreground(ckDimc).Padding(0, tokSpace2)
	// Money is a number, not a state: the terminal's own foreground.
	ckMoneyS = lipgloss.NewStyle()
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
	return box.Border(lipgloss.ThickBorder()).BorderForeground(ckAccent)
}
