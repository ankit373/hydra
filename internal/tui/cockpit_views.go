// SPDX-License-Identifier: MIT

package tui

// cockpit_views.go — the three cockpit view modes cycled by Tab:
//   view 0  chat + live code panel   (chatCode → codePanel)
//   view 1  dashboard                (dash) — reads real probe/cost/trust data
//   view 2  agent supervision tree   (tree) — still a fixed example; see #189
// plus their pure helpers (syntax highlighter, tier/state colour ramps). Neon
// identity via the ck-prefixed palette declared in cockpit.go.

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/ankit373/hydra/internal/budget"
	"github.com/ankit373/hydra/internal/trust"
)

// ── view 0 · live code panel ─────────────────────────────────────────────────

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
		b.WriteString("\n" + ckFaintS.Render("awaiting dispatch —") + "\n" +
			ckFaintS.Render("run a task to stream the") + "\n" +
			ckFaintS.Render("head's output here."))
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

// ── view 1 · dashboard ───────────────────────────────────────────────────────

// dash is the fleet pulse: discovered heads, real spend, real calibration
// stats, and the governor gauge.
//
// Everything here reads a real source. It previously rendered LCG-hashed
// "sparklines", a per-head confidence bucketed by tier, and a savings figure
// derived from an invented price table — all with no backing data (#189).
// Where a real figure is not available yet it is labelled unavailable rather
// than invented, because --snapshot publishes these frames.
func (m Cockpit) dash(w, h int) string {
	var fleet strings.Builder
	fleet.WriteString(ckLabelS.Render("FLEET · discovered heads") + "\n\n")
	if len(m.heads) == 0 {
		fleet.WriteString(ckDimS.Render(" no heads discovered — run `hyctl probe`") + "\n")
	}
	for _, hd := range m.heads {
		st := ckCheapS.Render("● up  ")
		if !hd.up {
			st = ckExpS.Render("● down")
		}
		name := lipgloss.NewStyle().Foreground(hd.color).Width(18).Render(truncate(hd.name, 18))
		price := ckDimS.Render("       —")
		if hd.price > 0 {
			price = ckDimS.Render(fmt.Sprintf(" ~$%.4f", hd.price))
		}
		fleet.WriteString(" " + name + " " + st +
			ckDimS.Render(fmt.Sprintf(" T%-2d", hd.tier)) + price + "\n")
	}
	fleet.WriteString("\n" + ckDimS.Render(fmt.Sprintf("%d head%s · mode ",
		len(m.heads), plural(len(m.heads)))) + ckCyanS.Render(m.mode))

	// Real spend, from cost.jsonl.
	spendBox := ckLabelS.Render("SPEND · today") + "\n\n " +
		lipgloss.NewStyle().Foreground(ckCheap).Bold(true).Render(fmt.Sprintf("$%.4f", m.spend)) + "\n " +
		ckFaintS.Render("estimated, not billed") + "\n " +
		ckDimS.Render("`hyctl cost` for the breakdown")

	// Real calibration stats, from trust.jsonl.
	confBox := ckLabelS.Render("TRUST · calibration") + "\n\n " + m.trustSummary()

	// One source of truth for the band — internal/budget. This block used to
	// re-implement the thresholds, making a third copy that already disagreed
	// with the governor (CLAUDE.md forbids exactly this).
	mode := budget.ModeFor(m.claudePct)
	bc := ckCheapS
	switch mode.String() {
	case "critical", "emergency":
		bc = ckExpS
	case "warning", "caution":
		bc = ckMidS
	case "compact":
		bc = ckMidS
	}
	govLine := ckDimS.Render("band ") + bc.Render(mode.String())
	if m.claudePct == 0 {
		govLine = ckFaintS.Render("no claude_pct in state.json yet")
	}
	govBox := ckLabelS.Render("GOVERNOR · claude_pct") + "\n\n " + ckBar(m.claudePct, 20) +
		ckDimS.Render(fmt.Sprintf("  %d%%", m.claudePct)) + "\n " + govLine

	right := lipgloss.JoinVertical(lipgloss.Left,
		ckBoxS.Render(spendBox), ckBoxS.Render(confBox), ckBoxS.Render(govBox))
	row := lipgloss.JoinHorizontal(lipgloss.Top, ckBoxS.Render(fleet.String()), " ", right)
	return lipgloss.NewStyle().Width(w).Height(h).Render(row)
}

// trustSummary renders real SPRT statistics, or says plainly that there are
// none yet. It never invents a confidence figure.
func (m Cockpit) trustSummary() string {
	runs, err := trust.LoadRuns(trust.DefaultLogPath())
	if err != nil || len(runs) == 0 {
		return ckFaintS.Render("no SPRT runs recorded") + "\n " +
			ckDimS.Render("`hyctl dispatch --confidence 0.95 …`")
	}
	st := trust.Aggregate(runs, 5)
	return lipgloss.NewStyle().Foreground(ckCyan).Bold(true).
		Render(fmt.Sprintf("%.2f", st.MeanFinalConf)) +
		ckDimS.Render(fmt.Sprintf("  mean over %d run%s", st.Runs, plural(st.Runs))) + "\n " +
		ckBar(int(st.MeanFinalConf*100), 20) + "\n " +
		ckDimS.Render(fmt.Sprintf("%.1f samples · %.0f%% auto-cleared", st.MeanSamples, st.AutoClearedPct))
}

// ── view 2 · agent tree ──────────────────────────────────────────────────────

// ckTreeNode is one node in the fixed example supervision tree. prefix carries
// the box-drawing indent so the flat slice renders as a tree.
type ckTreeNode struct {
	prefix string
	name   string
	model  string
	tier   int
	state  string // returned | running | await | pending | failed
	conf   float64
	instr  string
}

// ckTree is a small, representative run: the orchestrator delegates key
// rotation, tests, and docs; token-rotation itself fans out to two workers.
var ckTree = []ckTreeNode{
	{"", "orchestrator", "cortex · you", 1, "await", 0.71, "Coordinate: rotate signing key, add tests, update docs — target 0.95"},
	{"├─ ", "design", "agy · claude", 3, "returned", 0.93, "Design a safe key-rotation; check blast radius κ=3.1 (12 dependents)"},
	{"├─ ", "token-rotation", "agy · claude", 3, "running", 0.88, "Rotate signing key in internal/auth/token.go without breaking live tokens"},
	{"│  ├─ ", "worker-1", "gemini pro", 6, "returned", 0.91, "Implement RotateSigningKey(): stage the new key, retire the old one"},
	{"│  └─ ", "worker-2", "gemini flash", 8, "running", 0.62, "Update callers to reference the new active key ID"},
	{"├─ ", "tests", "gemini pro", 6, "pending", 0.0, "Write rotation tests; verify pre-rotation tokens still verify"},
	{"└─ ", "docs", "qwen · local", 10, "returned", 0.87, "Document the key-rotation runbook + rollback steps"},
}

// tree renders the supervision tree with a selection cursor and a detail line
// for the highlighted node. Ownership edges are solid (─); the A2A handoff is
// drawn dashed (┄) as an overlay note.
func (m Cockpit) tree(w, h int) string {
	sel := m.treeSel
	if sel < 0 || sel >= len(ckTree) {
		sel = 0
	}
	var b strings.Builder
	b.WriteString(ckLabelS.Render("AGENT TREE · supervision") +
		ckDimS.Render("   ownership ") + ckFaintS.Render("─") +
		ckDimS.Render("   A2A ") + lipgloss.NewStyle().Foreground(ckMagenta).Render("┄") + "\n\n")

	for i, n := range ckTree {
		marker := "  "
		if i == sel {
			marker = ckAquaS.Render("▸ ")
		}
		label := lipgloss.NewStyle().Foreground(ckCostColor(n.tier)).Bold(i == sel).Render(n.name)
		conf := ckDimS.Render("conf ") + ckCyanS.Render(fmt.Sprintf("%.2f", n.conf))
		if n.conf == 0 {
			conf = ckDimS.Render("conf —")
		}
		b.WriteString(marker + ckFaintS.Render(n.prefix) + label + "  " +
			ckDimS.Render(fmt.Sprintf("T%-2d", n.tier)) + "  " +
			ckDimS.Render(n.model) + ckFaintS.Render(" · ") +
			ckStateStyle(n.state).Render(n.state) + ckFaintS.Render(" · ") + conf + "\n")
	}

	b.WriteString("\n     " + lipgloss.NewStyle().Foreground(ckMagenta).Render("┄┄▶ A2A") +
		ckDimS.Render("  token-rotation → tests   ") +
		ckFaintS.Render("(handoff: files resolved +1 · context compacted)") + "\n")

	// selected-node detail
	s := ckTree[sel]
	detail := ckLabelS.Render("SELECTED") + "  " +
		lipgloss.NewStyle().Foreground(ckCostColor(s.tier)).Bold(true).Render(s.name) +
		ckDimS.Render(fmt.Sprintf(" · T%d · %s · ", s.tier, s.model)) +
		ckStateStyle(s.state).Render(s.state)
	instrW := w - 18
	if instrW < 10 {
		instrW = 10
	}
	instr := ckDimS.Render("instruction  ") + ckInkS.Render(truncate(s.instr, instrW))
	var confLine string
	if s.conf > 0 {
		confLine = ckDimS.Render("confidence   ") + ckBar(int(s.conf*100), 20) +
			ckCyanS.Render(fmt.Sprintf("  %.2f", s.conf)) + ckDimS.Render(" / target 0.95")
	} else {
		confLine = ckDimS.Render("confidence   ") + ckFaintS.Render("pending — not yet dispatched")
	}

	body := b.String() + "\n" + detail + "\n" + instr + "\n" + confLine
	return lipgloss.NewStyle().Width(w).Height(h).Padding(0, 1).Render(body)
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

// ── sparklines + colour ramps ────────────────────────────────────────────────

// ckCostColor ramps the cost/tier scale: T1 (expensive) red → mid amber →
// T10 (cheap, local) green.
func ckCostColor(tier int) lipgloss.Color {
	t := float64(tier-1) / 9.0
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	var a, b [3]int
	if t < 0.5 {
		a, b, t = [3]int{0xFF, 0x5A, 0x6E}, [3]int{0xE0, 0xA9, 0x3A}, t/0.5
	} else {
		a, b, t = [3]int{0xE0, 0xA9, 0x3A}, [3]int{0x3F, 0xD9, 0x8A}, (t-0.5)/0.5
	}
	l := func(x, y int) int { return int(float64(x) + float64(y-x)*t) }
	return lipgloss.Color(fmt.Sprintf("#%02X%02X%02X", l(a[0], b[0]), l(a[1], b[1]), l(a[2], b[2])))
}

// ckStateStyle colours an agent node's lifecycle state.
func ckStateStyle(state string) lipgloss.Style {
	switch state {
	case "returned":
		return ckCheapS
	case "running":
		return ckAquaS
	case "await":
		return ckMidS
	case "failed":
		return ckExpS
	default: // pending
		return ckFaintS
	}
}
