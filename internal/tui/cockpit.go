// SPDX-License-Identifier: MIT

package tui

// cockpit.go — the interactive terminal cockpit (`hyctl tui`).
// A real Bubble Tea program: chat/console + heads sidebar + live routing
// decisions, with a dashboard view (Tab). Neon identity, aqua interaction.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ankit373/hydra/internal/budget"
	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/cost"
	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/pricing"
	"github.com/ankit373/hydra/internal/probe"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/rank"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/security"
	"github.com/ankit373/hydra/internal/tree"
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
	id    string
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
	ckViewSecurity
)

var ckViewNames = []string{"chat+code", "dashboard", "agent-tree", "security"}

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

// ckHeadsFrom converts a real probe result into display rows, ranked the way
// dispatch ranks them so the cockpit shows the order routing would actually
// use. price comes from the live pricing DB; a head with no known price shows
// 0, which renders as "—" rather than a fabricated figure.
func ckHeadsFrom(heads []provider.Head, pr *pricing.DB) []ckHead {
	out := make([]ckHead, 0, len(heads))
	for _, h := range heads {
		tier := rank.UITier(h)
		var price float64
		if pr != nil {
			// Per-1K-token yardstick, only for the relative cost colour ramp.
			price = pr.EstimateCost(tier, 1000, 0)
		}
		out = append(out, ckHead{
			id:    h.ID,
			name:  h.Name,
			tier:  tier,
			price: price,
			up:    executor.Supports(h),
			color: ckTierColor(tier),
		})
	}
	return out
}

// ckTierColor maps a capability tier onto the cost ramp: cheap local heads
// green, mid amber, expensive frontier heads violet/red.
func ckTierColor(tier int) lipgloss.Color {
	switch {
	case tier <= 2:
		return ckViolet
	case tier <= 6:
		return ckCyan
	default:
		return ckCheap
	}
}

// Cockpit is the interactive `hyctl tui` model.
type Cockpit struct {
	w, h      int
	ready     bool
	view      int // one of the ckView* constants
	input     string
	log       []string
	heads     []ckHead
	mode      string
	runs      int
	claudePct int
	spend     float64   // today's real estimated spend, from cost.jsonl
	metrics   ckMetrics // real latency/savings/blast/calibration, loaded once

	// security is the OWASP LLM Top-10 coverage/ledger report, loaded once at
	// startup like everything else here — nil if Build failed, handled by the
	// security view the same way an empty machine is handled elsewhere.
	security *security.Report

	// live code panel (chat+code view): a snippet streamed line-by-line.
	codeLang  string
	codeLines []string
	codeShown int
	codeGen   int // generation guard so a new run cancels stale tick loops

	// agent-tree view: the reconstructed run and its flattened rows.
	treeSel  int
	runID    string
	runLive  bool
	treeRows []tree.Row
}

// NewCockpit builds the cockpit from the machine's real state: heads from a
// probe, the governor from state.json, spend from cost.jsonl.
//
// It previously shipped a hardcoded roster of five heads that may not exist on
// this machine, a governor that counted up from 52 as you typed, and a price
// table used to compute "savings" — all presented as live telemetry, and all
// reachable by `--snapshot`, which generates imagery for the docs site (#189).
// Anything not yet measurable is now omitted rather than simulated.
func NewCockpit() Cockpit {
	ctx, cancel := context.WithTimeout(context.Background(), ckProbeTimeout)
	defer cancel()
	probed := probe.Run(ctx)

	pr := pricing.Load()
	heads := ckHeadsFrom(probed.Heads, pr)

	pct := ckClaudePct()
	secReport, _ := security.Build(probed.Heads) // best-effort: nil on error, handled by the view
	metrics := ckLoadMetrics(pr)
	m := Cockpit{
		mode:      "dispatch",
		claudePct: pct,
		heads:     heads,
		spend:     metrics.spendUSD, // same value ckSpendToday used to fetch with its own separate cost.jsonl read
		metrics:   metrics,
		security:  secReport,
	}
	m.runID, m.runLive, m.treeRows = ckLoadTree()

	switch len(heads) {
	case 0:
		m.log = []string{
			ckDimS.Render("🐉 Hydra initialised · no heads discovered."),
			ckDimS.Render("Run `hyctl probe` to see what was found, or `hyctl init` to configure."),
		}
	default:
		m.log = []string{
			ckDimS.Render(fmt.Sprintf("🐉 Hydra initialised · %d head%s discovered · routing engine ready.",
				len(heads), plural(len(heads)))),
			ckDimS.Render("Type a task and press enter. Tab = chat/dash/tree/security · /trust /swarm /local · :q quits."),
		}
	}
	return m
}

// ckProbeTimeout bounds startup: a wedged provider must not hang the cockpit
// before it can draw anything.
const ckProbeTimeout = 5 * time.Second

// ckClaudePct reads the orchestrator's real context usage from state.json.
// Absent state means unknown, which renders as unknown — not as a number.
func ckClaudePct() int {
	raw, err := os.ReadFile(filepath.Join(config.Dir(), "logs", "state.json"))
	if err != nil {
		return 0
	}
	var s struct {
		ClaudePct int `json:"claude_pct"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return 0
	}
	return s.ClaudePct
}

// ckSpendToday returns today's real estimated spend from cost.jsonl. Kept as
// its own reader (NewCockpit no longer calls it — it gets spend from the
// metrics it already loaded, via cost.SummaryFromRows, to avoid a second full
// read of the same file) because it is independently useful and tested as
// the "absent data reads as zero, not a crash" contract on its own.
func ckSpendToday() float64 {
	summary, err := cost.Summary()
	if err != nil || summary == nil {
		return 0
	}
	return summary.Today.EstCostUSD
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
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
			if m.view == 2 && m.treeSel < len(m.treeRows)-1 {
				m.treeSel++
			}
		case tea.KeyEnter:
			// Only the chat+code view renders the input line or the log — on
			// any other tab this used to silently run the full dispatch-preview
			// pipeline with zero on-screen feedback (#506).
			if m.view == ckViewChatCode {
				return m.submit()
			}
		case tea.KeyBackspace:
			if m.view == ckViewChatCode {
				if n := len(m.input); n > 0 {
					m.input = m.input[:n-1]
				}
			}
		case tea.KeyEsc:
			if m.view == ckViewChatCode {
				m.input = ""
			}
		case tea.KeySpace:
			if m.view == ckViewChatCode {
				m.input += " "
			}
		case tea.KeyRunes:
			if m.view == ckViewChatCode {
				m.input += string(msg.Runes)
			}
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
	// Commands are matched case-insensitively (#465) — free-text task input
	// below is not, since a real task's casing is meaningful.
	lt := strings.ToLower(t)
	switch {
	case lt == "":
		return m, nil
	case lt == ":q" || lt == ":quit":
		return m, tea.Quit
	case lt == ":dash":
		m.view = 1
		return m, nil
	case lt == ":chat":
		m.view = 0
		return m, nil
	case lt == ":tree":
		m.view = 2
		return m, nil
	case lt == ":security":
		m.view = ckViewSecurity
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

// run previews the routing decision for a task and appends it to the log.
// It does not execute anything — see the note it prints.
func (m Cockpit) run(task string) Cockpit {
	m.runs++
	m.log = append(m.log, ckYouS.Render("❯ "+task))

	enum, wantTier := classifyTask(task, m.mode)
	idx := m.pickHead(wantTier)
	// Since #189 the roster is a real probe, so it can legitimately be empty on
	// a machine with nothing installed. There is no route to preview then, and
	// inventing one is exactly what this PR removes.
	if idx < 0 {
		m.log = append(m.log, ckDimS.Render("  no routable head — run `hyctl probe` to see why"))
		return m
	}
	h := m.heads[idx]
	fell := m.mode != "local" && h.tier > wantTier

	// Load a class-appropriate snippet and (re)start the code stream.
	m.codeLang, m.codeLines = ckSnippet(enum)
	m.codeShown = 0
	m.codeGen++

	cost := h.price
	base, baseName := m.baseline()

	flow := ckDimS.Render("  prompt ") + ckAquaS.Render("→ ") + ckInkS.Render(enum) +
		ckAquaS.Render(" → ") + ckDimS.Render(fmt.Sprintf("T%d", wantTier))
	if fell {
		flow += ckAquaS.Render(" → ") + ckMidS.Render("no head at that tier") +
			ckAquaS.Render(" → ") + ckCyanS.Render(fmt.Sprintf("T%d", h.tier))
	}
	flow += ckAquaS.Render(" → ") + lipgloss.NewStyle().Foreground(h.color).Render(h.name)
	m.log = append(m.log, flow)

	costStr := "free (local)"
	if cost > 0 {
		costStr = fmt.Sprintf("~$%.4f", cost)
	} else if base == 0 {
		// No pricing data loaded — say so rather than implying "free".
		costStr = "cost unknown"
	}
	line := ckDimS.Render("  route  ") +
		lipgloss.NewStyle().Foreground(h.color).Render(h.name) +
		ckDimS.Render("  "+costStr)
	if base > 0 {
		line += ckDimS.Render(fmt.Sprintf("  vs all-%s ~$%.4f", baseName, base))
	}
	m.log = append(m.log, line)

	// Real blast radius, walked from graph.json, when the prompt names a file
	// that is actually in the graph. This replaced a literal "κ=3.1 ⚠ 12
	// dependents" printed for any prompt containing a file path (#193).
	if f := ckFileRe.FindString(task); f != "" {
		if radius, deps, kappa, ok := m.metrics.ckBlastFor(f); ok {
			risk := ckCheapS
			if kappa >= 2 {
				risk = ckExpS
			}
			m.log = append(m.log, ckDimS.Render("  blast  ")+
				risk.Render(fmt.Sprintf("κ=%.1f", kappa))+
				ckDimS.Render(fmt.Sprintf("  %d dependent%s · radius %.2f×  → %s",
					deps, plural(deps), radius, truncate(f, 40))))
		}
	}

	// Confidence is still absent: it requires actually running the SPRT
	// ensemble, which the cockpit cannot do until it executes dispatches. Blast
	// radius above is different — it is a static graph property, computable
	// without running anything.
	m.log = append(m.log, ckDimS.Render("  plan   ")+
		ckDimS.Render("routing preview only — the cockpit does not execute dispatches yet"))

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
		// Nothing is routable. This used to fall back to the last head in the
		// roster whether or not it was up, so a machine with only an
		// unroutable head — the ollama binary with no server behind it — got a
		// routing preview naming a head nothing can drive. Same shape as #248:
		// a surface claiming a head is usable when it is not.
		return -1
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
	case ckViewSecurity:
		body = m.dashSecurity(m.w, bodyH)
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
		ckDimS.Render(" · "+m.headSummary())

	// budget.ModeFor is the single source of truth for the band. This used to
	// re-implement the thresholds inline — a fourth copy, alongside the two in
	// cockpit_views.go and the real one in internal/budget (#189).
	mode := budget.ModeFor(m.claudePct)
	mc := ckCheapS
	switch mode.String() {
	case "critical", "emergency":
		mc = ckExpS
	case "warning", "caution", "compact":
		mc = ckMidS
	}
	right := ckDimS.Render("MODE ") + mc.Render(mode.String()) +
		ckDimS.Render(fmt.Sprintf(" %d%%   ", m.claudePct)) +
		ckDimS.Render("today ") + ckCheapS.Render(fmt.Sprintf("$%.4f", m.spend))
	gap := m.w - lipgloss.Width(left) - lipgloss.Width(right) - 2
	if gap < 1 {
		gap = 1
	}
	return " " + left + strings.Repeat(" ", gap) + right
}

func (m Cockpit) sidebar(h int) string {
	head := []string{
		ckLabelS.Render("GOVERNOR"),
		ckBar(m.claudePct, 15),
		ckDimS.Render(fmt.Sprintf("claude %d%%", m.claudePct)),
		"",
		ckLabelS.Render("HEADS"),
	}
	foot := []string{"", ckLabelS.Render("MODE"), " " + ckCyanS.Render(m.mode)}

	// An uncapped head list can run to 20+ lines on a busy machine, and
	// lipgloss.Height only pads shorter content — it never truncates taller
	// content. Left alone, the sidebar's real height dictates the whole
	// view's height once JoinHorizontal pads every other column to match it
	// (#446). Cap the list to what's left after the fixed chrome above and
	// below it, and say how many were left out.
	avail := h - len(head) - len(foot)
	var shown []ckHead
	var more int
	var compact string
	switch {
	case avail <= 0:
		// No room for even one head row — the header above still states the
		// real count, so silently showing zero heads would contradict it (#506).
		if len(m.heads) > 0 {
			compact = fmt.Sprintf(" %d head%s (not enough room to list)", len(m.heads), plural(len(m.heads)))
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
		name := lipgloss.NewStyle().Foreground(hd.color).Render(truncate(hd.name, 15))
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
	logBox := lipgloss.NewStyle().Width(w).Height(logH).Render(ckVisibleLog(m.log, w, logH))
	input := ckCyanS.Render(m.mode+" ❯ ") + ckInkS.Render(m.input) + ckAquaS.Render("▏")
	return lipgloss.JoinVertical(lipgloss.Left, logBox, ckFaintS.Render(strings.Repeat("╌", max(1, w))), input)
}

// ckLogEntryCap bounds how many rendered lines a single log entry may
// occupy. Without it, one long line (a pasted stack trace, a 3000-char task)
// wraps past the whole pane on its own and pushes the input/divider/hint bar
// off-frame (#506).
const ckLogEntryCap = 6

// ckVisibleLog builds the tail of m.log that fits within logH lines as
// lipgloss will actually render them at width w. Tail-slicing by entry count
// alone is wrong — Style.Height only pads short content, it never truncates
// tall content — so a single word-wrapped entry could blow past the pane on
// its own; each entry is capped before the tail window is sized (#506).
func ckVisibleLog(log []string, w, logH int) string {
	entryCap := ckLogEntryCap
	if logH < entryCap {
		entryCap = logH
	}
	if entryCap < 1 {
		entryCap = 1
	}

	clipped := make([]string, len(log))
	for i, e := range log {
		clipped[i] = ckClipToLines(e, w, entryCap)
	}

	start, used := len(clipped), 0
	for start > 0 {
		n := ckRenderedLineCount(clipped[start-1], w)
		if used+n > logH {
			break
		}
		used += n
		start--
	}
	return strings.Join(clipped[start:], "\n")
}

// ckRenderedLineCount is how many lines s occupies once lipgloss word-wraps
// it at width w — the same wrapping the log box's own render performs.
func ckRenderedLineCount(s string, w int) int {
	if w < 1 {
		w = 1
	}
	return strings.Count(lipgloss.NewStyle().Width(w).Render(s), "\n") + 1
}

// ckClipToLines truncates one log entry to at most maxLines rendered lines,
// marking the cut — a single overlong entry must never be able to exceed the
// whole log pane on its own.
func ckClipToLines(s string, w, maxLines int) string {
	if w < 1 {
		w = 1
	}
	rows := strings.Split(lipgloss.NewStyle().Width(w).Render(s), "\n")
	if len(rows) <= maxLines {
		return s
	}
	if maxLines <= 1 {
		return ckFaintS.Render("…(truncated)")
	}
	rows = append(rows[:maxLines-1], ckFaintS.Render("…(truncated)"))
	return strings.Join(rows, "\n")
}

func (m Cockpit) hint() string {
	k := func(s string) string { return ckAquaS.Render(s) }
	// #465: this used to omit /dispatch (a valid mode command, alongside
	// /swarm /trust /local) and the :dash/:chat/:tree/:security jump
	// commands entirely — both discoverable only by reading the source.
	return ckFaintS.Render(" ") + k("enter") + ckFaintS.Render(" dispatch   ") +
		k("tab") + ckFaintS.Render("/") + k(":dash :chat :tree :security") + ckFaintS.Render(" jump   ") +
		k("↑↓") + ckFaintS.Render(" select   ") +
		k("/dispatch /trust /swarm /local") + ckFaintS.Render(" mode   ") +
		k(":q") + ckFaintS.Render(" quit")
}

// ── snapshot (static render for docs / non-tty preview) ─────────────────────────

// CockpitSnapshotView renders one static frame of the given view (0 chat+code,
// 1 dashboard, 2 agent-tree, 3 security) after two demo dispatches, with the
// code stream and tree selection settled so the frame is fully populated. An
// out-of-range view falls back to the default instead of panicking; callers
// that can report an error to the user should reject it up front with
// ValidSnapshotView.
func CockpitSnapshotView(view int) string {
	m := ckSnapshotModel()
	if !ckValidView(view) {
		view = ckViewChatCode
	}
	m.view = view
	return m.View()
}

// ckSnapshotModel builds the one demo Cockpit state every snapshot view
// renders from. Extracted so CockpitSnapshot builds it once instead of once
// per view — NewCockpit alone does a machine probe, a full security ledger
// scan, and a cost.jsonl read, and CockpitSnapshot used to pay for all three
// four times over for what is really one consistent snapshot shown from four
// angles.
func ckSnapshotModel() Cockpit {
	m := NewCockpit()
	m = m.run("write a User DTO for profile settings")            // SIMPLE → TS interface
	m = m.run("rotate the signing key in internal/auth/token.go") // CORE   → Go key-rotation
	m.codeShown = len(m.codeLines)                                // reveal the whole snippet
	m.treeSel = 2                                                 // highlight the token-rotation node
	m.w, m.h, m.ready = 100, 30, true
	return m
}

// CockpitSnapshot renders all four views stacked, each labelled — the
// representative frame shown by `hyctl tui --snapshot`.
func CockpitSnapshot() string {
	label := func(s string) string { return ckLabelS.Render("── " + s + " " + strings.Repeat("─", 40)) }
	base := ckSnapshotModel()
	view := func(v int) string {
		m := base
		if !ckValidView(v) {
			v = ckViewChatCode
		}
		m.view = v
		return m.View()
	}
	return label("VIEW 1/4 · CHAT + CODE (tab)") + "\n" + view(0) + "\n\n" +
		label("VIEW 2/4 · DASHBOARD (tab)") + "\n" + view(1) + "\n\n" +
		label("VIEW 3/4 · AGENT TREE (tab · ↑↓ select)") + "\n" + view(2) + "\n\n" +
		label("VIEW 4/4 · SECURITY (tab)") + "\n" + view(ckViewSecurity)
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

// ckBar is a pressure gauge: high is bad (cost spend, governor claude_pct), so
// the color ramp reddens as pct rises. Do not reuse it for a metric where high
// is good — see ckCoverageBar for that shape instead.
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

// ckCoverageBar is ckBar's color ramp inverted: high is good (OWASP LLM
// Top-10 coverage), so it greens as pct rises instead of reddening.
func ckCoverageBar(pct, width int) string {
	fill := pct * width / 100
	if fill > width {
		fill = width
	}
	if fill < 0 {
		fill = 0
	}
	col := ckExp
	switch {
	case pct >= 75:
		col = ckCheap
	case pct >= 50:
		col = ckMid
	}
	return lipgloss.NewStyle().Foreground(col).Render(strings.Repeat("█", fill)) +
		ckFaintS.Render(strings.Repeat("░", width-fill))
}

// ckSegmentedBar splits width characters proportionally across vals, each
// rendered in its own style — the ASCII equivalent of the desktop's
// allowed/denied/flagged donut, for a surface that has no pie charts. The
// last segment absorbs integer-division rounding so the bar always sums to
// exactly width. A zero total renders width faint dots, not a divide by zero.
func ckSegmentedBar(width int, vals []int, styles []lipgloss.Style) string {
	total := 0
	for _, v := range vals {
		total += v
	}
	if total <= 0 {
		return ckFaintS.Render(strings.Repeat("░", width))
	}
	var b strings.Builder
	used := 0
	for i, v := range vals {
		n := v * width / total
		if i == len(vals)-1 {
			n = width - used
		}
		b.WriteString(styles[i].Render(strings.Repeat("█", n)))
		used += n
	}
	return b.String()
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

// truncate bounds a label to n display cells.
//
// Counted in runes, not bytes. Slicing a string at a byte offset cuts a
// multi-byte rune in half: "日本語モデル" truncated to 8 came out as
// "日本\xe8…", which a terminal draws as a replacement character in the header
// bar. cmd/hydra's truncLabel already counted runes; this one did not.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

// headSummary names the discovered heads for the header, truncated to keep the
// bar from wrapping. It replaced a hardcoded "agy·gemini·openrouter·qwen"
// string that named heads the machine may not have (#189).
func (m Cockpit) headSummary() string {
	if len(m.heads) == 0 {
		return "no heads"
	}
	names := make([]string, 0, len(m.heads))
	for _, h := range m.heads {
		names = append(names, ckBaseName(h.name))
	}
	s := "heads: " + strings.Join(names, "·")
	return truncate(s, 46)
}

// ckLoadTree reconstructs the run to display: the live one if a heartbeat is
// fresh, else the most recent. Returns no rows when nothing has been recorded,
// which the view renders as an honest empty state rather than an example (#191).
func ckLoadTree() (runID string, live bool, rows []tree.Row) {
	if ids, err := runlog.LiveRuns(); err == nil && len(ids) > 0 {
		runID, live = ids[0], true
	} else {
		ids, err := runlog.Runs()
		if err != nil || len(ids) == 0 {
			return "", false, nil
		}
		runID = ids[0]
	}

	events, err := runlog.Load(runID)
	if err != nil || len(events) == 0 {
		return runID, live, nil
	}
	t, _ := tree.Reconstruct(events)
	return runID, live, t.Rows()
}
