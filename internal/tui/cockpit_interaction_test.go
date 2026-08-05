// SPDX-License-Identifier: MIT

package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/rank"
	"github.com/ankit373/hydra/internal/testutil"
	"github.com/ankit373/hydra/internal/tree"
)

// The cockpit's command bar is how a user drives `hyctl tui`. Every command it
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
	if !strings.Contains(joined, "no routable head") {
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

// Tab cycles the views, and the arrow keys only move the tree selection while
// the tree is the view being shown — otherwise a stray arrow in the chat pane
// silently moves a selection the user cannot see.
func TestCockpit_KeyBindings(t *testing.T) {
	m := NewCockpit()
	m.treeRows = make([]tree.Row, 4)

	start := m.view
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if next.(Cockpit).view == start {
		t.Error("tab did not change the view")
	}
	// Tab wraps rather than running off the end.
	c := next.(Cockpit)
	for i := 0; i < 10; i++ {
		n, _ := c.Update(tea.KeyMsg{Type: tea.KeyTab})
		c = n.(Cockpit)
		if c.view < 0 || c.view >= ckViewCount() {
			t.Fatalf("tab left view = %d, outside 0..%d", c.view, ckViewCount()-1)
		}
	}

	// Arrows in the chat view must not move the tree selection.
	chat := NewCockpit()
	chat.view = 0
	chat.treeRows = make([]tree.Row, 4)
	n, _ := chat.Update(tea.KeyMsg{Type: tea.KeyDown})
	if n.(Cockpit).treeSel != 0 {
		t.Error("an arrow key in the chat view moved a selection the user cannot see")
	}

	// In the tree view they do, bounded at both ends.
	treeView := NewCockpit()
	treeView.view = 2
	treeView.treeRows = make([]tree.Row, 3)
	cur := tea.Model(treeView)
	for i := 0; i < 10; i++ {
		cur, _ = cur.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	if got := cur.(Cockpit).treeSel; got != 2 {
		t.Errorf("treeSel = %d after running off the bottom of a 3-row tree, want 2", got)
	}
	for i := 0; i < 10; i++ {
		cur, _ = cur.Update(tea.KeyMsg{Type: tea.KeyUp})
	}
	if got := cur.(Cockpit).treeSel; got != 0 {
		t.Errorf("treeSel = %d after running off the top, want 0", got)
	}

	// Text editing: runes accumulate, space is a rune, backspace removes one,
	// escape clears. Backspace on an empty line must not underflow the slice.
	edit := tea.Model(NewCockpit())
	edit = typedModel(edit, "abc")
	edit, _ = edit.Update(tea.KeyMsg{Type: tea.KeySpace})
	edit = typedModel(edit, "d")
	if got := edit.(Cockpit).input; got != "abc d" {
		t.Errorf("input = %q, want %q", got, "abc d")
	}
	edit, _ = edit.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if got := edit.(Cockpit).input; got != "abc " {
		t.Errorf("input = %q after backspace", got)
	}
	edit, _ = edit.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if got := edit.(Cockpit).input; got != "" {
		t.Errorf("input = %q after escape, want empty", got)
	}
	for i := 0; i < 5; i++ {
		edit, _ = edit.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	}
	if got := edit.(Cockpit).input; got != "" {
		t.Errorf("backspace on an empty line produced %q", got)
	}

	if _, cmd := NewCockpit().Update(tea.KeyMsg{Type: tea.KeyCtrlC}); cmd == nil {
		t.Error("ctrl+c did not quit")
	}
}

func typedModel(m tea.Model, s string) tea.Model {
	for _, r := range s {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return m
}

// The dashboard's numbers are read from the same files `hyctl status` and
// `hyctl cost` read. Absent or corrupt state must render as unknown, never as
// a number the user would act on (#189).
func TestCockpitDashboardReaders_AbsentDataIsUnknownNotZero(t *testing.T) {
	testutil.NewSandbox(t)

	if got := ckClaudePct(); got != 0 {
		t.Errorf("ckClaudePct() = %d with no state.json, want 0 (rendered as unknown)", got)
	}
	if got := ckSpendToday(); got != 0 {
		t.Errorf("ckSpendToday() = %v with no cost log, want 0", got)
	}

	dir := filepath.Join(config.Dir(), "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte("{truncated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ckClaudePct(); got != 0 {
		t.Errorf("ckClaudePct() = %d on corrupt state.json", got)
	}

	// With real data both must report it — a reader that always says 0 is
	// indistinguishable from one that works on an empty machine.
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"claude_pct":73}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ckClaudePct(); got != 73 {
		t.Errorf("ckClaudePct() = %d with 73 on disk", got)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	row := `{"ts":"` + now + `","tier":1,"model":"m","prompt_tokens":10,` +
		`"response_tokens":5,"est_cost_usd":0.25}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "cost.jsonl"), []byte(row), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ckSpendToday(); got != 0.25 {
		t.Errorf("ckSpendToday() = %v with $0.25 spent today", got)
	}
}

// classifyTask picks the enum and tier a prompt routes to. Getting it wrong
// sends architectural work to a 7B local model, or a one-line rename to the
// most expensive head on the machine.
func TestClassifyTask_RoutesByWhatTheWorkActuallyIs(t *testing.T) {
	tests := []struct {
		task     string
		mode     string
		wantEnum string
		wantTier int
	}{
		// --local overrides everything: the point of the flag is that nothing
		// leaves the machine, whatever the task looks like.
		{"design a multi-tenant security model", "local", "LOCAL", 10},
		{"rotate the signing key without breaking live tokens", "", "CORE", 1},
		{"design the migration", "", "CORE", 1},
		{"refactor this for a data race", "", "COMPLEX", 3},
		{"review the concurrency here", "", "COMPLEX", 3},
		{"add pagination to the users endpoint", "", "STANDARD", 6},
		{"write a handler test", "", "STANDARD", 6},
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

	// A long prompt with no keyword is more than trivial, so it must not land
	// at the cheapest tier by default.
	long := strings.Repeat("please do the thing carefully ", 5)
	enum, tier := classifyTask(long, "")
	if tier >= 8 {
		t.Errorf("a %d-character prompt classified as %s/tier %d — length alone "+
			"should lift it above trivial", len(long), enum, tier)
	}

	// Every classification must name a routable tier.
	for _, tt := range tests {
		if _, tier := classifyTask(tt.task, tt.mode); tier < 1 || tier > 10 {
			t.Errorf("classifyTask(%q) produced tier %d, outside 1..10", tt.task, tier)
		}
	}
}

// The cockpit lays out three views against whatever terminal it is given. A
// panel that panics or renders nothing at an awkward width is a blank screen.
func TestCockpit_RendersAtEveryTerminalSize(t *testing.T) {
	testutil.NewSandbox(t)

	base := NewCockpit()
	base.heads = testHeads()
	m, _ := enter(typed(base, "add pagination to the users endpoint"))

	sizes := []struct{ w, h int }{
		{0, 0},     // before the first resize
		{20, 5},    // absurdly small
		{40, 12},   // too narrow to split chat and code
		{80, 24},   // the usual
		{200, 60},  // very wide
		{500, 200}, // wider than anything real
	}
	for _, view := range []int{0, 1, 2} {
		for _, sz := range sizes {
			m.view = view
			m.w, m.h, m.ready = sz.w, sz.h, true
			out := m.View()
			if strings.TrimSpace(out) == "" {
				t.Errorf("view %d rendered nothing at %dx%d", view, sz.w, sz.h)
			}
		}
	}

	// Before the first WindowSizeMsg the model is not ready; it must still
	// render something rather than a blank terminal.
	fresh := NewCockpit()
	if strings.TrimSpace(fresh.View()) == "" {
		t.Error("the cockpit renders nothing before its first resize")
	}
}

// truncate bounds the header and every table label. It must never exceed its
// budget in *display cells*, and must never cut a rune in half — the header
// draws model names, which are not all ASCII.
func TestCockpitTruncate_CountsRunesNotBytes(t *testing.T) {
	inputs := []string{
		"",
		"ab",
		"a-fairly-long-model-name",
		strings.Repeat("x", 200),
		"日本語モデル",                // every rune is 3 bytes
		"qwen2.5-coder:7b-日本語",  // mixed
		strings.Repeat("é", 60), // 2 bytes each
	}
	for _, n := range []int{0, 1, 2, 5, 8, 46} {
		for _, s := range inputs {
			got := truncate(s, n)
			if r := []rune(got); len(r) > n && len([]rune(s)) > n {
				t.Errorf("truncate(%q, %d) returned %d runes", s, n, len(r))
			}
			// A split rune renders as a replacement character in the header.
			for _, r := range got {
				if r == '\uFFFD' {
					t.Errorf("truncate(%q, %d) = %q — it cut a rune in half", s, n, got)
					break
				}
			}
		}
	}
	if got := truncate("short", 46); got != "short" {
		t.Errorf("truncate = %q", got)
	}
	if got := truncate("日本語モデル", 3); got != "日本…" {
		t.Errorf("truncate = %q, want the first two runes plus an ellipsis", got)
	}
}

// pickHead is the cockpit's own routing: the cheapest head at or below the
// wanted strength, falling back down the ladder. It must never answer
// "cheapest" with "most expensive" — the same inversion #165 fixed in the
// router itself.
func TestPickHead_NeverAnswersCheapestWithStrongest(t *testing.T) {
	m := NewCockpit()
	m.heads = []ckHead{
		{id: "opus", name: "opus", tier: 1, up: true},
		{id: "sonnet", name: "sonnet", tier: 3, up: true},
		{id: "qwen", name: "qwen", tier: 10, up: true},
	}

	// Asking for the cheapest tier must land on the local head.
	if got := m.pickHead(10); got < 0 || m.heads[got].id != "qwen" {
		t.Errorf("pickHead(10) = %d (%v), want the local head", got, m.heads)
	}
	// Asking for tier 3 must not escalate to tier 1.
	if got := m.pickHead(3); m.heads[got].tier < 3 {
		t.Errorf("pickHead(3) selected tier %d — stronger than requested",
			m.heads[got].tier)
	}
	// Asking for tier 1 gets tier 1.
	if got := m.pickHead(1); m.heads[got].id != "opus" {
		t.Errorf("pickHead(1) = %s", m.heads[got].id)
	}

	// A head that is down is not selectable, and the search falls to the
	// terminal tier rather than picking it anyway.
	m.heads[2].up = false
	if got := m.pickHead(10); got >= 0 && m.heads[got].id == "qwen" {
		t.Error("pickHead selected a head that is down")
	}

	// With everything down there is nothing to route to. Returning an index
	// anyway would have the cockpit preview a route to a head nothing can
	// drive — the same shape as #248.
	for i := range m.heads {
		m.heads[i].up = false
	}
	if got := m.pickHead(1); got >= 0 {
		t.Errorf("pickHead = %d with every head down; the cockpit would preview a "+
			"route to %q, which nothing can execute", got, m.heads[got].id)
	}

	empty := NewCockpit()
	empty.heads = nil
	if got := empty.pickHead(1); got >= 0 {
		t.Errorf("pickHead on an empty roster = %d, want a negative index so the "+
			"caller's guard fires", got)
	}
}

// A prompt naming a file that is in the graph gets a real blast radius; one
// naming a file that is not gets none, rather than a fabricated figure (#193).
func TestCockpitRun_BlastRadiusOnlyForFilesTheGraphKnows(t *testing.T) {
	testutil.NewSandbox(t)

	base := NewCockpit()
	base.heads = testHeads()

	withFile, _ := enter(typed(base, "rotate the key in internal/nowhere/absent.go"))
	joined := strings.Join(withFile.log, "\n")
	// No graph is loaded in a sandbox, so there is nothing to report — and a
	// literal blast line for any prompt containing a path is exactly what #193
	// removed.
	if strings.Contains(joined, "κ=") {
		t.Errorf("a blast radius was printed for a file no graph knows:\n%s", joined)
	}
	if !strings.Contains(joined, "routing preview only") {
		t.Errorf("the run did not complete:\n%s", joined)
	}
}

// The header carries the governor percentage and the head roster. It must
// render at any width without wrapping past its budget.
func TestCockpitHeader_RendersAtEveryWidth(t *testing.T) {
	testutil.NewSandbox(t)

	m := NewCockpit()
	m.heads = testHeads()
	for _, w := range []int{0, 10, 40, 80, 200} {
		m.w, m.h, m.ready = w, 24, true
		if got := m.header(); strings.TrimSpace(got) == "" {
			t.Errorf("header rendered nothing at width %d", w)
		}
	}
}
