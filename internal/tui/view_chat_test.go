// SPDX-License-Identifier: MIT

package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/testutil"
)

// Every :view command jumps; unknown commands are reported, not swallowed.
func TestChat_ViewCommandsSwitchViews(t *testing.T) {
	testutil.NewSandbox(t)
	for want, name := range ckViewNames {
		m, _ := enter(typed(testCockpit(), ":"+name))
		if m.view != want {
			t.Errorf(":%s left view = %d, want %d", name, m.view, want)
		}
		if m.input != "" {
			t.Errorf("the input still holds %q after submitting", m.input)
		}
	}
	m, _ := enter(typed(testCockpit(), ":nonsense"))
	if !strings.Contains(strings.Join(m.log, "\n"), "unknown") {
		t.Error("an unknown :command was not reported")
	}
}

func TestChat_QuitCommands(t *testing.T) {
	for _, cmd := range []string{":q", ":quit", ":Q"} {
		if _, c := enter(typed(testCockpit(), cmd)); c == nil {
			t.Errorf("%s did not quit", cmd)
		}
	}
}

// The /mode commands mirror the picker: every defined mode by name,
// case-insensitive (#465), unknown ones reported. The phase-1 strategy
// commands (/dispatch /swarm /trust /local) are gone — strategy is ctrl+o now.
func TestChat_ModeCommands(t *testing.T) {
	for _, cmd := range []string{"/ask", "/edit", "/plan", "/auto", "/architect", "/careful", "/unattended", "/Ask"} {
		m, _ := enter(typed(testCockpit(), cmd))
		want := strings.ToLower(cmd[1:])
		if m.mode != want {
			t.Errorf("%s left mode = %q", cmd, m.mode)
		}
		if !strings.Contains(strings.Join(m.log, "\n"), want) {
			t.Errorf("%s was not acknowledged in the log", cmd)
		}
	}
	for _, cmd := range []string{"/nonsense", "/swarm", "/dispatch"} {
		m, _ := enter(typed(testCockpit(), cmd))
		if m.mode == strings.ToLower(cmd[1:]) {
			t.Errorf("%s was accepted as a mode", cmd)
		}
		if !strings.Contains(strings.Join(m.log, "\n"), "unknown") {
			t.Errorf("%s was not reported as unknown", cmd)
		}
	}
}

// An empty submit is a no-op with nothing finished; after a finished task it
// opens the trace (tested in chat_exec_test.go).
func TestChat_EmptySubmitDoesNothing(t *testing.T) {
	before := testCockpit()
	m, cmd := enter(before)
	if len(m.log) != len(before.log) || cmd != nil {
		t.Error("an empty submit did something")
	}
	m, cmd = enter(typed(testCockpit(), "   "))
	if len(m.log) != len(before.log) || cmd != nil {
		t.Error("a whitespace-only submit was treated as a task")
	}
}

// With no models scanned there is no route, so nothing must execute. Inventing
// one is exactly what #189 removed.
func TestChat_NoModelsSaysSoAndRunsNothing(t *testing.T) {
	m := testCockpit()
	m.heads = nil
	m, cmd := enter(typed(m, "add pagination"))
	joined := strings.Join(m.log, "\n")
	if !strings.Contains(joined, "no routable model") {
		t.Errorf("the log does not say why nothing was routed:\n%s", joined)
	}
	if !strings.Contains(joined, "hyctl probe") {
		t.Error("the message does not tell the user how to find out why")
	}
	if m.exec != nil || cmd != nil {
		t.Error("a task started with nothing to route to")
	}
}

// pickHead is the cockpit's route preview: cheapest at or below the wanted
// strength, never answering "cheapest" with "most expensive" (#165's shape),
// and localOnly restricts the whole search to local heads.
func TestPickHead_NeverAnswersCheapestWithStrongest(t *testing.T) {
	m := testCockpit()
	if got := m.pickHead(10, false); got < 0 || m.heads[got].id != "qwen" {
		t.Errorf("pickHead(10) = %d, want the local head", got)
	}
	if got := m.pickHead(3, false); m.heads[got].tier < 3 {
		t.Errorf("pickHead(3) selected tier %d — stronger than requested", m.heads[got].tier)
	}
	if got := m.pickHead(1, false); m.heads[got].id != "opus" {
		t.Errorf("pickHead(1) = %s", m.heads[got].id)
	}
	// localOnly: the strongest local head wins even when asked for T1.
	if got := m.pickHead(1, true); got < 0 || m.heads[got].id != "qwen" {
		t.Errorf("pickHead(1, localOnly) = %d, want the local head", got)
	}
	m.heads[2].up = false
	if got := m.pickHead(10, false); got >= 0 && m.heads[got].id == "qwen" {
		t.Error("pickHead selected a head that is down")
	}
	if got := m.pickHead(1, true); got >= 0 {
		t.Errorf("pickHead(localOnly) = %d with the only local head down", got)
	}
	for i := range m.heads {
		m.heads[i].up = false
	}
	if got := m.pickHead(1, false); got >= 0 {
		t.Errorf("pickHead = %d with every head down (#248's shape)", got)
	}
	m.heads = nil
	if got := m.pickHead(1, false); got >= 0 {
		t.Errorf("pickHead on an empty roster = %d", got)
	}
}

func TestClassifyTask_RoutesByWhatTheWorkActuallyIs(t *testing.T) {
	tests := []struct {
		task     string
		wantEnum string
		wantTier int
	}{
		{"rotate the signing key without breaking live tokens", "CORE", 1},
		{"refactor this for a data race", "COMPLEX", 3},
		{"add pagination to the users endpoint", "STANDARD", 6},
		{"rename x to y", "SIMPLE", 8},
		{"", "SIMPLE", 8},
	}
	for _, tt := range tests {
		enum, tier := classifyTask(tt.task)
		if enum != tt.wantEnum || tier != tt.wantTier {
			t.Errorf("classifyTask(%q) = (%s, %d), want (%s, %d)",
				tt.task, enum, tier, tt.wantEnum, tt.wantTier)
		}
	}
	long := strings.Repeat("please do the thing carefully ", 5)
	if _, tier := classifyTask(long); tier >= 8 {
		t.Errorf("a long prompt classified at tier %d — length should lift it", tier)
	}
}

// The change-impact line renders only for files a graph actually knows (#193).
func TestChatStart_ImpactOnlyForFilesTheGraphKnows(t *testing.T) {
	testutil.NewSandbox(t)
	m := testCockpit()
	m, _ = enter(typed(m, "rotate the key in internal/nowhere/absent.go"))
	joined := strings.Join(m.log, "\n")
	if strings.Contains(joined, "κ=") {
		t.Errorf("an impact figure was printed with no graph loaded:\n%s", joined)
	}
	if !strings.Contains(stripANSI(joined), "auto-routed") {
		t.Errorf("the route line is missing:\n%s", joined)
	}
}

// chatMain's total output must be exactly h lines (#445), and one overlong
// entry must not push the input bar off-frame (#506).
func TestChatMain_GeometryHolds(t *testing.T) {
	m := testCockpit()
	for _, h := range []int{10, 24, 30, 60} {
		out := m.chatMain(60, h)
		if got := strings.Count(out, "\n") + 1; got != h {
			t.Errorf("chatMain(60, %d) produced %d lines", h, got)
		}
	}

	m.input = "still typing"
	m.log = []string{strings.Repeat("x", 3000)}
	for _, h := range []int{10, 24} {
		out := m.chatMain(60, h)
		if got := strings.Count(out, "\n") + 1; got != h {
			t.Errorf("one huge entry changed the height to %d", got)
		}
		if !strings.Contains(stripANSI(out), "still typing") {
			t.Errorf("the input line was pushed off frame:\n%s", stripANSI(out))
		}
	}
}

// The chat pane keeps only the newest lines that fit when live, and the input
// bar always shows the typed text and the mode chip.
func TestChatMain_KeepsTheNewestLinesAndTheInput(t *testing.T) {
	m := testCockpit()
	m.input = "half-typed prompt"
	m.log = nil
	for i := 0; i < 200; i++ {
		m.log = append(m.log, fmt.Sprintf("log line %d", i))
	}
	got := stripANSI(m.chatMain(60, 12))
	if !strings.Contains(got, "log line 199") {
		t.Errorf("the newest line is not shown:\n%s", got)
	}
	if strings.Contains(got, "log line 100") {
		t.Errorf("an old line survived a 12-row window:\n%s", got)
	}
	if !strings.Contains(got, "half-typed prompt") || !strings.Contains(got, "Auto") {
		t.Errorf("the input line or mode chip is missing:\n%s", got)
	}
	// Even with no room for the border, the input itself survives.
	tiny := stripANSI(m.chatMain(30, 3))
	if !strings.Contains(tiny, "prompt") {
		t.Errorf("at height 3 the input is gone:\n%s", tiny)
	}
}

// A very long input wraps inside the bar, keeps its newest characters visible,
// and never grows past the wrap cap.
func TestInputBar_WrapsAndKeepsTheTail(t *testing.T) {
	m := testCockpit()
	m.input = strings.Repeat("word ", 200) + "FINAL"
	bar := m.inputBar(60)
	if got := strings.Count(bar, "\n") + 1; got > ckInputWrapCap+2 {
		t.Errorf("input bar grew to %d lines", got)
	}
	if !strings.Contains(stripANSI(bar), "FINAL") {
		t.Errorf("the input's tail (cursor region) is not visible:\n%s", stripANSI(bar))
	}
	// Placeholder when idle and empty.
	m.input = ""
	if got := stripANSI(m.inputBar(60)); !strings.Contains(got, "what do you need done?") {
		t.Errorf("no placeholder on an empty idle input:\n%s", got)
	}
}

// The sidebar must never exceed its height however many models were scanned
// (#446), and must disclose what it cannot list (#506).
func TestSidebar_HeightBoundAndDisclosure(t *testing.T) {
	heads := make([]ckHead, 30)
	for i := range heads {
		heads[i] = ckHead{id: "x", name: "head", tier: 1}
	}
	m := testCockpit()
	m.heads = heads
	for _, h := range []int{10, 15, 20} {
		out := m.sidebar(h)
		if got := strings.Count(out, "\n") + 1; got > h {
			t.Errorf("sidebar(%d) with 30 models produced %d lines", h, got)
		}
	}
	if out := m.sidebar(10); !strings.Contains(out, "more") {
		t.Errorf("sidebar(10) does not disclose the hidden models:\n%s", out)
	}
	if got := stripANSI(m.sidebar(6)); !strings.Contains(got, "30") {
		t.Errorf("sidebar(6) does not disclose the real count:\n%s", got)
	}
	empty := testCockpit()
	empty.heads = nil
	if got := empty.sidebar(6); strings.Contains(got, "not enough room") {
		t.Errorf("sidebar with zero models printed a count line:\n%s", got)
	}
}

// The sidebar's context gauge must render the unknown state honestly.
func TestSidebar_UnknownContextSaysNoData(t *testing.T) {
	m := testCockpit()
	m.pctKnown = false
	if got := stripANSI(m.sidebar(20)); !strings.Contains(got, "no data yet") {
		t.Errorf("unknown claude_pct did not render as no data:\n%s", got)
	}
	m.pctKnown, m.claudePct = true, 52
	if got := stripANSI(m.sidebar(20)); !strings.Contains(got, "claude 52%") {
		t.Errorf("known claude_pct not rendered:\n%s", got)
	}
}

// The code panel renders whether or not anything has streamed yet, at any
// width — and in diff mode it colours by prefix instead of syntax.
func TestCodePanel_RendersAtEverySize(t *testing.T) {
	m := testCockpit()
	if got := m.codePanel(40, 20); !strings.Contains(got, "no edits yet") {
		t.Errorf("the empty state does not say what it is waiting for:\n%s", got)
	}
	m.codeLang = "go"
	m.codeLines = []string{"package main", "func main() {}", "// done"}
	m.codeShown = len(m.codeLines)
	for _, w := range []int{0, 1, 5, 40, 200} {
		if got := m.codePanel(w, 20); strings.TrimSpace(got) == "" {
			t.Errorf("codePanel(%d) rendered nothing", w)
		}
	}
	m.codeShown = len(m.codeLines) + 50
	if got := m.codePanel(60, 20); strings.TrimSpace(got) == "" {
		t.Error("codePanel rendered nothing with codeShown past the end")
	}
	// A long file shows its newest lines rather than only the top of the file.
	m.codeLines = nil
	for i := 0; i < 100; i++ {
		m.codeLines = append(m.codeLines, fmt.Sprintf("line%d", i))
	}
	m.codeShown = 100
	if got := stripANSI(m.codePanel(60, 12)); !strings.Contains(got, "line99") {
		t.Errorf("the panel does not show the newest streamed lines:\n%s", got)
	}

	m.codeDiff = true
	m.codeLang = "diff"
	m.codeLines = []string{"@@ -1,2 +1,2 @@", "-old line", "+new line", " ctx"}
	m.codeShown = len(m.codeLines)
	if got := stripANSI(m.codePanel(60, 20)); !strings.Contains(got, "DIFF") || !strings.Contains(got, "+new line") {
		t.Errorf("diff mode did not render the diff:\n%s", got)
	}
}

func TestCkCodeLine_DiffColoursByPrefix(t *testing.T) {
	for _, line := range []string{"+added", "-removed", "@@ hunk @@", " context"} {
		if got := stripANSI(ckCodeLine(line, true)); got != line {
			t.Errorf("diff colouring changed the text: %q → %q", line, got)
		}
	}
	if got := stripANSI(ckCodeLine("func main() {}", false)); got != "func main() {}" {
		t.Errorf("code colouring changed the text: %q", got)
	}
}

// ckHighlight styles one line of code; the text must survive styling.
func TestCkHighlight_PreservesEveryCharacter(t *testing.T) {
	lines := []string{
		`func main() { fmt.Println("hello") }`,
		`// a comment with "quotes" and 'apostrophes'`,
		"x := `a raw string`",
		`s := "unterminated`,
		`interface Foo { bar: string }`,
		``,
		`   `,
		`日本語のコメント // with a trailing comment`,
	}
	for _, line := range lines {
		if got := stripANSI(ckHighlight(line)); got != line {
			t.Errorf("ckHighlight changed the text:\n in: %q\nout: %q", line, got)
		}
	}
}

func TestIsWordChar(t *testing.T) {
	for _, r := range []rune{'a', 'Z', '0', '9', '_'} {
		if !isWordChar(r) {
			t.Errorf("isWordChar(%q) = false", r)
		}
	}
	for _, r := range []rune{' ', '(', '.', '"', '·', '日'} {
		if isWordChar(r) {
			t.Errorf("isWordChar(%q) = true", r)
		}
	}
}
