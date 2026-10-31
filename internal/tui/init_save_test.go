package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/provider"
)

const existing = `cortex = "old-head"
skills = ["code-gen"]
explore_rate = 0.15
embed_model = "nomic-embed-text"
cache_budget_mb = 512

[egress]
  strict = false

[openrouter]
  models = ["anthropic/claude-sonnet-4.5", "google/gemini-2.5-pro"]
`

func seedConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HYDRA_HOME", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "config.toml")
}

// The wizard asks five questions and used to write a config containing only
// their answers, so re-running it to change the Cortex discarded everything
// else on the machine (#1103).
func TestWizardKeepsTheSettingsItNeverAsksAbout(t *testing.T) {
	seedConfig(t, existing)

	m := InitModel{cortex: &provider.Head{ID: "new-head"}, skills: []string{"review"}}
	if err := m.save(); err != nil {
		t.Fatal(err)
	}

	got, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Cortex != "new-head" {
		t.Errorf("cortex = %q, want the wizard's answer", got.Cortex)
	}
	if got.ExploreRate != 0.15 {
		t.Errorf("explore_rate = %v, want 0.15 kept", got.ExploreRate)
	}
	if got.EmbedModel != "nomic-embed-text" {
		t.Errorf("embed_model = %q, want it kept", got.EmbedModel)
	}
	if got.CacheBudgetMB != 512 {
		t.Errorf("cache_budget_mb = %d, want 512 kept", got.CacheBudgetMB)
	}
	if got.StrictEgress() {
		t.Error("egress.strict reverted to on; the wizard never asked")
	}
	if len(got.OpenRouter.Normalized()) != 2 {
		t.Errorf("openrouter models = %v, want both kept; this is what makes per-model routing work at all", got.OpenRouter.Normalized())
	}
}

func TestWizardAppliesEveryAnswerItDoesAsk(t *testing.T) {
	seedConfig(t, existing)

	m := InitModel{
		cortex:    &provider.Head{ID: "new-head"},
		skills:    []string{"review"},
		localOnly: true,
		capture:   true,
		embed:     true,
		cache:     true,
	}
	if err := m.save(); err != nil {
		t.Fatal(err)
	}

	got, _ := config.Load()
	if got.Policies["pii"].Action != "local-only" {
		t.Errorf("pii policy = %+v, want local-only", got.Policies["pii"])
	}
	if !got.CapturePayloads || !got.CaptureEmbeddings || !got.CacheAnswers {
		t.Errorf("capture/embed/cache = %v/%v/%v, want all on", got.CapturePayloads, got.CaptureEmbeddings, got.CacheAnswers)
	}
}

// Answering "no" must be able to turn the rule off again, or the overlay is
// write-once.
func TestWizardCanTurnLocalOnlyBackOff(t *testing.T) {
	seedConfig(t, existing+"\n[policies.pii]\n  action = \"local-only\"\n")

	m := InitModel{cortex: &provider.Head{ID: "h"}, skills: []string{"review"}, localOnly: false}
	if err := m.save(); err != nil {
		t.Fatal(err)
	}

	got, _ := config.Load()
	if _, still := got.Policies["pii"]; still {
		t.Error("answering no left the pii rule in place")
	}
}

// A pii policy saying something else was written by hand, and "no" to the
// local-only question is not a request to delete it.
func TestWizardLeavesAPiiPolicyItDidNotWrite(t *testing.T) {
	seedConfig(t, existing+"\n[policies.pii]\n  action = \"budget-cap\"\n")

	m := InitModel{cortex: &provider.Head{ID: "h"}, skills: []string{"review"}, localOnly: false}
	if err := m.save(); err != nil {
		t.Fatal(err)
	}

	got, _ := config.Load()
	if got.Policies["pii"].Action != "budget-cap" {
		t.Errorf("pii policy = %+v, want the hand-written one untouched", got.Policies["pii"])
	}
}

// A config that is there and does not parse is the one file the wizard must not
// write over: it holds the only record of what the user meant, and `hyctl init`
// is precisely what a broken config tells people to run (#1030).
func TestWizardRefusesToOverwriteAnUnparseableConfig(t *testing.T) {
	path := seedConfig(t, "cortex = [this is not toml\n")

	m := InitModel{cortex: &provider.Head{ID: "h"}, skills: []string{"review"}}
	err := m.save()
	if err == nil {
		t.Fatal("want a refusal, got a successful overwrite")
	}
	if !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Errorf("error = %v, want it to say it refused rather than merely failing", err)
	}
	out, _ := os.ReadFile(path)
	if !strings.Contains(string(out), "this is not toml") {
		t.Errorf("the unreadable config was replaced anyway:\n%s", out)
	}
}

// And a machine with no config at all must still get one.
func TestWizardWritesAFirstConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HYDRA_HOME", dir)

	m := InitModel{cortex: &provider.Head{ID: "h"}, skills: []string{"review"}}
	if err := m.save(); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Cortex != "h" {
		t.Errorf("cortex = %q, want h", got.Cortex)
	}
}
