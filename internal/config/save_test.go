package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A config with something in it for every way Save could lose data: a key the
// struct declares, a section it declares, a `[[tiers]]` block config.go
// specifically leaves undecoded, and an unknown section.
const prior = `cortex = "old-head"
skills = ["code-gen"]
explore_rate = 0.15
embed_model = "nomic-embed-text"
cache_threshold = 0.93
cache_budget_mb = 512

[egress]
  strict = false

[openrouter]
  models = ["anthropic/claude-sonnet-4.5", "google/gemini-2.5-pro"]

[[tiers]]
  name = "fast"

[experimental]
  knob = 7
`

func seed(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HYDRA_HOME", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "config.toml")
}

func TestSaveKeepsWhatTheStructCannotHold(t *testing.T) {
	path := seed(t, prior)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Cortex = "new-head"
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}

	out, _ := os.ReadFile(path)
	// The whole point: these two are undecodable by Config, so encoding the
	// struct over the file is what deleted them.
	for _, want := range []string{"[[tiers]]", `name = "fast"`, "[experimental]", "knob = 7"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("Save dropped %q\n--- file ---\n%s", want, out)
		}
	}
}

func TestSaveKeepsEverySettingItWasNotAskedToChange(t *testing.T) {
	seed(t, prior)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Cortex = "new-head"
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Cortex != "new-head" {
		t.Errorf("cortex = %q, want the value just set", got.Cortex)
	}
	if got.ExploreRate != 0.15 {
		t.Errorf("explore_rate = %v, want 0.15; a lost explore rate makes every later counterfactual unidentifiable", got.ExploreRate)
	}
	if got.EmbedModel != "nomic-embed-text" {
		t.Errorf("embed_model = %q, want it kept", got.EmbedModel)
	}
	if got.CacheThreshold != 0.93 || got.CacheBudgetMB != 512 {
		t.Errorf("cache settings = %v/%v, want 0.93/512", got.CacheThreshold, got.CacheBudgetMB)
	}
	if got.StrictEgress() {
		t.Error("egress.strict came back on; absent means strict, so losing `strict = false` silently reverses the setting")
	}
	if len(got.OpenRouter.Normalized()) != 2 {
		t.Errorf("openrouter models = %v, want both kept", got.OpenRouter.Normalized())
	}
}

// Zero has to survive too, or nothing can ever be turned back off.
func TestSaveCanTurnASettingOff(t *testing.T) {
	seed(t, "cortex = \"c\"\ncache_answers = true\nexplore_rate = 0.15\n")

	cfg, _ := Load()
	cfg.CacheAnswers = false
	cfg.ExploreRate = 0
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}

	got, _ := Load()
	if got.CacheAnswers {
		t.Error("cache_answers came back on")
	}
	if got.ExploreRate != 0 {
		t.Errorf("explore_rate = %v, want 0", got.ExploreRate)
	}
}

// `omitempty` is not honoured for numeric zeros, so a fresh config grew six
// `= 0` lines it never asked for.
func TestSaveWritesNoKeyItWasNotGiven(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HYDRA_HOME", dir)

	if err := Save(&Config{Cortex: "c", Skills: []string{"go"}}); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(filepath.Join(dir, "config.toml"))
	for _, unwanted := range []string{"explore_rate", "payload_keep_rate", "payload_budget_mb", "embed_budget_mb", "cache_threshold", "cache_budget_mb"} {
		if strings.Contains(string(out), unwanted) {
			t.Errorf("wrote %q unasked\n--- file ---\n%s", unwanted, out)
		}
	}
}

// Save renders through a map, and map iteration is unordered. Two saves of one
// config that differ byte for byte would churn the file on every write.
func TestSaveIsDeterministic(t *testing.T) {
	path := seed(t, prior)

	cfg, _ := Load()
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(path)
	for i := 0; i < 8; i++ {
		again, _ := Load()
		if err := Save(again); err != nil {
			t.Fatal(err)
		}
		next, _ := os.ReadFile(path)
		if string(next) != string(first) {
			t.Fatalf("save %d differs\n--- first ---\n%s\n--- then ---\n%s", i, first, next)
		}
	}
}

// Writing over a file that is there and does not parse destroys the only copy
// of what the user meant, which is #1030 in the other direction.
func TestSaveOverAnUnparseableFileIsTheCallersChoice(t *testing.T) {
	path := seed(t, "cortex = [this is not toml\n")

	if _, err := Load(); err == nil {
		t.Fatal("Load should refuse an unparseable config")
	}
	// Save still works: it is also how a first config is written, so it carries
	// nothing rather than failing. The refusal belongs to the caller that knows
	// it is editing (internal/tui's wizard), not to the writer.
	if err := Save(&Config{Cortex: "c"}); err != nil {
		t.Fatalf("Save should still write: %v", err)
	}
	out, _ := os.ReadFile(path)
	if !strings.Contains(string(out), `cortex = "c"`) {
		t.Errorf("want the new config written, got:\n%s", out)
	}
}
