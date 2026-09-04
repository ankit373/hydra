// SPDX-License-Identifier: MIT

package tui

// view_chat.go — view 0: the chat console (keys, the bordered input bar with
// its mode chip), its sidebar, and the code panel. Kept thin: execution lives
// in chat_exec.go/chat_task.go, the strip and split panes in threads.go, modes
// in modes.go, the override in override.go.

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var ckFileRe = regexp.MustCompile(`[\w/]+\.(go|ts|js|py|rs|java)`)

// chatKey handles keys while the chat view is active. Typing always wins on a
// non-empty input; the letter commands (?, m, a/d/x/o) fire only from an empty
// one, the same discipline '?' set in phase 1. Pending states are modal.
func (m Cockpit) chatKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	t := m.th()
	switch {
	case m.modePick:
		return m.modePickerKey(msg)
	case m.ovOpen:
		return m.overrideKey(msg)
	case t.confirm != nil:
		return m.confirmKey(msg)
	case t.planWait != nil:
		return m.planKey(msg)
	}
	switch msg.Type {
	case tea.KeyShiftTab:
		m.mode = ckNextBasicMode(m.mode)
		m.flash = "mode → " + m.mode
		return m, nil
	case tea.KeyCtrlO:
		m.ovOpen, m.ovSel, m.ovStage, m.ovConfSel = true, 0, 0, 0
		return m, nil
	case tea.KeyCtrlLeft:
		return m.cycleThread(-1), nil
	case tea.KeyCtrlRight:
		return m.cycleThread(1), nil
	case tea.KeyCtrlBackslash:
		return m.toggleSplit(), nil
	case tea.KeyCtrlB:
		return m.backgroundThread(), nil
	case tea.KeyEnter:
		return m.submit()
	case tea.KeyBackspace:
		if n := len(t.input); n > 0 {
			t.input = t.input[:n-1]
		}
	case tea.KeySpace:
		t.input += " "
	case tea.KeyRunes:
		// Pasted text is literal input: a paste starting with m/a/d/x/o must
		// type, not fire the letter commands.
		if t.input == "" && len(msg.Runes) == 1 && !msg.Paste {
			switch msg.Runes[0] {
			case '?':
				m.glossary = true
				return m, nil
			case 'm':
				return m.openModePicker(), nil
			case 'a', 'd', 'x', 'o':
				if nm, cmd, ok := m.resultKey(msg.Runes[0]); ok {
					return nm, cmd
				}
			}
		}
		t.discardArm = false // any typing disarms the pending discard confirm
		t.input += string(msg.Runes)
	}
	return m, nil
}

// confirmKey is careful mode's y/n gate; esc (handled globally) also declines.
func (m Cockpit) confirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	t := m.th()
	if msg.Type != tea.KeyRunes || len(msg.Runes) != 1 {
		return m, nil
	}
	w := *t.confirm
	switch msg.Runes[0] {
	case 'y', 'Y':
		t.log = append(t.log, ckDimS.Render("  confirmed — writing"))
		return m.resumeTask(t, w)
	case 'n', 'N':
		note := "stopped before writing — nothing changed"
		if w.phase == ckPhaseFix {
			note = "fix declined — the file keeps its last write"
		}
		return m.stopWait(t, w, note)
	}
	return m, nil
}

// planKey is plan mode's approval gate: enter/y/a run the plan, esc (global)
// discards it. Everything else is swallowed while the question stands.
func (m Cockpit) planKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	t := m.th()
	switch msg.Type {
	case tea.KeyEnter:
		t.log = append(t.log, ckDimS.Render("  plan approved — running"))
		return m.resumeTask(t, *t.planWait)
	case tea.KeyRunes:
		if len(msg.Runes) == 1 {
			switch msg.Runes[0] {
			case 'y', 'Y', 'a', 'A':
				t.log = append(t.log, ckDimS.Render("  plan approved — running"))
				return m.resumeTask(t, *t.planWait)
			}
		}
	}
	return m, nil
}

func (m Cockpit) submit() (tea.Model, tea.Cmd) {
	th := m.th()
	t := strings.TrimSpace(th.input)
	th.input = ""
	th.scroll = 0 // sending your own message returns the view to live
	// Commands are matched case-insensitively (#465) — free-text task input
	// below is not, since a real task's casing is meaningful.
	lt := strings.ToLower(t)
	switch {
	case lt == "":
		// An empty enter after a finished task opens its trace, as the footer says.
		if th.lastDone != nil && th.exec == nil {
			return m.focusRun(th.lastDone.runID), nil
		}
		return m, nil
	case lt == ":q" || lt == ":quit":
		return m, tea.Quit
	case strings.HasPrefix(lt, ":"):
		for v, name := range ckViewNames {
			if lt == ":"+name {
				return m.jump(v), nil
			}
		}
		th.log = append(th.log, ckDimS.Render("unknown command "+t))
		return m, nil
	case strings.HasPrefix(lt, "/"):
		if ckIsMode(lt[1:]) {
			m.mode = lt[1:]
			th.log = append(th.log, ckDimS.Render("mode → ")+ckCyanS.Render(m.mode))
		} else {
			th.log = append(th.log, ckDimS.Render("unknown command "+t))
		}
		return m, nil
	}
	if th.exec != nil || th.queued != nil {
		th.input = t // nothing typed is lost
		m.flash = fmt.Sprintf("thread %d is busy — ctrl+t opens a new thread · esc cancels", th.id)
		return m, nil
	}
	return m.startTask(t)
}

// pickHead finds the cheapest available head at or below the wanted strength,
// falling back down the ladder to a local model when heads are down. localOnly
// restricts the whole search to local heads.
func (m Cockpit) pickHead(wantTier int, localOnly bool) int {
	eligible := func(h ckHead) bool { return h.up && (!localOnly || h.local) }
	best := -1
	for i, h := range m.heads {
		if h.tier >= wantTier && eligible(h) {
			if best == -1 || h.tier < m.heads[best].tier {
				best = i
			}
		}
	}
	if best == -1 {
		for i, h := range m.heads {
			if h.tier >= 10 && eligible(h) {
				return i
			}
		}
		// Nothing is routable — a machine with only the ollama binary and no
		// server behind it must not get a route naming an unusable model (#248).
		return -1
	}
	return best
}

// ── layout ────────────────────────────────────────────────────────────────────

// chatCode lays out the chat view: the thread strip (once threads exist),
// then either the active thread beside the code panel, or the ctrl+\ split.
// It falls back to chat-only when the terminal is too narrow to split.
func (m Cockpit) chatCode(bodyH int) string {
	var rows []string
	if m.showStrip() {
		rows = append(rows, m.threadStrip(max(1, m.w)))
		bodyH--
		if bodyH < 4 {
			bodyH = 4
		}
	}
	mainW := m.w - 23 // sidebar (21) + right border + gap
	if mainW < 20 {
		mainW = 20
	}
	sidebar := m.sidebar(bodyH)
	var main string
	switch {
	case m.split && m.threadByID(m.splitID) != nil:
		main = m.splitPanes(mainW, bodyH)
	default:
		chatW, split := ckChatSplit(mainW)
		if !split {
			// too tight for the code panel — the whole main pane is chat.
			main = m.chatMain(m.th(), mainW, bodyH)
		} else {
			main = lipgloss.JoinHorizontal(lipgloss.Top,
				m.chatMain(m.th(), chatW, bodyH), m.codePanel(m.th(), mainW-chatW, bodyH))
		}
	}
	rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, sidebar, " ", main))
	return strings.Join(rows, "\n")
}

// chatLogGeom mirrors chatCode/chatMain's layout for the ACTIVE thread so the
// scrollback handler clamps against the same window the renderer draws.
func (m Cockpit) chatLogGeom() (w, logH int) {
	bodyH := m.h - 3
	if bodyH < 6 {
		bodyH = 6
	}
	if m.showStrip() {
		bodyH--
		if bodyH < 4 {
			bodyH = 4
		}
	}
	mainW := m.w - 23
	if mainW < 20 {
		mainW = 20
	}
	t := m.th()
	if m.split && m.threadByID(m.splitID) != nil {
		w = (mainW - 1) / 2
		if w < 10 {
			w = 10
		}
		logH = bodyH - lipgloss.Height(m.inputBar(mainW)) - 1 // pane title
		if t.threadHeaderLine(w) != "" {
			logH--
		}
	} else {
		w = mainW
		if chatW, split := ckChatSplit(mainW); split {
			w = chatW
		}
		logH = bodyH - lipgloss.Height(m.inputBar(w))
		if t.threadHeaderLine(w) != "" {
			logH--
		}
	}
	if logH < 1 {
		logH = 1
	}
	return w, logH
}

// chatScrollBy moves the active thread's scrollback: 0 in scroll means live
// tail, L+1 means anchored at rendered line L — so an anchored reader is never
// yanked when new output appends below.
func (m Cockpit) chatScrollBy(delta int) Cockpit {
	t := m.th()
	w, logH := m.chatLogGeom()
	entryCap := ckLogEntryCap
	if logH < entryCap {
		entryCap = logH
	}
	top := len(ckLogLines(t.log, w, entryCap)) - logH
	if top <= 0 {
		t.scroll = 0 // everything fits — only live makes sense
		return m
	}
	cur := top
	if t.scroll > 0 {
		cur = t.scroll - 1
	}
	cur += delta
	if cur >= top {
		t.scroll = 0 // reached the tail — back to live
		return m
	}
	if cur < 0 {
		cur = 0
	}
	t.scroll = cur + 1
	return m
}

func (m Cockpit) sidebar(h int) string {
	gauge := ckDimS.Render(fmt.Sprintf("claude %d%%", m.claudePct))
	if !m.pctKnown {
		gauge = ckFaintS.Render("no data yet")
	}
	head := []string{
		ckLabelS.Render("CONTEXT BUDGET"),
		ckBar(m.claudePct, 15),
		gauge,
		"",
		ckLabelS.Render("MODELS"),
	}
	foot := []string{"", ckLabelS.Render("MODE"), " " + ckCyanS.Render(m.mode)}

	// lipgloss.Height only pads shorter content — it never truncates taller
	// content — so an uncapped list would dictate the whole view's height once
	// JoinHorizontal pads every other column to match it (#446). Cap it and
	// say how many were left out.
	avail := h - len(head) - len(foot)
	var shown []ckHead
	var more int
	var compact string
	switch {
	case avail <= 0:
		// No room for even one row — the count must not silently contradict
		// the rest of the UI (#506).
		if len(m.heads) > 0 {
			compact = fmt.Sprintf(" %d model%s (not enough room to list)", len(m.heads), plural(len(m.heads)))
		}
	case len(m.heads) <= avail:
		shown = m.heads
	default:
		keep := avail - 1
		shown = m.heads[:keep]
		more = len(m.heads) - keep
	}

	lines := append([]string{}, head...)
	for _, hd := range shown {
		st := ckCheapS.Render("✓")
		if !hd.up {
			st = ckExpS.Render("✗")
		}
		name := lipgloss.NewStyle().Foreground(ckTierColor(hd.tier)).Render(truncate(hd.name, 15))
		lines = append(lines, " "+st+" "+name)
	}
	if more > 0 {
		lines = append(lines, ckDimS.Render(fmt.Sprintf(" +%d more", more)))
	}
	if compact != "" {
		lines = append(lines, ckDimS.Render(compact))
	}
	lines = append(lines, foot...)

	return lipgloss.NewStyle().Width(21).Height(h).
		BorderStyle(lipgloss.NormalBorder()).BorderRight(true).BorderForeground(ckLineC).
		Render(strings.Join(lines, "\n"))
}

// chatMain renders one thread's log above the input bar.
func (m Cockpit) chatMain(t *ckThread, w, h int) string {
	// The pane is the log box plus the input bar, totalling exactly h lines —
	// anything more and Bubble Tea crops the overflow off the TOP, deleting
	// the header on every launch (#445).
	input := m.inputBar(w)
	logH := h - lipgloss.Height(input)
	var rows []string
	if hdr := t.threadHeaderLine(w); hdr != "" {
		rows = append(rows, hdr)
		logH--
	}
	if logH < 1 {
		logH = 1
	}
	rows = append(rows, lipgloss.NewStyle().Width(w).Height(logH).
		Render(ckVisibleLog(t.log, w, logH, t.scroll-1)))
	rows = append(rows, input)
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

// ckInputWrapCap bounds how many wrapped lines of a long input stay visible —
// the newest ones, where the cursor is. The rest is typed, just not shown.
const ckInputWrapCap = 3

// inputBar renders the bordered input with the mode chip and — once threads
// exist to tell apart — its target thread: idle placeholder, wrapped typing,
// the running spinner, or the pending question. Heights too small for a border
// degrade to one bare line — the input never disappears.
func (m Cockpit) inputBar(w int) string {
	t := m.th()
	chip := ckChipS.Render(ckModeByName(m.mode).chip + " ▾")
	if len(m.threads) > 1 {
		chip += ckDimS.Render(fmt.Sprintf(" → thread %d", t.id))
	}
	var body string
	switch {
	case t.exec != nil:
		frames := []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")
		elapsed := time.Since(t.exec.started)
		f := frames[int(elapsed/(200*time.Millisecond))%len(frames)]
		body = ckAquaS.Render(string(f)+" ") + ckInkS.Render(t.exec.Stage()) +
			ckDimS.Render(fmt.Sprintf(" · %.1fs · ", elapsed.Seconds())) + ckFaintS.Render("esc cancels")
	case t.confirm != nil:
		body = ckMidS.Render(t.confirm.question)
	case t.planWait != nil:
		body = ckCyanS.Render("plan ready — enter/y runs it · esc discards")
	case t.queued != nil:
		body = ckDimS.Render("◱ " + t.queued.reason + " · esc discards")
	case t.input == "":
		body = ckFaintS.Render("what do you need done? — enter runs it")
	default:
		body = ckInkS.Render(t.input) + ckAquaS.Render("▏")
	}
	line := chip + " " + body

	bodyH := m.h - 3
	if bodyH < 6 {
		bodyH = 6
	}
	inner := w - 4 // border (2) + padding (2)
	if bodyH < 8 || inner < 10 {
		// Too short for a border: one bare line, tail-truncated so the cursor
		// region stays visible.
		return ckTailTruncate(line, max(1, w))
	}
	wrapped := strings.Split(lipgloss.NewStyle().Width(inner).Render(line), "\n")
	if len(wrapped) > ckInputWrapCap {
		wrapped = wrapped[len(wrapped)-ckInputWrapCap:]
		wrapped[0] = ckFaintS.Render("… ") + wrapped[0]
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).BorderForeground(ckAqua).
		Padding(0, 1).Width(w - 2).
		Render(strings.Join(wrapped, "\n"))
}

// ckChatSplit decides the chat/code split for the main column budget.
func ckChatSplit(mainW int) (chatW int, split bool) {
	chatW = mainW / 2
	return chatW, mainW-chatW >= 22
}

// ckTailTruncate keeps the END of a styled line within n cells — the input's
// newest characters, unlike truncate which keeps the start.
func ckTailTruncate(s string, n int) string {
	if lipgloss.Width(s) <= n {
		return s
	}
	// Binary-search-free: drop rendered head lines via the wrap trick.
	rows := strings.Split(lipgloss.NewStyle().Width(max(1, n-1)).Render(s), "\n")
	return ckFaintS.Render("…") + rows[len(rows)-1]
}

// codePanel renders the code panel beside the chat console: the thread's last
// edit's real content (streamed), or its diff after `d`. w is the full column
// budget.
func (m Cockpit) codePanel(t *ckThread, w, h int) string {
	inner := w - 2 // border-left (1) + padding-left (1)
	if inner < 10 {
		inner = 10
	}
	lang := t.codeLang
	if lang == "" {
		lang = "—"
	}
	title := "CODE"
	if t.codeDiff {
		title = "DIFF"
	}
	rows := []string{ckLabelS.Render(title) + ckDimS.Render(" · "+lang)}

	if len(t.codeLines) == 0 {
		rows = append(rows, "", ckFaintS.Render("no edits yet —"),
			ckFaintS.Render("a task that changes a"), ckFaintS.Render("file streams it here."))
	} else {
		shown := min(t.codeShown, len(t.codeLines))
		// Width() budgets padding too, so a line may only use inner-1 cells;
		// the 4-cell gutter comes out of that. Overshooting wrapped every full
		// line in two and silently halved the panel.
		textW := inner - 1 - 4
		if textW < 4 {
			textW = 4
		}
		// The panel is a preview, not a pager: show the newest lines that fit.
		avail := max(1, h-1)
		start := 0
		if shown > avail {
			start = shown - avail
		}
		for i := start; i < shown; i++ {
			rows = append(rows, ckFaintS.Render(fmt.Sprintf("%3d ", i+1))+
				ckCodeLine(truncate(ckSafe(t.codeLines[i]), textW), t.codeDiff))
		}
		if shown < len(t.codeLines) && len(rows) <= avail { // cursor while streaming
			rows = append(rows, ckFaintS.Render(fmt.Sprintf("%3d ", shown+1))+ckAquaS.Render("▏"))
		}
	}
	if len(rows) > h {
		rows = rows[:h]
	}

	return lipgloss.NewStyle().Width(inner).Height(h).
		BorderStyle(lipgloss.NormalBorder()).BorderLeft(true).BorderForeground(ckLineC).
		PaddingLeft(1).Render(strings.Join(rows, "\n"))
}

// ckCodeLine paints one panel line: diff mode colours by +/-/@@ prefix, code
// mode uses the syntax highlighter.
func ckCodeLine(line string, isDiff bool) string {
	if !isDiff {
		return ckHighlight(line)
	}
	switch {
	case strings.HasPrefix(line, "+"):
		return ckCheapS.Render(line)
	case strings.HasPrefix(line, "-"):
		return ckExpS.Render(line)
	case strings.HasPrefix(line, "@@"):
		return ckCyanS.Render(line)
	default:
		return ckDimS.Render(line)
	}
}

// ── task classification ───────────────────────────────────────────────────────

// classifyTask maps a prompt to a routing enum and its tier — the cockpit's
// route preview; the dispatch layer re-derives its own decision at run time.
func classifyTask(task string) (string, int) {
	t := strings.ToLower(task)
	switch {
	case containsAny(t, "architect", "design", "multi-tenant", "security", "migration", "rotate", "signing", "distributed"):
		return "CORE", 1
	case containsAny(t, "refactor", "review", "debug", "optimi", "concurren", "race"):
		return "COMPLEX", 3
	case containsAny(t, "api", "endpoint", "crud", "pagination", "schema", "handler", "test", "users"):
		return "STANDARD", 6
	}
	if len(task) > 90 {
		return "MODERATE", 5
	}
	return "SIMPLE", 8
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// ── syntax highlighter ───────────────────────────────────────────────────────

// ckKeywords are the Go/TypeScript tokens painted violet by ckHighlight.
var ckKeywords = map[string]bool{
	"func": true, "interface": true, "export": true, "return": true,
	"if": true, "else": true, "for": true, "range": true, "var": true,
	"const": true, "type": true, "struct": true, "map": true, "package": true,
	"import": true, "string": true, "error": true, "nil": true, "bool": true,
	"int": true, "byte": true, "number": true, "boolean": true, "void": true,
	"Date": true, "Context": true, "context": true,
}

// ckHighlight applies minimal lipgloss syntax colouring to one line of code:
// comments faint, string/backtick literals green, keywords violet, the rest ink.
func ckHighlight(line string) string {
	var out strings.Builder
	var word strings.Builder
	flush := func() {
		w := word.String()
		if w == "" {
			return
		}
		if ckKeywords[w] {
			out.WriteString(ckVioletS.Render(w))
		} else {
			out.WriteString(ckInkS.Render(w))
		}
		word.Reset()
	}

	rs := []rune(line)
	for i := 0; i < len(rs); {
		r := rs[i]
		switch {
		case r == '/' && i+1 < len(rs) && rs[i+1] == '/': // line comment → faint to EOL
			flush()
			out.WriteString(ckFaintS.Render(string(rs[i:])))
			return out.String()
		case r == '"' || r == '`' || r == '\'': // string literal → green
			flush()
			j := i + 1
			for j < len(rs) && rs[j] != r {
				j++
			}
			if j < len(rs) {
				j++ // include closing quote
			}
			out.WriteString(ckCheapS.Render(string(rs[i:j])))
			i = j
		case isWordChar(r):
			word.WriteRune(r)
			i++
		default: // punctuation / whitespace → dim
			flush()
			out.WriteString(ckDimS.Render(string(r)))
			i++
		}
	}
	flush()
	return out.String()
}

func isWordChar(r rune) bool {
	return r == '_' ||
		(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}
