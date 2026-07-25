// SPDX-License-Identifier: MIT

package tui

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ankit373/hydra/internal/sysinfo"
)

// installOption describes something the user can install to get started.
type installOption struct {
	name        string
	description string
	local       bool
	installCmd  string
	postInstall string
	envKey      string // non-empty = show instructions instead of running a command
}

// buildInstallOptions constructs options dynamically, using machine specs
// to recommend the right Ollama model.
func buildInstallOptions(specs *sysinfo.Specs) []installOption {
	best := specs.BestOllamaModel()
	ollamaPost := fmt.Sprintf("Then run: ollama pull %s", best.Model)
	ollamaDesc := "Local, free, no API key."
	if specs.AnyLocalModelFits() {
		ollamaDesc = fmt.Sprintf("Local, free, no API key. Best for your machine: %s", best.DisplayName)
	} else {
		ollamaDesc = "Local, free — but memory may be too tight. See model list for details."
	}

	return []installOption{
		{
			name:        "Claude Code",
			description: "Best overall. Anthropic's official CLI — needs an account.",
			local:       false,
			installCmd:  "npm install -g @anthropic-ai/claude-code",
			postInstall: "Then run: claude login",
		},
		{
			name:        "Ollama  (local, free)",
			description: ollamaDesc,
			local:       true,
			installCmd:  ollamaInstallCmd(),
			postInstall: ollamaPost,
		},
		{
			name:        "OpenAI Codex",
			description: "OpenAI's coding agent. Needs OPENAI_API_KEY.",
			local:       false,
			installCmd:  "npm install -g @openai/codex",
			postInstall: "Then set: export OPENAI_API_KEY=sk-...",
		},
		{
			name:        "Set an API key",
			description: "You already have a key (Anthropic, OpenAI, Gemini, etc.).",
			local:       false,
			envKey:      "API_KEY",
			postInstall: "Export ANTHROPIC_API_KEY, OPENAI_API_KEY, or GEMINI_API_KEY in your shell.",
		},
	}
}

func ollamaInstallCmd() string {
	if runtime.GOOS == "darwin" {
		return "brew install ollama"
	}
	return "curl -fsSL https://ollama.ai/install.sh | sh"
}

// ── Steps ─────────────────────────────────────────────────────────────────────

type installStep int

const (
	installStepPick   installStep = iota
	installStepOllama             // show model options by hardware tier
	installStepConfirm
	installStepRunning
	installStepDone
)

type installDoneMsg struct{ err error }

// ── Model ─────────────────────────────────────────────────────────────────────

// InstallModel is the Bubbletea model shown when no heads are found.
type InstallModel struct {
	step        installStep
	cursor      int
	specs       *sysinfo.Specs
	options     []installOption
	selected    *installOption
	models      []sysinfo.ModelRecommendation // ollama model choices
	chosenModel sysinfo.ModelRecommendation
	err         bool
	done        bool
}

func NewInstallModel() InstallModel {
	specs := sysinfo.Detect()
	return InstallModel{
		specs:   specs,
		options: buildInstallOptions(specs),
		models:  specs.OllamaRecommendations(),
	}
}

func (m InstallModel) Init() tea.Cmd { return nil }

func (m InstallModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			m.cursor = clamp(m.cursor+1, 0, m.maxCursor())
		case "enter", " ":
			return m.confirm()
		}
	case installDoneMsg:
		m.step = installStepDone
		m.err = msg.err != nil
		m.done = true
		return m, tea.Quit
	}
	return m, nil
}

func (m InstallModel) maxCursor() int {
	switch m.step {
	case installStepPick:
		return len(m.options) - 1
	case installStepOllama:
		return len(m.models) - 1
	case installStepConfirm:
		return 1
	}
	return 0
}

func (m InstallModel) confirm() (tea.Model, tea.Cmd) {
	switch m.step {
	case installStepPick:
		opt := m.options[m.cursor]
		m.selected = &opt

		// If Ollama selected, show hardware-aware model picker first
		if opt.local && strings.Contains(opt.name, "Ollama") {
			m.step = installStepOllama
			m.cursor = 0
			// Set cursor to best fitting model
			for i, r := range m.models {
				if r.Fits {
					m.cursor = i
					break
				}
			}
			return m, nil
		}
		m.step = installStepConfirm
		m.cursor = 0

	case installStepOllama:
		m.chosenModel = m.models[m.cursor]
		// Update the post-install command to use chosen model
		updated := *m.selected
		updated.postInstall = "Then run: ollama pull " + m.chosenModel.Model
		m.selected = &updated
		m.step = installStepConfirm
		m.cursor = 0

	case installStepConfirm:
		if m.cursor == 1 { // "I'll do it myself"
			m.done = true
			return m, tea.Quit
		}
		if m.selected.installCmd == "" || m.selected.envKey != "" {
			m.done = true
			return m, tea.Quit
		}
		m.step = installStepRunning
		return m, runInstallCmd(m.selected.installCmd)
	}
	return m, nil
}

func runInstallCmd(cmd string) tea.Cmd {
	return func() tea.Msg {
		if strings.TrimSpace(cmd) == "" {
			return installDoneMsg{}
		}
		c := exec.Command("sh", "-lc", cmd)
		_, err := c.CombinedOutput()
		return installDoneMsg{err: err}
	}
}

// ── View ──────────────────────────────────────────────────────────────────────

var (
	warnStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))
	successStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("82"))
	codeStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("117")).Background(lipgloss.Color("236")).Padding(0, 1)
	fitsStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("82"))
	noFitStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)

func (m InstallModel) View() string {
	var b strings.Builder
	b.WriteString(sTitle.Render("  Hydra — No AI models found") + "\n\n")

	switch m.step {
	case installStepPick:
		b.WriteString(warnStyle.Render("  Nothing detected on this machine.") + "\n")
		b.WriteString(sDim.Render("  Hydra needs at least one model to work. What would you like to install?\n\n"))
		for i, opt := range m.options {
			tag := ""
			if opt.local {
				tag = fitsStyle.Render(" [local, no API key]")
			}
			row := fmt.Sprintf("  %-22s %s", opt.name, opt.description)
			if i == m.cursor {
				b.WriteString(sSelected.Render("› "+row) + tag + "\n")
			} else {
				b.WriteString(sDim.Render("  "+row) + tag + "\n")
			}
		}

	case installStepOllama:
		b.WriteString(sPrompt.Render("  Detected: ") + m.specs.Summary() + "\n")
		b.WriteString(sDim.Render("  "+m.specs.MemoryNote()) + "\n")

		// Show whether we're using historical data or just the current snapshot
		if h := m.specs.History; h != nil && h.Reliable {
			b.WriteString(sDim.Render(fmt.Sprintf(
				"  Based on %d samples over %d days · typical free: %.1fGB avg, %.1fGB p75\n",
				h.Samples, h.Days, h.AvgFreeGB, h.P75FreeGB,
			)))
		} else {
			b.WriteString(sDim.Render("  Using current snapshot (will improve with more usage history)\n"))
		}

		if w := m.specs.PressureWarning(); w != "" {
			b.WriteString(warnStyle.Render("  ⚠  "+w) + "\n")
		}
		b.WriteString("\n")
		b.WriteString(sPrompt.Render("  Choose an Ollama model — shows memory it will use:\n\n"))

		for i, rec := range m.models {
			var nameStyle, costStyle lipgloss.Style
			var icon string
			if !rec.Fits {
				nameStyle = noFitStyle
				costStyle = noFitStyle
				icon = "✗ "
			} else {
				nameStyle = fitsStyle
				costStyle = sDim
				icon = "✓ "
			}
			name := fmt.Sprintf("%-26s %2dB", rec.DisplayName, rec.SizeB)
			cost := rec.MemoryCost
			if i == m.cursor {
				b.WriteString(sSelected.Render("› "+icon+name+"   "+cost) + "\n")
			} else {
				b.WriteString(nameStyle.Render("  "+icon+name) + "   " + costStyle.Render(cost) + "\n")
			}
		}
		b.WriteString(sDim.Render("\n  ✓ fits · ✗ too large · memory cost = RAM it will occupy\n"))

		// Nothing fits locally — redirect to cloud options
		if !m.specs.AnyLocalModelFits() {
			b.WriteString("\n")
			b.WriteString(warnStyle.Render("  No local model fits your current memory.\n"))
			b.WriteString(sDim.Render("  Recommended alternatives (free tiers available):\n\n"))
			cloudAlts := []struct{ name, note string }{
				{"Claude Code", "Free tier via claude.ai — run: npm install -g @anthropic-ai/claude-code"},
				{"OpenAI Codex", "Free tier — run: npm install -g @openai/codex"},
				{"Cursor", "Free tier IDE with built-in AI — download at cursor.com"},
			}
			for _, alt := range cloudAlts {
				b.WriteString(fitsStyle.Render("  ✓ "+alt.name) + "\n")
				b.WriteString(sDim.Render("    "+alt.note) + "\n")
			}
			b.WriteString(sDim.Render("\n  Press q to go back and choose one of these instead.\n"))
		}

	case installStepConfirm:
		opt := m.selected
		b.WriteString(fmt.Sprintf("  Installing: %s\n\n", sSelected.Render(opt.name)))
		if opt.envKey != "" || opt.installCmd == "" {
			b.WriteString(sPrompt.Render("  Manual steps:\n"))
			b.WriteString("  " + codeStyle.Render(opt.postInstall) + "\n\n")
			b.WriteString(sDim.Render("  After setting up, run: hydra init\n"))
			b.WriteString(sHint.Render("\n  Press enter to exit\n"))
			return b.String()
		}
		b.WriteString(sPrompt.Render("  Install command:\n"))
		b.WriteString("  " + codeStyle.Render(opt.installCmd) + "\n\n")
		b.WriteString(sPrompt.Render("  After install:\n"))
		b.WriteString("  " + codeStyle.Render(opt.postInstall) + "\n\n")

		opts := []string{"  Run it now", "  I'll run it myself"}
		for i, o := range opts {
			if i == m.cursor {
				b.WriteString(sSelected.Render("› "+o) + "\n")
			} else {
				b.WriteString(sDim.Render("  "+o) + "\n")
			}
		}

	case installStepRunning:
		b.WriteString(warnStyle.Render("  Installing — this may take a minute...") + "\n")

	case installStepDone:
		if m.err {
			b.WriteString(sError.Render("  Install failed.\n\n"))
			b.WriteString(sDim.Render("  Try running the command manually, then: hydra init\n"))
		} else {
			b.WriteString(successStyle.Render("  Done!\n\n"))
			if m.selected != nil {
				b.WriteString("  " + codeStyle.Render(m.selected.postInstall) + "\n\n")
			}
			b.WriteString(sDim.Render("  Then run: hydra init\n"))
		}
	}

	b.WriteString(sHint.Render("\n  ↑↓ navigate   enter select   q quit\n"))
	return b.String()
}
