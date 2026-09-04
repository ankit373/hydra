// SPDX-License-Identifier: MIT

package tui

// chrome.go — the shell every view sits in: the header (brand · tabs ·
// session/context), and the one-line status bar (view keys left, facts right).

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/ankit373/hydra/internal/budget"
)

// header is the shared top line: brand · tabs · session cost + context gauge.
// Tabs degrade to just the active chip when the terminal is too narrow.
func (m Cockpit) header() string {
	left := " " + ckWordmark("HYDRA") + " "
	right := ckDimS.Render(fmt.Sprintf("session $%.4f · context ", m.sessionUSD)) + m.contextGauge() + " "

	tabs := make([]string, len(ckViewNames))
	for i, name := range ckViewNames {
		if i == m.view {
			tabs[i] = ckChipS.Render(name)
		} else {
			tabs[i] = ckTabS.Render(name)
		}
	}
	full := strings.Join(tabs, "")
	gap := m.w - lipgloss.Width(left) - lipgloss.Width(full) - lipgloss.Width(right)
	if gap < 1 {
		full = ckChipS.Render(ckViewName(m.view))
		gap = m.w - lipgloss.Width(left) - lipgloss.Width(full) - lipgloss.Width(right)
	}
	if gap < 1 {
		gap = 1
	}
	return ckFrame(left+full+strings.Repeat(" ", gap)+right, max(1, m.w), 1)
}

// contextGauge renders claude_pct as an eight-cell gauge. Unknown state (no
// state.json yet) renders "—", never a fabricated 0%.
func (m Cockpit) contextGauge() string {
	if !m.pctKnown {
		return ckFaintS.Render("—")
	}
	const cells = 8
	fill := m.claudePct * cells / 100
	if fill > cells {
		fill = cells
	}
	if fill < 0 {
		fill = 0
	}
	return ckBandStyle(m.claudePct).Render(strings.Repeat("▰", fill)) +
		ckFaintS.Render(strings.Repeat("▱", cells-fill)) +
		ckDimS.Render(fmt.Sprintf(" %d%%", m.claudePct))
}

// ckBandStyle colours a claude_pct by its budget band — the band itself comes
// from internal/budget, never a re-implemented threshold table.
func ckBandStyle(pct int) lipgloss.Style {
	switch budget.ModeFor(pct).String() {
	case "critical", "emergency":
		return ckExpS
	case "warning", "caution", "compact":
		return ckMidS
	default:
		return ckCheapS
	}
}

// statusBar is one line: the active view's keys (from ckKeymap) plus
// "? shortcuts" on the left, view-specific facts (or a transient flash) right.
// At narrow widths trailing keys are dropped — "? shortcuts" survives, and the
// glossary still documents everything that was dropped.
func (m Cockpit) statusBar() string {
	right := m.statusFact()
	if m.flash != "" {
		right = ckMidS.Render(m.flash)
	} else {
		right = ckDimS.Render(right)
	}
	shortcuts := ckAquaS.Render("?") + ckFaintS.Render(" shortcuts")

	segs := ckBarBindings(m.view)
	var left string
	for keep := len(segs); keep >= 0; keep-- {
		var b strings.Builder
		b.WriteString(" ")
		for _, s := range segs[:keep] {
			b.WriteString(ckAquaS.Render(s.keys) + ckFaintS.Render(" "+s.does+"  "))
		}
		b.WriteString(shortcuts)
		left = b.String()
		if lipgloss.Width(left)+lipgloss.Width(right)+2 <= m.w || keep == 0 {
			break
		}
	}
	gap := m.w - lipgloss.Width(left) - lipgloss.Width(right) - 1
	if gap < 1 {
		gap = 1
	}
	return ckFrame(left+strings.Repeat(" ", gap)+right+" ", max(1, m.w), 1)
}

// statusFact is the view-specific right-hand fact.
func (m Cockpit) statusFact() string {
	switch m.view {
	case ckViewChat:
		s := "mode " + m.mode + " · " + m.override.label()
		if m.pinnedTier > 0 {
			s += fmt.Sprintf(" · pinned T%d", m.pinnedTier)
		}
		if att := m.attentionFact(); att != "" {
			s = att + " · " + s
		}
		return s
	case ckViewAgents:
		live, done := m.agentCounts()
		return fmt.Sprintf("%d live · %d finished today", live, done)
	case ckViewModels:
		if m.scanning {
			return "scanning…"
		}
		return fmt.Sprintf("scanned %s ago · r rescan", time.Since(m.scannedAt).Round(time.Second))
	case ckViewActivity:
		n := len(m.activityRuns())
		s := fmt.Sprintf("%d run%s today", n, plural(n))
		if m.actFailOnly {
			s += " · failures only"
		}
		return s
	case ckViewUsage:
		return "grouped by " + m.usageGroupName()
	case ckViewAudit:
		return m.auditFact()
	}
	return ""
}

func (m Cockpit) usageGroupName() string {
	switch m.usageGroup {
	case 't':
		return "tier"
	case 'd':
		return "day"
	default:
		return "model"
	}
}

// ckWordmark paints the brand with the cyan→violet→magenta ramp.
func ckWordmark(s string) string {
	rs := []rune(s)
	n := len(rs)
	var b strings.Builder
	for i, r := range rs {
		t := 0.0
		if n > 1 {
			t = float64(i) / float64(n-1)
		}
		b.WriteString(lipgloss.NewStyle().Foreground(ckLerpHex(t)).Bold(true).Render(string(r)))
	}
	return b.String()
}

// ckLerpHex blends cyan → violet → magenta across t∈[0,1].
func ckLerpHex(t float64) lipgloss.Color {
	var a, b [3]int
	if t < 0.5 {
		a, b, t = [3]int{0x2A, 0xF0, 0xE0}, [3]int{0x8B, 0x5C, 0xF6}, t/0.5
	} else {
		a, b, t = [3]int{0x8B, 0x5C, 0xF6}, [3]int{0xE8, 0x52, 0xC8}, (t-0.5)/0.5
	}
	l := func(x, y int) int { return int(float64(x) + float64(y-x)*t) }
	return lipgloss.Color(fmt.Sprintf("#%02X%02X%02X", l(a[0], b[0]), l(a[1], b[1]), l(a[2], b[2])))
}
