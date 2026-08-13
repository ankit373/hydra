// SPDX-License-Identifier: MIT

package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// `hyctl tui --snapshot --view 3` used to panic with an index-out-of-range:
// header() indexed a 3-element slice literal with an unvalidated m.view.
func TestCockpitSnapshotView_OutOfRangeDoesNotPanic(t *testing.T) {
	for _, view := range []int{-1, -100, len(ckViewNames), 99} {
		t.Run(strings.TrimSpace(viewLabel(view)), func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("--view %d panicked: %v", view, r)
				}
			}()
			out := CockpitSnapshotView(view)
			if out == "" {
				t.Errorf("--view %d rendered nothing", view)
			}
		})
	}
}

func viewLabel(v int) string {
	if v < 0 {
		return "negative"
	}
	return "too-large"
}

func TestCockpitSnapshotView_ValidViewsRender(t *testing.T) {
	for view, name := range ckViewNames {
		out := CockpitSnapshotView(view)
		if out == "" {
			t.Fatalf("view %d (%s) rendered nothing", view, name)
		}
		// The header labels the active view — the frame really is the one asked for.
		if !strings.Contains(out, name) {
			t.Errorf("view %d frame does not mention %q", view, name)
		}
	}
}

func TestCkViewName_IsTotal(t *testing.T) {
	if got := ckViewName(-1); got != ckViewNames[ckViewChatCode] {
		t.Errorf("ckViewName(-1) = %q, want the default label", got)
	}
	if got := ckViewName(len(ckViewNames)); got != ckViewNames[ckViewChatCode] {
		t.Errorf("ckViewName(out of range) = %q, want the default label", got)
	}
	for i, want := range ckViewNames {
		if got := ckViewName(i); got != want {
			t.Errorf("ckViewName(%d) = %q, want %q", i, got, want)
		}
	}
}

// ValidSnapshotView is what the CLI uses to reject a bad --view before
// rendering, so it must agree with the internal bounds check.
func TestValidSnapshotView(t *testing.T) {
	for i := range ckViewNames {
		if ok, _ := ValidSnapshotView(i); !ok {
			t.Errorf("ValidSnapshotView(%d) = false, want true", i)
		}
	}
	for _, bad := range []int{-1, len(ckViewNames), 99} {
		if ok, _ := ValidSnapshotView(bad); ok {
			t.Errorf("ValidSnapshotView(%d) = true, want false", bad)
		}
	}
	if _, names := ValidSnapshotView(0); len(names) != len(ckViewNames) {
		t.Errorf("ValidSnapshotView returned %d names, want %d", len(names), len(ckViewNames))
	}
}

// The returned name slice must be a copy — a caller mutating it must not
// corrupt the package's view table.
func TestValidSnapshotView_ReturnsCopy(t *testing.T) {
	_, names := ValidSnapshotView(0)
	if len(names) == 0 {
		t.Fatal("no names returned")
	}
	names[0] = "mutated"
	if ckViewNames[0] == "mutated" {
		t.Error("ValidSnapshotView leaked the package view table to the caller")
	}
}

// Tab cycles through every view and always lands in range.
func TestTabCycleStaysInRange(t *testing.T) {
	view := 0
	for i := 0; i < ckViewCount()*3; i++ {
		view = (view + 1) % ckViewCount()
		if !ckValidView(view) {
			t.Fatalf("after %d tabs view = %d, out of range", i+1, view)
		}
	}
	if view != 0 {
		t.Errorf("cycling %d times should return to view 0, got %d", ckViewCount()*3, view)
	}
}

// chatMain's total output must be exactly h lines. It previously forgot the
// divider row stacked below the log box, coming out h+1 lines — Bubble Tea's
// renderer crops overflow off the TOP, silently deleting the header on every
// launch of the default tab (#445).
func TestChatMain_OutputIsExactlyHLines(t *testing.T) {
	m := Cockpit{w: 120, h: 40, ready: true, mode: "dispatch"}
	for _, h := range []int{10, 24, 30, 60} {
		out := m.chatMain(60, h)
		if got := strings.Count(out, "\n") + 1; got != h {
			t.Errorf("chatMain(60, %d) produced %d lines, want %d", h, got, h)
		}
	}
}

// The sidebar must never exceed the height it's given, however many heads
// were discovered — an uncapped list let a busy machine's real head count
// dictate the whole view's height once JoinHorizontal pads every other
// column to match it (#446).
func TestSidebar_NeverExceedsGivenHeight(t *testing.T) {
	heads := make([]ckHead, 30)
	for i := range heads {
		heads[i] = ckHead{id: strings.Repeat("x", i%5+1), name: "head", tier: 1}
	}
	m := Cockpit{w: 120, h: 40, ready: true, heads: heads, mode: "dispatch"}

	// The GOVERNOR/MODE chrome above and below the head list is fixed at 8
	// lines regardless of h — h below that floor is asking for something the
	// layout cannot do. The bug this guards is a large head list blowing past
	// h once h is at least big enough for that fixed chrome to fit.
	for _, h := range []int{10, 15, 20} {
		out := m.sidebar(h)
		if got := strings.Count(out, "\n") + 1; got > h {
			t.Errorf("sidebar(%d) with 30 heads produced %d lines, want <= %d", h, got, h)
		}
	}

	// With more heads than fit, the overflow must be disclosed, not silently
	// dropped.
	out := m.sidebar(10)
	if !strings.Contains(out, "more") {
		t.Errorf("sidebar(10) with 30 heads does not disclose the hidden ones:\n%s", out)
	}
}

// Below the width the two-column layout needs, dash and dashSecurity must
// stack into a single column rather than corrupt: join-then-clamp-to-w splits
// box borders across the wrong rows once a box's real width exceeds what's
// left after clamping (#447).
func TestDashAndDashSecurity_NoLineExceedsWidthWhenNarrow(t *testing.T) {
	m := Cockpit{w: 40, h: 30, ready: true, mode: "dispatch"}

	for name, render := range map[string]func(w, h int) string{
		"dash":         m.dash,
		"dashSecurity": m.dashSecurity,
	} {
		t.Run(name, func(t *testing.T) {
			out := render(40, 20)
			for i, line := range strings.Split(out, "\n") {
				if got := lipgloss.Width(line); got > 40 {
					t.Errorf("line %d is %d cells wide, want <= 40:\n%q", i, got, line)
				}
			}
		})
	}
}
