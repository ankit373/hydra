// SPDX-License-Identifier: MIT

package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// Regression for a live 80x24 frame: entries a bit wider than the narrow chat
// pane (route lines, footers) were counted as one line but rendered as three,
// pushing the input bar off-frame. The window count and the render must agree
// for every pane at the split's narrowest widths.
func TestChatCode_LiveFrame80x24NeverOverflows(t *testing.T) {
	m := testCockpit()
	m.w, m.h, m.ready = 80, 24, true
	var heads []ckHead
	for i := 0; i < 15; i++ {
		heads = append(heads, ckHead{id: "h", name: "Gemini 3.5 Flash (Medium)", tier: 8, up: true})
	}
	m.heads = heads
	m.mode = "ask"
	m.th().log = []string{
		"init line one",
		"Type a task and press enter. shift+tab mode · ctrl+o route · ? shortcuts · :q quits.",
		"mode → ask",
	}
	done := ckTask{
		runID:  "20260904T120000Z-706a524d",
		answer: "A goroutine is a lightweight thread managed by the Go runtime.",
		mode:   ckModeByName("ask"), elapsed: 26300000000,
	}
	m.th().log = append(m.th().log, ckYouS.Render("❯ what is a goroutine? answer in one short sentence"))
	m.th().log = append(m.th().log, m.routeLines(done, heads[0], "SIMPLE", ckOverride{}, false)...)
	m.th().log = append(m.th().log, ckResultLines(done)...)
	m.th().lastDone = &done

	bodyH := m.h - 3
	if got := lipgloss.Height(m.chatCode(bodyH)); got != bodyH {
		t.Errorf("chatCode height = %d, want exactly %d", got, bodyH)
	}
	out := stripANSI(m.View())
	if strings.Contains(out, "enlarge the terminal") {
		t.Errorf("the live frame trips ckFrame's crop disclosure:\n%s", out)
	}
	lines := strings.Split(out, "\n")
	if last := lines[len(lines)-1]; !strings.Contains(last, "shortcuts") {
		t.Errorf("the status bar is not the final line: %q", last)
	}
	// The wrapped-count/rendered-line agreement, at the exact narrow width.
	for i, e := range m.th().log {
		for j, l := range strings.Split(ckClipToLines(e, 28, 6), "\n") {
			if lipgloss.Width(l) > 28 {
				t.Errorf("entry %d line %d renders %d cells at width 28", i, j, lipgloss.Width(l))
			}
		}
	}
}
