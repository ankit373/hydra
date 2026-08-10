// SPDX-License-Identifier: MIT

package tui

import (
	"strings"
	"testing"
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
