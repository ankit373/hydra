// SPDX-License-Identifier: MIT

package tui

import (
	"image/color"
	"io"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/lucasb-eyer/go-colorful"

	"github.com/ankit373/hydra/internal/config"
)

// OutputProfile is the colour depth stdout can show, read from the
// environment once by Lip Gloss and never by asking the terminal. NO_COLOR
// draws no SGR at all, bold and reverse included, as Lip Gloss v1 did.
func OutputProfile() colorprofile.Profile {
	if p := lipgloss.Writer.Profile; p != colorprofile.ASCII {
		return p
	}
	return colorprofile.NoTTY
}

// ANSI16 is the 16-colour pick Lip Gloss v1 drew c with: termenv's nearest
// basic colour in HSLuv. x/ansi's table differs on 10 of the colours Hydra
// uses (warning orange turns red), so v2 output keeps this one.
func ANSI16(c color.Color) ansi.BasicColor {
	if b, ok := c.(ansi.BasicColor); ok {
		return b
	}
	from, _ := colorful.MakeColor(ansi.Convert256(c))
	best, nearest := 0, math.MaxFloat64
	for i := range 16 {
		to, _ := colorful.MakeColor(ansi.BasicColor(i))
		if d := from.DistanceHSLuv(to); d < nearest {
			best, nearest = i, d
		}
	}
	return ansi.BasicColor(best)
}

// ckDepth is c as the current terminal draws a colour without an explicit
// 16-colour token: v1's pick on a 16-colour terminal, unchanged otherwise.
func ckDepth(c color.Color) color.Color {
	if ckProfile == colorprofile.ANSI {
		return ANSI16(c)
	}
	return c
}

// OwnTerminal readies the process for a full-screen program and returns what
// undoes it. A log line written while the program owns the screen paints
// over the frame and desyncs the renderer (TUI-101), so logs go to a file.
func OwnTerminal() (restore func()) {
	switch os.Getenv("HYDRA_THEME") {
	case "light":
		ckSetTheme(ckProfile, false)
	case "dark":
		ckSetTheme(ckProfile, true)
	default:
		// Ask the terminal now, before the program reads the tty: asked from
		// the first frame, the reply arrives as keystrokes (OSC 11). Only a
		// full-screen program gets here, so no other command queries (TUI-31).
		ckSetTheme(ckProfile, lipgloss.HasDarkBackground(os.Stdin, os.Stdout))
	}
	prev := log.Writer()
	var sink io.Writer = io.Discard
	dir := filepath.Join(config.Dir(), "logs")
	var f *os.File
	if os.MkdirAll(dir, 0o700) == nil {
		var err error
		if f, err = os.OpenFile(filepath.Join(dir, "tui.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			sink = f
		}
	}
	log.SetOutput(sink)
	return func() {
		log.SetOutput(prev)
		if f != nil {
			_ = f.Close()
		}
	}
}

// NewProgram runs a full-screen model drawn at OutputProfile, the depth the
// styles were resolved for, so the renderer never re-picks a token's colour.
func NewProgram(m tea.Model) *tea.Program {
	return tea.NewProgram(m, tea.WithColorProfile(OutputProfile()))
}

// ckEditorDoneMsg is $EDITOR returning control, with whatever stopped it.
type ckEditorDoneMsg struct{ err error }

// ckOpenEditor hands the terminal to $EDITOR for file and takes it back.
func ckOpenEditor(ed, file string) tea.Cmd {
	return tea.ExecProcess(exec.Command(ed, file), func(err error) tea.Msg { return ckEditorDoneMsg{err} })
}
