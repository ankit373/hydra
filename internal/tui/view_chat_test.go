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

// Mode commands change where the next task routes, so the change must be
// visible in the log; case-insensitive (#465); unknown ones reported.
func TestChat_ModeCommands(t *testing.T) {
	for _, cmd := range []string{"/dispatch", "/swarm", "/trust", "/local", "/Trust"} {
		m, _ := enter(typed(testCockpit(), cmd))
		want := strings.ToLower(cmd[1:])
		if m.mode != want {
			t.Errorf("%s left mode = %q", cmd, m.mode)
		}
		if !strings.Contains(strings.Join(m.log, "\n"), want) {
			t.Errorf("%s was not acknowledged in the log", cmd)
		}
	}
	m, _ := enter(typed(testCockpit(), "/nonsense"))
	if m.mode == "nonsense" {
		t.Error("an unknown command was accepted as a mode")
	}
	if !strings.Contains(strings.Join(m.log, "\n"), "unknown") {
		t.Error("the unknown command was not reported")
	}
}

// An empty submit is a no-op, not a request of the empty string.
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

// With no models scanned there is no route to preview. Inventing one is
// exactly what #189 removed.
func TestChat_NoModelsSaysSoAndStreamsNothing(t *testing.T) {
	m := testCockpit()
	m.heads = nil
	m, _ = enter(typed(m, "add pagination"))
	joined := strings.Join(m.log, "\n")
	if !strings.Contains(joined, "no routable model") {
		t.Errorf("the log does not say why nothing was routed:\n%s", joined)
	}
	if !strings.Contains(joined, "hyctl probe") {
		t.Error("the message does not tell the user how to find out why")
	}
	if len(m.codeLines) != 0 {
		t.Error("a code stream started with nothing to route to")
	}
}

// Submitting a task previews the route and starts the code stream — and the
// per-line "vs all-frontier" comparison is gone (it lives in usage now).
func TestChat_TaskPreviewStreamsAndCarriesNoComparison(t *testing.T) {
	before := testCockpit()
	m, cmd := enter(typed(before, "add pagination to the users endpoint"))

	if len(m.log) <= len(before.log) {
		t.Fatal("submitting a task produced no log output")
	}
	if cmd == nil {
		t.Error("no code-stream tick was scheduled")
	}
	if m.codeGen == before.codeGen {
		t.Error("the stream generation did not advance")
	}
	if m.runs != before.runs+1 {
		t.Errorf("runs = %d, want incremented", m.runs)
	}
	joined := stripANSI(strings.Join(m.log, "\n"))
	if !strings.Contains(joined, "routing preview only") {
		t.Errorf("the log does not say nothing was sent:\n%s", joined)
	}
	if !strings.Contains(joined, "add pagination") {
		t.Error("the log does not echo what was asked")
	}
	if strings.Contains(joined, "vs all-") {
		t.Errorf("the per-line cost comparison is back — it moved to the usage view:\n%s", joined)
	}
}

// The route line's cost claim must be honest: a real price, "free (local)"
// only for a local model, and "cost unknown" otherwise.
func TestChat_RouteCostClaims(t *testing.T) {
	m := testCockpit()
	m.heads = []ckHead{{id: "q", name: "qwen", tier: 10, up: true, local: true}}
	m, _ = enter(typed(m, "rename x to y"))
	if !strings.Contains(strings.Join(m.log, "\n"), "free (local)") {
		t.Errorf("a local model's preview does not say free:\n%s", strings.Join(m.log, "\n"))
	}

	m = testCockpit()
	m.heads = []ckHead{{id: "c", name: "claude", tier: 1, up: true}}
	m, _ = enter(typed(m, "rotate the signing key")) // CORE → T1, the only head
	if !strings.Contains(strings.Join(m.log, "\n"), "cost unknown") {
		t.Errorf("an unpriced remote model's preview does not say unknown:\n%s", strings.Join(m.log, "\n"))
	}
}

// A pinned tier (set from the models view) overrides classification and is
// visible in the preview.
func TestChat_PinnedTierOverridesClassification(t *testing.T) {
	m := testCockpit()
	m.pinnedTier = 3
	m, _ = enter(typed(m, "rename x to y")) // SIMPLE → T8 normally
	joined := stripANSI(strings.Join(m.log, "\n"))
	if !strings.Contains(joined, "pinned T3") {
		t.Errorf("the preview does not show the pin:\n%s", joined)
	}
	if !strings.Contains(joined, "claude-sonnet") {
		t.Errorf("the pin did not route to the T3 head:\n%s", joined)
	}
	// --local mode still wins over the pin: nothing leaves the machine.
	m = testCockpit()
	m.pinnedTier = 1
	m.mode = "local"
	m, _ = enter(typed(m, "rename x to y"))
	if !strings.Contains(stripANSI(strings.Join(m.log, "\n")), "qwen") {
		t.Error("local mode did not override the pin")
	}
}

// pickHead is the cockpit's own routing: cheapest at or below the wanted
// strength, never answering "cheapest" with "most expensive" (#165's shape).
func TestPickHead_NeverAnswersCheapestWithStrongest(t *testing.T) {
	m := testCockpit()
	if got := m.pickHead(10); got < 0 || m.heads[got].id != "qwen" {
		t.Errorf("pickHead(10) = %d, want the local head", got)
	}
	if got := m.pickHead(3); m.heads[got].tier < 3 {
		t.Errorf("pickHead(3) selected tier %d — stronger than requested", m.heads[got].tier)
	}
	if got := m.pickHead(1); m.heads[got].id != "opus" {
		t.Errorf("pickHead(1) = %s", m.heads[got].id)
	}
	m.heads[2].up = false
	if got := m.pickHead(10); got >= 0 && m.heads[got].id == "qwen" {
		t.Error("pickHead selected a head that is down")
	}
	for i := range m.heads {
		m.heads[i].up = false
	}
	if got := m.pickHead(1); got >= 0 {
		t.Errorf("pickHead = %d with every head down (#248's shape)", got)
	}
	m.heads = nil
	if got := m.pickHead(1); got >= 0 {
		t.Errorf("pickHead on an empty roster = %d", got)
	}
}

func TestClassifyTask_RoutesByWhatTheWorkActuallyIs(t *testing.T) {
	tests := []struct {
		task     string
		mode     string
		wantEnum string
		wantTier int
	}{
		{"design a multi-tenant security model", "local", "LOCAL", 10},
		{"rotate the signing key without breaking live tokens", "", "CORE", 1},
		{"refactor this for a data race", "", "COMPLEX", 3},
		{"add pagination to the users endpoint", "", "STANDARD", 6},
		{"rename x to y", "", "SIMPLE", 8},
		{"", "", "SIMPLE", 8},
	}
	for _, tt := range tests {
		enum, tier := classifyTask(tt.task, tt.mode)
		if enum != tt.wantEnum || tier != tt.wantTier {
			t.Errorf("classifyTask(%q, %q) = (%s, %d), want (%s, %d)",
				tt.task, tt.mode, enum, tier, tt.wantEnum, tt.wantTier)
		}
	}
	long := strings.Repeat("please do the thing carefully ", 5)
	if _, tier := classifyTask(long, ""); tier >= 8 {
		t.Errorf("a long prompt classified at tier %d — length should lift it", tier)
	}
}

// The change-impact line renders only for files a graph actually knows (#193).
func TestChatRun_ImpactOnlyForFilesTheGraphKnows(t *testing.T) {
	testutil.NewSandbox(t)
	m := testCockpit()
	m, _ = enter(typed(m, "rotate the key in internal/nowhere/absent.go"))
	joined := strings.Join(m.log, "\n")
	if strings.Contains(joined, "κ=") {
		t.Errorf("an impact figure was printed with no graph loaded:\n%s", joined)
	}
	if !strings.Contains(joined, "routing preview only") {
		t.Errorf("the run did not complete:\n%s", joined)
	}
}

// chatMain's total output must be exactly h lines (#445), and one overlong
// entry must not push the input off-frame (#506).
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

// The chat pane keeps only the newest lines that fit when live.
func TestChatMain_KeepsTheNewestLinesAndTheInput(t *testing.T) {
	m := testCockpit()
	m.input = "half-typed prompt"
	m.log = nil
	for i := 0; i < 200; i++ {
		m.log = append(m.log, fmt.Sprintf("log line %d", i))
	}
	got := stripANSI(m.chatMain(60, 10))
	if !strings.Contains(got, "log line 199") {
		t.Errorf("the newest line is not shown:\n%s", got)
	}
	if strings.Contains(got, "log line 100") {
		t.Errorf("an old line survived a 10-row window:\n%s", got)
	}
	if !strings.Contains(got, "half-typed prompt") || !strings.Contains(got, "dispatch") {
		t.Errorf("the input line or mode prompt is missing:\n%s", got)
	}
	tiny := stripANSI(m.chatMain(20, 0))
	if !strings.Contains(tiny, "half-typed prompt") {
		t.Errorf("at height 0 the input is gone:\n%s", tiny)
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

// The code panel renders whether or not anything has streamed yet, at any width.
func TestCodePanel_RendersAtEverySize(t *testing.T) {
	m := testCockpit()
	if got := m.codePanel(40, 20); !strings.Contains(got, "awaiting a request") {
		t.Errorf("the empty state does not say what it is waiting for:\n%s", got)
	}
	after, _ := enter(typed(m, "write a handler"))
	after.codeShown = len(after.codeLines)
	for _, w := range []int{0, 1, 5, 40, 200} {
		if got := after.codePanel(w, 20); strings.TrimSpace(got) == "" {
			t.Errorf("codePanel(%d) rendered nothing", w)
		}
	}
	after.codeShown = len(after.codeLines) + 50
	if got := after.codePanel(60, 20); strings.TrimSpace(got) == "" {
		t.Error("codePanel rendered nothing with codeShown past the end")
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

// ckSnippet must return a language tag and at least one newline-free line for
// every enum, including one it has never seen.
func TestCkSnippet_EveryEnumYieldsRenderableCode(t *testing.T) {
	for _, enum := range []string{"CORE", "COMPLEX", "STANDARD", "SIMPLE", "GRUNT", "", "NOT_AN_ENUM"} {
		lang, lines := ckSnippet(enum)
		if lang == "" || len(lines) == 0 {
			t.Errorf("ckSnippet(%q) = (%q, %d lines)", enum, lang, len(lines))
		}
		for i, l := range lines {
			if strings.Contains(l, "\n") {
				t.Errorf("ckSnippet(%q) line %d contains a newline", enum, i)
			}
		}
	}
}
