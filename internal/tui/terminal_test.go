// SPDX-License-Identifier: MIT

package tui

import (
	"bytes"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ankit373/hydra/internal/sysinfo"
	"github.com/ankit373/hydra/internal/testutil"
)

// Regression tests for the Phase 1 TUI correctness pass (#1335): readable on
// light terminals, no wizard indent, nothing written over the frame, Unicode-
// safe editing, sanitised model output, and cues that survive NO_COLOR.

// ── contrast (TUI-01, TUI-07) ─────────────────────────────────────────────────

func ckRGB(t *testing.T, c string) [3]float64 {
	t.Helper()
	if strings.HasPrefix(c, "#") {
		var r, g, b int
		if _, err := fmt.Sscanf(c, "#%02x%02x%02x", &r, &g, &b); err != nil {
			t.Fatalf("bad hex %q", c)
		}
		return [3]float64{float64(r), float64(g), float64(b)}
	}
	n, err := strconv.Atoi(c)
	if err != nil || n < 16 || n > 255 {
		t.Fatalf("expected an ANSI-256 cube or grey value, got %q", c)
	}
	if n >= 232 {
		v := float64(8 + (n-232)*10)
		return [3]float64{v, v, v}
	}
	n -= 16
	lv := []float64{0, 95, 135, 175, 215, 255}
	return [3]float64{lv[n/36], lv[(n/6)%6], lv[n%6]}
}

func ckContrast(a, b [3]float64) float64 {
	lum := func(c [3]float64) float64 {
		var l [3]float64
		for i, v := range c {
			v /= 255
			if v <= 0.03928 {
				l[i] = v / 12.92
			} else {
				l[i] = math.Pow((v+0.055)/1.055, 2.4)
			}
		}
		return 0.2126*l[0] + 0.7152*l[1] + 0.0722*l[2]
	}
	x, y := lum(a), lum(b)
	return (math.Max(x, y) + 0.05) / (math.Min(x, y) + 0.05)
}

var (
	ckOnDark  = [3]float64{0x1E, 0x1E, 0x1E}
	ckOnWhite = [3]float64{0xFF, 0xFF, 0xFF}
	ckOnSolar = [3]float64{0xFD, 0xF6, 0xE3} // Solarized Light
)

// Every generated role meets its token floor on a dark terminal and on two
// light ones, at truecolor and ANSI-256 (macOS Terminal is white and 256).
// The text roles the views use must carry the 4.5:1 floor.
func TestPalette_TextRolesMeetAAOnLightAndDark(t *testing.T) {
	floors := map[string]float64{}
	for _, r := range tokRoles {
		floors[r.path] = r.min
		for _, v := range []string{r.c.Dark.TrueColor, r.c.Dark.ANSI256} {
			if c := ckContrast(ckRGB(t, v), ckOnDark); c < r.min {
				t.Errorf("%s dark %s is %.2f:1 on #1E1E1E, floor %.1f", r.path, v, c, r.min)
			}
		}
		for _, v := range []string{r.c.Light.TrueColor, r.c.Light.ANSI256} {
			for _, bg := range [][3]float64{ckOnWhite, ckOnSolar} {
				if c := ckContrast(ckRGB(t, v), bg); c < r.min {
					t.Errorf("%s light %s is %.2f:1 on %v, floor %.1f", r.path, v, c, bg, r.min)
				}
			}
		}
		for _, a := range []string{r.c.Dark.ANSI, r.c.Light.ANSI} {
			if n, err := strconv.Atoi(a); err != nil || n < 0 || n > 15 {
				t.Errorf("%s has ANSI-16 slot %q; termenv would guess one", r.path, a)
			}
		}
	}
	for _, path := range []string{"color.text.muted", "color.text.subtle", "color.accent.default",
		"color.accent.strong", "color.accent.secondary", "color.status.success", "color.status.warning", "color.status.danger"} {
		if floors[path] < 4.5 {
			t.Errorf("text role %s has floor %.1f, want 4.5", path, floors[path])
		}
	}
	// Dim and faint must stay apart at 256 colours, or the label hierarchy
	// flattens on macOS Terminal's default profile.
	if tokTextMuted.Light.ANSI256 == tokTextSubtle.Light.ANSI256 || tokTextMuted.Dark.ANSI256 == tokTextSubtle.Dark.ANSI256 {
		t.Errorf("text.muted and text.subtle share an ANSI-256 index: %v %v", tokTextMuted, tokTextSubtle)
	}
	text := map[string]lipgloss.CompleteAdaptiveColor{
		"aqua": ckAqua, "cyan": ckCyan, "violet": ckViolet, "magenta": ckMagenta, "dim": ckDimc, "faint": ckFaint,
		"cheap": ckCheap, "mid": ckMid, "exp": ckExp,
	}
	for name, c := range text {
		found := false
		for _, r := range tokRoles {
			found = found || (r.c == c && r.min >= 4.5)
		}
		if !found {
			t.Errorf("ck%s is not a generated text role", name)
		}
	}
	wizard := map[string]lipgloss.Style{
		"title": sTitle, "selected": sSelected, "dim": sDim, "prompt": sPrompt, "error": sError,
		"hint": sHint, "warn": warnStyle, "success": successStyle, "fits": fitsStyle, "code": codeStyle,
	}
	for name, s := range wizard {
		c, ok := s.GetForeground().(lipgloss.AdaptiveColor)
		if !ok {
			t.Errorf("wizard %s is not adaptive: %T", name, s.GetForeground())
			continue
		}
		if r := ckContrast(ckRGB(t, c.Dark), ckOnDark); r < 4.5 {
			t.Errorf("wizard %s dark %s is %.2f:1", name, c.Dark, r)
		}
		if r := ckContrast(ckRGB(t, c.Light), ckOnWhite); r < 4.5 {
			t.Errorf("wizard %s light %s is %.2f:1", name, c.Light, r)
		}
	}
}

// Body text carries no colour, so it is the terminal's own foreground on any
// theme instead of near-white on white.
func TestPalette_BodyTextUsesTheTerminalForeground(t *testing.T) {
	for _, s := range []lipgloss.Style{ckInkS, ckYouS} {
		if _, none := s.GetForeground().(lipgloss.NoColor); !none {
			t.Errorf("body text sets a foreground: %v", s.GetForeground())
		}
	}
}

// ── wizard layout (TUI-02) ────────────────────────────────────────────────────

// ckCheckWizardFrame fails on what styled newlines produced: a row of spaces
// where a blank line belongs, and an option pushed right of the gutter.
func ckCheckWizardFrame(t *testing.T, name, view string) {
	t.Helper()
	for i, l := range strings.Split(stripANSI(view), "\n") {
		if l != "" && strings.TrimSpace(l) == "" {
			t.Errorf("%s line %d is %d spaces, a newline rendered inside a style", name, i, len(l))
		}
		if j := strings.Index(l, "›"); j >= 0 && lipgloss.Width(l[:j]) > 2 {
			t.Errorf("%s: the selected option starts at column %d:\n%s", name, lipgloss.Width(l[:j]), view)
		}
	}
}

func TestWizards_OptionsStartAtTheGutter(t *testing.T) {
	testutil.NewSandbox(t)
	im := NewInitModel(wizardHeads())
	h := wizardHeads().Heads[0]
	im.cortex, im.skills = &h, defaultSkills(&h)
	for s := stepCortex; s <= stepDone; s++ {
		im.step = s
		ckCheckWizardFrame(t, fmt.Sprintf("init step %d", s), im.View())
	}
	in := newInstallModelFor(&sysinfo.Specs{TotalRAMGB: 8, FreeRAMGB: 1})
	in.selected = &in.options[0]
	for s := installStepPick; s <= installStepDone; s++ {
		in.step = s
		ckCheckWizardFrame(t, fmt.Sprintf("install step %d", s), in.View())
	}
}

// esc steps back instead of the copy promising a back key that did not exist.
func TestWizards_EscGoesBack(t *testing.T) {
	testutil.NewSandbox(t)
	m, _ := send(NewInitModel(wizardHeads()), "enter", "enter")
	if m.(InitModel).step != stepPrivacy {
		t.Fatalf("step = %v", m.(InitModel).step)
	}
	m, cmd := send(m, "esc")
	if m.(InitModel).step != stepTiers || cmd != nil {
		t.Errorf("esc: step=%v cmd=%v, want tiers and no quit", m.(InitModel).step, cmd)
	}
	if m, _ = send(m, "esc", "esc"); m.(InitModel).step != stepCortex {
		t.Errorf("esc past the first step moved to %v", m.(InitModel).step)
	}

	in := newInstallModelFor(&sysinfo.Specs{TotalRAMGB: 8, FreeRAMGB: 1})
	in.step = installStepOllama
	if v := stripANSI(in.View()); strings.Contains(v, "Press q to go back") {
		t.Error("the copy still sends the user to q, which quits")
	}
	var im tea.Model = in
	if im, _ = send(im, "esc"); im.(InstallModel).step != installStepPick {
		t.Errorf("install esc: step=%v", im.(InstallModel).step)
	}
}

// ── log bleed and theme override (TUI-101) ────────────────────────────────────

func TestOwnTerminal_SendsLogsToAFileUntilRestored(t *testing.T) {
	home := testutil.NewSandbox(t)
	var screen bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&screen)
	t.Cleanup(func() { log.SetOutput(prev) })

	restore := OwnTerminal()
	log.Printf("hydra/pricing: initial background fetch failed")
	restore()
	if screen.Len() != 0 {
		t.Errorf("a log line reached the screen while the TUI owned it: %q", screen.String())
	}
	raw, err := os.ReadFile(filepath.Join(home.HydraHome, "logs", "tui.log"))
	if err != nil || !strings.Contains(string(raw), "background fetch failed") {
		t.Errorf("the line was not kept in tui.log: %q %v", raw, err)
	}
	log.Printf("after")
	if !strings.Contains(screen.String(), "after") {
		t.Error("restore did not give the logger back")
	}
}

func TestOwnTerminal_HonoursHydraTheme(t *testing.T) {
	testutil.NewSandbox(t)
	was := lipgloss.HasDarkBackground()
	t.Cleanup(func() { lipgloss.SetHasDarkBackground(was) })
	for theme, dark := range map[string]bool{"light": false, "dark": true} {
		t.Setenv("HYDRA_THEME", theme)
		OwnTerminal()()
		if lipgloss.HasDarkBackground() != dark {
			t.Errorf("HYDRA_THEME=%s left dark=%v", theme, lipgloss.HasDarkBackground())
		}
	}
}

// ── Unicode editing (TUI-103) ─────────────────────────────────────────────────

func TestBackspace_RemovesOneGrapheme(t *testing.T) {
	cases := map[string]string{
		"café":      "caf",
		"日本":        "日",
		"ok 👍🏽":     "ok ",
		"fam 👨‍👩‍👧": "fam ",
		"e\u0301":   "",
		"":          "",
	}
	for in, want := range cases {
		m := testCockpit()
		m.th().input = in
		m = press(m, tea.KeyBackspace)
		if got := m.th().input; got != want || !utf8.ValidString(got) {
			t.Errorf("backspace on %q = %q, want %q", in, got, want)
		}
	}
}

func TestStreamClip_NeverSplitsARune(t *testing.T) {
	s := &ckStream{}
	s.delta(strings.Repeat("é", ckLiveCapBytes)) // 2 bytes each, far past the cap
	s.delta("x")
	_, live, clipped := s.snapshot()
	if !clipped || !utf8.ValidString(live) {
		t.Errorf("clip produced invalid UTF-8 (clipped=%v)", clipped)
	}
}

// ── untrusted output (TUI-104) ────────────────────────────────────────────────

func TestStream_StripsTerminalControlSequences(t *testing.T) {
	evil := "line one\r\nclip\x1b]52;c;ZXZpbA==\x07 title\x1b]0;pwned\x1b\\ wipe\x1b[2J\x9b31m\tend"
	out := strings.Join(ckStreamEntries(
		[]ckAbandoned{{head: "h\x1b[2J", reason: "r", text: evil, open: true}}, "head\x1b]0;x\x07", evil, false), "\n")
	for _, bad := range []string{"\x1b]", "\x07", "\x1b[2J", "\x9b", "\r"} {
		if strings.Contains(out, bad) {
			t.Errorf("rendered stream still carries %q", bad)
		}
	}
	if got := ckSafeText("a\r\nb\tc"); got != "a\nb    c" {
		t.Errorf("ckSafeText dropped layout: %q", got)
	}
}

// ── NO_COLOR cues (TUI-06) and mouse capture (TUI-105) ───────────────────────

// stripANSI is what NO_COLOR leaves: no colour, no bold, no reverse. The
// active tab and a focused pane must still be told apart.
func TestNoColor_SelectionHasAGlyph(t *testing.T) {
	m := testCockpit()
	m.w = 160
	if h := stripANSI(m.header()); !strings.Contains(h, "[chat]") || strings.Contains(h, "[agents]") {
		t.Errorf("the active tab has no non-colour cue: %q", h)
	}
	m.w = 50
	if h := stripANSI(m.header()); !strings.Contains(h, "[chat]") {
		t.Errorf("the narrow header lost the cue: %q", h)
	}
	if b := stripANSI(ckFocusBox(ckBoxS).Render("x")); !strings.Contains(b, "┏") {
		t.Errorf("a focused pane is told apart by colour alone: %q", b)
	}
}

func TestMouse_CtrlSHandsSelectionToTheTerminal(t *testing.T) {
	m := testCockpit()
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = next.(Cockpit)
	if !m.mouseOff || cmd == nil || fmt.Sprintf("%T", cmd()) != "tea.disableMouseMsg" {
		t.Fatalf("ctrl+s did not release the mouse: off=%v", m.mouseOff)
	}
	if !strings.Contains(stripANSI(m.statusBar()), "select mode") {
		t.Error("select mode is not shown")
	}
	next, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if next.(Cockpit).mouseOff || cmd == nil || fmt.Sprintf("%T", cmd()) != "tea.enableMouseCellMotionMsg" {
		t.Error("a second ctrl+s did not restore wheel scrolling")
	}
}

// Coming back from $EDITOR re-asks for the mouse bubbletea v1 dropped, and a
// failed editor says so instead of returning silently.
func TestEditorReturn_RestoresMouseAndReportsErrors(t *testing.T) {
	m := testCockpit()
	next, cmd := m.Update(ckEditorDoneMsg{err: fmt.Errorf("exec: %q: not found", "nope")})
	if cmd == nil || fmt.Sprintf("%T", cmd()) != "tea.enableMouseCellMotionMsg" {
		t.Error("the mouse was not re-enabled after the editor")
	}
	if !strings.Contains(next.(Cockpit).flash, "not found") {
		t.Errorf("the editor failure is silent: %q", next.(Cockpit).flash)
	}
	m.mouseOff = true
	if _, cmd = m.Update(ckEditorDoneMsg{}); cmd != nil {
		t.Error("select mode was overridden by the editor return")
	}
}

// A settled answer keeps tabs and CRLF as layout, as the stream showed it.
func TestResultLines_KeepLayoutOfASettledAnswer(t *testing.T) {
	out := stripANSI(strings.Join(ckResultLines(ckTask{answer: "func f() {\r\n\treturn\x1b]0;t\x07\r\n}"}), "\n"))
	if strings.Contains(out, "{�") || !strings.Contains(out, "    return") || strings.Contains(out, "\x1b]") {
		t.Errorf("settled answer = %q", out)
	}
}
