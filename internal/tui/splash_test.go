// SPDX-License-Identifier: MIT

package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/ankit373/hydra/internal/testutil"
)

// The splash is the first thing `hyctl probe` prints. Pinned as text, so a
// change to the mark shows up as a diff a reviewer can read.
func TestSplash_Golden(t *testing.T) {
	withTheme(t, colorprofile.NoTTY, true)
	testutil.Golden(t, "splash", stripANSI(splash(renderMark(markUnicode), "Claude Code")))
	testutil.Golden(t, "splash_no_main_model", stripANSI(splash(renderMark(markUnicode), "")))

	// The golden normaliser turns a backslash into a slash, so the ASCII mark
	// is checked as the same drawing in the other glyph set.
	toASCII := strings.NewReplacer("▲", "^", "╲", `\`, "│", "|", "╱", "/", "●", "o")
	uni := stripANSI(splash(renderMark(markUnicode), "Claude Code"))
	if got, want := stripANSI(splash(renderMark(markASCII), "Claude Code")), toASCII.Replace(uni); got != want {
		t.Errorf("ASCII splash is not the same drawing:\n%s\nwant:\n%s", got, want)
	}
}

// It must fit an 80-column terminal, and with colour off it must carry no
// colour, so NO_COLOR and pipes get the shape alone. Bold is left for the
// CLI's writer to drop, as it does for every other line.
func TestSplash_FitsEightyColumnsAndHasNoColourWhenOff(t *testing.T) {
	withTheme(t, colorprofile.NoTTY, true)
	for _, g := range []markGlyphs{markUnicode, markASCII} {
		out := splash(renderMark(g), strings.Repeat("a-long-model-name", 2))
		if strings.Contains(out, "38;") {
			t.Errorf("colour codes with colour off:\n%q", out)
		}
		for _, l := range strings.Split(out, "\n") {
			if w := lipgloss.Width(l); w > 80 {
				t.Errorf("line is %d cells, over 80: %q", w, l)
			}
		}
	}

	withTheme(t, colorprofile.TrueColor, true)
	if out := renderMark(markUnicode); !strings.Contains(out, "\x1b[") {
		t.Errorf("mark has no colour on a truecolor terminal:\n%q", out)
	}
	if got := stripANSI(renderMark(markUnicode)); got != stripANSI(func() string {
		withTheme(t, colorprofile.NoTTY, true)
		return renderMark(markUnicode)
	}()) {
		t.Errorf("colour changed the shape:\n%s", got)
	}
}

// Terminals without diagonal box drawing get the ASCII mark.
func TestMarkFor_PicksGlyphsTheTerminalCanDraw(t *testing.T) {
	env := func(kv map[string]string) func(string) string {
		return func(k string) string { return kv[k] }
	}
	for _, tt := range []struct {
		goos string
		env  map[string]string
		want markGlyphs
	}{
		{"windows", nil, markASCII},
		{"windows", map[string]string{"WT_SESSION": "x"}, markUnicode},
		{"linux", map[string]string{"TERM": "linux"}, markASCII},
		{"linux", map[string]string{"TERM": "xterm-256color"}, markUnicode},
		{"darwin", nil, markUnicode},
	} {
		if got := markFor(tt.goos, env(tt.env)); got != tt.want {
			t.Errorf("markFor(%s, %v) = %+v, want %+v", tt.goos, tt.env, got, tt.want)
		}
	}
}

// A chat thread that has run nothing shows the mark above the greeting, and
// loses it with the first task. Pinned at the chat pane's width in an 80- and
// a 160-column terminal.
func TestChat_EmptyThreadShowsTheMark(t *testing.T) {
	withTheme(t, colorprofile.NoTTY, true)
	was := termMark
	termMark = func() markGlyphs { return markUnicode }
	t.Cleanup(func() { termMark = was })
	m := testCockpit()
	m.th().log = []string{
		"14 models found by the scan.",
		"Type a task and press enter. shift+tab mode · ctrl+t thread · ? shortcuts · :q quits.",
	}
	testutil.Golden(t, "chat_empty_80", stripANSI(m.chatMain(m.th(), 29, 20)))
	testutil.Golden(t, "chat_empty_160", stripANSI(m.chatMain(m.th(), 70, 40)))

	// No room for the mark and the greeting: the greeting wins.
	if got := stripANSI(m.chatMain(m.th(), 29, 8)); strings.Contains(got, "●") {
		t.Errorf("mark drawn in a pane too short for it:\n%s", got)
	}

	m.th().name = "add pagination"
	if got := stripANSI(m.chatMain(m.th(), 70, 40)); strings.Contains(got, "●") {
		t.Errorf("mark still drawn after the first task:\n%s", got)
	}
}
