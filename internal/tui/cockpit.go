// SPDX-License-Identifier: MIT

package tui

// cockpit.go — the interactive terminal cockpit (`hydra tui`).
// A real Bubble Tea program: chat/console + heads sidebar + live routing
// decisions, with a dashboard view (Tab). Neon identity, aqua interaction.

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ── palette (ck-prefixed to avoid clashing with splash.go/init.go) ──────────────

var (
	ckAqua    = lipgloss.Color("#00E6C3")
	ckCyan    = lipgloss.Color("#2AF0E0")
	ckViolet  = lipgloss.Color("#8B5CF6")
	ckMagenta = lipgloss.Color("#E852C8")
	ckInk     = lipgloss.Color("#E7E9F5")
	ckDimc    = lipgloss.Color("#9AA0C4")
	ckFaint   = lipgloss.Color("#5A5F85")
	ckCheap   = lipgloss.Color("#3FD98A")
	ckMid     = lipgloss.Color("#E0A93A")
	ckExp     = lipgloss.Color("#FF5A6E")
	ckLineC   = lipgloss.Color("#2A2E52")

	ckAquaS   = lipgloss.NewStyle().Foreground(ckAqua)
	ckCyanS   = lipgloss.NewStyle().Foreground(ckCyan)
	ckVioletS = lipgloss.NewStyle().Foreground(ckViolet)
	ckInkS    = lipgloss.NewStyle().Foreground(ckInk)
	ckDimS    = lipgloss.NewStyle().Foreground(ckDimc)
	ckFaintS  = lipgloss.NewStyle().Foreground(ckFaint)
	ckCheapS  = lipgloss.NewStyle().Foreground(ckCheap)
	ckMidS    = lipgloss.NewStyle().Foreground(ckMid)
	ckExpS    = lipgloss.NewStyle().Foreground(ckExp)
	ckLabelS  = lipgloss.NewStyle().Foreground(ckFaint).Bold(true)
	ckYouS    = lipgloss.NewStyle().Foreground(ckInk).Bold(true)
	ckBoxS    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(ckLineC).Padding(0, 1)
)

var ckFileRe = regexp.MustCompile(`[\w/]+\.(go|ts|js|py|rs|java)`)

// ── model ───────────────────────────────────────────────────────────────────────

type ckHead struct {
	name  string
	tier  int
	price float64
	up    bool
	color lipgloss.Color
}

// Cockpit views. The name table is the single source of truth — deriving the
// count and the bounds check from it keeps the Tab cycle, the header label, and
// the --view validation from drifting apart when a view is added.
const (
	ckViewChatCode = iota
	ckViewDashboard
	ckViewAgentTree
)

var ckViewNames = []string{"chat+code", "dashboard", "agent-tree"}

// ckViewCount is how many views exist.
func ckViewCount() int { return len(ckViewNames) }

// ckViewName is total: an out-of-range view yields the default label rather
// than panicking. `--snapshot --view N` reaches the header with unvalidated N.
func ckViewName(v int) string {
	if !ckValidView(v) {
		return ckViewNames[ckViewChatCode]
	}
	return ckViewNames[v]
}

// ckValidView reports whether v names a real view.
func ckValidView(v int) bool { return v >= 0 && v < len(ckViewNames) }

// ValidSnapshotView reports whether view is a usable --view value, and returns
// the valid view names so a caller can build a useful error message.
func ValidSnapshotView(view int) (ok bool, names []string) {
	return ckValidView(view), append([]string(nil), ckViewNames...)
}

// Cockpit is the interactive `hydra tui` model.
type Cockpit struct {
	w, h      int
	ready     bool
	view      int // one of the ckView* constants
	input     string
	log       []string
	heads     []ckHead
	mode      string
	runs      int
	saved     float64
	local     int
	claudePct int

	// live code panel (chat+code view): a snippet streamed line-by-line.
	codeLang  string
	codeLines []string
	codeShown int
	codeGen   int // generation guard so a new run cancels stale tick loops

	// last dispatch confidence (dashboard figure).
	lastConf   float64
	lastTarget float64

	// agent-tree view selection cursor.
	treeSel int
}

// NewCockpit builds the cockpit model with discovered-head placeholders.
func NewCockpit() Cockpit {
	return Cockpit{
		mode:      "dispatch",
		claudePct: 52,
		heads: []ckHead{
			{"agy · claude", 1, 0.015, true, ckViolet},
			{"gemini pro", 5, 0.0012, true, ckCyan},
			{"gemini flash", 8, 0.00012, true, ckCyan},
			{"openrouter", 6, 0.0009, true, ckCyan},
			{"qwen · local", 10, 0, true, ckCheap},
		},
		log: []string{
			ckDimS.Render("🐉 Hydra initialised · 5 heads discovered · routing engine ready."),
			ckDimS.Render("Type a task and press enter. Tab = chat/dash/tree · /trust /swarm /local · :q quits."),
		},
	}
}

func (m Cockpit) Init() tea.Cmd { return nil }

func (m Cockpit) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h, m.ready = msg.Width, msg.Height, true
	case ckCodeTickMsg:
		// Reveal one more code line; ignore ticks from a superseded run.
		if msg.gen == m.codeGen && m.codeShown < len(m.codeLines) {
			m.codeShown++
			if m.codeShown < len(m.codeLines) {
				return m, ckCodeTick(m.codeGen)
			}
		}
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC:
			return m, tea.Quit
		case tea.KeyTab:
			m.view = (m.view + 1) % ckViewCount()
		case tea.KeyUp:
			if m.view == 2 && m.treeSel > 0 {
				m.treeSel--
			}
		case tea.KeyDown:
			if m.view == 2 && m.treeSel < len(ckTree)-1 {
				m.treeSel++
			}
		case tea.KeyEnter:
			return m.submit()
		case tea.KeyBackspace:
			if n := len(m.input); n > 0 {
				m.input = m.input[:n-1]
			}
		case tea.KeyEsc:
			m.input = ""
		case tea.KeySpace:
			m.input += " "
		case tea.KeyRunes:
			m.input += string(msg.Runes)
		}
	}
	return m, nil
}

// ── code-stream ticker ─────────────────────────────────────────────────────────

type ckCodeTickMsg struct{ gen int }

// ckCodeTick schedules the next code line to reveal. gen tags the current run so
// a fresh dispatch cancels the previous stream instead of double-speeding it.
func ckCodeTick(gen int) tea.Cmd {
	return tea.Tick(time.Second/20, func(time.Time) tea.Msg { return ckCodeTickMsg{gen} })
}

func (m Cockpit) submit() (tea.Model, tea.Cmd) {
	t := strings.TrimSpace(m.input)
	m.input = ""
	switch {
	case t == "":
		return m, nil
	case t == ":q" || t == ":quit":
		return m, tea.Quit
	case t == ":dash":
		m.view = 1
		return m, nil
	case t == ":chat":
		m.view = 0
		return m, nil
	case t == ":tree":
		m.view = 2
		return m, nil
	case strings.HasPrefix(t, "/"):
		switch t {
		case "/dispatch", "/swarm", "/trust", "/local":
			m.mode = t[1:]
			m.log = append(m.log, ckDimS.Render("mode → ")+ckCyanS.Render(m.mode))
		default:
			m.log = append(m.log, ckDimS.Render("unknown command "+t))
		}
		return m, nil
	}
	nm := m.run(t)
	return nm, ckCodeTick(nm.codeGen)
}

// baseline returns the cost and short model name of the priciest available head —
// the "route everything to the top tier" reference the savings are measured against.
// Provider-neutral by construction: it reflects whatever the most expensive
// discovered head happens to be, never a hardcoded vendor.
func (m Cockpit) baseline() (float64, string) {
	base, name := 0.0, "frontier"
	for _, h := range m.heads {
		if h.up && h.price > base {
			base, name = h.price, ckBaseName(h.name)
		}
	}
	return base, name
}

// ckBaseName trims a "provider · model" head label down to the model part.
func ckBaseName(n string) string {
	if i := strings.LastIndex(n, "· "); i >= 0 {
		return strings.TrimSpace(n[i+len("· "):])
	}
	return n
}

// run simulates a dispatch and appends the live routing decision to the log.
func (m Cockpit) run(task string) Cockpit {
	m.runs++
	m.log = append(m.log, ckYouS.Render("❯ "+task))

	enum, wantTier := classifyTask(task, m.mode)
	idx := m.pickHead(wantTier)
	h := m.heads[idx]
	fell := m.mode != "local" && h.tier > wantTier

	// Load a class-appropriate snippet and (re)start the code stream.
	m.codeLang, m.codeLines = ckSnippet(enum)
	m.codeShown = 0
	m.codeGen++

	cost := h.price
	base, baseName := m.baseline()
	saved := base - cost
	if saved < 0 {
		saved = 0
	}
	m.saved += saved
	if h.tier >= 9 {
		m.local++
	}

	target := 0.80
	if m.mode == "trust" || hasFilePath(task) {
		target = 0.95
	}
	reached := target
	if h.tier <= 3 {
		reached += 0.03
	}
	if h.tier >= 9 {
		reached -= 0.06
	}
	if reached > 0.999 {
		reached = 0.999
	}
	m.lastConf, m.lastTarget = reached, target

	flow := ckDimS.Render("  prompt ") + ckAquaS.Render("→ ") + ckInkS.Render(enum) +
		ckAquaS.Render(" → ") + ckDimS.Render(fmt.Sprintf("T%d", wantTier))
	if fell {
		flow += ckAquaS.Render(" → ") + ckMidS.Render("✗ rate-limited") +
			ckAquaS.Render(" → ") + ckCyanS.Render(fmt.Sprintf("T%d fallback", h.tier))
	}
	flow += ckAquaS.Render(" → ") + lipgloss.NewStyle().Foreground(h.color).Render(h.name)
	m.log = append(m.log, flow)

	costStr := "free (local)"
	if cost > 0 {
		costStr = fmt.Sprintf("$%.4f", cost)
	}
	m.log = append(m.log, ckDimS.Render("  route  ")+
		lipgloss.NewStyle().Foreground(h.color).Render(h.name)+
		ckDimS.Render(fmt.Sprintf("  %s  vs all-%s $%.4f", costStr, baseName, base)))
	if hasFilePath(task) {
		m.log = append(m.log, ckDimS.Render("  blast  ")+ckExpS.Render("κ=3.1 ⚠")+
			ckDimS.Render("  12 dependents → confidence bar raised to 0.95"))
	}
	m.log = append(m.log, ckDimS.Render("  conf   ")+ckCyanS.Render(fmt.Sprintf("%.2f", reached))+
		ckDimS.Render(fmt.Sprintf(" / target %.2f · SPRT ", target))+ckCheapS.Render("accept"))
	m.log = append(m.log, ckCheapS.Render("  ✔ done")+ckDimS.Render("  · saved ")+
		ckCheapS.Render(fmt.Sprintf("$%.4f", saved)))

	m.claudePct = min(96, 52+m.runs*3)
	return m
}

// pickHead finds the cheapest available head at or below the wanted strength,
// falling back down the ladder to local qwen when heads are down.
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
		return len(m.heads) - 1
	}
	return best
}

// ── view ──────────────────────────────────────────────────────────────────────

func (m Cockpit) View() string {
	if !m.ready {
		return "\n  starting hydra cockpit…\n"
	}
	bodyH := m.h - 3
	if bodyH < 6 {
		bodyH = 6
	}
	var body string
	switch m.view {
	case 1:
		body = m.dash(m.w, bodyH)
	case 2:
		body = m.tree(m.w, bodyH)
	default:
		body = m.chatCode(bodyH)
	}
	return m.header() + "\n" + ckFaintS.Render(strings.Repeat("─", max(1, m.w))) + "\n" + body + "\n" + m.hint()
}

// chatCode lays out the chat console beside the live code panel (view 0).
// It falls back to chat-only when the terminal is too narrow to split.
func (m Cockpit) chatCode(bodyH int) string {
	mainW := m.w - 23 // sidebar (21) + right border + gap
	if mainW < 20 {
		mainW = 20
	}
	sidebar := m.sidebar(bodyH)
	chatW := mainW / 2
	codeW := mainW - chatW
	if codeW < 22 {
		// too tight to split — give the whole main pane to chat.
		return lipgloss.JoinHorizontal(lipgloss.Top, sidebar, " ", m.chatMain(mainW, bodyH))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top,
		sidebar, " ", m.chatMain(chatW, bodyH), m.codePanel(codeW, bodyH))
}

func (m Cockpit) header() string {
	viewName := ckViewName(m.view)
	left := ckWordmark("HYDRA") + ckDimS.Render(" ▸ ") + ckCyanS.Render(viewName) +
		ckDimS.Render(" · heads: agy·gemini·openrouter·qwen")
	mode, mc := "normal", ckCheapS
	switch {
	case m.claudePct >= 75:
		mode, mc = "critical", ckExpS
	case m.claudePct >= 65:
		mode, mc = "warning", ckMidS
	case m.claudePct >= 50:
		mode, mc = "compact", ckMidS
	}
	right := ckDimS.Render("MODE ") + mc.Render(mode) +
		ckDimS.Render(fmt.Sprintf(" %d%%   ", m.claudePct)) +
		ckDimS.Render("saved ") + ckCheapS.Render(fmt.Sprintf("$%.2f", m.saved))
	gap := m.w - lipgloss.Width(left) - lipgloss.Width(right) - 2
	if gap < 1 {
		gap = 1
	}
	return " " + left + strings.Repeat(" ", gap) + right
}

func (m Cockpit) sidebar(h int) string {
	var b strings.Builder
	b.WriteString(ckLabelS.Render("GOVERNOR") + "\n")
	b.WriteString(ckBar(m.claudePct, 15) + "\n")
	b.WriteString(ckDimS.Render(fmt.Sprintf("claude %d%%", m.claudePct)) + "\n\n")
	b.WriteString(ckLabelS.Render("HEADS") + "\n")
	for _, hd := range m.heads {
		st := ckCheapS.Render("✓")
		if !hd.up {
			st = ckExpS.Render("✗")
		}
		name := lipgloss.NewStyle().Foreground(hd.color).Render(truncate(hd.name, 15))
		b.WriteString(" " + st + " " + name + "\n")
	}
	b.WriteString("\n" + ckLabelS.Render("MODE") + "\n " + ckCyanS.Render(m.mode) + "\n")
	return lipgloss.NewStyle().Width(21).Height(h).
		BorderStyle(lipgloss.NormalBorder()).BorderRight(true).BorderForeground(ckLineC).
		Render(b.String())
}

func (m Cockpit) chatMain(w, h int) string {
	logH := h - 1
	if logH < 1 {
		logH = 1
	}
	lines := m.log
	if len(lines) > logH {
		lines = lines[len(lines)-logH:]
	}
	logBox := lipgloss.NewStyle().Width(w).Height(logH).Render(strings.Join(lines, "\n"))
	input := ckCyanS.Render(m.mode+" ❯ ") + ckInkS.Render(m.input) + ckAquaS.Render("▏")
	return lipgloss.JoinVertical(lipgloss.Left, logBox, ckFaintS.Render(strings.Repeat("╌", max(1, w))), input)
}

func (m Cockpit) hint() string {
	k := func(s string) string { return ckAquaS.Render(s) }
	return ckFaintS.Render(" ") + k("enter") + ckFaintS.Render(" dispatch   ") +
		k("tab") + ckFaintS.Render(" chat/dash/tree   ") +
		k("↑↓") + ckFaintS.Render(" select   ") +
		k("/trust /swarm /local") + ckFaintS.Render(" mode   ") +
		k(":q") + ckFaintS.Render(" quit")
}

// ── snapshot (static render for docs / non-tty preview) ─────────────────────────

// CockpitSnapshotView renders one static frame of the given view (0 chat+code,
// 1 dashboard, 2 agent-tree) after two demo dispatches, with the code stream and
// tree selection settled so the frame is fully populated. An out-of-range view
// falls back to the default instead of panicking; callers that can report an
// error to the user should reject it up front with ValidSnapshotView.
func CockpitSnapshotView(view int) string {
	m := NewCockpit()
	m = m.run("write a User DTO for profile settings")            // SIMPLE → TS interface
	m = m.run("rotate the signing key in internal/auth/token.go") // CORE   → Go key-rotation
	m.codeShown = len(m.codeLines)                                // reveal the whole snippet
	m.treeSel = 2                                                 // highlight the token-rotation node
	if !ckValidView(view) {
		view = ckViewChatCode
	}
	m.view = view
	m.w, m.h, m.ready = 100, 30, true
	return m.View()
}

// CockpitSnapshot renders all three views stacked, each labelled — the
// representative frame shown by `hydra tui --snapshot`.
func CockpitSnapshot() string {
	label := func(s string) string { return ckLabelS.Render("── " + s + " " + strings.Repeat("─", 40)) }
	return label("VIEW 1/3 · CHAT + CODE (tab)") + "\n" + CockpitSnapshotView(0) + "\n\n" +
		label("VIEW 2/3 · DASHBOARD (tab)") + "\n" + CockpitSnapshotView(1) + "\n\n" +
		label("VIEW 3/3 · AGENT TREE (tab · ↑↓ select)") + "\n" + CockpitSnapshotView(2)
}

// ── helpers ─────────────────────────────────────────────────────────────────────

func ckWordmark(s string) string {
	rs := []rune(s)
	n := len(rs)
	var b strings.Builder
	for i, r := range rs {
		t := 0.0
		if n > 1 {
			t = float64(i) / float64(n-1)
		}
		b.WriteString(lipgloss.NewStyle().Foreground(ckLerpHex(t)).Bold(true).Render(string(r)))
	}
	return b.String()
}

// ckLerpHex blends cyan → violet → magenta across t∈[0,1].
func ckLerpHex(t float64) lipgloss.Color {
	var a, b [3]int
	if t < 0.5 {
		a, b, t = [3]int{0x2A, 0xF0, 0xE0}, [3]int{0x8B, 0x5C, 0xF6}, t/0.5
	} else {
		a, b, t = [3]int{0x8B, 0x5C, 0xF6}, [3]int{0xE8, 0x52, 0xC8}, (t-0.5)/0.5
	}
	l := func(x, y int) int { return int(float64(x) + float64(y-x)*t) }
	return lipgloss.Color(fmt.Sprintf("#%02X%02X%02X", l(a[0], b[0]), l(a[1], b[1]), l(a[2], b[2])))
}

func ckBar(pct, width int) string {
	fill := pct * width / 100
	if fill > width {
		fill = width
	}
	if fill < 0 {
		fill = 0
	}
	col := ckCheap
	switch {
	case pct >= 75:
		col = ckExp
	case pct >= 50:
		col = ckMid
	}
	return lipgloss.NewStyle().Foreground(col).Render(strings.Repeat("█", fill)) +
		ckFaintS.Render(strings.Repeat("░", width-fill))
}

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

func hasFilePath(s string) bool { return ckFileRe.MatchString(s) }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}
