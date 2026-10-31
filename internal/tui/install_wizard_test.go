// SPDX-License-Identifier: MIT

package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ankit373/hydra/internal/sysinfo"
	"github.com/ankit373/hydra/internal/testutil"
)

// The install wizard runs shell commands on the user's machine. What it offers,
// and which command each option carries, is the whole of its behaviour.

func TestBuildInstallOptions_EveryOptionIsActionable(t *testing.T) {
	specs := &sysinfo.Specs{TotalRAMGB: 32, FreeRAMGB: 20, IsAppleSilicon: true}
	opts := buildInstallOptions(specs)

	if len(opts) == 0 {
		t.Fatal("no install options were offered")
	}
	for _, o := range opts {
		if o.name == "" || o.description == "" {
			t.Errorf("option %+v has nothing to show the user", o)
		}
		// Every option must either run something or tell the user what to do.
		// One that does neither is a dead menu entry.
		if o.installCmd == "" && o.envKey == "" && o.postInstall == "" {
			t.Errorf("option %q does nothing and explains nothing", o.name)
		}
	}

	// The Ollama entry must name the model that actually fits this machine,
	// recommending one that does not fit is how a user ends up swapping.
	var ollama *installOption
	for i := range opts {
		if strings.Contains(opts[i].name, "Ollama") {
			ollama = &opts[i]
		}
	}
	if ollama == nil {
		t.Fatal("no Ollama option was offered")
	}
	if !ollama.local {
		t.Error("the Ollama option is not marked local")
	}
	best := specs.BestOllamaModel()
	if !strings.Contains(ollama.postInstall, best.Model) {
		t.Errorf("post-install %q does not name the recommended model %q",
			ollama.postInstall, best.Model)
	}
	if !strings.Contains(ollama.description, best.DisplayName) {
		t.Errorf("description %q does not name the model that fits", ollama.description)
	}
}

// On a machine too small for any local model the description must say so
// rather than recommending one that will swap.
func TestBuildInstallOptions_TightMemorySaysSo(t *testing.T) {
	tight := &sysinfo.Specs{TotalRAMGB: 4, FreeRAMGB: 1.8}
	if tight.AnyLocalModelFits() {
		t.Skip("a 4GB machine unexpectedly fits a model")
	}

	for _, o := range buildInstallOptions(tight) {
		if strings.Contains(o.name, "Ollama") {
			if !strings.Contains(o.description, "memory") {
				t.Errorf("description = %q, want it to warn about memory", o.description)
			}
			return
		}
	}
	t.Fatal("no Ollama option was offered")
}

// Unknown hardware must not produce a confident recommendation.
func TestBuildInstallOptions_UnknownHardwareDoesNotRecommend(t *testing.T) {
	for _, o := range buildInstallOptions(&sysinfo.Specs{}) {
		if strings.Contains(o.name, "Ollama") {
			if strings.Contains(o.description, "Best for your machine") {
				t.Errorf("a machine whose memory could not be read got a confident "+
					"recommendation: %q", o.description)
			}
			return
		}
	}
}

// The Ollama branch selects the model, so it needs an extra step; every other
// option goes straight to confirm.
func TestInstallWizard_OllamaGetsAModelPicker(t *testing.T) {
	testutil.NewSandbox(t)

	m := tea.Model(newInstallModelFor(&sysinfo.Specs{TotalRAMGB: 32, FreeRAMGB: 20}))
	// Move to the Ollama entry.
	im := m.(InstallModel)
	idx := -1
	for i, o := range im.options {
		if strings.Contains(o.name, "Ollama") {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("no Ollama option")
	}
	for i := 0; i < idx; i++ {
		m, _ = m.Update(key("down"))
	}
	m, _ = m.Update(key("enter"))

	im = m.(InstallModel)
	if im.step != installStepOllama {
		t.Fatalf("step = %v after choosing Ollama, want the model picker", im.step)
	}
	// The cursor must start on a model that actually fits, not on the largest.
	if len(im.models) > 0 && !im.models[im.cursor].Fits && im.specs.AnyLocalModelFits() {
		t.Errorf("the picker opened on %q, which does not fit", im.models[im.cursor].Model)
	}

	// Choosing a model carries it into the post-install instruction.
	m, _ = m.Update(key("enter"))
	im = m.(InstallModel)
	if im.step != installStepConfirm {
		t.Fatalf("step = %v after picking a model, want confirm", im.step)
	}
	if !strings.Contains(im.selected.postInstall, im.chosenModel.Model) {
		t.Errorf("post-install %q does not name the model the user picked (%q)",
			im.selected.postInstall, im.chosenModel.Model)
	}
}

// "I'll do it myself" must run nothing.
func TestInstallWizard_DecliningRunsNothing(t *testing.T) {
	testutil.NewSandbox(t)

	m := tea.Model(newInstallModelFor(&sysinfo.Specs{TotalRAMGB: 32, FreeRAMGB: 20}))
	m, _ = m.Update(key("enter")) // pick the first option → confirm
	if m.(InstallModel).step != installStepConfirm {
		t.Fatalf("step = %v, want confirm", m.(InstallModel).step)
	}

	m, _ = m.Update(key("down")) // cursor 1 = "I'll do it myself"
	m, cmd := m.Update(key("enter"))

	im := m.(InstallModel)
	if !im.done {
		t.Error("declining did not finish the wizard")
	}
	if im.step == installStepRunning {
		t.Error("declining started the install anyway")
	}
	if cmd == nil {
		t.Error("declining did not quit")
	}
}

// An option that only sets an environment variable has nothing to run, so
// confirming it must finish rather than shelling out to an empty command.
func TestInstallWizard_EnvKeyOptionRunsNothing(t *testing.T) {
	testutil.NewSandbox(t)

	im := newInstallModelFor(&sysinfo.Specs{TotalRAMGB: 32, FreeRAMGB: 20})
	idx := -1
	for i, o := range im.options {
		if o.envKey != "" {
			idx = i
		}
	}
	if idx < 0 {
		t.Skip("no env-key option in this build")
	}

	m := tea.Model(im)
	for i := 0; i < idx; i++ {
		m, _ = m.Update(key("down"))
	}
	m, _ = m.Update(key("enter")) // → confirm
	m, cmd := m.Update(key("enter"))

	got := m.(InstallModel)
	if got.step == installStepRunning {
		t.Error("an option with no command to run started an install")
	}
	if !got.done || cmd == nil {
		t.Errorf("the wizard did not finish: done=%v cmd=%v", got.done, cmd)
	}
}

// The cursor is bounded per step, and each step's list has a different length.
// An unbounded cursor is an index panic in confirm().
func TestInstallWizard_CursorIsBoundedPerStep(t *testing.T) {
	testutil.NewSandbox(t)

	m := tea.Model(newInstallModelFor(&sysinfo.Specs{TotalRAMGB: 32, FreeRAMGB: 20}))
	for i := 0; i < 50; i++ {
		m, _ = m.Update(key("down"))
	}
	im := m.(InstallModel)
	if im.cursor > len(im.options)-1 {
		t.Fatalf("cursor = %d with %d options; confirm() would index out of range",
			im.cursor, len(im.options))
	}

	// Selecting at the boundary must not panic.
	m, _ = m.Update(key("enter"))
	if m.(InstallModel).selected == nil {
		t.Error("nothing was selected at the list boundary")
	}

	// The confirm step is a two-option list.
	for i := 0; i < 20; i++ {
		m, _ = m.Update(key("down"))
	}
	if got := m.(InstallModel); got.step == installStepConfirm && got.cursor > 1 {
		t.Errorf("confirm cursor = %d, want it bounded at 1", got.cursor)
	}
}

// A finished install reports its outcome rather than leaving the user on a
// spinner, and a failure is reported as a failure.
func TestInstallWizard_ReportsTheOutcome(t *testing.T) {
	testutil.NewSandbox(t)

	m := tea.Model(newInstallModelFor(&sysinfo.Specs{TotalRAMGB: 32, FreeRAMGB: 20}))
	m, cmd := m.Update(installDoneMsg{})
	im := m.(InstallModel)
	if !im.done || im.err {
		t.Errorf("a successful install reported done=%v err=%v", im.done, im.err)
	}
	if cmd == nil {
		t.Error("a finished install did not quit")
	}
	if v := im.View(); strings.TrimSpace(v) == "" {
		t.Error("the done screen renders nothing")
	}

	m2, _ := tea.Model(newInstallModelFor(&sysinfo.Specs{TotalRAMGB: 32})).
		Update(installDoneMsg{err: errFake{}})
	if !m2.(InstallModel).err {
		t.Error("a failed install was reported as a success")
	}
	if v := m2.(InstallModel).View(); !strings.Contains(strings.ToLower(v), "fail") {
		t.Errorf("the failure is not visible on screen:\n%s", v)
	}
}

// Every step must render something the user can act on.
func TestInstallWizard_EveryStepRenders(t *testing.T) {
	testutil.NewSandbox(t)

	m := tea.Model(newInstallModelFor(&sysinfo.Specs{TotalRAMGB: 32, FreeRAMGB: 20}))
	for i := 0; i < 4; i++ {
		if v := m.(InstallModel).View(); strings.TrimSpace(v) == "" {
			t.Fatalf("step %v rendered nothing", m.(InstallModel).step)
		}
		m, _ = m.Update(key("enter"))
	}

	// Quitting works from any step.
	_, cmd := tea.Model(newInstallModelFor(&sysinfo.Specs{})).Update(key("q"))
	if cmd == nil {
		t.Error("q did not quit the install wizard")
	}
	if newInstallModelFor(&sysinfo.Specs{}).Init() != nil {
		t.Error("Init() returned a command; there is nothing to do on start")
	}
	// A non-key, non-done message must be ignored.
	before := m.(InstallModel).step
	m, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if m.(InstallModel).step != before {
		t.Error("a window resize advanced the install wizard")
	}
}

// newInstallModelFor builds an InstallModel against fixed specs, so the test
// does not depend on the machine it runs on. NewInstallModel() itself calls
// sysinfo.Detect and is covered separately.
func newInstallModelFor(specs *sysinfo.Specs) InstallModel {
	return InstallModel{
		specs:   specs,
		options: buildInstallOptions(specs),
		models:  specs.OllamaRecommendations(),
	}
}

func TestNewInstallModel_ReadsTheRealMachine(t *testing.T) {
	testutil.NewSandbox(t)

	m := NewInstallModel()
	if m.specs == nil {
		t.Fatal("NewInstallModel read no specs")
	}
	if len(m.options) == 0 {
		t.Error("NewInstallModel offered no options")
	}
	if len(m.models) == 0 {
		t.Error("NewInstallModel listed no Ollama models")
	}
}

type errFake struct{}

func (errFake) Error() string { return "install failed" }

// ── splash ────────────────────────────────────────────────────────────────────

// The splash and dashboard are the first thing a user sees. They must render
// without panicking on any input and must contain what they claim to show.
func TestSplashAndDashboard_Render(t *testing.T) {
	logo := Logo()
	if strings.TrimSpace(logo) == "" {
		t.Fatal("Logo() rendered nothing")
	}

	splash := Splash("Claude Code")
	if !strings.Contains(splash, "Claude Code") {
		t.Errorf("Splash does not name the cortex:\n%s", splash)
	}

	dash := Dashboard("Claude Code", Stats{Heads: 7, Tasks: 42, CostUSD: 1.2345, SavePct: 87})
	for _, want := range []string{"Claude Code", "7", "42", "87"} {
		if !strings.Contains(dash, want) {
			t.Errorf("Dashboard is missing %q:\n%s", want, dash)
		}
	}

	// Zero values and an empty cortex name must still render, a fresh install
	// has both.
	if got := Dashboard("", Stats{}); strings.TrimSpace(got) == "" {
		t.Error("Dashboard rendered nothing for a fresh install")
	}
	if got := Splash(""); strings.TrimSpace(got) == "" {
		t.Error("Splash rendered nothing with no cortex")
	}
	// A very long name must not panic the layout.
	if got := Dashboard(strings.Repeat("very-long-model-name-", 20), Stats{Heads: 999999}); got == "" {
		t.Error("Dashboard rendered nothing for an over-long name")
	}
}
