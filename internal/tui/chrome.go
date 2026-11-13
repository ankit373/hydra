// SPDX-License-Identifier: MIT

package tui

// chrome.go, the shell every view sits in: the header (name · tabs · what
// needs watching · spend today), and the one-line status bar (view keys left, facts right).

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/ankit373/hydra/internal/budget"
)

// header is the shared top line: the name, the views, then what needs
// watching on every screen: how many runs are running, waiting and failed,
// and what today has cost. Narrow terminals give up the tab names first, then
// the counts; the spend stays.
func (m Cockpit) header() string {
	left := " " + ckWordmark("hydra") + "  "
	spend := ckDimS.Render("today ") + ckMoneyS.Render(ckUSD(m.spend)) + " "
	counts := m.headerCounts()

	tabs := make([]string, len(ckViewNames))
	for i, name := range ckViewNames {
		if i == m.view {
			tabs[i] = ckActiveTab(name)
		} else {
			tabs[i] = ckTabS.Render(name)
		}
	}
	chip := ckActiveTab(ckViewName(m.view))
	// The chip alone would say nothing about the other views, so the compact
	// form keeps the position and the key that moves between them (#630).
	compact := chip + ckFaintS.Render(fmt.Sprintf(" %d/%d tab", m.view+1, ckViewCount()))
	tries := []struct{ tabs, counts string }{
		{strings.Join(tabs, ""), counts},
		{compact, counts},
		{chip, counts},
		{chip, ""},
	}
	var line string
	for _, try := range tries {
		right := spend
		if try.counts != "" {
			right = try.counts + "   " + spend
		}
		gap := m.w - lipgloss.Width(left) - lipgloss.Width(try.tabs) - lipgloss.Width(right)
		line = left + try.tabs + strings.Repeat(" ", max(1, gap)) + right
		if gap >= 1 {
			break
		}
	}
	return ckFrame(line, max(1, m.w), 1)
}

// headerCounts is "● 2 running  ! 1 waiting  ✗ 1 failed", zero counts left out.
func (m Cockpit) headerCounts() string {
	running, waiting, failed := m.watchCounts()
	var parts []string
	add := func(n int, s ckWatchState, word string) {
		if n > 0 {
			parts = append(parts, ckWatchGlyph(s)+ckDimS.Render(fmt.Sprintf(" %d %s", n, word)))
		}
	}
	add(running, ckStRunning, "running")
	add(waiting, ckStWaiting, "waiting")
	add(failed, ckStFailed, "failed")
	return strings.Join(parts, "  ")
}

// ckBandStyle colours a claude_pct by its budget band, the band itself comes
// from internal/budget, never a re-implemented threshold table.
func ckBandStyle(pct int) lipgloss.Style {
	switch budget.ModeFor(pct).String() {
	case "critical", "emergency":
		return ckBadS
	case "warning", "caution", "compact":
		return ckWarnS
	default:
		return ckOkS
	}
}

// statusBar is one line: the active view's keys (from ckKeymap), then
// "? keys" and, outside chat, "q quit"; view-specific facts (or a transient
// flash) on the right. At narrow widths trailing keys are dropped first, and
// the key list still documents everything that was dropped.
func (m Cockpit) statusBar() string {
	right := m.statusFact()
	if m.flash != "" {
		right = ckWarnS.Render(ckSafe(m.flash))
	} else {
		right = ckDimS.Render(right)
	}
	if m.mouseOff {
		right = ckWarnS.Render("select mode, ctrl+s ends") + ckDimS.Render(" · ") + right
	}
	hint := func(k, does string) string { return ckAccentS.Render(k) + ckFaintS.Render(" "+does+"  ") }
	tail := hint("?", "keys")
	if m.view != ckViewChat {
		tail += hint("q", "quit")
	}

	segs := ckBarBindings(m.view)
	var left string
	for keep := len(segs); keep >= 0; keep-- {
		var b strings.Builder
		b.WriteString(" ")
		for _, s := range segs[:keep] {
			b.WriteString(hint(s.keys, s.does))
		}
		b.WriteString(tail)
		left = strings.TrimRight(b.String(), " ")
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
	case ckViewWatch:
		if !m.pctKnown {
			return ""
		}
		return fmt.Sprintf("context %d%% used", m.claudePct)
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

// ckWordmark is the brand name in plain bold: the colour belongs to the mark,
// never spread across the letters.
func ckWordmark(s string) string {
	return lipgloss.NewStyle().Bold(true).Render(s)
}
