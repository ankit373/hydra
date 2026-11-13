// SPDX-License-Identifier: MIT

package tui

import (
	"os"
	"runtime"
	"strings"

	"charm.land/lipgloss/v2"
)

// The three-headed mark at terminal size: three heads whose necks meet at one
// point, drawn after the SVG in desktop/frontend/src/brand.tsx. Each row is
// left head, neck, centre, neck, right head, and the point they meet at.
type markGlyphs struct {
	head, left, centre, right, core string
}

var (
	markUnicode = markGlyphs{head: "▲", left: "╲", centre: "│", right: "╱", core: "●"}
	markASCII   = markGlyphs{head: "^", left: `\`, centre: "|", right: "/", core: "o"}
)

// markWidth is the mark's width in cells, in both glyph sets.
const markWidth = 9

// markFor picks the glyphs a terminal can draw. The Windows console host and
// the Linux text console have no diagonal box drawing; Windows Terminal sets
// WT_SESSION and has it.
func markFor(goos string, getenv func(string) string) markGlyphs {
	if goos == "windows" && getenv("WT_SESSION") == "" {
		return markASCII
	}
	if getenv("TERM") == "linux" {
		return markASCII
	}
	return markUnicode
}

// Mark returns the mark, five rows by nine cells, in the brand colours: the
// centre head cyan, the side heads magenta, the meeting point in the accent.
// Without colour the shape alone carries it.
func Mark() string {
	return renderMark(termMark())
}

// termMark is this terminal's glyph set; a variable so tests can pin one.
var termMark = func() markGlyphs { return markFor(runtime.GOOS, os.Getenv) }

func renderMark(g markGlyphs) string {
	at := func(c tokColor) lipgloss.Style {
		return lipgloss.NewStyle().Foreground(c.at(ckProfile, ckDark))
	}
	mid, side := at(tokBrandStart), at(tokBrandEnd)
	core := at(tokAccentDefault).Bold(true)

	row := func(indent, gap int, l, c, r string) string {
		pad := strings.Repeat(" ", gap)
		return strings.Repeat(" ", indent) + side.Render(l) + pad + mid.Render(c) + pad + side.Render(r)
	}
	return strings.Join([]string{
		row(0, 3, g.head, g.head, g.head),
		row(1, 2, g.left, g.centre, g.right),
		row(2, 1, g.left, g.centre, g.right),
		row(3, 0, g.left, g.centre, g.right),
		strings.Repeat(" ", 4) + core.Render(g.core),
	}, "\n")
}

// Splash is what `hyctl probe` prints first: the mark, the plain wordmark,
// one line on what Hydra does, and the main model when there is one.
func Splash(mainModel string) string {
	return splash(Mark(), mainModel)
}

func splash(mark, mainModel string) string {
	text := []string{
		"",
		lipgloss.NewStyle().Bold(true).Render("HYDRA"),
		ckDimS.Render("Picks a model for each task, and can check the answer."),
	}
	if mainModel != "" {
		text = append(text, ckDimS.Render("main model ")+mainModel)
	}
	block := lipgloss.JoinHorizontal(lipgloss.Top,
		lipgloss.NewStyle().Width(markWidth).Render(mark),
		"   ",
		strings.Join(text, "\n"),
	)
	lines := strings.Split(block, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight("  "+l, " ")
	}
	return "\n" + strings.Join(lines, "\n") + "\n"
}
