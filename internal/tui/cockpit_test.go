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

// One overlong entry (a pasted stack trace, a 3000-char task) must not wrap
// past the log pane and push the input/divider off-frame — the log renderer
// used to tail-slice by logical entry count, not rendered visual-line count,
// and Style.Height only pads short content, it never truncates tall (#506).
func TestChatMain_OneOverlongEntryDoesNotPushInputOffFrame(t *testing.T) {
	m := Cockpit{w: 120, h: 40, ready: true, mode: "dispatch", input: "still typing"}
	m.log = []string{strings.Repeat("x", 3000)}

	for _, h := range []int{10, 24, 30} {
		out := m.chatMain(60, h)
		if got := strings.Count(out, "\n") + 1; got != h {
			t.Errorf("chatMain(60, %d) with one huge entry produced %d lines, want %d", h, got, h)
		}
		if !strings.Contains(stripANSI(out), "still typing") {
			t.Errorf("the input line was pushed off frame by one long entry:\n%s", stripANSI(out))
		}
	}
}

// A single overlong entry must be visibly truncated, not silently dropped —
// the user should see something was cut, not just less log (#506).
func TestCkClipToLines_MarksTruncationAndBoundsLength(t *testing.T) {
	long := strings.Repeat("word ", 400)
	got := stripANSI(ckClipToLines(long, 20, 4))
	rows := strings.Split(got, "\n")
	if len(rows) != 4 {
		t.Fatalf("ckClipToLines produced %d rows, want 4", len(rows))
	}
	if !strings.Contains(rows[len(rows)-1], "truncated") {
		t.Errorf("the last row does not disclose the truncation: %q", rows[len(rows)-1])
	}

	short := "a short line"
	if got := ckClipToLines(short, 60, 4); got != short {
		t.Errorf("a short entry was altered: %q", got)
	}
}

// The visible-log window must size itself in wrapped lines, not logical
// entries, and always keep the newest entry (#506).
func TestCkVisibleLog_CapsEachEntryAndFillsFromTheTail(t *testing.T) {
	log := []string{
		"short one",
		strings.Repeat("word ", 400), // would wrap past the whole pane alone
		"short two",
	}
	got := stripANSI(ckVisibleLog(log, 20, 8))
	if n := len(strings.Split(got, "\n")); n > 8 {
		t.Errorf("ckVisibleLog produced %d lines, want <= 8:\n%s", n, got)
	}
	if !strings.Contains(got, "short two") {
		t.Errorf("the newest entry is missing:\n%s", got)
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

// At terminal heights too short for even one head row, the sidebar used to
// show zero heads and no "+N more" line, contradicting the header directly
// above it, which still states the real count (#506).
func TestSidebar_DisclosesCountWhenNoRoomForAnyHeadRow(t *testing.T) {
	heads := make([]ckHead, 12)
	for i := range heads {
		heads[i] = ckHead{id: strings.Repeat("x", i%5+1), name: "head", tier: 1}
	}
	m := Cockpit{w: 120, h: 40, ready: true, heads: heads, mode: "dispatch"}

	// The fixed GOVERNOR/MODE chrome is 8 lines; h=6 leaves no room for a
	// single head row (avail <= 0) — exactly the state this bug is about.
	got := stripANSI(m.sidebar(6))
	if !strings.Contains(got, "12") {
		t.Errorf("sidebar(6) with 12 heads does not disclose the real count:\n%s", got)
	}

	// With no heads at all there is nothing to disclose.
	empty := Cockpit{w: 120, h: 40, ready: true, mode: "dispatch"}
	if got := empty.sidebar(6); strings.Contains(got, "not enough room") {
		t.Errorf("sidebar with zero heads still printed a count line:\n%s", got)
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
