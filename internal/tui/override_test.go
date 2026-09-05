// SPDX-License-Identifier: MIT

package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func openOverride(t *testing.T) Cockpit {
	t.Helper()
	m := testCockpit()
	m = press(m, tea.KeyCtrlO)
	if !m.ovOpen {
		t.Fatal("ctrl+o did not open the override modal")
	}
	return m
}

// The simple options apply directly: local only and best of 3.
func TestOverride_PicksDirectOptions(t *testing.T) {
	m := openOverride(t)
	m = typed(m, "jj") // → local only
	m, _ = enter(m)
	if m.ovOpen || m.override.kind != 'L' {
		t.Fatalf("local only not applied: %+v", m.override)
	}
	if got := m.override.label(); got != "route local only" {
		t.Errorf("label = %q", got)
	}

	m = press(m, tea.KeyCtrlO)
	m = typed(m, "jjj") // → best of 3
	m, _ = enter(m)
	if m.override.kind != 'B' || m.override.strategy() != "best of 3" {
		t.Fatalf("best of 3 not applied: %+v", m.override)
	}

	// auto (row 0) resets to no override.
	m = press(m, tea.KeyCtrlO)
	m, _ = enter(m)
	if m.override.kind != 0 || m.override.label() != "route auto" {
		t.Errorf("auto did not reset the override: %+v", m.override)
	}
}

// Force tier is a two-step pick: the row, then one digit (0 = T10).
func TestOverride_ForceTierDigits(t *testing.T) {
	m := openOverride(t)
	m = typed(m, "j")
	m, _ = enter(m)
	if m.ovStage != 'T' || !m.ovOpen {
		t.Fatal("force tier… did not open the tier sub-stage")
	}
	// A non-digit is ignored while the stage waits.
	m = typed(m, "z")
	if m.ovStage != 'T' {
		t.Fatal("a non-digit was accepted as a tier")
	}
	m = typed(m, "3")
	if m.ovOpen || m.override.kind != 'T' || m.override.tier != 3 {
		t.Fatalf("tier 3 not applied: %+v", m.override)
	}

	m = press(m, tea.KeyCtrlO)
	m = typed(m, "j")
	m, _ = enter(m)
	m = typed(m, "0")
	if m.override.tier != 10 {
		t.Errorf("0 should force T10, got %d", m.override.tier)
	}
}

// Consensus is a two-step pick from the design's 90–99.9% targets, by digit or
// j/k+enter.
func TestOverride_ConsensusTargets(t *testing.T) {
	m := openOverride(t)
	m = typed(m, "jjjj")
	m, _ = enter(m)
	if m.ovStage != 'C' {
		t.Fatal("consensus check… did not open the target sub-stage")
	}
	m = typed(m, "2")
	if m.override.kind != 'C' || m.override.conf != 0.95 {
		t.Fatalf("target 95%% not applied: %+v", m.override)
	}
	if got := m.override.strategy(); got != "consensus ≥95%" {
		t.Errorf("strategy = %q", got)
	}

	// j/k + enter picks too — 99.9% renders without a fabricated ".0".
	m = press(m, tea.KeyCtrlO)
	m = typed(m, "jjjj")
	m, _ = enter(m)
	m = typed(m, "jjjjjj") // clamps at the last target
	m, _ = enter(m)
	if m.override.conf != 0.999 {
		t.Fatalf("conf = %v, want 0.999", m.override.conf)
	}
	if got := m.override.strategy(); got != "consensus ≥99.9%" {
		t.Errorf("strategy = %q", got)
	}
}

// esc backs out one level at a time: sub-stage → list → closed. j/k clamp.
func TestOverride_EscBacksOut(t *testing.T) {
	m := openOverride(t)
	for i := 0; i < 10; i++ {
		m = typed(m, "j")
	}
	if m.ovSel != len(ckOvRows)-1 {
		t.Fatalf("j ran off the end: %d", m.ovSel)
	}
	for i := 0; i < 10; i++ {
		m = typed(m, "k")
	}
	if m.ovSel != 0 {
		t.Fatalf("k ran off the top: %d", m.ovSel)
	}
	m = typed(m, "j")
	m, _ = enter(m) // tier sub-stage
	m = press(m, tea.KeyEsc)
	if !m.ovOpen || m.ovStage != 0 {
		t.Fatal("esc did not return to the option list")
	}
	m = press(m, tea.KeyEsc)
	if m.ovOpen {
		t.Fatal("esc did not close the modal")
	}
	if m.override.kind != 0 {
		t.Errorf("backing out changed the override: %+v", m.override)
	}
}

// The modal renders inside the frame at every mandated size, in all stages.
func TestOverride_ModalRendersWithinFrame(t *testing.T) {
	sizes := []struct{ w, h int }{{60, 15}, {80, 24}, {100, 30}, {120, 40}}
	for _, stage := range []byte{0, 'T', 'C'} {
		m := openOverride(t)
		m.ovStage = stage
		for _, sz := range sizes {
			m.w, m.h, m.ready = sz.w, sz.h, true
			out := m.View()
			lines := strings.Split(out, "\n")
			if len(lines) > sz.h {
				t.Errorf("stage %q at %dx%d renders %d lines", stage, sz.w, sz.h, len(lines))
			}
			joined := stripANSI(out)
			if !strings.Contains(joined, "ROUTING OVERRIDE") {
				t.Errorf("stage %q at %dx%d does not show the modal:\n%s", stage, sz.w, sz.h, joined)
			}
		}
	}
	// The modal says what it is: mode = what, this = where.
	m := openOverride(t)
	m.w, m.h, m.ready = 100, 30, true
	if got := stripANSI(m.View()); !strings.Contains(got, "where it runs") {
		t.Error("the modal does not explain the mode/override split")
	}
}

func TestOverride_LabelsAndPct(t *testing.T) {
	cases := map[string]ckOverride{
		"route auto":           {},
		"route T3 forced":      {kind: 'T', tier: 3},
		"route local only":     {kind: 'L'},
		"route best of 3":      {kind: 'B'},
		"route consensus ≥90%": {kind: 'C', conf: 0.90},
	}
	for want, ov := range cases {
		if got := ov.label(); got != want {
			t.Errorf("label(%+v) = %q, want %q", ov, got, want)
		}
	}
	if got := ckPct(0.999); got != "99.9%" {
		t.Errorf("ckPct(0.999) = %q", got)
	}
	if got := ckPct(0.95); got != "95%" {
		t.Errorf("ckPct(0.95) = %q", got)
	}
}
