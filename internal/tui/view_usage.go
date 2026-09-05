// SPDX-License-Identifier: MIT

package tui

// view_usage.go — view 4: spend tiles, the by-model/tier/day breakdown, and
// the context budget panel. Every figure on this screen must cohere with every
// other; a value that cannot be computed renders "—", never an invention.

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/ankit373/hydra/internal/budget"
	"github.com/ankit373/hydra/internal/cost"
)

func (m Cockpit) viewUsage(w, h int) string {
	out, _ := ckScrollLines(strings.Split(m.usageBody(w, h), "\n"), m.usageOff, h)
	return strings.Join(out, "\n")
}

// usageBody composes the tiles, the breakdown and the context-budget panel.
// The view scrolls it as one document, so the breakdown lists every model
// instead of paging inside a body that also pages (#630).
func (m Cockpit) usageBody(w, h int) string {
	tiles := lipgloss.JoinHorizontal(lipgloss.Top,
		ckBoxS.Render(m.usageToday()), " ",
		ckBoxS.Render(m.usageMonth()), " ",
		ckBoxS.Render(m.usageSaved()))
	if lipgloss.Width(tiles) > w {
		// Too narrow for three boxes: the same three facts as compact lines,
		// so nothing is silently dropped.
		tiles = m.usageTilesCompact(w)
	}

	breakdown := ckBoxS.Render(m.usageBreakdown())
	budgetBox := ckBoxS.Render(m.usageContextBudget())
	bottom := lipgloss.JoinHorizontal(lipgloss.Top, breakdown, " ", budgetBox)
	if lipgloss.Width(bottom) > w {
		// Stack rather than drop a panel — and the stack scrolls, so a short
		// terminal hides nothing behind "enlarge the terminal" (#630).
		bottom = lipgloss.JoinVertical(lipgloss.Left, breakdown, budgetBox)
	}
	return lipgloss.JoinVertical(lipgloss.Left, tiles, bottom)
}

// usageLines is the body's height at the current width — what a scroll offset
// is clamped against.
func (m Cockpit) usageLines() int {
	return len(strings.Split(m.usageBody(max(1, m.w), max(1, m.h)), "\n"))
}

// usageTilesCompact is the tiles' narrow form: one line per tile, identical
// facts, no boxes.
func (m Cockpit) usageTilesCompact(w int) string {
	runs := len(m.runsToday)
	today := ckLabelS.Render("TODAY ") + ckCheapS.Bold(true).Render(fmt.Sprintf("$%.4f", m.spend)) +
		ckDimS.Render(fmt.Sprintf(" · %d run%s · %d request%s · est, never billed",
			runs, plural(runs), m.metrics.todayReqs, plural(m.metrics.todayReqs)))
	month := ckLabelS.Render("MONTH ") + ckCheapS.Render(fmt.Sprintf("$%.4f", m.metrics.monthUSD)) +
		ckDimS.Render(fmt.Sprintf(" · %d request%s (UTC month)", m.metrics.monthReqs, plural(m.metrics.monthReqs)))
	saved, base := m.metrics.savedTodayUSD, m.metrics.baseTodayUSD
	savedLine := ckLabelS.Render("SAVED ") + ckFaintS.Render("— · no priced requests today")
	switch {
	case base > 0 && saved > 0:
		savedLine = ckLabelS.Render("SAVED ") + ckCheapS.Render(fmt.Sprintf("$%.4f (%.0f%%)", saved, saved/base*100)) +
			ckDimS.Render(fmt.Sprintf(" vs all-tier-1 $%.4f", base))
	case base > 0:
		savedLine = ckLabelS.Render("SAVED ") + ckDimS.Render(fmt.Sprintf("$0.0000 (0%%) · tier-1 rate $%.4f", base))
	}
	return ckFrame(strings.Join([]string{" " + today, " " + month, " " + savedLine, ""}, "\n"), w, 4)
}

// usageToday: today's spend, run/request counts, and the token-provenance
// honesty line (est vs actual).
func (m Cockpit) usageToday() string {
	runs := len(m.runsToday)
	honesty := "no requests yet"
	if m.metrics.todayReqs > 0 {
		a, e := m.metrics.todayActualTok, m.metrics.todayEstTok
		switch {
		case e == 0:
			honesty = fmt.Sprintf("%s tokens, provider-reported", ckTokens(a))
		case a == 0:
			honesty = fmt.Sprintf("%s tokens, all estimated", ckTokens(e))
		default:
			honesty = fmt.Sprintf("%s actual · %s estimated tokens", ckTokens(a), ckTokens(e))
		}
	}
	return ckLabelS.Render("TODAY") + "\n\n " +
		ckCheapS.Bold(true).Render(fmt.Sprintf("$%.4f", m.spend)) + "\n " +
		ckDimS.Render(fmt.Sprintf("%d run%s · %d request%s", runs, plural(runs), m.metrics.todayReqs, plural(m.metrics.todayReqs))) + "\n " +
		ckFaintS.Render(honesty) + "\n " +
		ckFaintS.Render("estimated, never billed")
}

func (m Cockpit) usageMonth() string {
	return ckLabelS.Render("THIS MONTH") + "\n\n " +
		ckCheapS.Bold(true).Render(fmt.Sprintf("$%.4f", m.metrics.monthUSD)) + "\n " +
		ckDimS.Render(fmt.Sprintf("%d request%s", m.metrics.monthReqs, plural(m.metrics.monthReqs))) + "\n " +
		ckFaintS.Render("calendar month, UTC") + "\n "
}

// usageSaved: what today's work would have cost had every request hit tier 1.
// The three figures shown (saved $, %, and the tier-1 total) all derive from
// the same two numbers, so they cannot contradict each other.
func (m Cockpit) usageSaved() string {
	title := ckLabelS.Render("SAVED vs ALL-FRONTIER") + "\n\n "
	saved, base := m.metrics.savedTodayUSD, m.metrics.baseTodayUSD
	switch {
	case base <= 0:
		return title + ckFaintS.Render("—") + "\n " +
			ckFaintS.Render("no priced requests today") + "\n " +
			ckFaintS.Render("nothing to compare yet") + "\n "
	case saved <= 0:
		return title + ckDimS.Render("$0.0000 (0%)") + "\n " +
			ckDimS.Render(fmt.Sprintf("tier-1 rate: $%.4f", base)) + "\n " +
			ckFaintS.Render("today's spend is not below it") + "\n "
	}
	return title + ckCheapS.Bold(true).Render(fmt.Sprintf("$%.4f (%.0f%%)", saved, saved/base*100)) + "\n " +
		ckDimS.Render(fmt.Sprintf("all-tier-1 would be $%.4f", base)) + "\n " +
		ckFaintS.Render("same tokens, tier-1 rates") + "\n "
}

// ckUsageNameW / ckUsageBarW are the breakdown's fixed column budgets.
const (
	ckUsageNameW = 20
	ckUsageBarW  = 16
)

// usageRows is the active grouping's data — also what the scroll offset is
// clamped against.
func (m Cockpit) usageRows() []cost.GroupRow {
	switch m.usageGroup {
	case 't':
		return m.metrics.byTierToday
	case 'd':
		return m.metrics.byDay
	default:
		return m.metrics.byModelToday
	}
}

// usageBreakdown is the BY MODEL / BY TIER / BY DAY bar list (m/t/d), shown
// through a rowCap-line scroll window (pgup/pgdn · wheel).
func (m Cockpit) usageBreakdown() string {
	rows := m.usageRows()
	title := map[byte]string{'t': "BY TIER · today", 'd': "BY DAY · last 14 days"}[m.usageGroup]
	if title == "" {
		title = "BY MODEL · today"
	}
	var b strings.Builder
	b.WriteString(ckLabelS.Render(title))

	var maxCost, totalCost float64
	maxCalls := 0
	for _, r := range rows {
		if r.EstCostUSD > maxCost {
			maxCost = r.EstCostUSD
		}
		totalCost += r.EstCostUSD
		if r.Calls > maxCalls {
			maxCalls = r.Calls
		}
	}
	// An all-local day has nothing to scale a cost bar by — scale by calls
	// instead and say so, rather than rendering a blank chart.
	byCalls := totalCost == 0 && len(rows) > 0
	if byCalls {
		b.WriteString(ckDimS.Render(" · bars by calls — everything was free"))
	}
	b.WriteString("\n\n")

	if len(rows) == 0 {
		b.WriteString(ckFaintS.Render(" no requests recorded") + "\n")
		return b.String()
	}
	names := make([]string, len(rows))
	for i, r := range rows {
		names[i] = ckSafe(r.Key)
	}
	names = ckDistinctTruncate(names, ckUsageNameW)

	lines := make([]string, len(rows))
	for i, r := range rows {
		frac := 0.0
		switch {
		case byCalls && maxCalls > 0:
			frac = float64(r.Calls) / float64(maxCalls)
		case maxCost > 0:
			frac = r.EstCostUSD / maxCost
		}
		fill := int(frac * ckUsageBarW)
		if fill > ckUsageBarW {
			fill = ckUsageBarW
		}
		bar := ckCyanS.Render(strings.Repeat("▮", fill)) + ckFaintS.Render(strings.Repeat("▯", ckUsageBarW-fill))
		costStr := fmt.Sprintf("$%.4f", r.EstCostUSD)
		if m.usageGroup == 'm' && m.metrics.localModels[r.Key] {
			costStr = "local · free"
		}
		lines[i] = " " + ckCell(names[i], ckUsageNameW) + " " + bar + " " +
			ckDimS.Render(ckRCell(costStr, 12)) + " " +
			ckFaintS.Render(ckRCell(fmt.Sprintf("%d call%s", r.Calls, plural(r.Calls)), 9))
	}
	b.WriteString(strings.Join(lines, "\n"))
	return b.String()
}

// usageContextBudget is the claude_pct panel: band bar, burn rate +
// first-passage risk, and the band legend.
func (m Cockpit) usageContextBudget() string {
	var b strings.Builder
	b.WriteString(ckLabelS.Render("CONTEXT BUDGET") + ckDimS.Render(" · orchestrator") + "\n\n")

	if !m.pctKnown {
		b.WriteString(ckFaintS.Render(" no context data yet — logs/state.json") + "\n" +
			ckFaintS.Render(" has no claude_pct to read") + "\n")
	} else {
		mode := budget.ModeFor(m.claudePct)
		b.WriteString(" " + ckBar(m.claudePct, 20) +
			ckDimS.Render(fmt.Sprintf("  %d%% · ", m.claudePct)) +
			ckBandStyle(m.claudePct).Render(mode.String()) + "\n")
		if len(m.pctHist) < 2 {
			b.WriteString(" " + ckFaintS.Render("burn — · not enough history for a rate") + "\n")
		} else {
			burn, risk := budget.RiskFromHistory(m.pctHist)
			b.WriteString(" " + ckDimS.Render(fmt.Sprintf("burn %+.1f%%/step · risk of hitting 80%%: ", burn)) +
				ckBandStyle(m.claudePct).Render(fmt.Sprintf("%.0f%%", risk*100)) + "\n")
		}
	}
	b.WriteString("\n " + ckFaintS.Render("bands 0–49 normal · 50 compact · 65 caution") + "\n " +
		ckFaintS.Render("      70 downgrade · 75 critical · 80 stop"))
	return b.String()
}
