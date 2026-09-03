// SPDX-License-Identifier: MIT

package tui

// view_chat.go — view 0: the chat console (bordered input bar with the mode
// chip), its sidebar, and the code panel. Kept thin: execution lives in
// chat_exec.go/chat_task.go, modes in modes.go, the override in override.go.

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
// non-empty input; the letter commands (?, m, d/x/o) fire only from an empty
// one, the same discipline '?' set in phase 1. Pending states are modal.
func (m Cockpit) chatKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case m.modePick:
		return m.modePickerKey(msg)
	case m.ovOpen:
		return m.overrideKey(msg)
	case m.confirm != nil:
		return m.confirmKey(msg)
	case m.planWait != nil:
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
	case tea.KeyEnter:
		return m.submit()
	case tea.KeyBackspace:
		if n := len(m.input); n > 0 {
			m.input = m.input[:n-1]
		}
	case tea.KeySpace:
		m.input += " "
	case tea.KeyRunes:
		// Pasted text is literal input: a paste starting with m/d/x/o must
		// type, not fire the letter commands.
		if m.input == "" && len(msg.Runes) == 1 && !msg.Paste {
			switch msg.Runes[0] {
			case '?':
				m.glossary = true
				return m, nil
			case 'm':
				return m.openModePicker(), nil
			case 'd', 'x', 'o':
				if nm, cmd, ok := m.resultKey(msg.Runes[0]); ok {
					return nm, cmd
				}
			}
		}
		m.input += string(msg.Runes)
	}
	return m, nil
}

// confirmKey is careful mode's y/n gate; esc (handled globally) also declines.
func (m Cockpit) confirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type != tea.KeyRunes || len(msg.Runes) != 1 {
		return m, nil
	}
	w := *m.confirm
	switch msg.Runes[0] {
	case 'y', 'Y':
		m.log = append(m.log, ckDimS.Render("  confirmed — writing"))
		return m.resumeTask(w)
	case 'n', 'N':
		note := "stopped before writing — nothing changed"
		if w.phase == ckPhaseFix {
			note = "fix declined — the file keeps its last write"
		}
		return m.stopWait(w, note)
	}
	return m, nil
}

// planKey is plan mode's approval gate: enter/y/a run the plan, esc (global)
// discards it. Everything else is swallowed while the question stands.
func (m Cockpit) planKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		m.log = append(m.log, ckDimS.Render("  plan approved — running"))
		return m.resumeTask(*m.planWait)
	case tea.KeyRunes:
		if len(msg.Runes) == 1 {
			switch msg.Runes[0] {
			case 'y', 'Y', 'a', 'A':
				m.log = append(m.log, ckDimS.Render("  plan approved — running"))
				return m.resumeTask(*m.planWait)
			}
		}
	}
	return m, nil
}

func (m Cockpit) submit() (tea.Model, tea.Cmd) {
	t := strings.TrimSpace(m.input)
	m.input = ""
	m.chatScroll = 0 // sending your own message returns the view to live
	// Commands are matched case-insensitively (#465) — free-text task input
	// below is not, since a real task's casing is meaningful.
	lt := strings.ToLower(t)
	switch {
	case lt == "":
		// An empty enter after a finished task opens its trace, as the footer says.
		if m.lastDone != nil && m.exec == nil {
			return m.focusRun(m.lastDone.runID), nil
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
		m.log = append(m.log, ckDimS.Render("unknown command "+t))
		return m, nil
	case strings.HasPrefix(lt, "/"):
		if ckIsMode(lt[1:]) {
			m.mode = lt[1:]
			m.log = append(m.log, ckDimS.Render("mode → ")+ckCyanS.Render(m.mode))
		} else {
			m.log = append(m.log, ckDimS.Render("unknown command "+t))
		}
		return m, nil
	}
	if m.exec != nil {
		m.input = t // nothing typed is lost — the task queue is phase 3
		m.flash = "a task is running — esc cancels it"
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

// chatCode lays out the chat console beside the code panel (view 0).
// It falls back to chat-only when the terminal is too narrow to split.
func (m Cockpit) chatCode(bodyH int) string {
	mainW := m.w - 23 // sidebar (21) + right border + gap
	if mainW < 20 {
		mainW = 20
	}
	sidebar := m.sidebar(bodyH)
	chatW, split := ckChatSplit(mainW)
	if !split {
		// too tight to split — give the whole main pane to chat.
		return lipgloss.JoinHorizontal(lipgloss.Top, sidebar, " ", m.chatMain(mainW, bodyH))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top,
		sidebar, " ", m.chatMain(chatW, bodyH), m.codePanel(mainW-chatW, bodyH))
}

// ckChatSplit decides the chat/code split for the main column budget.
func ckChatSplit(mainW int) (chatW int, split bool) {
	chatW = mainW / 2
	return chatW, mainW-chatW >= 22
}

// chatLogGeom mirrors chatCode/chatMain's layout so the scrollback handler
// clamps against the same window the renderer draws.
func (m Cockpit) chatLogGeom() (w, logH int) {
	mainW := m.w - 23
	if mainW < 20 {
		mainW = 20
	}
	w = mainW
	if chatW, split := ckChatSplit(mainW); split {
		w = chatW
	}
	bodyH := m.h - 3
	if bodyH < 6 {
		bodyH = 6
	}
	logH = bodyH - lipgloss.Height(m.inputBar(w))
	if logH < 1 {
		logH = 1
	}
	return w, logH
}

// chatScrollBy moves the chat scrollback: 0 in m.chatScroll means live tail,
// L+1 means anchored at rendered line L — so an anchored reader is never
// yanked when new output appends below.
func (m Cockpit) chatScrollBy(delta int) Cockpit {
	w, logH := m.chatLogGeom()
	entryCap := ckLogEntryCap
	if logH < entryCap {
		entryCap = logH
	}
	top := len(ckLogLines(m.log, w, entryCap)) - logH
	if top <= 0 {
		m.chatScroll = 0 // everything fits — only live makes sense
		return m
	}
	cur := top
	if m.chatScroll > 0 {
		cur = m.chatScroll - 1
	}
	cur += delta
	if cur >= top {
		m.chatScroll = 0 // reached the tail — back to live
		return m
	}
	if cur < 0 {
		cur = 0
	}
	m.chatScroll = cur + 1
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

func (m Cockpit) chatMain(w, h int) string {
	// The pane is the log box plus the input bar, totalling exactly h lines —
	// anything more and Bubble Tea crops the overflow off the TOP, deleting
	// the header on every launch (#445).
	input := m.inputBar(w)
	logH := h - lipgloss.Height(input)
	if logH < 1 {
		logH = 1
	}
	logBox := lipgloss.NewStyle().Width(w).Height(logH).Render(ckVisibleLog(m.log, w, logH, m.chatScroll-1))
	return lipgloss.JoinVertical(lipgloss.Left, logBox, input)
}

// ckInputWrapCap bounds how many wrapped lines of a long input stay visible —
// the newest ones, where the cursor is. The rest is typed, just not shown.
const ckInputWrapCap = 3

// inputBar renders the bordered input with the mode chip: idle placeholder,
// wrapped typing, the running spinner, or the pending question. At heights too
// small for a border it degrades to one bare line, so the input can never
// disappear (#506's discipline).
func (m Cockpit) inputBar(w int) string {
	chip := ckChipS.Render(ckModeByName(m.mode).chip + " ▾")
	var body string
	switch {
	case m.exec != nil:
		frames := []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")
		elapsed := time.Since(m.exec.started)
		f := frames[int(elapsed/(200*time.Millisecond))%len(frames)]
		body = ckAquaS.Render(string(f)+" ") + ckInkS.Render(m.exec.Stage()) +
			ckDimS.Render(fmt.Sprintf(" · %.1fs · ", elapsed.Seconds())) + ckFaintS.Render("esc cancels")
	case m.confirm != nil:
		body = ckMidS.Render(m.confirm.question)
	case m.planWait != nil:
		body = ckCyanS.Render("plan ready — enter/y runs it · esc discards")
	case m.input == "":
		body = ckFaintS.Render("what do you need done? — enter runs it")
	default:
		body = ckInkS.Render(m.input) + ckAquaS.Render("▏")
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

// codePanel renders the code panel beside the chat console: the last edit's
// real content (streamed), or its diff after `d`. w is the full column budget.
func (m Cockpit) codePanel(w, h int) string {
	inner := w - 2 // border-left (1) + padding-left (1)
	if inner < 10 {
		inner = 10
	}
	var b strings.Builder
	lang := m.codeLang
	if lang == "" {
		lang = "—"
	}
	title := "CODE"
	if m.codeDiff {
		title = "DIFF"
	}
	b.WriteString(ckLabelS.Render(title) + ckDimS.Render(" · "+lang) + "\n")

	if len(m.codeLines) == 0 {
		b.WriteString("\n" + ckFaintS.Render("no edits yet —") + "\n" +
			ckFaintS.Render("a task that changes a") + "\n" +
			ckFaintS.Render("file streams it here."))
	} else {
		shown := m.codeShown
		if shown > len(m.codeLines) {
			shown = len(m.codeLines)
		}
		textW := inner - 4 // "NN " gutter + slack
		if textW < 4 {
			textW = 4
		}
		// The panel is a preview, not a pager: show the newest lines that fit.
		avail := h - 1
		if avail < 1 {
			avail = 1
		}
		start := 0
		if shown > avail {
			start = shown - avail
		}
		for i := start; i < shown; i++ {
			gutter := ckFaintS.Render(fmt.Sprintf("%3d ", i+1))
			b.WriteString(gutter + ckCodeLine(truncate(ckSafe(m.codeLines[i]), textW), m.codeDiff) + "\n")
		}
		if shown < len(m.codeLines) { // cursor while streaming
			b.WriteString(ckFaintS.Render(fmt.Sprintf("%3d ", shown+1)) + ckAquaS.Render("▏"))
		}
	}

	return lipgloss.NewStyle().Width(inner).Height(h).
		BorderStyle(lipgloss.NormalBorder()).BorderLeft(true).BorderForeground(ckLineC).
		PaddingLeft(1).Render(b.String())
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
