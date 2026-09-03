// SPDX-License-Identifier: MIT

package tui

// view_chat.go — view 0: the chat console, its sidebar, and the live code
// panel. Routing preview only in this phase: nothing is executed (#597 adds
// that), and the preview says so. Kept thin: threads/modes land in #597.

import (
	"fmt"
	"regexp"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var ckFileRe = regexp.MustCompile(`[\w/]+\.(go|ts|js|py|rs|java)`)

// chatKey handles keys while the chat view is active. Typing always wins:
// digits and letters go to the input; the one addition is '?' on an EMPTY
// input, which opens the glossary — a keystroke that never produced a useful
// task, so no existing binding is overridden.
func (m Cockpit) chatKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		return m.submit()
	case tea.KeyBackspace:
		if n := len(m.input); n > 0 {
			m.input = m.input[:n-1]
		}
	case tea.KeySpace:
		m.input += " "
	case tea.KeyRunes:
		if string(msg.Runes) == "?" && m.input == "" {
			m.glossary = true
			return m, nil
		}
		m.input += string(msg.Runes)
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
		switch lt {
		case "/dispatch", "/swarm", "/trust", "/local":
			m.mode = lt[1:]
			m.log = append(m.log, ckDimS.Render("mode → ")+ckCyanS.Render(m.mode))
		default:
			m.log = append(m.log, ckDimS.Render("unknown command "+t))
		}
		return m, nil
	}
	nm := m.run(t)
	return nm, ckCodeTick(nm.codeGen)
}

// run previews the routing decision for a task and appends it to the log.
// It does not execute anything — see the note it prints.
func (m Cockpit) run(task string) Cockpit {
	m.runs++
	m.log = append(m.log, ckYouS.Render("❯ "+task))

	enum, wantTier := classifyTask(task, m.mode)
	pinned := m.pinnedTier > 0 && m.mode != "local"
	if pinned {
		wantTier = m.pinnedTier
	}
	idx := m.pickHead(wantTier)
	// The roster is a real scan, so it can legitimately be empty on a machine
	// with nothing installed. There is no route to preview then, and inventing
	// one is exactly what #189 removed.
	if idx < 0 {
		m.log = append(m.log, ckDimS.Render("  no routable model — run `hyctl probe` to see why"))
		return m
	}
	h := m.heads[idx]
	fell := m.mode != "local" && h.tier > wantTier

	// Load a class-appropriate snippet and (re)start the code stream.
	m.codeLang, m.codeLines = ckSnippet(enum)
	m.codeShown = 0
	m.codeGen++

	tierLabel := fmt.Sprintf("T%d", wantTier)
	if pinned {
		tierLabel = fmt.Sprintf("pinned T%d", wantTier)
	}
	name := lipgloss.NewStyle().Foreground(ckTierColor(h.tier))
	flow := ckDimS.Render("  prompt ") + ckAquaS.Render("→ ") + ckInkS.Render(enum) +
		ckAquaS.Render(" → ") + ckDimS.Render(tierLabel)
	if fell {
		flow += ckAquaS.Render(" → ") + ckMidS.Render("no model at that tier") +
			ckAquaS.Render(" → ") + ckCyanS.Render(fmt.Sprintf("T%d", h.tier))
	}
	flow += ckAquaS.Render(" → ") + name.Render(h.name)
	m.log = append(m.log, flow)

	// Free is a claim only a local model can make; a paid model with no
	// pricing data reads "cost unknown" rather than implying free. The
	// "vs all-frontier" comparison lives in the usage view, not per line.
	costStr := "cost unknown"
	switch {
	case h.price > 0:
		costStr = fmt.Sprintf("~$%.4f", h.price)
	case h.local:
		costStr = "free (local)"
	}
	m.log = append(m.log, ckDimS.Render("  route  ")+name.Render(h.name)+ckDimS.Render("  "+costStr))

	// Real change impact, walked from graph.json, when the prompt names a file
	// that is actually in the graph — never a fixed number (#193).
	if f := ckFileRe.FindString(task); f != "" {
		if radius, deps, kappa, ok := m.metrics.ckBlastFor(f); ok {
			risk := ckCheapS
			if kappa >= 2 {
				risk = ckExpS
			}
			m.log = append(m.log, ckDimS.Render("  impact ")+
				risk.Render(fmt.Sprintf("κ=%.1f", kappa))+
				ckDimS.Render(fmt.Sprintf("  %d dependent%s · radius %.2f×  → %s",
					deps, plural(deps), radius, truncate(f, 40))))
		}
	}

	m.log = append(m.log, ckDimS.Render("  plan   ")+
		ckDimS.Render("routing preview only — chat does not send requests yet"))

	return m
}

// pickHead finds the cheapest available head at or below the wanted strength,
// falling back down the ladder to a local model when heads are down.
func (m Cockpit) pickHead(wantTier int) int {
	best := -1
	for i, h := range m.heads {
		if h.tier >= wantTier && h.up {
			if best == -1 || h.tier < m.heads[best].tier {
				best = i
			}
		}
	}
	if best == -1 {
		for i, h := range m.heads {
			if h.tier >= 10 && h.up {
				return i
			}
		}
		// Nothing is routable — a machine with only the ollama binary and no
		// server behind it must not get a preview naming an unusable model (#248).
		return -1
	}
	return best
}

// ── layout ────────────────────────────────────────────────────────────────────

// chatCode lays out the chat console beside the live code panel (view 0).
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
	logH = bodyH - 2
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
	// h covers the log box, the ╌ divider below it, and the input line — not
	// just the log box, or the total comes out to h+1 and Bubble Tea crops the
	// overflow off the TOP, deleting the header on every launch (#445).
	logH := h - 2
	if logH < 1 {
		logH = 1
	}
	logBox := lipgloss.NewStyle().Width(w).Height(logH).Render(ckVisibleLog(m.log, w, logH, m.chatScroll-1))
	input := ckCyanS.Render(m.mode+" ❯ ") + ckInkS.Render(m.input) + ckAquaS.Render("▏")
	return lipgloss.JoinVertical(lipgloss.Left, logBox, ckFaintS.Render(strings.Repeat("╌", max(1, w))), input)
}

// codePanel renders the streaming code snippet beside the chat console. w is the
// full column budget (content + left border + left padding).
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
	b.WriteString(ckLabelS.Render("CODE") + ckDimS.Render(" · "+lang) + "\n")

	if len(m.codeLines) == 0 {
		b.WriteString("\n" + ckFaintS.Render("awaiting a request —") + "\n" +
			ckFaintS.Render("type a task to preview") + "\n" +
			ckFaintS.Render("its route here."))
	} else {
		shown := m.codeShown
		if shown > len(m.codeLines) {
			shown = len(m.codeLines)
		}
		textW := inner - 4 // "NN " gutter + slack
		if textW < 4 {
			textW = 4
		}
		for i := 0; i < shown; i++ {
			gutter := ckFaintS.Render(fmt.Sprintf("%2d ", i+1))
			b.WriteString(gutter + ckHighlight(truncate(m.codeLines[i], textW)) + "\n")
		}
		if shown < len(m.codeLines) { // blinking-ish cursor while streaming
			b.WriteString(ckFaintS.Render(fmt.Sprintf("%2d ", shown+1)) + ckAquaS.Render("▏"))
		}
	}

	return lipgloss.NewStyle().Width(inner).Height(h).
		BorderStyle(lipgloss.NormalBorder()).BorderLeft(true).BorderForeground(ckLineC).
		PaddingLeft(1).Render(b.String())
}

// ── task classification (preview only) ───────────────────────────────────────

func classifyTask(task, mode string) (string, int) {
	if mode == "local" {
		return "LOCAL", 10
	}
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

// ckSnippet returns a short, class-appropriate code sample to stream into the
// code panel: a TS interface for SIMPLE, a Go handler for STANDARD, and a Go
// key-rotation func for CORE-flavoured work.
func ckSnippet(enum string) (string, []string) {
	switch enum {
	case "CORE", "COMPLEX":
		return "go", []string{
			"// rotate the active signing key without breaking live tokens",
			"func RotateSigningKey(ctx context.Context, ks KeyStore) error {",
			"    next, err := GenerateKey()",
			"    if err != nil {",
			"        return fmt.Errorf(\"generate: %w\", err)",
			"    }",
			"    ks.Stage(next)         // new key signs from now on",
			"    ks.Retire(ks.Active()) // old key still verifies",
			"    return ks.Commit(ctx)",
			"}",
		}
	case "STANDARD", "MODERATE":
		return "go", []string{
			"// paginated users endpoint",
			"func (s *Server) ListUsers(w http.ResponseWriter, r *http.Request) {",
			"    page := parsePage(r.URL.Query())",
			"    users, err := s.repo.Users(r.Context(), page)",
			"    if err != nil {",
			"        http.Error(w, err.Error(), 500)",
			"        return",
			"    }",
			"    json.NewEncoder(w).Encode(users)",
			"}",
		}
	default: // SIMPLE / LOCAL
		return "typescript", []string{
			"// User profile settings DTO",
			"export interface UserProfile {",
			"    id: string;",
			"    displayName: string;",
			"    email: string;",
			"    locale: string;",
			"    createdAt: Date;",
			"}",
		}
	}
}
