// SPDX-License-Identifier: MIT

package tui

// view_usage.go, view 4: spend tiles, the by-model/tier/day breakdown, and
// the context budget panel. Every figure on this screen must cohere with every
// other; a value that cannot be computed renders "—", never an invention.

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

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
		ckBoxS.Render(m.usagePerPassed()))
	if lipgloss.Width(tiles) > w {
		// Too narrow for three boxes: the same three facts as compact lines,
		// so nothing is silently dropped.
		tiles = m.usageTilesCompact(w)
	}

	breakdown := ckBoxS.Render(m.usageBreakdown())
	budgetBox := ckBoxS.Render(m.usageContextBudget())
	bottom := lipgloss.JoinHorizontal(lipgloss.Top, breakdown, " ", budgetBox)
	if lipgloss.Width(bottom) > w {
		// Stack rather than drop a panel, and the stack scrolls, so a short
		// terminal hides nothing behind "enlarge the terminal" (#630).
		bottom = lipgloss.JoinVertical(lipgloss.Left, breakdown, budgetBox)
	}
	return lipgloss.JoinVertical(lipgloss.Left, tiles, bottom)
}

// usageLines is the body's height at the current width, what a scroll offset
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
	passLine := ckLabelS.Render("PER PASS ") + ckFaintS.Render("— · "+m.checkedSoFar())
	if m.metrics.perPassedOK {
		passLine = ckLabelS.Render("PER PASS ") + ckCheapS.Render(ckPassLine(m.metrics.perPassed.Routed))
		if k, ok := ckBestKind(m.metrics.perPassed); ok {
			passLine += ckDimS.Render(fmt.Sprintf(" · on %s routed %s, best single %s %s",
				ckSafe(k.Kind), ckPassLine(k.Routed), ckSafe(k.Best), ckPassLine(k.BestRecord)))
		}
	}
	return ckFrame(strings.Join([]string{" " + today, " " + month, " " + passLine, ""}, "\n"), w, 4)
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

// usagePerPassed is cost per passed answer for the routing used and, on the
// busiest kind of work, the routing beside the best single model on that same
// kind. Below cost.MinChecked checked answers it shows no figure at all.
func (m Cockpit) usagePerPassed() string {
	title := ckLabelS.Render("PER PASSED ANSWER") + "\n\n "
	if !m.metrics.perPassedOK {
		return title + ckFaintS.Render("—") + "\n " +
			ckFaintS.Render("not enough checked answers yet") + "\n " +
			ckFaintS.Render(m.checkedSoFar()) + "\n "
	}
	rep := m.metrics.perPassed
	out := title + ckCheapS.Bold(true).Render(ckPassLine(rep.Routed)) + "\n " +
		ckDimS.Render(fmt.Sprintf("routed · %d checked, last %d days", rep.Routed.Answers, ckPassedDays)) + "\n "
	k, ok := ckBestKind(rep)
	if !ok {
		return out + ckFaintS.Render("no single model has enough checked") + "\n " +
			ckFaintS.Render("answers on one task type yet")
	}
	return out + ckDimS.Render(fmt.Sprintf("on %s, routed:", ckSafe(k.Kind))) + "\n " +
		ckDimS.Render(ckPassLine(k.Routed)) + "\n " +
		ckDimS.Render(fmt.Sprintf("best single model, %s:", ckSafe(k.Best))) + "\n " +
		ckDimS.Render(ckPassLine(k.BestRecord))
}

// checkedSoFar says how far a short history is from the minimum.
func (m Cockpit) checkedSoFar() string {
	return fmt.Sprintf("%d of %d checked, last %d days", m.metrics.checked, cost.MinChecked, ckPassedDays)
}

// ckPassLine is a record's cost per passed answer, always beside its pass rate
// and 95% interval, so a cheap route that fails more cannot look like a win.
func ckPassLine(r cost.PassRecord) string {
	per := "no passes"
	if c, ok := r.PerPassed(); ok {
		per = fmt.Sprintf("$%.4f", c)
	}
	return fmt.Sprintf("%s · %.0f%% passed (%.0f–%.0f%%)", per, r.Rate()*100, r.Lo*100, r.Hi*100)
}

// ckBestKind is the busiest kind of work that has a best single model.
func ckBestKind(rep cost.PassedReport) (cost.KindRecord, bool) {
	for _, k := range rep.Kinds {
		if k.Best != "" {
			return k, true
		}
	}
	return cost.KindRecord{}, false
}

// ckUsageNameW / ckUsageBarW are the breakdown's fixed column budgets.
const (
	ckUsageNameW = 20
	ckUsageBarW  = 16
)

// usageRows is the active grouping's data, also what the scroll offset is
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
	// An all-local day has nothing to scale a cost bar by, scale by calls
	// instead and say so, rather than rendering a blank chart.
	byCalls := totalCost == 0 && len(rows) > 0
	if byCalls {
		b.WriteString(ckDimS.Render(" · bars by calls, everything was free"))
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
		b.WriteString(ckFaintS.Render(" no context data yet, logs/state.json") + "\n" +
			ckFaintS.Render(" has no claude_pct to read") + "\n")
	} else {
		mode := budget.ModeFor(m.claudePct)
		b.WriteString(" " + ckBar(m.claudePct, 20) +
			ckDimS.Render(fmt.Sprintf("  %d%% · ", m.claudePct)) +
			ckBandStyle(m.claudePct).Render(mode.String()) + "\n")
		if len(m.pctHist) < 2 {
			b.WriteString(" " + ckFaintS.Render("burn, · not enough history for a rate") + "\n")
		} else {
			burn, risk := budget.RiskFromHistory(m.pctHist)
			b.WriteString(" " + ckDimS.Render(fmt.Sprintf("burn %+.1f%%/step · risk of hitting 80%%: ", burn)) +
				ckBandStyle(m.claudePct).Render(fmt.Sprintf("%.0f%%", risk*100)) + "\n")
		}
	}
	b.WriteString("\n " + ckFaintS.Render("bands 0-49 normal · 50 compact · 65 caution") + "\n " +
		ckFaintS.Render("      70 downgrade · 75 critical · 80 stop"))
	return b.String()
}
