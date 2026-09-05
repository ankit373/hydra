// SPDX-License-Identifier: MIT

package tui

// scroll_test.go, the invariant that #630 was six instances of: a frame that
// says there is more below must move when a key that claims to move is pressed.
// Rendering tests alone never caught it; nobody pressed a key.

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ankit373/hydra/internal/testutil"
	"github.com/ankit373/hydra/internal/trust"
)

// scrollKeys are the gestures a "↓ N more" cue invites. Each must change the
// frame in any context showing that cue.
var scrollKeys = []struct {
	name string
	msg  tea.KeyMsg
}{
	{"j", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}},
	{"down", tea.KeyMsg{Type: tea.KeyDown}},
	{"pgdn", tea.KeyMsg{Type: tea.KeyPgDown}},
}

// overflowing builds a cockpit whose every view has more content than a small
// terminal can show.
func overflowing() Cockpit {
	m := testCockpit()
	for i := 0; i < 30; i++ {
		m.runsToday = append(m.runsToday,
			testRun(fmt.Sprintf("20260904T1010%02dZ-r%03d", i, i), "ok",
				fmt.Sprintf("task number %d that runs a while", i)))
	}
	a := testAudit(nil, nil)
	for i := 0; i < 20; i++ {
		a.scorecard = append(a.scorecard, trust.Stat{
			Source: fmt.Sprintf("src-%02d", i), Domain: "go", N: float64(i), Se: 0.8, Sp: 0.7, D: 0.5,
		})
	}
	m.audit = a
	for i := 0; i < 40; i++ {
		m.th().log = append(m.th().log, fmt.Sprintf("line %d of scrollback", i))
	}
	return m
}

// ckCueRe matches the overflow cue itself, the glossary documents "↑/↓" as
// keys, and that arrow is text, not a promise of more content.
var ckCueRe = regexp.MustCompile(`[↑↓] \d+ more`)

func hasCue(frame, arrow string) bool {
	// ckFrame's crop disclosure makes the same promise as a cue: there is more
	// than fits. A view that shows it and cannot scroll has hidden the rest.
	if arrow == "↓" && strings.Contains(frame, "more line") {
		return true
	}
	for _, m := range ckCueRe.FindAllString(frame, -1) {
		if strings.HasPrefix(m, arrow) {
			return true
		}
	}
	return false
}

func frameOf(m Cockpit, w, h int) string {
	m.w, m.h, m.ready = w, h, true
	return stripANSI(m.View())
}

// A cue that no key answers is content the user cannot reach, the audit view
// hid 16 lines that way, and the glossary ignored the arrows (#630).
func TestScrollCue_EveryAdvertisedKeyMoves(t *testing.T) {
	testutil.NewSandbox(t)

	sizes := []struct{ w, h int }{{60, 15}, {80, 24}}
	for view := 0; view < ckViewCount(); view++ {
		for _, sz := range sizes {
			m := overflowing().jump(view)
			before := frameOf(m, sz.w, sz.h)
			if !hasCue(before, "↓") {
				continue // nothing claims there is more below
			}
			for _, k := range scrollKeys {
				if view == ckViewChat && k.name == "j" {
					continue // chat's input owns letter keys
				}
				m.w, m.h, m.ready = sz.w, sz.h, true
				next, _ := m.Update(k.msg)
				if frameOf(next.(Cockpit), sz.w, sz.h) == before {
					t.Errorf("view %d at %dx%d shows a ↓ cue but %s does not move it",
						view, sz.w, sz.h, k.name)
				}
			}
		}
	}
}

// Overlays make the same promise. The glossary is the one a new user meets
// first, and the one that ignored ↑/↓ while j/k worked.
func TestScrollCue_OverlaysMoveToo(t *testing.T) {
	testutil.NewSandbox(t)

	m := overflowing()
	m.glossary = true
	before := frameOf(m, 80, 24)
	if !hasCue(before, "↓") {
		t.Fatal("the glossary no longer overflows an 80x24 terminal, pick a smaller size")
	}
	for _, k := range scrollKeys {
		m.w, m.h, m.ready = 80, 24, true
		next, _ := m.Update(k.msg)
		if frameOf(next.(Cockpit), 80, 24) == before {
			t.Errorf("the glossary shows a ↓ cue but %s does not move it", k.name)
		}
	}

	// And scrolling reaches the end: the last group's keys become visible.
	end := m
	end.w, end.h, end.ready = 80, 24, true
	for i := 0; i < 40; i++ {
		end = end.scrollBy(1)
	}
	out := frameOf(end, 80, 24)
	if !hasCue(out, "↑") {
		t.Errorf("a scrolled glossary shows no top cue:\n%s", out)
	}
	if hasCue(out, "↓") {
		t.Errorf("the glossary never reaches its end:\n%s", out)
	}
}
