// SPDX-License-Identifier: MIT

package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// The header: brand, the six tab labels with the active one chipped, and the
// session/context readout — one line at any width, degrading to the active
// chip alone when the tab strip cannot fit.
func TestHeader_TabsSessionAndContext(t *testing.T) {
	m := testCockpit()
	m.view = ckViewModels
	m.pctKnown, m.claudePct = true, 25
	m.w = 120

	got := stripANSI(m.header())
	for _, want := range append(append([]string{}, ckViewNames...), "HYDRA", "session $0.0000", "context", "25%") {
		if !strings.Contains(got, want) {
			t.Errorf("header missing %q: %q", want, got)
		}
	}
	if strings.Contains(got, "\n") {
		t.Error("the header wrapped")
	}

	// Narrow: the strip goes, but the position and the key that moves between
	// views stay — otherwise nothing at 80 columns says they exist (#630).
	m.w = 80
	got = stripANSI(m.header())
	if strings.Contains(got, "activity") {
		t.Errorf("80-col header still renders the whole strip: %q", got)
	}
	for _, want := range []string{"models", "3/6", "tab"} {
		if !strings.Contains(got, want) {
			t.Errorf("80-col header missing %q: %q", want, got)
		}
	}

	// Narrower still: only the active view survives, the frame still holds.
	m.w = 46
	got = stripANSI(m.header())
	if !strings.Contains(got, "models") {
		t.Errorf("narrow header lost the active view: %q", got)
	}
	if strings.Contains(got, "activity") {
		t.Errorf("narrow header still renders the whole strip: %q", got)
	}
	for _, w := range []int{0, 1, 10, 40, 80, 200} {
		m.w = w
		line := m.header()
		if strings.TrimSpace(stripANSI(line)) == "" {
			t.Errorf("header rendered nothing at width %d", w)
		}
		if lipgloss.Width(line) > max(1, w) {
			t.Errorf("header exceeds width %d: %d cells", w, lipgloss.Width(line))
		}
	}
}

// The context gauge: unknown renders as a dash, never 0%; the fill and colour
// track claude_pct through the budget bands.
func TestContextGauge(t *testing.T) {
	m := testCockpit()
	m.pctKnown = false
	if got := stripANSI(m.contextGauge()); got != "—" {
		t.Errorf("unknown context = %q, want —", got)
	}
	m.pctKnown = true
	for _, tt := range []struct {
		pct  int
		fill string
	}{
		{0, "▱▱▱▱▱▱▱▱"},
		{50, "▰▰▰▰▱▱▱▱"},
		{100, "▰▰▰▰▰▰▰▰"},
		{200, "▰▰▰▰▰▰▰▰"}, // clamped
	} {
		m.claudePct = tt.pct
		if got := stripANSI(m.contextGauge()); !strings.Contains(got, tt.fill) {
			t.Errorf("gauge at %d%% = %q, want %q", tt.pct, got, tt.fill)
		}
	}
	// The band colour comes from internal/budget, not an inline table.
	if ckBandStyle(10).GetForeground() == ckBandStyle(77).GetForeground() {
		t.Error("normal and critical bands are coloured identically")
	}
	if ckBandStyle(55).GetForeground() == ckBandStyle(10).GetForeground() {
		t.Error("compact and normal bands are coloured identically")
	}
}

// The status bar: one line, view facts on the right, flash notes override
// them until the next action.
func TestStatusBar_FactsAndFlash(t *testing.T) {
	m := testCockpit()
	m.view = ckViewChat
	got := stripANSI(m.statusBar())
	if !strings.Contains(got, "mode auto") || !strings.Contains(got, "route auto") {
		t.Errorf("chat facts missing: %q", got)
	}
	m.override = ckOverride{kind: 'C', conf: 0.95}
	if got := stripANSI(m.statusBar()); !strings.Contains(got, "consensus ≥95%") {
		t.Errorf("the override is not shown: %q", got)
	}
	m.override = ckOverride{}
	m.pinnedTier = 7
	if got := stripANSI(m.statusBar()); !strings.Contains(got, "pinned T7") {
		t.Errorf("the pin is not shown: %q", got)
	}

	m.view = ckViewAgents
	if got := stripANSI(m.statusBar()); !strings.Contains(got, "1 live · 2 finished today") {
		t.Errorf("agents facts missing: %q", got)
	}

	m.view = ckViewModels
	if got := stripANSI(m.statusBar()); !strings.Contains(got, "scanned") || !strings.Contains(got, "r rescan") {
		t.Errorf("models facts missing: %q", got)
	}

	m.view = ckViewActivity
	if got := stripANSI(m.statusBar()); !strings.Contains(got, "3 runs today") {
		t.Errorf("activity facts missing: %q", got)
	}
	m.actFailOnly = true
	if got := stripANSI(m.statusBar()); !strings.Contains(got, "failures only") {
		t.Errorf("the filter state is invisible: %q", got)
	}

	m.view = ckViewUsage
	if got := stripANSI(m.statusBar()); !strings.Contains(got, "grouped by model") {
		t.Errorf("usage facts missing: %q", got)
	}
	m.usageGroup = 't'
	if got := stripANSI(m.statusBar()); !strings.Contains(got, "grouped by tier") {
		t.Errorf("usage grouping fact stale: %q", got)
	}

	m.view = ckViewAudit
	if got := stripANSI(m.statusBar()); !strings.Contains(got, "not checked yet") {
		t.Errorf("audit facts missing: %q", got)
	}

	// A flash replaces the fact until the next action clears it.
	m.flash = "chat pinned to T7"
	if got := stripANSI(m.statusBar()); !strings.Contains(got, "chat pinned to T7") {
		t.Errorf("the flash is not shown: %q", got)
	}
	m = m.jump(ckViewChat)
	if m.flash != "" {
		t.Error("jumping views did not clear the flash")
	}

	for _, w := range []int{0, 10, 40, 200} {
		m.w = w
		if line := m.statusBar(); lipgloss.Width(line) > max(1, w) {
			t.Errorf("status bar exceeds width %d", w)
		}
	}
}

// The wordmark and every status bar stay single-line and inside the frame at
// every view (the per-view keys come from the table — tested in keys_test).
func TestStatusBar_SingleLineEverywhere(t *testing.T) {
	m := testCockpit()
	for v := 0; v < ckViewCount(); v++ {
		m.view = v
		if strings.Contains(m.statusBar(), "\n") {
			t.Errorf("view %d status bar wrapped", v)
		}
	}
}
