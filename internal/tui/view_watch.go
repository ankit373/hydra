// SPDX-License-Identifier: MIT

package tui

// view_watch.go, the first screen: one line per run, grouped by who has to
// act. Needs you (waiting on an answer, or failed), then running, then what
// finished today, then the models if there is room. Chat is one key away.

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

// ckWatchState is what a row is doing, in the order the screen groups them.
type ckWatchState int

const (
	ckStWaiting ckWatchState = iota
	ckStFailed
	ckStRunning
	ckStQueued
	ckStDone
)

// ckWatchRow is one run or chat thread on the watching screen.
type ckWatchRow struct {
	state  ckWatchState
	label  string // short run id, or "thread N"
	task   string
	model  string
	note   string // what the state means right now: "plan ready", "failed 12m ago"
	dur    string
	cost   float64
	runID  string
	thread int // 0 for a run with no thread in this session
}

// ckWatchModelsMinW is the width at which the model column fits beside the task.
const ckWatchModelsMinW = 110

// watchRows is every row, grouped and in screen order. A chat thread stands for
// its own run, so a run a thread owns is listed once, as the thread.
func (m Cockpit) watchRows() (needs, running, done []ckWatchRow) {
	now := m.now()
	for _, t := range m.threads {
		r, ok := m.threadRow(t)
		if !ok {
			continue
		}
		switch r.state {
		case ckStWaiting, ckStFailed:
			needs = append(needs, r)
		case ckStRunning, ckStQueued:
			running = append(running, r)
		default:
			done = append(done, r)
		}
	}
	for _, r := range m.runsToday {
		if m.threadForRun(r.id) != nil {
			continue
		}
		row := ckWatchRow{
			label: ckShortID(r.id), task: r.task, model: r.model,
			cost: m.runCostUSD(r), runID: r.id,
		}
		if row.task == "" {
			row.task = "(task not recorded)"
		}
		switch r.status {
		case "running":
			row.state, row.note = ckStRunning, "running"
			if !r.start.IsZero() {
				row.dur = ckShortDur(now.Sub(r.start))
			}
			running = append(running, row)
		case "failed":
			row.state, row.note = ckStFailed, "failed "+ckAgoAt(r.start, now)
			row.dur = ckShortDur(time.Duration(r.durMS) * time.Millisecond)
			needs = append(needs, row)
		default:
			row.state, row.note = ckStDone, "done "+ckAgoAt(r.start, now)
			row.dur = ckShortDur(time.Duration(r.durMS) * time.Millisecond)
			done = append(done, row)
		}
	}
	return needs, running, done
}

// threadRow is a chat thread as a watch row; a thread that has run nothing
// yet is not a row.
func (m Cockpit) threadRow(t *ckThread) (ckWatchRow, bool) {
	r := ckWatchRow{label: fmt.Sprintf("thread %d", t.id), task: t.name, thread: t.id, runID: t.lastRunID}
	if d := t.lastDone; d != nil {
		r.model, r.cost, r.dur = d.headName, d.costUSD, ckShortDur(d.elapsed)
	}
	switch {
	case t.queued != nil:
		r.state, r.note = ckStQueued, "queued"
	case t.exec != nil:
		r.state, r.note = ckStRunning, t.exec.Stage()
		r.model, r.cost = "", 0
		r.dur = ckShortDur(m.now().Sub(t.exec.started))
	case t.planWait != nil:
		r.state, r.note = ckStWaiting, "plan ready"
	case t.confirm != nil:
		r.state, r.note = ckStWaiting, "asks to write"
	case t.undoAsk != nil:
		r.state, r.note = ckStWaiting, "asks to undo"
	case t.attn:
		r.state, r.note = ckStFailed, "failed"
	case t.lastDone != nil:
		r.state, r.note = ckStDone, "done"
	default:
		return r, false
	}
	if r.task == "" {
		r.task = "(no task yet)"
	}
	return r, true
}

// watchCounts is the header's tally, from the same rows the screen lists.
func (m Cockpit) watchCounts() (running, waiting, failed int) {
	needs, run, _ := m.watchRows()
	for _, r := range needs {
		if r.state == ckStFailed {
			failed++
		} else {
			waiting++
		}
	}
	for _, r := range run {
		if r.state == ckStRunning {
			running++
		}
	}
	return running, waiting, failed
}

// watchList is the selectable rows in screen order, what j/k move through.
func (m Cockpit) watchList() []ckWatchRow {
	needs, running, done := m.watchRows()
	return append(append(needs, running...), done...)
}

// ckWatchCols is the grid for one terminal width. Every row uses it, so the
// columns line up from section to section.
type ckWatchCols struct{ id, task, model, note, dur, cost int }

func ckWatchGrid(w int) ckWatchCols {
	c := ckWatchCols{id: 9, note: 14, dur: 6, cost: 7}
	if w >= ckWatchModelsMinW {
		c.model = 18
	}
	// "▸ g " lead, then two-space gaps between columns, one cell at the end.
	fixed := 4 + c.id + 2 + 2 + c.note + 1 + c.dur + 2 + c.cost + 1
	if c.model > 0 {
		fixed += c.model + 2
	}
	c.task = max(8, w-fixed)
	return c
}

func (c ckWatchCols) row(glyph string, r ckWatchRow, selected bool) string {
	lead := "  "
	if selected {
		lead = ckAccentS.Render("▸") + " "
	}
	task := ckInkS.Bold(selected).Render(ckCell(ckSafe(r.task), c.task))
	line := lead + glyph + " " + ckDimS.Render(ckCell(r.label, c.id)) + "  " + task + "  "
	if c.model > 0 {
		line += ckDimS.Render(ckCell(ckSafe(r.model), c.model)) + "  "
	}
	cost := ""
	if r.cost > 0 {
		cost = ckUSD(r.cost)
	}
	return line + ckDimS.Render(ckCell(ckSafe(r.note), c.note)) + " " +
		ckDimS.Render(ckRCell(r.dur, c.dur)) + "  " + ckMoneyS.Render(ckRCell(cost, c.cost))
}

// ckWatchGlyph is a state's glyph, in its status colour. The glyph alone
// carries the state when there is no colour.
func ckWatchGlyph(s ckWatchState) string {
	g := ckGlyphs()
	switch s {
	case ckStWaiting:
		return ckWarnS.Render(g.wait)
	case ckStFailed:
		return ckBadS.Render(g.bad)
	case ckStRunning:
		return ckAccentS.Render(g.run)
	case ckStQueued:
		return ckDimS.Render(g.queued)
	default:
		return ckOkS.Render(g.ok)
	}
}

func (m Cockpit) viewWatch(w, h int) string {
	needs, running, done := m.watchRows()
	cols := ckWatchGrid(w)
	sel := m.watchSel
	idx := 0
	var lines []string
	selLine := 0
	section := func(title, right string, rows []ckWatchRow) {
		head := ckLabelS.Render(title)
		if right != "" {
			head = ckCell(head, w-lipgloss.Width(right)-1) + ckDimS.Render(right)
		}
		lines = append(lines, head)
		for _, r := range rows {
			if idx == sel {
				selLine = len(lines)
			}
			lines = append(lines, cols.row(ckWatchGlyph(r.state), r, idx == sel))
			idx++
		}
	}

	section("Needs you", "", needs)
	if len(needs) == 0 {
		lines = append(lines, ckFaintS.Render("    nothing is waiting on you"))
	}
	if len(running) > 0 {
		section("Running", "", running)
	}
	if len(done) > 0 {
		var total float64
		for _, r := range done {
			total += r.cost
		}
		right := fmt.Sprintf("%d run%s", len(done), plural(len(done)))
		if total > 0 {
			right += "  " + ckRCell(ckUSD(total), cols.cost)
		}
		section("Done today", right, done)
	}
	if len(needs)+len(running)+len(done) == 0 {
		lines = append(lines, "",
			ckDimS.Render(" Nothing has run today. Press c to open chat and give Hydra a task,"),
			ckDimS.Render(` or run hyctl dispatch "add a retry to the refresher".`))
	}

	top := []string{m.modelsSummary(w)}
	avail := max(1, h-len(top))
	// The model list only when everything above it fits: runs come first.
	if models := m.watchModels(w); len(lines)+len(models) <= avail {
		lines = append(lines, models...)
	}
	if len(lines) > avail {
		lines = ckSelScroll(lines, selLine, avail)
	}
	return strings.Join(append(top, lines...), "\n")
}

// modelsSummary is the line above the runs: how many models can take work,
// and the first one that cannot, with its reason when the scan gave one.
func (m Cockpit) modelsSummary(w int) string {
	ready := 0
	var down string
	for _, hd := range m.heads {
		if hd.up {
			ready++
		} else if down == "" {
			down = hd.name
		}
	}
	s := ckDimS.Render(fmt.Sprintf(" models %d of %d ready", ready, len(m.heads)))
	if len(m.heads) == 0 {
		s = ckWarnS.Render(" no models found") + ckDimS.Render(", run hyctl probe to see what was scanned")
	} else if down != "" {
		s += ckDimS.Render("   " + ckSafe(down) + " cannot take work")
	}
	return truncate(s, w)
}

// watchModels is the models section, one row per model, when there is room.
func (m Cockpit) watchModels(w int) []string {
	if len(m.heads) == 0 {
		return nil
	}
	g := ckGlyphs()
	out := []string{ckLabelS.Render("Models")}
	nameW := min(28, max(10, w-24))
	for _, hd := range m.heads {
		glyph, where, note := ckOkS.Render(g.ok), "api", ""
		switch {
		case hd.local:
			where = "local"
		case hd.cli:
			where = "cli"
		}
		if !hd.up {
			glyph, note = ckBadS.Render(g.bad), "cannot take work"
		}
		out = append(out, "  "+glyph+" "+ckCell(ckSafe(hd.name), nameW)+"  "+
			ckDimS.Render(ckCell(where, 6))+ckDimS.Render(note))
	}
	return out
}

// ── formatters ────────────────────────────────────────────────────────────────

// ckUSD is money at the precision it is worth: cents above a dollar, a tenth
// of a cent below, and never a run of zeros.
func ckUSD(v float64) string {
	switch {
	case v <= 0:
		return "$0.00"
	case v < 0.001:
		return "<$0.001"
	case v < 1:
		return fmt.Sprintf("$%.3f", v)
	default:
		return fmt.Sprintf("$%.2f", v)
	}
}

// ckShortDur is a duration at a glance: 4.2s, 42s, 1m12s, 2h05m.
func ckShortDur(d time.Duration) string {
	switch {
	case d <= 0:
		return ""
	case d < 10*time.Second:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// ckAgoAt is how long before now t was, coarsely: "12m ago".
func ckAgoAt(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
}

// now is the cockpit's clock; tests pin it so a frame renders the same twice.
func (m Cockpit) now() time.Time {
	if m.clock != nil {
		return m.clock()
	}
	return time.Now()
}
