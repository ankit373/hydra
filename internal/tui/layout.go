// SPDX-License-Identifier: MIT

package tui

// layout.go — the shared alignment/wrapping/clipping helpers every view uses.
// All measurement is in display cells (lipgloss.Width / ansi.Truncate), never
// len(): byte or rune counts misalign columns the moment a name is not ASCII.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/ankit373/hydra/internal/util"
)

// ckSafe strips control characters (including embedded newlines) so untrusted
// text cannot break row layout or terminal state. Apply before truncation:
// cutting an escape in half corrupts the frame.
func ckSafe(s string) string { return util.SafeTerminal(s) }

// truncate bounds a label to n display cells, ANSI- and wide-rune-aware, with
// an ellipsis marking the cut.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	return ansi.Truncate(s, n, "…")
}

// ckCell is a fixed table cell: s truncated and padded to exactly w display
// cells, left-aligned. Styled input is fine — measurement strips ANSI.
func ckCell(s string, w int) string {
	s = truncate(s, w)
	return s + strings.Repeat(" ", w-lipgloss.Width(s))
}

// ckRCell right-aligns within w cells — for numeric columns.
func ckRCell(s string, w int) string {
	s = truncate(s, w)
	return strings.Repeat(" ", w-lipgloss.Width(s)) + s
}

// ckFrame clamps a rendered body to at most h lines of at most w cells each.
// Overflow is disclosed, never silently dropped — and never allowed to push
// the chrome off-frame, which is what an uncropped pane does (#445, #446).
func ckFrame(body string, w, h int) string {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	lines := strings.Split(body, "\n")
	if len(lines) > h {
		n := len(lines) - (h - 1)
		lines = lines[:h-1]
		lines = append(lines, ckFaintS.Render(truncate(fmt.Sprintf("… %d more line%s — enlarge the terminal", n, plural(n)), w)))
	}
	for i, l := range lines {
		if lipgloss.Width(l) > w {
			lines[i] = ansi.Truncate(l, w, "…")
		}
	}
	return strings.Join(lines, "\n")
}

// ckSplit joins two panes side by side when the width allows; otherwise it
// shows one whole pane instead of two broken ones — the secondary when the
// user has focused into it, else the primary.
func ckSplit(w int, primary, secondary string, focusSecondary bool) string {
	if lipgloss.Width(primary)+1+lipgloss.Width(secondary) <= w {
		return lipgloss.JoinHorizontal(lipgloss.Top, primary, " ", secondary)
	}
	if focusSecondary {
		return secondary
	}
	return primary
}

// ckDistinctTruncate truncates each sibling name to width display cells. A
// family of siblings whose shared prefix would eat most of the budget gets
// that prefix dropped so the distinguishing tail survives — "…Flash (Med)",
// never three near-identical "Gemini 3.5 Fla…" rows.
func ckDistinctTruncate(names []string, width int) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = truncate(n, width)
	}
	// Cluster into prefix families: sort, then adjacent names whose pairwise
	// prefix fills at least half the budget belong together.
	idx := make([]int, len(names))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool { return names[idx[a]] < names[idx[b]] })
	var families [][]int
	for k := 0; k < len(idx); k++ {
		if k > 0 {
			lcp := ckCommonPrefix([]string{names[idx[k-1]], names[idx[k]]})
			if lipgloss.Width(string([]rune(names[idx[k]])[:lcp])) >= (width+1)/2 {
				last := len(families) - 1
				families[last] = append(families[last], idx[k])
				continue
			}
		}
		families = append(families, []int{idx[k]})
	}
	for _, fam := range families {
		if len(fam) < 2 {
			continue
		}
		set := make([]string, len(fam))
		for j, i := range fam {
			set[j] = names[i]
		}
		prefix := ckCommonPrefix(set)
		for _, i := range fam {
			r := []rune(names[i])
			if lipgloss.Width(names[i]) <= width || prefix <= 0 || prefix >= len(r) {
				continue // fits, or duplicates that cannot be told apart
			}
			out[i] = truncate("…"+string(r[prefix:]), width)
		}
	}
	return out
}

// ckCommonPrefix is the rune length of the prefix every name shares.
func ckCommonPrefix(names []string) int {
	first := []rune(names[0])
	n := len(first)
	for _, s := range names[1:] {
		r := []rune(s)
		if len(r) < n {
			n = len(r)
		}
		for i := 0; i < n; i++ {
			if r[i] != first[i] {
				n = i
				break
			}
		}
	}
	return n
}

// ── gauges ────────────────────────────────────────────────────────────────────

// ckBar is a pressure gauge: high is bad (cost spend, context budget), so the
// color ramp reddens as pct rises. Do not reuse it where high is good.
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

// ckSegmentedBar splits width characters proportionally across vals, each in
// its own style. The last segment absorbs integer-division rounding so the bar
// always sums to exactly width. A zero total renders faint dots, not a panic.
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

// ── scrolling ─────────────────────────────────────────────────────────────────

// ckWindowSel is the shared "selection can never walk off-screen" rule: the
// start of an avail-row window over n rows that keeps sel visible AND clear
// of the edge rows, which ckScrollLines replaces with the ↑/↓ cues.
func ckWindowSel(sel, n, avail int) int {
	if avail < 1 {
		avail = 1
	}
	if n <= avail {
		return 0
	}
	switch {
	case sel <= avail-2:
		return 0 // no top cue; sel stays above the bottom cue
	case sel >= n-avail+1:
		return n - avail // no bottom cue; sel stays below the top cue
	default:
		return sel - avail + 2 // both cues; sel one row above the bottom cue
	}
}

// ckSelScroll windows rows around sel so the selection is always rendered,
// with the overflow cues on the edges.
func ckSelScroll(rows []string, sel, avail int) []string {
	out, _ := ckScrollLines(rows, ckWindowSel(sel, len(rows), avail), avail)
	return out
}

// ckScrollLines clips pre-rendered lines to an avail-line window starting at
// off (clamped), replacing the edge rows with "↑/↓ N more" cues when content
// continues beyond them. Returns the window and the clamped offset.
func ckScrollLines(lines []string, off, avail int) ([]string, int) {
	if avail < 1 {
		avail = 1
	}
	maxOff := len(lines) - avail
	if maxOff < 0 {
		maxOff = 0
	}
	if off > maxOff {
		off = maxOff
	}
	if off < 0 {
		off = 0
	}
	if len(lines) <= avail {
		return lines, 0
	}
	out := append([]string(nil), lines[off:off+avail]...)
	if off > 0 {
		out[0] = ckFaintS.Render(fmt.Sprintf("↑ %d more", off))
	}
	if below := len(lines) - off - avail; below > 0 {
		out[len(out)-1] = ckFaintS.Render(fmt.Sprintf("↓ %d more", below))
	}
	return out, off
}

// ── wrapped-log clipping (#506) ───────────────────────────────────────────────

// ckLogEntryCap bounds how many rendered lines a single log entry may occupy.
// Without it, one long entry wraps past the whole pane on its own and pushes
// the input line off-frame (#506).
const ckLogEntryCap = 6

// ckLogLines renders every log entry as clipped display lines — each entry
// capped at entryCap rendered lines (#506) — flattened for the scroll window.
func ckLogLines(log []string, w, entryCap int) []string {
	if entryCap < 1 {
		entryCap = 1
	}
	var out []string
	for _, e := range log {
		out = append(out, strings.Split(ckClipToLines(e, w, entryCap), "\n")...)
	}
	return out
}

// ckVisibleLog is the chat scrollback window: scroll < 0 follows the live
// tail; otherwise the window is anchored at that line, so appends never yank
// a reader who scrolled up — the arrivals just grow the "below" cue.
func ckVisibleLog(log []string, w, logH, scroll int) string {
	entryCap := ckLogEntryCap
	if logH < entryCap {
		entryCap = logH
	}
	lines := ckLogLines(log, w, entryCap)
	if len(lines) <= logH {
		return strings.Join(lines, "\n")
	}
	top := len(lines) - logH
	if scroll < 0 || scroll >= top {
		out, _ := ckScrollLines(lines, top, logH)
		return strings.Join(out, "\n")
	}
	out, off := ckScrollLines(lines, scroll, logH)
	out[len(out)-1] = ckFaintS.Render(truncate(
		fmt.Sprintf("↓ %d below · end → live", len(lines)-off-logH), max(1, w)))
	return strings.Join(out, "\n")
}

// ckClipToLines wraps one log entry to width w, truncated to maxLines with the
// cut marked (#506). Always returns the WRAPPED text: returning the original
// made the window count 1 line where the render painted 3 (#597).
func ckClipToLines(s string, w, maxLines int) string {
	if w < 1 {
		w = 1
	}
	rows := strings.Split(lipgloss.NewStyle().Width(w).Render(s), "\n")
	if len(rows) > maxLines {
		if maxLines <= 1 {
			return ckFaintS.Render("…(truncated)")
		}
		rows = append(rows[:maxLines-1], ckFaintS.Render("…(truncated)"))
	}
	return strings.Join(rows, "\n")
}

// ── small formatters ──────────────────────────────────────────────────────────

// ckFmtMS renders a latency/duration figure; unknown renders "—".
func ckFmtMS(ms int64) string {
	if ms <= 0 {
		return "—"
	}
	if ms >= 10000 {
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	}
	return fmt.Sprintf("%dms", ms)
}

// ckTokens renders a token count compactly without truncating small ones to 0.
func ckTokens(n int) string {
	if n >= 10000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
