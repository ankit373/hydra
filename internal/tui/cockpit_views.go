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
	"github.com/ankit373/hydra/internal/tree"
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
		name := lipgloss.NewStyle().Foreground(hd.color).Width(15).Render(truncate(hd.name, 15))

		// Real latency history from cost.jsonl wall_ms — "—" for a head that
		// has never run, rather than a zero-filled chart.
		series, lastMS := m.metrics.ckSeriesFor(hd.name, hd.id)
		spark := lipgloss.NewStyle().Foreground(hd.color).
			Render(fmt.Sprintf("%-*s", ckSparkWidth, ckSpark(series)))
		lat := ckDimS.Render(fmt.Sprintf("%7s", ckFmtMS(lastMS)))

		// Calibrated diagnosticity (nats) where the trust ledger has data.
		// Keyed by head ID: trust records sources as ids, not display names.
		diag := ckFaintS.Render("   —")
		if d := m.metrics.ckDiagnosticity(hd.id, ""); d > 0 {
			diag = ckCyanS.Render(fmt.Sprintf("%4.2f", d))
		}

		fleet.WriteString(" " + name + " " + st +
			ckDimS.Render(fmt.Sprintf(" T%-2d ", hd.tier)) + spark + lat + " " + diag + "\n")
	}
	fleet.WriteString("\n" + ckFaintS.Render(fmt.Sprintf(" %-16s %-6s %-4s %-*s %7s %4s",
		"", "", "", ckSparkWidth, "latency", "last", "D")) + "\n")
	fleet.WriteString(ckDimS.Render(fmt.Sprintf("%d head%s · mode ",
		len(m.heads), plural(len(m.heads)))) + ckCyanS.Render(m.mode))

	// Real spend and real savings, both from cost.jsonl rows priced through the
	// same pricing DB — so the comparison is like-for-like.
	spendBox := ckLabelS.Render("SPEND · today") + "\n\n " +
		lipgloss.NewStyle().Foreground(ckCheap).Bold(true).Render(fmt.Sprintf("$%.4f", m.spend)) + "\n " +
		ckFaintS.Render("estimated, not billed")
	if m.metrics.baseUSD > 0 {
		pct := 0
		if m.metrics.baseUSD > 0 {
			pct = int(m.metrics.savedUSD / m.metrics.baseUSD * 100)
		}
		spendBox += "\n\n " + ckDimS.Render("saved vs all-T1  ") +
			ckCheapS.Render(fmt.Sprintf("$%.4f", m.metrics.savedUSD)) + "\n " +
			ckBar(pct, 20) + ckDimS.Render(fmt.Sprintf("  %d%%", pct))
	}

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

// tree renders the supervision tree for a real run, reconstructed from the
// per-run event log via internal/tree.
//
// It replaced a hand-authored 7-node literal in which each row carried its own
// box-drawing indent as a string field — a picture of a tree rather than a
// tree, fixed at one shape and describing a run that never happened (#191).
// Prefixes are now derived from depth and last-child position, so arbitrary
// depth and branching render correctly.
//
// Ownership edges are solid (─); A2A handoffs are a dashed overlay (┄), kept
// visually distinct because they are a different relation, not a parent.
func (m Cockpit) tree(w, h int) string {
	var b strings.Builder
	b.WriteString(ckLabelS.Render("AGENT TREE · supervision") +
		ckDimS.Render("   ownership ") + ckFaintS.Render("─") +
		ckDimS.Render("   A2A ") + lipgloss.NewStyle().Foreground(ckMagenta).Render("┄"))

	if m.runID != "" {
		live := ""
		if m.runLive {
			live = ckCheapS.Render("  ● live")
		}
		b.WriteString(ckFaintS.Render("   run "+truncate(m.runID, 24)) + live)
	}
	b.WriteString("\n\n")

	rows := m.treeRows
	if len(rows) == 0 {
		// No fictional fallback: say there is nothing and how to make some.
		b.WriteString(ckFaintS.Render(" no runs recorded yet") + "\n\n" +
			ckDimS.Render(" Run a dispatch to populate this view:") + "\n" +
			ckDimS.Render("   hyctl dispatch \"add a retry to the token refresher\"") + "\n\n" +
			ckFaintS.Render(" Events are written to ~/.hydra/logs/runs/<run_id>.jsonl"))
		return lipgloss.NewStyle().Width(w).Height(h).Padding(0, 1).Render(b.String())
	}

	sel := m.treeSel
	if sel < 0 || sel >= len(rows) {
		sel = 0
	}

	for i, r := range rows {
		marker := "  "
		if i == sel {
			marker = ckAquaS.Render("▸ ")
		}
		n := r.Node
		label := lipgloss.NewStyle().Foreground(ckCostColor(n.Tier)).Bold(i == sel).
			Render(truncate(nodeLabel(n), 28))

		line := marker + ckFaintS.Render(treePrefix(r)) + label +
			ckDimS.Render(fmt.Sprintf("  T%-2d", n.Tier))
		if n.Model != "" {
			line += ckDimS.Render("  " + truncate(n.Model, 20))
		}
		line += ckFaintS.Render(" · ") + ckStateStyle(string(n.State)).Render(string(n.State))
		if n.CostUSD > 0 {
			line += ckFaintS.Render(" · ") + ckDimS.Render(fmt.Sprintf("$%.4f", n.CostUSD))
		}
		if len(n.Handoffs) > 0 {
			line += lipgloss.NewStyle().Foreground(ckMagenta).
				Render(fmt.Sprintf("  ┄%d", len(n.Handoffs)))
		}
		b.WriteString(line + "\n")
	}

	// Selected-node detail.
	s := rows[sel].Node
	detail := ckLabelS.Render("SELECTED") + "  " +
		lipgloss.NewStyle().Foreground(ckCostColor(s.Tier)).Bold(true).Render(nodeLabel(s)) +
		ckDimS.Render(fmt.Sprintf(" · T%d", s.Tier))
	if s.Model != "" {
		detail += ckDimS.Render(" · " + s.Model)
	}
	detail += ckDimS.Render(" · ") + ckStateStyle(string(s.State)).Render(string(s.State))

	lines := []string{b.String(), detail}

	if s.DurationMS > 0 {
		lines = append(lines, ckDimS.Render("duration     ")+
			ckInkS.Render(fmt.Sprintf("%dms", s.DurationMS)))
	}
	if s.Detail != "" {
		dw := w - 18
		if dw < 10 {
			dw = 10
		}
		lines = append(lines, ckDimS.Render("detail       ")+ckInkS.Render(truncate(s.Detail, dw)))
	}
	// Confidence is shown only when one was actually recorded — never invented.
	if s.Confidence > 0 {
		lines = append(lines, ckDimS.Render("confidence   ")+ckBar(int(s.Confidence*100), 20)+
			ckCyanS.Render(fmt.Sprintf("  %.2f", s.Confidence)))
	}
	for _, hf := range s.Handoffs {
		lines = append(lines, lipgloss.NewStyle().Foreground(ckMagenta).Render("┄┄▶ A2A      ")+
			ckDimS.Render(hf.To+"  ")+ckFaintS.Render(truncate(hf.Detail, 40)))
	}

	return lipgloss.NewStyle().Width(w).Height(h).Padding(0, 1).
		Render(strings.Join(lines, "\n"))
}

// nodeLabel prefers a human name over an opaque id.
func nodeLabel(n *tree.Node) string {
	if n.Head != "" {
		return n.Head
	}
	return n.ID
}

// treePrefix builds the box-drawing indent from a row's depth and last-child
// flag. The old implementation stored this string per node, which is why it
// only ever rendered one fixed shape.
func treePrefix(r tree.Row) string {
	if r.Depth == 0 {
		return ""
	}
	branch := "├─ "
	if r.Last {
		branch = "└─ "
	}
	return strings.Repeat("│  ", r.Depth-1) + branch
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
