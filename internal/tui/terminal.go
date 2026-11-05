// SPDX-License-Identifier: MIT

package tui

import (
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ankit373/hydra/internal/config"
)

// OwnTerminal readies the process for a full-screen program and returns what
// undoes it. A log line written while the program owns the screen paints
// over the frame and desyncs the renderer (TUI-101), so logs go to a file.
func OwnTerminal() (restore func()) {
	switch os.Getenv("HYDRA_THEME") {
	case "light":
		lipgloss.SetHasDarkBackground(false)
	case "dark":
		lipgloss.SetHasDarkBackground(true)
	default:
		// Ask the terminal now, before the program reads the tty: asked from
		// the first frame, the reply arrives as keystrokes (OSC 11).
		lipgloss.HasDarkBackground()
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

// ckEditorDoneMsg is $EDITOR returning control, with whatever stopped it.
type ckEditorDoneMsg struct{ err error }

// ckOpenEditor hands the terminal to $EDITOR for file and takes it back.
func ckOpenEditor(ed, file string) tea.Cmd {
	return tea.ExecProcess(exec.Command(ed, file), func(err error) tea.Msg { return ckEditorDoneMsg{err} })
}
