// SPDX-License-Identifier: MIT

package tui

// cockpit.go — the interactive terminal cockpit (`hyctl tui`): the model, the
// six-view table, and the frame loop. Chrome lives in chrome.go, bindings in
// keys.go, layout helpers in layout.go, and each view in view_<name>.go.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/pricing"
	"github.com/ankit373/hydra/internal/probe"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/rank"
)

// Cockpit views. The name table is the single source of truth — deriving the
// count and the bounds check from it keeps the Tab cycle, the header tabs, and
// the --view validation from drifting apart when a view is added.
const (
	ckViewChat = iota
	ckViewAgents
	ckViewModels
	ckViewActivity
	ckViewUsage
	ckViewAudit
)

var ckViewNames = []string{"chat", "agents", "models", "activity", "usage", "audit"}

// ckViewCount is how many views exist.
func ckViewCount() int { return len(ckViewNames) }

// ckViewName is total: an out-of-range view yields the default label rather
// than panicking. `--snapshot --view N` reaches the header with unvalidated N.
func ckViewName(v int) string {
	if !ckValidView(v) {
		return ckViewNames[ckViewChat]
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

// ── heads (chat routing preview) ─────────────────────────────────────────────────

type ckHead struct {
	id    string
	name  string
	tier  int
	price float64
	up    bool
	local bool
}

// ckHeadsFrom converts a real scan result into display rows, ranked the way
// dispatch ranks them so the cockpit shows the order routing would actually
// use. price comes from the live pricing DB; a head with no known price shows
// 0, which renders as unknown rather than a fabricated figure.
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
			local: h.LocalOnly,
		})
	}
	return out
}

// ── model ───────────────────────────────────────────────────────────────────────

// Cockpit is the interactive `hyctl tui` model.
type Cockpit struct {
	w, h     int
	ready    bool
	view     int // one of the ckView* constants
	glossary bool
	flash    string // transient status-bar note, replaced by the next action

	// chat (view 0) — routing preview only in this phase; execution is #597.
	input      string
	log        []string
	mode       string
	runs       int
	pinnedTier int // session default tier for chat, set from the models view
	chatScroll int // 0 = follow the live tail; L+1 = scrollback anchored at line L
	codeLang   string
	codeLines  []string
	codeShown  int
	codeGen    int // generation guard so a new run cancels stale tick loops

	claudePct  int
	pctKnown   bool // false when state.json has no claude_pct to read
	pctHist    []int
	spend      float64 // today's real estimated spend, from cost.jsonl
	sessionUSD float64 // est cost accrued by THIS process — zero until chat executes
	metrics    ckMetrics
	heads      []ckHead
	// probedHeads is kept from the startup scan so the models view and the
	// audit build can reuse it without re-scanning the machine.
	probedHeads []provider.Head

	// models (view 2)
	groups     []ckModelGroup
	collapsed  map[string]bool
	modelSel   int
	modelFocus bool
	scannedAt  time.Time
	scanning   bool

	// agents (1) + activity (3) share today's runs, loaded once (runs.go).
	runsToday   []ckRun
	agentSel    int
	actSel      int
	actDrill    bool
	actFailOnly bool
	traceOff    int

	// usage (view 4)
	usageGroup byte // 'm' model · 't' tier · 'd' day
	usageOff   int  // breakdown table scroll

	// audit (view 5) — built lazily on entry (#524) and refreshed on each
	// entry; nil until then or when the build failed.
	audit        *ckAudit
	auditSel     int
	scoreOff     int // scorecard table scroll
	auditIgnored map[string]bool

	glossOff int // glossary overlay scroll, for short terminals
}

// NewCockpit builds the cockpit from the machine's real state: models from a
// scan, the context budget from state.json, spend from cost.jsonl. Anything
// not yet measurable is omitted rather than simulated (#189).
func NewCockpit() Cockpit {
	ctx, cancel := probeContext()
	defer cancel()
	probed := probe.Run(ctx)

	pr := pricing.Load()
	heads := ckHeadsFrom(probed.Heads, pr)
	pct, hist, known := ckClaudePct()
	metrics := ckLoadMetrics(pr)

	m := Cockpit{
		mode:         "dispatch",
		claudePct:    pct,
		pctKnown:     known,
		pctHist:      hist,
		heads:        heads,
		spend:        metrics.spendUSD,
		metrics:      metrics,
		probedHeads:  probed.Heads,
		groups:       ckGroupHeads(probed.Heads),
		collapsed:    map[string]bool{},
		scannedAt:    time.Now(),
		usageGroup:   'm',
		auditIgnored: map[string]bool{},
	}
	m.runsToday = ckLoadRuns(time.Now().UTC())

	switch len(heads) {
	case 0:
		m.log = []string{
			ckDimS.Render("🐉 Hydra initialised · no models found by the scan."),
			ckDimS.Render("Run `hyctl probe` to see what was scanned, or `hyctl init` to configure."),
		}
	default:
		m.log = []string{
			ckDimS.Render(fmt.Sprintf("🐉 Hydra initialised · %d model%s scanned · routing engine ready.",
				len(heads), plural(len(heads)))),
			ckDimS.Render("Type a task and press enter. tab cycles views · ? shortcuts · :q quits."),
		}
	}
	return m
}

// ckProbeTimeout bounds startup: a wedged provider must not hang the cockpit
// before it can draw anything.
const ckProbeTimeout = 5 * time.Second

// ckClaudePct reads the orchestrator's real context usage and its history from
// state.json. Absent state reads as unknown, which renders as "—", not as 0%.
func ckClaudePct() (pct int, hist []int, ok bool) {
	raw, err := os.ReadFile(filepath.Join(config.Dir(), "logs", "state.json"))
	if err != nil {
		return 0, nil, false
	}
	var s struct {
		ClaudePct        *int  `json:"claude_pct"`
		ClaudePctHistory []int `json:"claude_pct_history"`
	}
	if err := json.Unmarshal(raw, &s); err != nil || s.ClaudePct == nil {
		return 0, nil, false
	}
	return *s.ClaudePct, s.ClaudePctHistory, true
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
	case ckRescanMsg:
		return m.applyRescan(msg), nil
	case tea.MouseMsg:
		switch tea.MouseEvent(msg).Button {
		case tea.MouseButtonWheelUp:
			return m.scrollBy(-3), nil
		case tea.MouseButtonWheelDown:
			return m.scrollBy(3), nil
		}
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

// ── frame ─────────────────────────────────────────────────────────────────────

func (m Cockpit) View() string {
	if !m.ready {
		return "\n  starting hydra cockpit…\n"
	}
	bodyH := m.h - 3
	if bodyH < 6 {
		bodyH = 6
	}
	w := max(1, m.w)
	var body string
	switch {
	case m.glossary:
		body = m.viewGlossary(w, bodyH)
	case m.view == ckViewAgents:
		body = m.viewAgents(w, bodyH)
	case m.view == ckViewModels:
		body = m.viewModels(w, bodyH)
	case m.view == ckViewActivity:
		body = m.viewActivity(w, bodyH)
	case m.view == ckViewUsage:
		body = m.viewUsage(w, bodyH)
	case m.view == ckViewAudit:
		body = m.viewAudit(w, bodyH)
	default:
		body = m.chatCode(bodyH)
	}
	// ckFrame is the shell's guarantee: no view can push the status bar
	// off-frame or bleed past the terminal's width, whatever its content.
	return m.header() + "\n" + ckFaintS.Render(strings.Repeat("─", w)) + "\n" +
		ckFrame(body, w, bodyH) + "\n" + m.statusBar()
}

// ── code-stream ticker ─────────────────────────────────────────────────────────

type ckCodeTickMsg struct{ gen int }

// ckCodeTick schedules the next code line to reveal. gen tags the current run so
// a fresh preview cancels the previous stream instead of double-speeding it.
func ckCodeTick(gen int) tea.Cmd {
	return tea.Tick(time.Second/20, func(time.Time) tea.Msg { return ckCodeTickMsg{gen} })
}

// ── snapshot (static render for docs / non-tty preview) ─────────────────────────

// CockpitSnapshotView renders one static frame of the given view (0 chat,
// 1 agents, 2 models, 3 activity, 4 usage, 5 audit) after two demo previews,
// with the code stream settled so the frame is fully populated. An
// out-of-range view falls back to the default instead of panicking; callers
// that can report an error to the user should reject it up front with
// ValidSnapshotView.
func CockpitSnapshotView(view int) string {
	m := ckSnapshotModel()
	if !ckValidView(view) {
		view = ckViewChat
	}
	m = m.jump(view)
	return m.View()
}

// ckSnapshotModel builds the one demo Cockpit state every snapshot view
// renders from. Built once instead of once per view — NewCockpit alone does a
// machine scan and a cost.jsonl read. The audit report stays lazy (#524):
// jump() builds it only for the frame that shows it.
func ckSnapshotModel() Cockpit {
	m := NewCockpit()
	m = m.run("write a User DTO for profile settings")            // SIMPLE → TS interface
	m = m.run("rotate the signing key in internal/auth/token.go") // CORE   → Go key-rotation
	m.codeShown = len(m.codeLines)                                // reveal the whole snippet
	m.w, m.h, m.ready = 100, 30, true
	return m
}

// CockpitSnapshot renders all six views stacked plus the shortcut glossary,
// each labelled — the representative frame shown by `hyctl tui --snapshot`.
func CockpitSnapshot() string {
	label := func(s string) string { return ckLabelS.Render("── " + s + " " + strings.Repeat("─", 40)) }
	base := ckSnapshotModel()
	var b strings.Builder
	for v, name := range ckViewNames {
		b.WriteString(label(fmt.Sprintf("VIEW %d/%d · %s (tab · %d)", v+1, len(ckViewNames), strings.ToUpper(name), v+1)))
		b.WriteString("\n" + base.jump(v).View() + "\n\n")
	}
	g := base
	g.glossary = true
	b.WriteString(label("GLOSSARY (?)") + "\n" + g.View())
	return b.String()
}
