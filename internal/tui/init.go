// SPDX-License-Identifier: MIT

// Package tui contains the Hydra init wizard built with Bubbletea.
package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/probe"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/sysinfo"
)

// ── Styles ────────────────────────────────────────────────────────────────────

var (
	sTitle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	sSelected = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10"))
	sDim      = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	sPrompt   = lipgloss.NewStyle().Foreground(lipgloss.Color("33"))
	sError    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	sHint     = lipgloss.NewStyle().Italic(true).Foreground(lipgloss.Color("240"))
)

// ── Steps ─────────────────────────────────────────────────────────────────────

type step int

const (
	stepCortex  step = iota // user picks the Cortex
	stepTiers               // user confirms auto-assigned tiers
	stepPrivacy             // does the user need local-only routing for PII?
	stepSkills              // which skills to enable
	stepDone                // confirmation screen
)

// ── Model ─────────────────────────────────────────────────────────────────────

// InitModel is the Bubbletea model for the first-run wizard.
type InitModel struct {
	result    *probe.Result
	step      step
	cursor    int
	cortex    *provider.Head
	localOnly bool
	skills    []string
	err       error
}

func NewInitModel(result *probe.Result) InitModel {
	return InitModel{result: result}
}

func (m InitModel) Init() tea.Cmd { return nil }

func (m InitModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	switch key.String() {
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

	return m, nil
}

func (m InitModel) confirm() (tea.Model, tea.Cmd) {
	switch m.step {
	case stepCortex:
		h := m.result.Heads[m.cursor]
		m.cortex = &h
		m.step = stepTiers
		m.cursor = 0

	case stepTiers:
		m.step = stepPrivacy
		m.cursor = 0

	case stepPrivacy:
		m.localOnly = m.cursor == 0
		m.step = stepSkills
		m.skills = defaultSkills(m.cortex)
		m.cursor = 0

	case stepSkills:
		m.step = stepDone
		if err := m.save(); err != nil {
			m.err = err
		}
		return m, tea.Quit
	}

	return m, nil
}

func (m InitModel) maxCursor() int {
	switch m.step {
	case stepCortex:
		return len(m.result.Heads) - 1
	case stepPrivacy:
		return 1
	}
	return 0
}

// ── View ──────────────────────────────────────────────────────────────────────

func (m InitModel) View() string {
	var b strings.Builder
	b.WriteString(sTitle.Render("  Hydra — First Run Setup") + "\n\n")

	switch m.step {
	case stepCortex:
		m.viewCortex(&b)
	case stepTiers:
		m.viewTiers(&b)
	case stepPrivacy:
		m.viewPrivacy(&b)
	case stepSkills:
		m.viewSkills(&b)
	case stepDone:
		m.viewDone(&b)
	}

	b.WriteString(sHint.Render("\n  ↑↓ navigate   enter select   q quit\n"))
	return b.String()
}

func (m InitModel) viewCortex(b *strings.Builder) {
	b.WriteString(sPrompt.Render("  Which model should be your Cortex (main brain)?\n\n"))
	for i, h := range m.result.Heads {
		suffix := ""
		if i == 0 {
			suffix = sDim.Render("  ← recommended")
		}
		row := fmt.Sprintf("  %-28s  score:%-3d  %-5s", h.Name, h.CapScore, h.Source)
		if i == m.cursor {
			b.WriteString(sSelected.Render("› "+row) + suffix + "\n")
		} else {
			b.WriteString(sDim.Render("  "+row) + suffix + "\n")
		}
	}
}

func (m InitModel) viewTiers(b *strings.Builder) {
	b.WriteString(sPrompt.Render("  Auto-assigned Heads by default score bands:\n\n"))
	tiers := buildTiers(m.result.Heads, m.cortex)
	for _, t := range tiers {
		b.WriteString(fmt.Sprintf("  %-12s → %s\n", t.Name, strings.Join(t.Heads, ", ")))
	}

	// Show hardware note if any local heads are present
	hasLocal := false
	for _, h := range m.result.Heads {
		if h.LocalOnly {
			hasLocal = true
			break
		}
	}
	if hasLocal {
		specs := sysinfo.Detect()
		best := specs.BestOllamaModel()
		b.WriteString(sDim.Render(fmt.Sprintf(
			"\n  Hardware: %s\n  Best local model for your machine: %s\n",
			specs.Summary(), best.DisplayName,
		)))
	}

	b.WriteString(sPrompt.Render("\n  Press enter to confirm\n"))
}

func (m InitModel) viewPrivacy(b *strings.Builder) {
	b.WriteString(sPrompt.Render("  Should prompts that look like PII stay on local Heads only?\n\n"))
	opts := []string{
		"Yes — keep likely PII on local Heads only",
		"No  — use any Head",
	}
	for i, opt := range opts {
		if i == m.cursor {
			b.WriteString(sSelected.Render("  › "+opt) + "\n")
		} else {
			b.WriteString(sDim.Render("    "+opt) + "\n")
		}
	}
}

func (m InitModel) viewSkills(b *strings.Builder) {
	b.WriteString(sPrompt.Render("  Skills enabled for your setup:\n\n"))
	for _, s := range m.skills {
		b.WriteString("    ✓ " + s + "\n")
	}
	b.WriteString(sPrompt.Render("\n  Press enter to finish\n"))
}

func (m InitModel) viewDone(b *strings.Builder) {
	if m.err != nil {
		b.WriteString(sError.Render(fmt.Sprintf("  ✗ Setup failed: %v\n", m.err)))
		return
	}
	b.WriteString(sSelected.Render("  ✓ Hydra is ready\n\n"))
	b.WriteString(fmt.Sprintf("  Cortex : %s\n", m.cortex.Name))
	b.WriteString(fmt.Sprintf("  Config : %s\n", config.Path()))
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func (m InitModel) save() error {
	tiers := buildTiers(m.result.Heads, m.cortex)
	cfg := &config.Config{
		Cortex: m.cortex.ID,
		Tiers:  tiers,
		Skills: m.skills,
	}
	if m.localOnly {
		cfg.Policies = map[string]config.Policy{
			"pii": {Action: "local-only"},
		}
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	return exportToRoutingYAML(tiers, m.cortex)
}

// exportToRoutingYAML appends a discovered_heads block to registry/routing.yaml
// so that route.sh and human operators can see what hydra init found.
// Any existing auto-discovered block is replaced.
func exportToRoutingYAML(tiers []config.Tier, cortex *provider.Head) error {
	routingPath := filepath.Join(config.ScriptHome(), "registry", "routing.yaml")

	existing, err := os.ReadFile(routingPath)
	if err != nil {
		return nil // routing.yaml not present in this install layout — skip silently
	}

	// Strip any previously written discovered block.
	const marker = "\n# ── Auto-discovered by hydra init"
	base := string(existing)
	if idx := strings.Index(base, marker); idx != -1 {
		base = base[:idx]
	}

	// Build the new discovered block.
	var b strings.Builder
	b.WriteString(marker)
	b.WriteString(" ────────────────────────────────────────\n")
	b.WriteString(fmt.Sprintf("# Generated: %s\n", time.Now().UTC().Format("2006-01-02T15:04:05Z")))
	b.WriteString("# Re-run `hydra init` to refresh.\n")
	b.WriteString("discovered_heads:\n")
	if cortex != nil {
		b.WriteString(fmt.Sprintf("  cortex: %s\n", cortex.ID))
	}
	for _, t := range tiers {
		b.WriteString(fmt.Sprintf("  %s: [%s]\n", t.Name, strings.Join(t.Heads, ", ")))
	}

	return os.WriteFile(routingPath, []byte(strings.TrimRight(base, "\n")+"\n"+b.String()), 0o644)
}

// buildTiers assigns Heads to named tiers by default score bands.
func buildTiers(heads []provider.Head, cortex *provider.Head) []config.Tier {
	bands := []struct {
		name string
		min  int
	}{
		{"expert", 85},
		{"complex", 75},
		{"standard", 65},
		{"simple", 55},
		{"local", 0},
	}

	buckets := map[string][]string{}
	for _, h := range heads {
		if cortex != nil && h.ID == cortex.ID {
			continue
		}
		for _, b := range bands {
			if h.CapScore >= b.min {
				buckets[b.name] = append(buckets[b.name], h.ID)
				break
			}
		}
	}

	var tiers []config.Tier
	for _, b := range bands {
		if ms := buckets[b.name]; len(ms) > 0 {
			tiers = append(tiers, config.Tier{Name: b.name, Heads: ms})
		}
	}
	return tiers
}

func defaultSkills(cortex *provider.Head) []string {
	base := []string{"code-gen", "review", "benchmark"}
	if cortex != nil && !cortex.LocalOnly {
		base = append(base, "swarm", "cost-stats")
	}
	return base
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
