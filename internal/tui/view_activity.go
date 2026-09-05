// SPDX-License-Identifier: MIT

package tui

// view_activity.go, view 3: today's runs on the left, the selected run as a
// trace timeline on the right. Everything shown is a recorded event or the
// cost.jsonl join for that run, never an example (#191).

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ankit373/hydra/internal/runlog"
)

// focusRun jumps to the activity view with one run's trace drilled in, how
// the agents view and the audit queue hand a run over without touching this
// view's internals themselves.
func (m Cockpit) focusRun(id string) Cockpit {
	m = m.jump(ckViewActivity)
	m.actFailOnly = false
	m.actSel = 0
	for i, r := range m.activityRuns() {
		if r.id == id {
			m.actSel = i
			break
		}
	}
	m.actDrill = true
	m.traceOff = 0
	return m
}

// ── trace ─────────────────────────────────────────────────────────────────────

type ckTraceRow struct {
	label string
	text  string
	sub   string
	tone  lipgloss.Style
}

// ckPlainWording maps internal jargon in recorded event details to the UI
// vocabulary; internal names in the files themselves are unchanged.
var ckPlainWording = strings.NewReplacer(
	"SPRT ensemble", "consensus check",
	"SPRT", "consensus",
	"ledger policy", "guardrails",
	"ledger", "audit log",
)

// ckTrace turns a run's event stream into the trace timeline. Pure, the
// inputs are the recorded events, the cost.jsonl join, and the resolved run
// cost (so the done row agrees with the list rows).
func ckTrace(run ckRun, rc ckRunCost, hasRC bool, costUSD float64) []ckTraceRow {
	var rows []ckTraceRow
	add := func(label, text, sub string, tone lipgloss.Style) {
		rows = append(rows, ckTraceRow{label, text, sub, tone})
	}
	strategy := rc.strategy
	if hasRC && strategy == "" {
		strategy = "single"
	}
	selected := map[string]bool{}
	selCount := 0

	for i, e := range run.events {
		switch e.Kind {
		case runlog.KindHeadSelected:
			selCount++
			selected[e.Head] = true
			label := "routed"
			if selCount > 1 {
				label = "fallback"
			}
			txt := fmt.Sprintf("T%d · %s", e.Tier, e.Model)
			if selCount == 1 && rc.enum != "" {
				txt = rc.enum + " → " + txt
				if strategy != "" {
					txt += " · " + strategy
				}
			}
			add(label, txt, "why: "+ckPlainWording.Replace(e.Detail), ckCyanS)
			if dec, reason := ckPolicyOutcome(run.events[i+1:], e.Head); dec != "" {
				sub := "recorded in the audit log (l)"
				if dec == "denied" {
					add("policy", "denied, "+ckPlainWording.Replace(reason), sub, ckExpS)
				} else {
					add("policy", "allowed", sub, ckCheapS)
				}
			}
		case runlog.KindDispatchFinished:
			add("request", e.Head+" → "+e.Model, "", ckInkS)
			if hasRC && rc.prompt+rc.resp > 0 {
				add("stream", fmt.Sprintf("%s + %s tokens · %s",
					ckTokens(rc.prompt), ckTokens(rc.resp), ckTokenSource(rc)), "", ckDimS)
			} else {
				add("stream", "tokens not recorded for this run", "", ckFaintS)
			}
		case runlog.KindError:
			// Denied errors were already rendered as their policy row.
			if e.Status == "denied" && selected[e.Head] {
				continue
			}
			add("error", e.Model+", "+ckPlainWording.Replace(e.Detail), "", ckExpS)
		case runlog.KindEdit:
			add("edit", e.File+"  "+e.Detail, "", ckMidS)
		case runlog.KindAttempt:
			txt := fmt.Sprintf("%s · %s", e.Model, e.Status)
			if e.Detail != "" {
				txt += " · " + e.Detail
			}
			add("attempt", txt, "", ckCyanS)
		case runlog.KindSample:
			txt := ckPlainWording.Replace(e.Detail)
			if e.Confidence > 0 {
				txt += fmt.Sprintf(" · confidence %.2f", e.Confidence)
			}
			add("consensus", txt, "", ckVioletS)
		case runlog.KindTaskStarted:
			if e.Detail != "" && e.Detail != run.task {
				add("plan", ckPlainWording.Replace(e.Detail), "", ckDimS)
			}
		case runlog.KindHandoff:
			add("handoff", ckPlainWording.Replace(e.Detail), "", ckMagentaS)
		}
	}

	if selCount == 1 && run.fails == 0 && run.status == "ok" {
		add("fallbacks", "none, first candidate answered", "", ckFaintS)
	}
	switch run.status {
	case "running":
		add("live", "still running…", "", ckAquaS)
	case "ok":
		add("done", fmt.Sprintf("$%.4f est · %s", costUSD, ckFmtMS(run.durMS)), "", ckCheapS)
	default:
		add("failed", fmt.Sprintf("no successful answer · %s", ckFmtMS(run.durMS)), "", ckExpS)
	}
	return rows
}

// ckTokenSource labels the run's token provenance honestly.
func ckTokenSource(rc ckRunCost) string {
	switch {
	case rc.est == 0 && rc.actual > 0:
		return "provider-reported"
	case rc.actual == 0 && rc.est > 0:
		return "estimated"
	default:
		return "mixed actual/estimated"
	}
}

// ckPolicyOutcome infers the guardrail decision for a selected head from what
// followed: an execution means the gate allowed it, a denied error means it
// did not. No later event → unknown, and no policy row is invented.
func ckPolicyOutcome(rest []runlog.Event, head string) (decision, reason string) {
	for _, e := range rest {
		if e.Head != head {
			continue
		}
		switch e.Kind {
		case runlog.KindError:
			if e.Status == "denied" {
				return "denied", e.Detail
			}
			return "allowed", ""
		case runlog.KindDispatchFinished:
			return "allowed", ""
		}
	}
	return "", ""
}

// ── render ────────────────────────────────────────────────────────────────────

// ckActTaskW is the run list's task column budget, fixed so durations align.
const ckActTaskW = 24

func (m Cockpit) viewActivity(w, h int) string {
	runs := m.activityRuns()
	sel := m.actSel
	if sel < 0 || sel >= len(runs) {
		sel = 0
	}

	var list strings.Builder
	ok, failed, running := ckRunCounts(runs)
	title := fmt.Sprintf("RUNS · today, %d", len(runs))
	if m.actFailOnly {
		title = fmt.Sprintf("RUNS · failures, %d", len(runs))
	}
	list.WriteString(ckLabelS.Render(title) + "\n")
	list.WriteString(ckDimS.Render(fmt.Sprintf("%d ok · %d failed · %d running", ok, failed, running)) + "\n\n")

	if len(runs) == 0 {
		reason := "no runs today"
		if m.actFailOnly {
			reason = "no failed runs today"
		}
		list.WriteString(ckFaintS.Render(" "+reason) + "\n\n" +
			ckDimS.Render(" Requests appear here as they run,") + "\n" +
			ckDimS.Render(" start one from chat, or:") + "\n" +
			ckDimS.Render("   hyctl dispatch \"add a retry to the refresher\""))
	}
	avail := h - 6
	if avail < 3 {
		avail = 3
	}
	rows := make([]string, len(runs))
	for i, r := range runs {
		marker := "  "
		if i == sel {
			marker = ckAquaS.Render("▸ ")
		}
		task := r.task
		if task == "" {
			task = "(task not recorded)"
		}
		rows[i] = marker + ckStatusGlyph(r.status) + " " +
			ckFaintS.Render(ckCell(ckShortID(r.id), 8)) + " " +
			ckInkS.Bold(i == sel).Render(ckCell(ckSafe(task), ckActTaskW)) + " " +
			ckDimS.Render(ckRCell(ckFmtMS(r.durMS), 7))
	}
	list.WriteString(strings.Join(ckSelScroll(rows, sel, avail), "\n"))
	listBox := ckBoxS.Render(list.String())

	var traceBox string
	if len(runs) == 0 {
		traceBox = ckBoxS.Render(ckLabelS.Render("TRACE") + "\n\n " + ckFaintS.Render("nothing selected"))
	} else {
		traceBox = m.traceBox(runs[sel], h)
	}
	return lipgloss.NewStyle().Width(w).Height(h).Render(ckSplit(w, listBox, traceBox, m.actDrill))
}

// traceBox renders the selected run as a trace timeline, scrolled by traceOff
// when drilled in.
func (m Cockpit) traceBox(r ckRun, h int) string {
	rc, hasRC := m.metrics.runCost[r.id]
	rows := ckTrace(r, rc, hasRC, m.runCostUSD(r))

	var b strings.Builder
	b.WriteString(ckLabelS.Render("TRACE") + ckDimS.Render(" · "+truncate(r.id, 28)))
	if r.live {
		b.WriteString(ckCheapS.Render("  ● live"))
	}
	if m.actDrill {
		b.WriteString(ckAquaS.Render("  drilled · esc back"))
	}
	b.WriteString("\n")
	if r.task != "" {
		b.WriteString(ckFaintS.Render(" " + truncate(ckSafe(r.task), 50)))
	}
	b.WriteString("\n\n")

	avail := h - 8
	if avail < 3 {
		avail = 3
	}
	var lines []string
	for _, row := range rows {
		lines = append(lines, " "+ckDimS.Render(ckCell(row.label, 9))+row.tone.Render(truncate(ckSafe(row.text), 42)))
		if row.sub != "" {
			lines = append(lines, "          "+ckFaintS.Render(truncate(ckSafe(row.sub), 40)))
		}
	}
	off := 0
	if m.actDrill {
		off = m.traceOff
	}
	window, _ := ckScrollLines(lines, off, avail)
	b.WriteString(strings.Join(window, "\n"))
	if len(lines) > avail && !m.actDrill {
		b.WriteString("\n" + ckFaintS.Render(" enter to drill, then j/k · pgup/pgdn scroll"))
	}
	box := ckBoxS
	if m.actDrill {
		box = box.BorderForeground(ckAqua)
	}
	return box.Render(b.String())
}

// openEditedFile opens the selected run's first edited file in $EDITOR.
func (m Cockpit) openEditedFile() (tea.Model, tea.Cmd) {
	runs := m.activityRuns()
	if len(runs) == 0 {
		return m, nil
	}
	sel := m.actSel
	if sel < 0 || sel >= len(runs) {
		sel = 0
	}
	r := runs[sel]
	if len(r.edited) == 0 {
		m.flash = "no edited files in this run"
		return m, nil
	}
	ed := os.Getenv("EDITOR")
	if ed == "" {
		m.flash = "set $EDITOR to open " + truncate(r.edited[0], 30)
		return m, nil
	}
	return m, tea.ExecProcess(exec.Command(ed, r.edited[0]), func(error) tea.Msg { return nil })
}
