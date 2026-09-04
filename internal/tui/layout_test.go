// SPDX-License-Identifier: MIT

package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// stripANSI removes lipgloss's escape sequences so the underlying text can be
// compared.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			i++ // skip the 'm'
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// truncate bounds every label in the cockpit. It must never exceed its budget
// in *display cells* — rune or byte counts misalign the moment a name carries
// wide characters — and must never cut a rune or an escape in half.
func TestTruncate_CountsDisplayCells(t *testing.T) {
	inputs := []string{
		"",
		"ab",
		"a-fairly-long-model-name",
		strings.Repeat("x", 200),
		"日本語モデル",                // every rune is 2 cells wide
		"qwen2.5-coder:7b-日本語",  // mixed
		strings.Repeat("é", 60), // 2 bytes each, 1 cell each
	}
	for _, n := range []int{0, 1, 2, 5, 8, 46} {
		for _, s := range inputs {
			got := truncate(s, n)
			if w := lipgloss.Width(got); w > n {
				t.Errorf("truncate(%q, %d) is %d cells wide", s, n, w)
			}
			for _, r := range got {
				if r == '�' {
					t.Errorf("truncate(%q, %d) = %q — it cut a rune in half", s, n, got)
					break
				}
			}
		}
	}
	if got := truncate("short", 46); got != "short" {
		t.Errorf("truncate = %q, want unchanged", got)
	}
	// A cut is disclosed with an ellipsis, not silent.
	if got := truncate("abcdefgh", 4); !strings.HasSuffix(got, "…") {
		t.Errorf("truncate(…, 4) = %q, want an ellipsis marking the cut", got)
	}
	// Styled input must survive without a torn escape sequence.
	styled := ckCyanS.Render("a styled label that is long")
	if got := truncate(styled, 10); lipgloss.Width(got) > 10 {
		t.Errorf("styled truncate is %d cells wide", lipgloss.Width(got))
	}
}

// ckCell/ckRCell are the table cells every column is built from: exactly w
// cells, whatever the content — a long value must never shift its neighbours.
func TestCkCell_ExactWidthAndAlignment(t *testing.T) {
	for _, s := range []string{"", "x", "abcdef", "日本語モデル", ckCheapS.Render("styled"), strings.Repeat("y", 99)} {
		for _, w := range []int{1, 4, 8, 20} {
			if got := lipgloss.Width(ckCell(s, w)); got != w {
				t.Errorf("ckCell(%q, %d) is %d cells", s, w, got)
			}
			if got := lipgloss.Width(ckRCell(s, w)); got != w {
				t.Errorf("ckRCell(%q, %d) is %d cells", s, w, got)
			}
		}
	}
	if got := stripANSI(ckRCell("42", 6)); got != "    42" {
		t.Errorf("ckRCell right-aligns wrong: %q", got)
	}
	if got := stripANSI(ckCell("42", 6)); got != "42    " {
		t.Errorf("ckCell left-aligns wrong: %q", got)
	}
}

// ckFrame is the shell's guarantee: no body can exceed its box, and a crop is
// disclosed rather than silently pushing chrome off-frame.
func TestCkFrame_ClampsAndDiscloses(t *testing.T) {
	tall := strings.Repeat("line\n", 50) + "last"
	got := ckFrame(tall, 40, 10)
	lines := strings.Split(got, "\n")
	if len(lines) != 10 {
		t.Fatalf("ckFrame produced %d lines, want 10", len(lines))
	}
	if !strings.Contains(stripANSI(lines[9]), "more line") {
		t.Errorf("the crop is not disclosed: %q", lines[9])
	}

	wide := strings.Repeat("wide ", 40)
	for i, l := range strings.Split(ckFrame(wide, 30, 5), "\n") {
		if w := lipgloss.Width(l); w > 30 {
			t.Errorf("line %d is %d cells wide, want <= 30", i, w)
		}
	}

	// Content that already fits keeps its lines, padded to exactly h so the
	// status bar below it always lands on the last row (#630).
	padded := ckFrame("a\nb", 20, 5)
	if lines := strings.Split(padded, "\n"); len(lines) != 5 {
		t.Errorf("ckFrame padded to %d lines, want 5: %q", len(lines), padded)
	} else if lines[0] != "a" || lines[1] != "b" {
		t.Errorf("ckFrame altered fitting content: %q", padded)
	}
	// Degenerate budgets must not panic.
	if got := ckFrame("x", 0, 0); got == "" {
		t.Error("ckFrame(0,0) rendered nothing")
	}
}

// ckSplit shows two whole panes or one whole pane — never two broken ones.
func TestCkSplit_JoinsWideAndFallsBackNarrow(t *testing.T) {
	a, b := "aaaa\naaaa", "bbbb\nbbbb"
	joined := ckSplit(40, a, b, false)
	if !strings.Contains(joined, "aaaa") || !strings.Contains(joined, "bbbb") {
		t.Errorf("wide split lost a pane:\n%s", joined)
	}
	if got := ckSplit(6, a, b, false); strings.Contains(got, "bbbb") {
		t.Errorf("narrow split shows the secondary without focus:\n%s", got)
	}
	if got := ckSplit(6, a, b, true); !strings.Contains(got, "bbbb") || strings.Contains(got, "aaaa") {
		t.Errorf("narrow focused split should show only the secondary:\n%s", got)
	}
}

// The smart truncation drops a prefix all siblings share, so three Gemini
// variants never render as three identical rows (the issue's own example).
func TestCkDistinctTruncate_KeepsTheDistinguishingPart(t *testing.T) {
	names := []string{
		"Gemini 3.5 Flash (High)",
		"Gemini 3.5 Flash (Medium)",
		"Gemini 3.5 Flash (Low)",
	}
	got := ckDistinctTruncate(names, 12)
	seen := map[string]bool{}
	for i, g := range got {
		if lipgloss.Width(g) > 12 {
			t.Errorf("row %d is %d cells wide: %q", i, lipgloss.Width(g), g)
		}
		if seen[g] {
			t.Fatalf("two siblings truncated identically: %q in %v", g, got)
		}
		seen[g] = true
	}
	for i, want := range []string{"High", "Medium", "Low"} {
		if !strings.Contains(got[i], want) {
			t.Errorf("row %d = %q lost its distinguishing part %q", i, got[i], want)
		}
	}
	// The dropped prefix is disclosed.
	if !strings.HasPrefix(got[0], "…") {
		t.Errorf("a dropped prefix is not marked: %q", got[0])
	}

	// Names that fit are left alone.
	short := []string{"qwen", "llama"}
	if got := ckDistinctTruncate(short, 12); got[0] != "qwen" || got[1] != "llama" {
		t.Errorf("short names were altered: %v", got)
	}
	// A single name has no sibling prefix to drop — plain right truncation.
	one := ckDistinctTruncate([]string{"Gemini 3.5 Flash (High)"}, 10)
	if strings.HasPrefix(one[0], "…") {
		t.Errorf("a lone name was prefix-cut: %q", one[0])
	}
	// No shared prefix → plain truncation, still unique input untouched.
	mixed := ckDistinctTruncate([]string{"alpha-model-very-long-name", "beta"}, 10)
	if lipgloss.Width(mixed[0]) > 10 || mixed[1] != "beta" {
		t.Errorf("mixed truncation wrong: %v", mixed)
	}
	if got := ckDistinctTruncate(nil, 10); len(got) != 0 {
		t.Errorf("nil names produced %v", got)
	}

	// A mixed group (the real antigravity roster shape): only the COLLIDING
	// subset's shared prefix is dropped; unrelated siblings keep their names.
	group := []string{
		"Claude Opus 4.6 (Thinking)",
		"Gemini 3.5 Flash (High)",
		"Gemini 3.5 Flash (Medium)",
		"Gemini 3.5 Flash (Low)",
		"qwen",
	}
	got2 := ckDistinctTruncate(group, 16)
	seen2 := map[string]bool{}
	for i, g := range got2 {
		if lipgloss.Width(g) > 16 {
			t.Errorf("row %d is %d cells: %q", i, lipgloss.Width(g), g)
		}
		if seen2[g] {
			t.Fatalf("mixed-group siblings truncated identically: %q in %v", g, got2)
		}
		seen2[g] = true
	}
	for i, want := range map[int]string{1: "High", 2: "Medium", 3: "Low"} {
		if !strings.Contains(got2[i], want) {
			t.Errorf("row %d = %q lost %q", i, got2[i], want)
		}
	}
	if got2[4] != "qwen" {
		t.Errorf("an unrelated short sibling was altered: %q", got2[4])
	}

	// Genuine duplicates cannot be told apart — and must not loop forever.
	dup := ckDistinctTruncate([]string{"same-long-name-here-x", "same-long-name-here-x"}, 10)
	if len(dup) != 2 {
		t.Fatalf("dup = %v", dup)
	}
}

// ckBar and ckSegmentedBar render numbers into fixed-width cells. Overflowing
// the width breaks panel layout; a negative fill panics strings.Repeat.
func TestBars_StayWithinTheirWidth(t *testing.T) {
	for _, w := range []int{1, 5, 10, 20} {
		for _, pct := range []int{-100, -1, 0, 1, 50, 74, 75, 99, 100, 500} {
			bar := stripANSI(ckBar(pct, w))
			if n := len([]rune(bar)); n != w {
				t.Errorf("ckBar(%d, %d) is %d cells wide: %q", pct, w, n, bar)
			}
		}
	}
	if ckBar(10, 10) == ckBar(90, 10) {
		t.Error("ckBar renders 10% and 90% identically")
	}

	styles := []lipgloss.Style{ckCheapS, ckExpS, ckMidS}
	for _, vals := range [][]int{{0, 0, 0}, {1, 0, 0}, {0, 0, 1}, {1, 1, 1}, {7, 3, 2}, {100, 1, 1}, {1, 1, 100}} {
		for _, w := range []int{1, 10, 20} {
			bar := stripANSI(ckSegmentedBar(w, vals, styles))
			if n := len([]rune(bar)); n != w {
				t.Errorf("ckSegmentedBar(%d, %v) is %d cells: %q", w, vals, n, bar)
			}
		}
	}
	if got := stripANSI(ckSegmentedBar(10, []int{0, 0}, styles[:2])); got != strings.Repeat("░", 10) {
		t.Errorf("zero total = %q, want 10 faint dots", got)
	}
}

// One overlong entry must be visibly truncated, not silently dropped (#506).
func TestCkClipToLines_MarksTruncationAndBoundsLength(t *testing.T) {
	long := strings.Repeat("word ", 400)
	got := stripANSI(ckClipToLines(long, 20, 4))
	rows := strings.Split(got, "\n")
	if len(rows) != 4 {
		t.Fatalf("ckClipToLines produced %d rows, want 4", len(rows))
	}
	if !strings.Contains(rows[len(rows)-1], "truncated") {
		t.Errorf("the last row does not disclose the truncation: %q", rows[len(rows)-1])
	}
	// A short entry keeps its text and is returned WRAPPED (padded to width):
	// the window counts these lines and the renderer paints them, so the two
	// must be the same string — returning the original un-wrapped entry made
	// the count 1 where the render was 3 (#597's off-frame input bar).
	short := "a short line"
	got = ckClipToLines(short, 60, 4)
	if stripANSI(strings.TrimRight(got, " ")) != short {
		t.Errorf("a short entry lost its text: %q", got)
	}
	if strings.Contains(got, "\n") {
		t.Errorf("a fitting entry was split: %q", got)
	}
	// A moderately long entry (wider than the pane, within the cap) must come
	// back as real display lines, none wider than the pane.
	mid := strings.Repeat("word ", 14) // 70 cells at width 28
	for i, l := range strings.Split(ckClipToLines(mid, 28, 6), "\n") {
		if w := lipgloss.Width(l); w > 28 {
			t.Errorf("line %d is %d cells wide, want <= 28", i, w)
		}
	}
}

// The visible-log window must size itself in wrapped lines, not logical
// entries, and always keep the newest entry (#506).
func TestCkVisibleLog_CapsEachEntryAndFillsFromTheTail(t *testing.T) {
	log := []string{
		"short one",
		strings.Repeat("word ", 400), // would wrap past the whole pane alone
		"short two",
	}
	got := stripANSI(ckVisibleLog(log, 20, 8, -1))
	if n := len(strings.Split(got, "\n")); n > 8 {
		t.Errorf("ckVisibleLog produced %d lines, want <= 8:\n%s", n, got)
	}
	if !strings.Contains(got, "short two") {
		t.Errorf("the newest entry is missing:\n%s", got)
	}
}

// Chat scrollback semantics: an anchored window must not move when new lines
// append below it — the arrivals only grow the "below" cue — and the tail
// window follows appends when live.
func TestCkVisibleLog_AnchoredWindowDoesNotYankOnAppend(t *testing.T) {
	var log []string
	for i := 0; i < 40; i++ {
		log = append(log, fmt.Sprintf("line %02d", i))
	}
	before := stripANSI(ckVisibleLog(log, 30, 10, 5))
	if !strings.Contains(before, "line 06") {
		t.Fatalf("anchor at 5 does not show line 06:\n%s", before)
	}

	appended := append(append([]string(nil), log...), "brand new output")
	after := stripANSI(ckVisibleLog(appended, 30, 10, 5))
	if !strings.Contains(after, "line 06") {
		t.Errorf("an append yanked the anchored window:\n%s", after)
	}
	if strings.Contains(after, "brand new") {
		t.Errorf("an anchored window jumped to the new output:\n%s", after)
	}
	// The below-cue must disclose the new content and how to get back.
	if !strings.Contains(after, "below") || !strings.Contains(after, "end → live") {
		t.Errorf("no new-output cue while scrolled up:\n%s", after)
	}

	// Live mode follows the tail.
	live := stripANSI(ckVisibleLog(appended, 30, 10, -1))
	if !strings.Contains(live, "brand new output") {
		t.Errorf("live mode does not follow the tail:\n%s", live)
	}
	// And discloses the scrollback above.
	if !strings.Contains(live, "↑") {
		t.Errorf("live mode does not show the position cue:\n%s", live)
	}
}

// ckWindowSel keeps the selection visible and clear of the cue rows at every
// position — the "selection can never walk off-screen" rule.
func TestCkWindowSel_SelectionAlwaysRendered(t *testing.T) {
	rows := make([]string, 40)
	for i := range rows {
		rows[i] = fmt.Sprintf("row %02d", i)
	}
	for _, avail := range []int{3, 5, 20} {
		for sel := 0; sel < len(rows); sel++ {
			got := ckSelScroll(rows, sel, avail)
			if len(got) > avail {
				t.Fatalf("sel %d avail %d: window is %d lines", sel, avail, len(got))
			}
			if !strings.Contains(stripANSI(strings.Join(got, "\n")), fmt.Sprintf("row %02d", sel)) {
				t.Fatalf("sel %d avail %d: the selected row is not rendered:\n%s",
					sel, avail, strings.Join(got, "\n"))
			}
		}
	}
	// The canonical case from the requirement: 40 rows, 20-row pane, row 35.
	got := stripANSI(strings.Join(ckSelScroll(rows, 35, 20), "\n"))
	if !strings.Contains(got, "row 35") {
		t.Errorf("row 35 not visible in a 20-row pane:\n%s", got)
	}
}

// ckScrollLines clamps its offset and replaces edge rows with cues.
func TestCkScrollLines_CuesAndClamping(t *testing.T) {
	lines := make([]string, 30)
	for i := range lines {
		lines[i] = fmt.Sprintf("L%02d", i)
	}
	out, off := ckScrollLines(lines, 10, 10)
	if off != 10 || len(out) != 10 {
		t.Fatalf("off=%d len=%d", off, len(out))
	}
	if s := stripANSI(out[0]); !strings.Contains(s, "↑ 10 more") {
		t.Errorf("top cue = %q", s)
	}
	if s := stripANSI(out[9]); !strings.Contains(s, "↓ 10 more") {
		t.Errorf("bottom cue = %q", s)
	}
	// Offsets beyond the end clamp to the last window.
	if _, off := ckScrollLines(lines, 999, 10); off != 20 {
		t.Errorf("overscroll clamped to %d, want 20", off)
	}
	if _, off := ckScrollLines(lines, -5, 10); off != 0 {
		t.Errorf("negative offset clamped to %d, want 0", off)
	}
	// Content that fits needs no cues and no copy.
	short := []string{"a", "b"}
	if out, off := ckScrollLines(short, 3, 10); off != 0 || len(out) != 2 {
		t.Errorf("fitting content was windowed: %v %d", out, off)
	}
}

func TestFormatters(t *testing.T) {
	for _, tt := range []struct {
		in   int64
		want string
	}{
		{0, "—"}, {-1, "—"}, {250, "250ms"}, {9999, "9999ms"}, {10000, "10.0s"}, {37654, "37.7s"},
	} {
		if got := ckFmtMS(tt.in); got != tt.want {
			t.Errorf("ckFmtMS(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}
	if got := ckTokens(999); got != "999" {
		t.Errorf("ckTokens(999) = %q", got)
	}
	// Small counts must not truncate to "0.0k" — user-visible zeros lie.
	if got := ckTokens(12345); got != "12.3k" {
		t.Errorf("ckTokens(12345) = %q", got)
	}
	if got := ckSafe("a\nb\x1b[31mc"); strings.ContainsAny(got, "\n\x1b") {
		t.Errorf("ckSafe left control characters in %q", got)
	}
}

// ckCommonPrefix drives the smart truncation; wrong lengths cut real content.
func TestCkCommonPrefix(t *testing.T) {
	for _, tt := range []struct {
		names []string
		want  int
	}{
		{[]string{"abc", "abd"}, 2},
		{[]string{"same", "same"}, 4},
		{[]string{"x", "y"}, 0},
		{[]string{"日本語A", "日本語B"}, 3},
		{[]string{"only"}, 4},
	} {
		if got := ckCommonPrefix(tt.names); got != tt.want {
			t.Errorf("ckCommonPrefix(%v) = %d, want %d", tt.names, got, tt.want)
		}
	}
}

// ckWordmark/ckLerpHex paint the brand; the text must survive styling.
func TestCkWordmark_PreservesText(t *testing.T) {
	if got := stripANSI(ckWordmark("HYDRA")); got != "HYDRA" {
		t.Errorf("wordmark text = %q", got)
	}
	if c := ckLerpHex(0.0); c != lipgloss.Color("#2AF0E0") {
		t.Errorf("lerp(0) = %v", c)
	}
	if c := ckLerpHex(1.0); c != lipgloss.Color("#E852C8") {
		t.Errorf("lerp(1) = %v", c)
	}
}

func TestPlural(t *testing.T) {
	if plural(1) != "" || plural(0) != "s" || plural(2) != "s" {
		t.Error("plural is wrong")
	}
}

// The tier colour ramp must be defined for every tier, including ones outside
// the documented 1–10 range.
func TestCkTierColor_CoversEveryTier(t *testing.T) {
	seen := map[lipgloss.Color]bool{}
	for tier := -1; tier <= 12; tier++ {
		c := ckTierColor(tier)
		if c == "" {
			t.Errorf("tier %d has no colour", tier)
		}
		seen[c] = true
	}
	if len(seen) < 3 {
		t.Errorf("the ramp collapsed to %d colours", len(seen))
	}
}

// A sanity pin on the ellipsis contract, since every column depends on it.
func TestTruncate_TableExamples(t *testing.T) {
	for _, tt := range []struct {
		in   string
		n    int
		want string
	}{
		{"abcdef", 6, "abcdef"},
		{"abcdefg", 6, "abcde…"},
		{"日本語モデル", 5, "日本…"},
	} {
		if got := truncate(tt.in, tt.n); got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
	}
	_ = fmt.Sprintf // keep fmt for future table extension
}
