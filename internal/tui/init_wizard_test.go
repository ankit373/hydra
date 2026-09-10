// SPDX-License-Identifier: MIT

package tui

import (
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/probe"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/testutil"
)

// `hyctl init` is the first thing a user runs and the only thing that writes
// their config. Nothing about it was covered: a wizard that walks through
// cleanly and then saves the wrong tiers is indistinguishable from one that
// works, until every dispatch routes somewhere unexpected.

func wizardHeads() *probe.Result {
	return &probe.Result{Heads: []provider.Head{
		{ID: "claude", Name: "Claude Code", Provider: "anthropic", CapScore: 95},
		{ID: "gemini", Name: "Gemini CLI", Provider: "google", CapScore: 82},
		{ID: "cody", Name: "Cody", Provider: "sourcegraph", CapScore: 75},
		{ID: "qwen", Name: "Qwen 7B", Provider: "local", CapScore: 60, LocalOnly: true},
	}}
}

func key(s string) tea.KeyMsg {
	if s == " " {
		return tea.KeyMsg{Type: tea.KeySpace}
	}
	if len(s) == 1 {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	}
	panic("unhandled key " + s)
}

// send pushes a sequence of keys through the model and returns the final state.
func send(m tea.Model, keys ...string) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	for _, k := range keys {
		m, cmd = m.Update(key(k))
	}
	return m, cmd
}

// A full walk through the wizard must write a config that later runs can load.
func TestInitWizard_FullWalkWritesALoadableConfig(t *testing.T) {
	testutil.NewSandbox(t)

	m := tea.Model(NewInitModel(wizardHeads()))
	// Cortex: move down twice, pick "cody".
	m, _ = send(m, "down", "down", "enter")
	// Tiers: confirm.
	m, _ = send(m, "enter")
	// Privacy: cursor 0 is local-only.
	m, _ = send(m, "enter")
	// Payload capture: cursor 0 is "no".
	m, _ = send(m, "enter")
	// Skills: confirm and save.
	m, cmd := send(m, "enter")

	im := m.(InitModel)
	if im.err != nil {
		t.Fatalf("the wizard reported an error: %v", im.err)
	}
	if cmd == nil {
		t.Error("the final step returned no command, so the wizard never quits")
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("the wizard wrote no loadable config: %v", err)
	}
	if cfg.Cortex != "cody" {
		t.Errorf("Cortex = %q, want the head the user selected", cfg.Cortex)
	}
	if len(cfg.Skills) == 0 {
		t.Error("no skills were enabled")
	}
	// Cursor 0 on the privacy step is "yes, keep PII local".
	if cfg.Policies["pii"].Action != "local-only" {
		t.Errorf("pii policy = %q, want local-only, the user chose it and every "+
			"PII dispatch depends on it", cfg.Policies["pii"].Action)
	}
	// The wizard used to write a [[tiers]] head list, and had to keep the
	// Cortex out of it so work did not route back to the router. There is no
	// such list any more: tier 1 is the orchestrator's own tier, named `core`
	// in routing.yaml, so the exclusion is expressed by the enum table (#782).
}

// The done screen must not have a stray whitespace-only line between "ready"
// and "Cortex :", lipgloss pads every line of a multi-line Render to its
// widest line, so a blank line written *inside* the styled block became a row
// of spaces glued onto the next line instead of a real newline (#465).
func TestInitWizard_DoneScreenHasNoStrayWhitespaceLine(t *testing.T) {
	testutil.NewSandbox(t)

	m := tea.Model(NewInitModel(wizardHeads()))
	m, _ = send(m, "enter", "enter", "enter", "enter", "enter")

	im := m.(InitModel)
	if im.err != nil {
		t.Fatalf("the wizard reported an error: %v", im.err)
	}
	for _, line := range strings.Split(im.View(), "\n") {
		if strings.TrimSpace(line) == "" && strings.Trim(line, " ") != line {
			t.Errorf("done screen has a whitespace-only (not empty) line: %q\nfull view:\n%s",
				line, im.View())
		}
	}
}

// Declining local-only must leave no PII policy, rather than writing one that
// says something else.
func TestInitWizard_DecliningLocalOnlyWritesNoPIIPolicy(t *testing.T) {
	testutil.NewSandbox(t)

	m := tea.Model(NewInitModel(wizardHeads()))
	m, _ = send(m, "enter")         // cortex: claude
	m, _ = send(m, "enter")         // tiers
	m, _ = send(m, "down", "enter") // privacy: cursor 1 = no
	m, _ = send(m, "enter")         // capture: cursor 0 = no
	_, _ = send(m, "enter")         // skills → save; the config is the assertion

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, present := cfg.Policies["pii"]; present {
		t.Errorf("a pii policy was written despite the user declining: %v", cfg.Policies)
	}
}

// The cursor must not run off either end of a list, an out-of-range index is
// a panic in confirm(), which indexes m.result.Heads directly.
func TestInitWizard_CursorStaysInRange(t *testing.T) {
	testutil.NewSandbox(t)

	m := tea.Model(NewInitModel(wizardHeads()))
	// Far past the end of a four-head list.
	for i := 0; i < 20; i++ {
		m, _ = send(m, "down")
	}
	if got := m.(InitModel).cursor; got > 3 {
		t.Fatalf("cursor = %d with 4 heads; confirm() would index out of range", got)
	}

	// And back past the start.
	for i := 0; i < 20; i++ {
		m, _ = send(m, "up")
	}
	if got := m.(InitModel).cursor; got != 0 {
		t.Errorf("cursor = %d after running off the top, want 0", got)
	}

	// Selecting at the boundary must not panic.
	m, _ = send(m, "down", "down", "down", "down", "down", "enter")
	if m.(InitModel).cortex == nil {
		t.Error("no cortex was selected at the list boundary")
	}
}

// The privacy step is a two-option list, so its cursor is bounded at 1
// regardless of how many heads were discovered.
func TestInitWizard_PrivacyStepIsATwoOptionList(t *testing.T) {
	testutil.NewSandbox(t)

	m := tea.Model(NewInitModel(wizardHeads()))
	m, _ = send(m, "enter", "enter") // through cortex and tiers
	for i := 0; i < 10; i++ {
		m, _ = send(m, "down")
	}
	if got := m.(InitModel).cursor; got != 1 {
		t.Errorf("privacy cursor = %d, want it bounded at 1", got)
	}
}

// Quitting must not write a partial config, a half-configured Hydra is worse
// than an unconfigured one, because Exists() then reports it as set up.
func TestInitWizard_QuittingWritesNothing(t *testing.T) {
	testutil.NewSandbox(t)

	m := tea.Model(NewInitModel(wizardHeads()))
	m, _ = send(m, "enter") // pick a cortex
	_, cmd := send(m, "q")
	if cmd == nil {
		t.Error("q did not quit")
	}
	if config.Exists() {
		t.Error("a config was written by a wizard the user quit halfway through")
	}

	_, cmd = send(tea.Model(NewInitModel(wizardHeads())), "ctrl+c")
	if cmd == nil {
		t.Error("ctrl+c did not quit")
	}
}

// Every step must render something. A blank screen is a wizard the user cannot
// complete.
func TestInitWizard_EveryStepRenders(t *testing.T) {
	testutil.NewSandbox(t)

	m := tea.Model(NewInitModel(wizardHeads()))
	seen := map[step]string{}
	for i := 0; i < 5; i++ {
		im := m.(InitModel)
		view := im.View()
		if strings.TrimSpace(view) == "" {
			t.Fatalf("step %d rendered nothing", im.step)
		}
		if !strings.Contains(view, "Hydra") {
			t.Errorf("step %d does not identify itself:\n%s", im.step, view)
		}
		seen[im.step] = view
		m, _ = send(m, "enter")
	}
	if len(seen) != 5 {
		t.Errorf("reached %d distinct steps, want all 5", len(seen))
	}
	// The head list must actually name the discovered heads, or the user is
	// choosing blind.
	if !strings.Contains(seen[stepCortex], "Claude Code") {
		t.Errorf("the cortex step does not list the discovered heads:\n%s", seen[stepCortex])
	}
	// Non-key messages must be ignored rather than advancing the wizard.
	before := m.(InitModel).step
	m, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if m.(InitModel).step != before {
		t.Error("a window resize advanced the wizard")
	}
	if NewInitModel(wizardHeads()).Init() != nil {
		t.Error("Init() returned a command; the wizard has nothing to do on start")
	}
}

// The tier screen is what the user reads before confirming, so every head
// discovery found must appear on it against the tier it will actually route
// at. A head shown nowhere is a head the user believes is unavailable.
//
// It used to render buildTiers' CapScore bands, a third routing table that
// disagreed with routing.yaml about what every tier name meant (#782).
func TestInitWizard_TierScreenShowsEveryHeadAgainstItsRoutingTier(t *testing.T) {
	testutil.NewSandbox(t)

	m := tea.Model(NewInitModel(wizardHeads()))
	m, _ = send(m, "enter") // pick the first cortex, land on the tier screen
	view := m.View()

	for _, h := range wizardHeads().Heads {
		if !strings.Contains(view, h.Name) {
			t.Errorf("the tier screen does not show discovered head %q:\n%s", h.Name, view)
		}
	}
	// And it is labelled with the words --tier accepts, not a band name.
	for _, name := range []string{"core", "local"} {
		if !strings.Contains(view, name) {
			t.Errorf("the tier screen does not name --tier %s:\n%s", name, view)
		}
	}
}

func TestDefaultSkills_LocalCortexGetsNoPaidSkills(t *testing.T) {
	cloud := &provider.Head{ID: "claude"}
	local := &provider.Head{ID: "qwen", LocalOnly: true}

	cloudSkills := defaultSkills(cloud)
	localSkills := defaultSkills(local)

	if len(cloudSkills) <= len(localSkills) {
		t.Errorf("a cloud cortex got %v and a local one %v; swarm and cost-stats "+
			"only make sense with a paid head", cloudSkills, localSkills)
	}
	for _, s := range localSkills {
		if s == "swarm" || s == "cost-stats" {
			t.Errorf("a local-only cortex was given %q", s)
		}
	}
	if len(defaultSkills(nil)) == 0 {
		t.Error("defaultSkills(nil) is empty; a wizard that skipped selection gets no skills")
	}
}

func TestClamp(t *testing.T) {
	if got := clamp(5, 0, 3); got != 3 {
		t.Errorf("clamp(5,0,3) = %d, want 3", got)
	}
	if got := clamp(-1, 0, 3); got != 0 {
		t.Errorf("clamp(-1,0,3) = %d, want 0", got)
	}
	if got := clamp(2, 0, 3); got != 2 {
		t.Errorf("clamp(2,0,3) = %d, want 2", got)
	}
	// An inverted range must not return something outside both bounds.
	if got := clamp(5, 3, 0); got != 0 && got != 3 {
		t.Errorf("clamp(5,3,0) = %d, outside both bounds", got)
	}
}

// A save that cannot write must surface on the done screen rather than showing
// a success the user will act on.
func TestInitWizard_SaveFailureIsSurfaced(t *testing.T) {
	testutil.NewSandbox(t)

	// Dir() is a regular file, so the config directory cannot be created. The
	// sandbox pre-creates it as an empty directory, so remove that first.
	if err := os.RemoveAll(config.Dir()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.Dir(), []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}

	m := tea.Model(NewInitModel(wizardHeads()))
	m, _ = send(m, "enter", "enter", "enter", "enter", "enter")

	im := m.(InitModel)
	if im.err == nil {
		t.Fatal("the wizard reported success with an unwritable config directory")
	}
	if !strings.Contains(im.View(), "Setup failed") {
		t.Errorf("the done screen does not show the failure:\n%s", im.View())
	}
}

// Payload capture stores verbatim source and prompts, so a user who presses
// enter through the wizard must not end up with it on. The default is the whole
// safety property here, not a preference.
func TestInitWizard_PayloadCaptureIsOffUnlessChosen(t *testing.T) {
	testutil.NewSandbox(t)

	m := tea.Model(NewInitModel(wizardHeads()))
	_, _ = send(m, "enter", "enter", "enter", "enter", "enter") // straight through

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("the wizard wrote no loadable config: %v", err)
	}
	if cfg.CapturePayloads {
		t.Error("pressing enter through the wizard enabled payload capture; " +
			"storing the user's source must be a choice, never a default")
	}
}

func TestInitWizard_PayloadCaptureIsOnWhenChosen(t *testing.T) {
	testutil.NewSandbox(t)

	m := tea.Model(NewInitModel(wizardHeads()))
	m, _ = send(m, "enter")         // cortex
	m, _ = send(m, "enter")         // tiers
	m, _ = send(m, "enter")         // privacy
	m, _ = send(m, "down", "enter") // capture: cursor 1 = yes
	_, _ = send(m, "enter")         // skills → save

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("the wizard wrote no loadable config: %v", err)
	}
	if !cfg.CapturePayloads {
		t.Error("the user selected capture and it was not saved")
	}
}

// The step has to say what it is asking for. "Store payloads?" means nothing to
// someone who has not read the design doc.
func TestInitWizard_CaptureStepExplainsWhatIsStored(t *testing.T) {
	testutil.NewSandbox(t)

	m := tea.Model(NewInitModel(wizardHeads()))
	m, _ = send(m, "enter", "enter", "enter") // land on the capture step

	view := m.(InitModel).View()
	for _, want := range []string{"prompts and responses", "budget", "redacted", "secret detector"} {
		if !strings.Contains(view, want) {
			t.Errorf("the capture step never mentions %q:\n%s", want, view)
		}
	}
	// The store stopped sampling in #728. A wizard describing the old
	// behaviour is worse than one describing none.
	if strings.Contains(view, "sampled") {
		t.Errorf("the capture step still claims payloads are sampled:\n%s", view)
	}
}
