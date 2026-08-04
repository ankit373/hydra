// SPDX-License-Identifier: MIT

package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/rank"
)

// The cockpit's command bar is how a user drives `hydra tui`. Every command it
// accepts must do what it says, and every one it does not recognise must say so
// rather than being swallowed.

func typed(m Cockpit, s string) Cockpit {
	for _, r := range s {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(Cockpit)
	}
	return m
}

func enter(m Cockpit) (Cockpit, tea.Cmd) {
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	return next.(Cockpit), cmd
}

func TestCockpit_ViewCommandsSwitchViews(t *testing.T) {
	tests := []struct {
		cmd  string
		want int
	}{
		{":dash", 1},
		{":tree", 2},
		{":chat", 0},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			m, _ := enter(typed(NewCockpit(), tt.cmd))
			if m.view != tt.want {
				t.Errorf("%s left view = %d, want %d", tt.cmd, m.view, tt.want)
			}
			if m.input != "" {
				t.Errorf("the input line still holds %q after submitting", m.input)
			}
		})
	}
}

func TestCockpit_QuitCommands(t *testing.T) {
	for _, cmd := range []string{":q", ":quit"} {
		if _, c := enter(typed(NewCockpit(), cmd)); c == nil {
			t.Errorf("%s did not quit", cmd)
		}
	}
}

// Mode commands change where the next task routes, so the change must be
// visible in the log — silently switching mode is worse than not switching.
func TestCockpit_ModeCommandsAreAcknowledged(t *testing.T) {
	for _, cmd := range []string{"/dispatch", "/swarm", "/trust", "/local"} {
		m, _ := enter(typed(NewCockpit(), cmd))
		if m.mode != cmd[1:] {
			t.Errorf("%s left mode = %q", cmd, m.mode)
		}
		if len(m.log) == 0 || !strings.Contains(strings.Join(m.log, "\n"), cmd[1:]) {
			t.Errorf("%s was not acknowledged in the log", cmd)
		}
	}
}

// An unknown slash command must be reported, not silently ignored — a user who
// typos a command otherwise thinks it worked.
func TestCockpit_UnknownCommandIsReported(t *testing.T) {
	before := len(NewCockpit().log)
	m, _ := enter(typed(NewCockpit(), "/nonsense"))
	if len(m.log) <= before {
		t.Fatal("an unknown command produced no output")
	}
	if !strings.Contains(strings.Join(m.log, "\n"), "unknown") {
		t.Errorf("the log does not say the command was unrecognised: %v", m.log)
	}
	if m.mode == "nonsense" {
		t.Error("an unknown command was accepted as a mode")
	}
}

// An empty submit is a no-op, not a dispatch of the empty string.
func TestCockpit_EmptySubmitDoesNothing(t *testing.T) {
	before := NewCockpit()
	m, cmd := enter(before)
	if len(m.log) != len(before.log) {
		t.Errorf("an empty submit appended to the log: %v", m.log)
	}
	if cmd != nil {
		t.Error("an empty submit scheduled work")
	}

	// Whitespace only is the same thing.
	m, cmd = enter(typed(NewCockpit(), "   "))
	if len(m.log) != len(before.log) || cmd != nil {
		t.Error("a whitespace-only submit was treated as a task")
	}
}

// With no heads discovered there is no route to preview. Inventing one is
// exactly what #189 removed, so the cockpit must say so and start no stream.
func TestCockpit_NoHeadsDiscoveredSaysSoAndStreamsNothing(t *testing.T) {
	before := NewCockpit()
	before.heads = nil

	m, _ := enter(typed(before, "add pagination"))
	joined := strings.Join(m.log, "\n")
	if !strings.Contains(joined, "no heads discovered") {
		t.Errorf("the log does not say why nothing was routed:\n%s", joined)
	}
	if !strings.Contains(joined, "hyctl probe") {
		t.Error("the message does not tell the user how to find out why")
	}
	if len(m.codeLines) != 0 {
		t.Error("a code stream started with nothing to route to")
	}
}

// Submitting a task previews the routing decision and starts the code stream.
func TestCockpit_SubmittingATaskLogsARoutingPreviewAndStreams(t *testing.T) {
	before := NewCockpit()
	before.heads = testHeads()

	m, cmd := enter(typed(before, "add pagination to the users endpoint"))

	if len(m.log) <= len(before.log) {
		t.Fatal("submitting a task produced no log output")
	}
	if cmd == nil {
		t.Error("no code-stream tick was scheduled")
	}
	if m.codeGen == before.codeGen {
		t.Error("the stream generation did not advance; a second task would " +
			"double-speed the first one's stream")
	}
	if len(m.codeLines) == 0 {
		t.Error("no code lines to stream")
	}
	if m.codeShown != 0 {
		t.Errorf("codeShown = %d before the first tick", m.codeShown)
	}
	joined := strings.Join(m.log, "\n")
	if !strings.Contains(joined, "routing preview only") {
		t.Errorf("the log does not say the cockpit executes nothing:\n%s", joined)
	}
	if !strings.Contains(joined, "add pagination") {
		t.Error("the log does not echo what was asked")
	}
	if m.runs != before.runs+1 {
		t.Errorf("runs = %d, want it incremented", m.runs)
	}
}

// The tick reveals one line at a time and stops at the end. A tick from a
// superseded run must be ignored, or two streams interleave.
func TestCockpit_CodeStreamRevealsOneLineAndIgnoresStaleTicks(t *testing.T) {
	c := NewCockpit()
	c.heads = testHeads()
	m, _ := enter(typed(c, "write a handler"))
	if len(m.codeLines) == 0 {
		t.Fatal("no code lines for this task")
	}

	next, cmd := m.Update(ckCodeTickMsg{gen: m.codeGen})
	m = next.(Cockpit)
	if m.codeShown != 1 {
		t.Fatalf("codeShown = %d after one tick, want 1", m.codeShown)
	}
	if cmd == nil && m.codeShown < len(m.codeLines) {
		t.Error("the stream stopped before reaching the end")
	}

	// A tick tagged with a previous generation must not advance anything.
	stale := m.codeShown
	next, _ = m.Update(ckCodeTickMsg{gen: m.codeGen - 1})
	if got := next.(Cockpit).codeShown; got != stale {
		t.Errorf("a stale tick advanced the stream from %d to %d", stale, got)
	}

	// Run to the end; the last tick must schedule nothing further.
	for i := m.codeShown; i < len(m.codeLines); i++ {
		next, cmd = m.Update(ckCodeTickMsg{gen: m.codeGen})
		m = next.(Cockpit)
	}
	if m.codeShown != len(m.codeLines) {
		t.Errorf("codeShown = %d, want all %d lines", m.codeShown, len(m.codeLines))
	}
	if cmd != nil {
		t.Error("the stream scheduled another tick after revealing the last line")
	}
}

// A resize must be recorded, since every panel is laid out against it.
func TestCockpit_ResizeIsRecorded(t *testing.T) {
	next, _ := NewCockpit().Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m := next.(Cockpit)
	if m.w != 120 || m.h != 40 || !m.ready {
		t.Errorf("resize left w=%d h=%d ready=%v", m.w, m.h, m.ready)
	}
	if NewCockpit().Init() != nil {
		t.Error("Init() returned a command; the cockpit has nothing to do on start")
	}
}

// The snapshot is what `hyctl tui --snapshot` prints. All three views must
// render and be labelled, or the output is unreadable in a bug report.
func TestCockpitSnapshot_RendersAllThreeViews(t *testing.T) {
	got := CockpitSnapshot()
	if strings.TrimSpace(got) == "" {
		t.Fatal("CockpitSnapshot rendered nothing")
	}
	for _, want := range []string{"VIEW 1/3", "VIEW 2/3", "VIEW 3/3"} {
		if !strings.Contains(got, want) {
			t.Errorf("the snapshot is missing %q", want)
		}
	}
}

// baseline is the "route everything to the top tier" reference the savings
// percentage is measured against. It must be the priciest *available* head, and
// must never be a hardcoded vendor.
func TestCockpitBaseline_IsThePriciestLiveHead(t *testing.T) {
	m := NewCockpit()
	m.heads = []ckHead{
		{name: "anthropic · claude-opus", price: 15, up: true},
		{name: "openai · gpt-4o", price: 5, up: true},
		{name: "google · gemini-ultra", price: 99, up: false}, // down: not available
	}

	price, name := m.baseline()
	if price != 15 {
		t.Errorf("baseline price = %v, want the priciest *live* head (15)", price)
	}
	if name != "claude-opus" {
		t.Errorf("baseline name = %q, want the model part only", name)
	}

	// With nothing up there is no reference; the label must still be neutral
	// rather than naming a vendor that was never discovered.
	m.heads = []ckHead{{name: "x", price: 9, up: false}}
	if price, name := m.baseline(); price != 0 || name == "" {
		t.Errorf("baseline with no live heads = (%v, %q), want (0, a neutral label)",
			price, name)
	}
}

func TestCkBaseName_TrimsTheProviderPrefix(t *testing.T) {
	tests := map[string]string{
		"anthropic · claude-sonnet-4.5": "claude-sonnet-4.5",
		"claude-sonnet-4.5":             "claude-sonnet-4.5",
		"a · b · c":                     "c",
		"":                              "",
	}
	for in, want := range tests {
		if got := ckBaseName(in); got != want {
			t.Errorf("ckBaseName(%q) = %q, want %q", in, got, want)
		}
	}
}

// ── syntax highlighting ───────────────────────────────────────────────────────

// ckHighlight styles one line of code. Colour is not testable here, but the
// text must survive: a highlighter that drops or duplicates characters
// corrupts the code the user is reading.
func TestCkHighlight_PreservesEveryCharacter(t *testing.T) {
	lines := []string{
		`func main() { fmt.Println("hello") }`,
		`// a comment with "quotes" and 'apostrophes'`,
		"x := `a raw string`",
		`s := "unterminated`,
		`if err != nil { return fmt.Errorf("x: %w", err) }`,
		`interface Foo { bar: string }`,
		``,
		`   `,
		`日本語のコメント // with a trailing comment`,
	}
	for _, line := range lines {
		got := stripANSI(ckHighlight(line))
		if got != line {
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

// ckSnippet must return a language tag and at least one line for every enum,
// including one it has never seen — the code panel renders whatever it gets.
func TestCkSnippet_EveryEnumYieldsRenderableCode(t *testing.T) {
	for _, enum := range []string{"CORE", "COMPLEX", "STANDARD", "SIMPLE", "GRUNT", "", "NOT_AN_ENUM"} {
		lang, lines := ckSnippet(enum)
		if lang == "" {
			t.Errorf("ckSnippet(%q) returned no language tag", enum)
		}
		if len(lines) == 0 {
			t.Errorf("ckSnippet(%q) returned no lines; the code panel would be blank", enum)
		}
		for i, l := range lines {
			if strings.Contains(l, "\n") {
				t.Errorf("ckSnippet(%q) line %d contains a newline; the panel reveals "+
					"one line per tick", enum, i)
			}
		}
	}
}

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

// testHeads is a fixed roster so routing assertions do not depend on what
// happens to be installed on the machine running the test.
func testHeads() []ckHead {
	return []ckHead{
		{id: "opus", name: "anthropic · claude-opus", tier: 1, price: 15, up: true},
		{id: "sonnet", name: "anthropic · claude-sonnet", tier: 3, price: 3, up: true},
		{id: "qwen", name: "ollama · qwen2.5-coder", tier: 10, price: 0, up: true},
	}
}

// ckHeadsFrom is what turns a real probe into the cockpit's roster. It must
// rank heads the way dispatch does, mark unroutable ones down, and never
// fabricate a price for a head whose cost is unknown (#189).
func TestCkHeadsFrom_MirrorsRoutingAndDoesNotFabricatePrices(t *testing.T) {
	heads := []provider.Head{
		{ID: "claude", Name: "anthropic · Claude Code", Provider: "anthropic",
			Source: "cli", CapScore: 95, AuthReady: true},
		{ID: "qwen", Name: "ollama · qwen", Provider: "ollama", Source: "port",
			CapScore: 60, LocalOnly: true, AuthReady: true},
		// Discovered but not routable: the ollama binary with no server behind
		// it. probe lists it; nothing can drive it (#248).
		{ID: "ollama-bin", Name: "ollama", Provider: "local", Source: "cli",
			CapScore: 60, LocalOnly: true},
	}

	// nil pricing DB: every price must be 0, which renders as "—" rather than
	// as a figure the user might believe.
	rows := ckHeadsFrom(heads, nil)
	if len(rows) != len(heads) {
		t.Fatalf("got %d rows for %d heads", len(rows), len(heads))
	}
	for _, r := range rows {
		if r.price != 0 {
			t.Errorf("%s priced at %v with no pricing DB loaded", r.id, r.price)
		}
		if r.tier != rank.UITier(headByID(heads, r.id)) {
			t.Errorf("%s shows tier %d but routing ranks it %d", r.id, r.tier,
				rank.UITier(headByID(heads, r.id)))
		}
	}

	byID := map[string]ckHead{}
	for _, r := range rows {
		byID[r.id] = r
	}
	if !byID["claude"].up {
		t.Error("a routable head is shown as down")
	}
	if byID["ollama-bin"].up {
		t.Error("a head nothing can execute is shown as up; the user looks at " +
			"probe, sees it listed, and learns nothing (#248)")
	}
	// A local head belongs at the cheapest tier regardless of its score.
	if byID["qwen"].tier != 10 {
		t.Errorf("the local head is at tier %d, want 10", byID["qwen"].tier)
	}

	if got := ckHeadsFrom(nil, nil); len(got) != 0 {
		t.Errorf("ckHeadsFrom(nil) = %v", got)
	}
}

func headByID(heads []provider.Head, id string) provider.Head {
	for _, h := range heads {
		if h.ID == id {
			return h
		}
	}
	return provider.Head{}
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
		t.Errorf("the ramp collapsed to %d colours; cheap, mid and expensive "+
			"heads would look the same", len(seen))
	}
}

// The header names the heads actually discovered. It replaced a hardcoded list
// that named heads the machine may not have (#189), so an empty roster must say
// "no heads" rather than an empty label.
func TestHeadSummary_NamesWhatWasDiscovered(t *testing.T) {
	m := NewCockpit()
	m.heads = nil
	if got := m.headSummary(); got != "no heads" {
		t.Errorf("headSummary with an empty roster = %q, want \"no heads\"", got)
	}

	m.heads = testHeads()
	got := m.headSummary()
	if !strings.Contains(got, "claude-opus") || !strings.Contains(got, "qwen2.5-co") {
		t.Errorf("headSummary = %q, does not name the discovered heads", got)
	}
	if strings.Contains(got, "anthropic ·") {
		t.Errorf("headSummary = %q, want the model names only", got)
	}

	// A long roster must be truncated, not wrapped — the header is one line.
	var many []ckHead
	for i := 0; i < 20; i++ {
		many = append(many, ckHead{name: "vendor · a-fairly-long-model-name", up: true})
	}
	m.heads = many
	if n := len([]rune(m.headSummary())); n > 46 {
		t.Errorf("headSummary is %d runes; the header bar would wrap", n)
	}
}

// The code panel renders whether or not anything has streamed yet, and must
// survive a column budget far too small to lay out.
func TestCodePanel_RendersAtEverySize(t *testing.T) {
	m := NewCockpit()
	if got := m.codePanel(40, 20); !strings.Contains(got, "awaiting dispatch") {
		t.Errorf("the empty state does not tell the user what to do:\n%s", got)
	}

	m.heads = testHeads()
	after, _ := enter(typed(m, "write a handler"))
	after.codeShown = len(after.codeLines)

	for _, w := range []int{0, 1, 5, 40, 200} {
		got := after.codePanel(w, 20)
		if strings.TrimSpace(got) == "" {
			t.Errorf("codePanel(%d) rendered nothing", w)
		}
	}
	// codeShown beyond the line count must clamp rather than index out of range.
	after.codeShown = len(after.codeLines) + 50
	if got := after.codePanel(60, 20); strings.TrimSpace(got) == "" {
		t.Error("codePanel rendered nothing with codeShown past the end")
	}
}

// Every lifecycle state must have a distinct enough style that a failed agent
// does not read as a returned one.
func TestCkStateStyle_CoversEveryState(t *testing.T) {
	for _, state := range []string{"returned", "running", "await", "failed", "pending", "", "unknown"} {
		if got := ckStateStyle(state).Render("x"); got == "" {
			t.Errorf("state %q renders nothing", state)
		}
	}
	// Compared on the style, not the rendered string: lipgloss strips colour
	// when stdout is not a terminal, so both would render as a bare "x" under
	// `go test` and the assertion would pass on identical styles.
	if ckStateStyle("failed").GetForeground() == ckStateStyle("returned").GetForeground() {
		t.Error("a failed agent is coloured identically to a returned one")
	}
	if ckStateStyle("running").GetForeground() == ckStateStyle("pending").GetForeground() {
		t.Error("a running agent is coloured identically to a pending one")
	}
}
