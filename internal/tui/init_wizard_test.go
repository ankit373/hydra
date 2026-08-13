// SPDX-License-Identifier: MIT

package tui

import (
	"os"
	"path/filepath"
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
		{ID: "qwen", Name: "Qwen 7B", Provider: "ollama", CapScore: 60, LocalOnly: true},
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
		t.Errorf("pii policy = %q, want local-only — the user chose it and every "+
			"PII dispatch depends on it", cfg.Policies["pii"].Action)
	}
	// The Cortex is the orchestrator; it must not also be listed as a delegate
	// tier, or work routes back to the model that is doing the routing.
	for _, tier := range cfg.Tiers {
		for _, h := range tier.Heads {
			if h == cfg.Cortex {
				t.Errorf("the Cortex %q is also in tier %q", h, tier.Name)
			}
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
	_, _ = send(m, "enter")         // skills → save; the config is the assertion

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, present := cfg.Policies["pii"]; present {
		t.Errorf("a pii policy was written despite the user declining: %v", cfg.Policies)
	}
}

// The cursor must not run off either end of a list — an out-of-range index is
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

// Quitting must not write a partial config — a half-configured Hydra is worse
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

// buildTiers is what decides where every future dispatch goes. Its bands must
// place each head exactly once, and never include the Cortex.
func TestBuildTiers_AssignsEachHeadOnceAndExcludesTheCortex(t *testing.T) {
	heads := wizardHeads().Heads
	cortex := &heads[0] // claude, 95

	tiers := buildTiers(heads, cortex)

	seen := map[string]string{}
	for _, tier := range tiers {
		if len(tier.Heads) == 0 {
			t.Errorf("tier %q is empty and should not have been written", tier.Name)
		}
		for _, id := range tier.Heads {
			if prev, dup := seen[id]; dup {
				t.Errorf("%s appears in both %q and %q", id, prev, tier.Name)
			}
			seen[id] = tier.Name
		}
	}
	if _, present := seen["claude"]; present {
		t.Error("the Cortex was assigned a delegate tier; work would route back to " +
			"the model doing the routing")
	}
	for _, id := range []string{"gemini", "cody", "qwen"} {
		if _, present := seen[id]; !present {
			t.Errorf("%s was discovered but assigned no tier, so it can never be routed to", id)
		}
	}
	// Bands are capability-ordered, so a stronger head must not land in a
	// cheaper tier than a weaker one.
	if seen["gemini"] == seen["qwen"] {
		t.Errorf("an 82-score head and a 60-score head landed in the same tier %q",
			seen["gemini"])
	}

	// With no cortex chosen every head is available.
	if got := buildTiers(heads, nil); len(got) == 0 {
		t.Error("buildTiers with no cortex produced no tiers")
	}
	if got := buildTiers(nil, cortex); len(got) != 0 {
		t.Errorf("buildTiers with no heads produced %v", got)
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

// exportToRoutingYAML is written back into the operator's own registry file. It
// must replace its previous block rather than appending a new one every run,
// and must never touch the hand-written part above it.
func TestExportToRoutingYAML_ReplacesItsOwnBlockAndKeepsTheRest(t *testing.T) {
	s := testutil.NewSandbox(t)

	regDir := filepath.Join(s.HydraHome, "registry")
	if err := os.MkdirAll(regDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(regDir, "routing.yaml")
	handWritten := "version: \"1.0\"\n# an operator's own comment\nenums:\n  SIMPLE: 8\n"
	if err := os.WriteFile(path, []byte(handWritten), 0o600); err != nil {
		t.Fatal(err)
	}

	heads := wizardHeads().Heads
	cortex := &heads[0]
	tiers := buildTiers(heads, cortex)

	if err := exportToRoutingYAML(tiers, cortex); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(first), "an operator's own comment") {
		t.Errorf("the operator's own content was destroyed:\n%s", first)
	}
	if !strings.Contains(string(first), "discovered_heads:") {
		t.Errorf("no discovered block was written:\n%s", first)
	}
	if !strings.Contains(string(first), "cortex: claude") {
		t.Errorf("the block does not name the chosen cortex:\n%s", first)
	}

	// A second run must replace the block, not stack another one.
	if err := exportToRoutingYAML(tiers, cortex); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(second), "discovered_heads:"); n != 1 {
		t.Errorf("routing.yaml holds %d discovered blocks after two runs; every "+
			"`hyctl init` would append another", n)
	}
	if !strings.Contains(string(second), "an operator's own comment") {
		t.Error("the second run destroyed the operator's content")
	}
}

// An install layout with no routing.yaml on disk is the normal case — the
// registry is embedded in the binary. Skipping silently is correct; failing
// would make `hyctl init` error on every installed build.
func TestExportToRoutingYAML_NoFileOnDiskIsNotAnError(t *testing.T) {
	testutil.NewSandbox(t)

	if err := exportToRoutingYAML(nil, nil); err != nil {
		t.Errorf("exportToRoutingYAML errored with no on-disk routing.yaml: %v — "+
			"that is every installed binary (#238)", err)
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
	m, _ = send(m, "enter", "enter", "enter", "enter")

	im := m.(InitModel)
	if im.err == nil {
		t.Fatal("the wizard reported success with an unwritable config directory")
	}
	if !strings.Contains(im.View(), "Setup failed") {
		t.Errorf("the done screen does not show the failure:\n%s", im.View())
	}
}
