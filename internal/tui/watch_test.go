// SPDX-License-Identifier: MIT

package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/testutil"
)

// watchNow is the fixed clock every watching-screen frame is drawn at.
var watchNow = time.Date(2026, 11, 14, 14, 6, 0, 0, time.UTC)

// watchRun is a run that started ago before watchNow and took dur.
func watchRun(id, status, task, model string, ago, dur time.Duration, cost float64) ckRun {
	r := testRun(id, status, task)
	r.start = watchNow.Add(-ago)
	r.durMS = dur.Milliseconds()
	r.model, r.costUSD = model, cost
	r.events = []runlog.Event{{Kind: runlog.KindRunStarted, TS: r.start.Format(time.RFC3339), Detail: task}}
	return r
}

// watchCockpit is a day with something in every group: a thread waiting on a
// plan, a failed run, two running, four done, and one model that cannot work.
func watchCockpit(w, h int) Cockpit {
	m := testCockpit()
	m.view = ckViewWatch
	m.w, m.h = w, h
	m.clock = func() time.Time { return watchNow }
	m.spend = 1.834
	m.pctKnown, m.claudePct = true, 48
	m.heads = []ckHead{
		{name: "qwen3:8b", up: true, local: true},
		{name: "claude-sonnet-4.5", up: true},
		{name: "gpt-5", up: true},
		{name: "agy/claude-opus"},
	}
	m.runsToday = []ckRun{
		watchRun("20261114T135400Z-a104", "failed", "fix the flaky workflow resume test", "gpt-5", 12*time.Minute, 95*time.Second, 0.204),
		watchRun("20261114T140200Z-a108", "running", "rotate the signing key in internal/auth/token.go", "", 42*time.Second, 0, 0),
		watchRun("20261114T140100Z-a107", "running", "add pagination to the heads endpoint", "", 72*time.Second, 0, 0),
		watchRun("20261114T140500Z-a106", "ok", "review the diff on main", "gpt-5", time.Minute, 8*time.Second, 0.088),
		watchRun("20261114T135900Z-a105", "ok", "summarise the incident thread", "gemini-2.5-pro", 7*time.Minute, 5*time.Second, 0.012),
		watchRun("20261114T135500Z-a103", "ok", "extract the invoice totals", "qwen3:8b", 11*time.Minute, 14*time.Second, 0),
		watchRun("20261114T135000Z-a102", "ok", "rename Head.Tier to Head.Level", "claude-sonnet-4.5", 16*time.Minute, 41*time.Second, 0.41),
	}
	m = m.withThreads()
	m.th().log = []string{"welcome"}
	plan := newThread(2)
	plan.name = "apply the plan for the retry refresher"
	plan.planWait = &ckWait{question: "run the plan?"}
	m.threads = append(m.threads, plan)
	m.nextID = 3
	return m
}

// The watching screen at the two sizes that matter, with no colour, so the
// golden is the layout alone.
func TestWatch_Frames(t *testing.T) {
	withTheme(t, colorprofile.NoTTY, true)
	for _, sz := range []struct {
		name string
		w, h int
	}{{"watch_80x24", 80, 24}, {"watch_120x40", 120, 40}} {
		t.Run(sz.name, func(t *testing.T) {
			frame := watchCockpit(sz.w, sz.h).frame()
			checkFrame(t, frame, sz.w, sz.h)
			testutil.Golden(t, sz.name, stripANSI(frame))
		})
	}
}

// checkFrame is the shell's contract: exactly h lines, none wider than w.
func checkFrame(t *testing.T, frame string, w, h int) {
	t.Helper()
	lines := strings.Split(frame, "\n")
	if len(lines) != h {
		t.Errorf("%dx%d frame is %d lines", w, h, len(lines))
	}
	for i, l := range lines {
		if lipgloss.Width(l) > w {
			t.Errorf("%dx%d line %d is %d cells: %q", w, h, i, lipgloss.Width(l), stripANSI(l))
		}
	}
}

// Every row of the grid ends its cost column in the same cell, at both widths:
// the columns line up from section to section.
func TestWatch_GridLinesUp(t *testing.T) {
	withTheme(t, colorprofile.NoTTY, true)
	for _, w := range []int{80, 120} {
		m := watchCockpit(w, 40)
		ends := map[int]int{}
		for _, l := range strings.Split(stripANSI(m.viewWatch(w, 37)), "\n") {
			if strings.Contains(l, "$0.") || strings.Contains(l, "$1.") {
				ends[lipgloss.Width(strings.TrimRight(l, " "))]++
			}
		}
		if len(ends) != 1 {
			t.Errorf("at %d columns the cost column ends in %d places: %v", w, len(ends), ends)
		}
	}
	// The model column appears only where it fits beside the task.
	row := func(w int) string {
		for _, l := range strings.Split(stripANSI(watchCockpit(w, 40).viewWatch(w, 37)), "\n") {
			if strings.Contains(l, "fix the flaky") {
				return l
			}
		}
		return ""
	}
	if r := row(80); strings.Contains(r, "gpt-5") {
		t.Errorf("the 80-column grid has a model column: %q", r)
	}
	if r := row(120); !strings.Contains(r, "gpt-5") {
		t.Errorf("the 120-column grid has no model column: %q", r)
	}
}

// Who has to act comes first, and the header counts the same rows.
func TestWatch_NeedsYouFirstAndHeaderAgrees(t *testing.T) {
	m := watchCockpit(120, 40)
	out := stripANSI(m.viewWatch(120, 37))
	order := []string{"Needs you", "apply the plan", "fix the flaky", "Running", "rotate the signing", "Done today", "review the diff", "Models"}
	last := -1
	for _, s := range order {
		i := strings.Index(out, s)
		if i < 0 || i < last {
			t.Fatalf("%q is missing or out of order:\n%s", s, out)
		}
		last = i
	}
	running, waiting, failed := m.watchCounts()
	if running != 2 || waiting != 1 || failed != 1 {
		t.Errorf("counts = %d running, %d waiting, %d failed; want 2, 1, 1", running, waiting, failed)
	}
	hdr := stripANSI(m.header())
	for _, want := range []string{"2 running", "1 waiting", "1 failed", "today $1.83"} {
		if !strings.Contains(hdr, want) {
			t.Errorf("header missing %q: %q", want, hdr)
		}
	}
	// A short terminal gives up the model list, never a run.
	short := stripANSI(m.viewWatch(80, 15))
	if strings.Contains(short, "Models") {
		t.Errorf("the model list pushed a run out:\n%s", short)
	}
	if !strings.Contains(short, "rename Head.Tier") {
		t.Errorf("a run was dropped while there was room for it:\n%s", short)
	}
	if !strings.Contains(short, "models 3 of 4 ready") || !strings.Contains(short, "agy/claude-opus cannot take work") {
		t.Errorf("the models summary line is missing:\n%s", short)
	}
}

// c opens chat on the selected thread, esc with nothing to cancel comes back,
// enter on a run opens its trace, and q quits outside chat only.
func TestWatch_KeysChatOneKeyAway(t *testing.T) {
	m := watchCockpit(80, 24)
	m.watchSel = 0 // the waiting thread
	m = typed(m, "c")
	if m.view != ckViewChat || m.th().id != 2 {
		t.Fatalf("c went to view %d thread %d, want chat on thread 2", m.view, m.th().id)
	}
	m.th().planWait = nil
	m = press(m, "esc")
	if m.view != ckViewWatch {
		t.Fatalf("esc from an idle chat left view %d, want watch", m.view)
	}
	// esc clears typed input before it leaves.
	m = m.jump(ckViewChat)
	m = typed(m, "hello")
	m = press(m, "esc")
	if m.view != ckViewChat || m.th().input != "" {
		t.Errorf("esc with input: view %d input %q, want chat with the input cleared", m.view, m.th().input)
	}

	m = m.jump(ckViewWatch)
	m.watchSel = 1 // the failed run
	next, _ := m.Update(key("enter"))
	if got := next.(Cockpit); got.view != ckViewActivity || !got.actDrill {
		t.Errorf("enter on a run left view %d drill %v, want its trace", got.view, got.actDrill)
	}

	if _, cmd := m.Update(key("q")); cmd == nil {
		t.Error("q on the watching screen did not quit")
	}
	m = m.jump(ckViewChat)
	if next, cmd := m.Update(key("q")); cmd != nil || next.(Cockpit).th().input != "q" {
		t.Error("q in chat did not type")
	}
}

// Chat gets the whole width. The code panel joins it only when the thread
// has code to show and the terminal is wide enough for both.
func TestChat_FullWidth(t *testing.T) {
	withTheme(t, colorprofile.NoTTY, true)
	for _, w := range []int{80, 120, 160} {
		m := testCockpit()
		m.w, m.h = w, 24
		if chatW, codeW := m.chatCols(); chatW != w || codeW != 0 {
			t.Errorf("empty thread at %d columns: chat %d, code %d", w, chatW, codeW)
		}
		m.th().codeLines, m.th().codeShown = []string{"package main"}, 1
		chatW, codeW := m.chatCols()
		if w < ckCodePanelMinCols && (chatW != w || codeW != 0) {
			t.Errorf("an edit at %d columns took the code panel: chat %d, code %d", w, chatW, codeW)
		}
		if w >= ckCodePanelMinCols && (codeW == 0 || chatW+codeW != w) {
			t.Errorf("an edit at %d columns: chat %d + code %d", w, chatW, codeW)
		}
	}

	m := watchCockpit(80, 24)
	m = m.jump(ckViewChat)
	m.th().name = "rotate the signing key"
	m.th().log = []string{
		ckYouS.Render("❯ rotate the signing key in internal/auth/token.go and keep the old one valid for 24 hours"),
		"Added a second key slot. New tokens are signed with the new key, and the old key still verifies for a day.",
	}
	frame := m.frame()
	checkFrame(t, frame, 80, 24)
	testutil.Golden(t, "chat_80x24", stripANSI(frame))
	// The conversation's longest line uses the width, not a third of it.
	if !strings.Contains(frame, "rotate the signing key in internal/auth/token.go and keep the old one") {
		t.Errorf("the prompt does not get the full width:\n%s", frame)
	}
}

// With NO_COLOR nothing paints, and every state still reads: its glyph and its
// word are both there. The frame goes through the writer the program draws
// with, which is where NO_COLOR drops every SGR, bold and reverse included.
func TestWatch_NoColorStillReads(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	withTheme(t, OutputProfile(), true)
	m := watchCockpit(80, 24)
	var buf strings.Builder
	w := &colorprofile.Writer{Forward: &buf, Profile: OutputProfile()}
	if _, err := w.WriteString(m.frame()); err != nil {
		t.Fatal(err)
	}
	frame := buf.String()
	if strings.Contains(frame, "\x1b[") {
		t.Errorf("NO_COLOR frame carries escape sequences: %q", frame)
	}
	g := ckGlyphs()
	for _, want := range []string{"[watch]", g.wait + " thread 2", g.bad + " a104", g.run + " a108", g.ok + " a106", "plan ready", "failed 12m ago", "running"} {
		if !strings.Contains(frame, want) {
			t.Errorf("NO_COLOR frame lost %q:\n%s", want, frame)
		}
	}

	// With colour, the states are coloured and money is not.
	withTheme(t, colorprofile.TrueColor, true)
	if ckMoneyS.GetForeground() != (lipgloss.NoColor{}) {
		t.Error("money is drawn in a colour; it is a number, not a state")
	}
	if !strings.Contains(ckWatchGlyph(ckStFailed), "\x1b[") {
		t.Error("a failed state has no colour on a colour terminal")
	}
}

// The Windows console host and the Linux text console get ASCII glyphs.
func TestGlyphsFor_ASCIIConsoles(t *testing.T) {
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }
	for _, tt := range []struct {
		goos string
		env  map[string]string
		want statusGlyphs
	}{
		{"darwin", nil, glyphsUnicode},
		{"windows", nil, glyphsASCII},
		{"windows", map[string]string{"WT_SESSION": "1"}, glyphsUnicode},
		{"linux", map[string]string{"TERM": "linux"}, glyphsASCII},
	} {
		if got := glyphsFor(tt.goos, env(tt.env)); got != tt.want {
			t.Errorf("glyphsFor(%s, %v) = %+v", tt.goos, tt.env, got)
		}
	}
	for _, s := range []string{glyphsASCII.ok, glyphsASCII.bad, glyphsASCII.wait, glyphsASCII.run, glyphsASCII.queued, glyphsASCII.idle} {
		if len(s) != 1 || s[0] > 0x7e {
			t.Errorf("ASCII glyph %q is not one printable ASCII byte", s)
		}
	}
}

func TestCkUSD(t *testing.T) {
	for _, tt := range []struct {
		v    float64
		want string
	}{{0, "$0.00"}, {0.0004, "<$0.001"}, {0.0621, "$0.062"}, {1.834, "$1.83"}, {12, "$12.00"}} {
		if got := ckUSD(tt.v); got != tt.want {
			t.Errorf("ckUSD(%v) = %q, want %q", tt.v, got, tt.want)
		}
	}
}

func TestCkShortDur(t *testing.T) {
	for _, tt := range []struct {
		d    time.Duration
		want string
	}{{0, ""}, {42 * time.Second, "42s"}, {72 * time.Second, "1m12s"}, {125 * time.Minute, "2h05m"}} {
		if got := ckShortDur(tt.d); got != tt.want {
			t.Errorf("ckShortDur(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

var _ tea.Model = Cockpit{}
