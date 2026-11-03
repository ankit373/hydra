// SPDX-License-Identifier: MIT

package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/config"
)

func settingsHome(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HYDRA_HOME", dir)
	path := filepath.Join(dir, "config.toml")
	if body != "" {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

// Everything the view shows, so a field that stops being read is caught here
// rather than by someone noticing a dead toggle.
const fullConfig = `cortex = "claude"
skills = ["code-gen", "review"]
explore_rate = 0.2
capture_payloads = true
payload_budget_mb = 256
capture_embeddings = true
embed_model = "nomic-embed-text"
embed_budget_mb = 128
cache_answers = true
cache_threshold = 0.93
cache_budget_mb = 512

[egress]
  strict = false

[policies.pii]
  action = "local-only"

[openrouter]
  models = ["anthropic/claude-sonnet-4.5"]
`

func TestGetSettingsReadsEveryFieldItShows(t *testing.T) {
	settingsHome(t, fullConfig)

	s := (&API{}).GetSettings()
	if !s.Readable || !s.Exists {
		t.Fatalf("readable=%v exists=%v, want both true", s.Readable, s.Exists)
	}
	for _, c := range []struct {
		name string
		got  any
		want any
	}{
		{"cortex", s.Cortex, "claude"},
		{"piiLocalOnly", s.PIILocalOnly, true},
		{"strictEgress", s.StrictEgress, false},
		{"capturePayloads", s.CapturePayloads, true},
		{"payloadBudgetMb", s.PayloadBudgetMB, 256},
		{"captureEmbeddings", s.CaptureEmbeddings, true},
		{"embedModel", s.EmbedModel, "nomic-embed-text"},
		{"embedBudgetMb", s.EmbedBudgetMB, 128},
		{"cacheAnswers", s.CacheAnswers, true},
		{"cacheThreshold", s.CacheThreshold, 0.93},
		{"cacheBudgetMb", s.CacheBudgetMB, 512},
		{"exploreRate", s.ExploreRate, 0.2},
	} {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if len(s.Skills) != 2 || len(s.OpenRouterModels) != 1 {
		t.Errorf("skills=%v models=%v, want both carried", s.Skills, s.OpenRouterModels)
	}
}

// A machine that has never run `hyctl init` has every default, and the answer
// is knowable without a file. Reporting plumbing instead is what #1106 was.
func TestGetSettingsOnAMachineWithNoConfig(t *testing.T) {
	settingsHome(t, "")

	s := (&API{}).GetSettings()
	if !s.Readable {
		t.Error("a missing config is not an unreadable one")
	}
	if s.Exists {
		t.Error("exists should say there is no file yet")
	}
	if !s.StrictEgress {
		t.Error("absent means strict; a default that leaks is the wrong way to be wrong")
	}
	if s.CacheAnswers || s.CapturePayloads || s.CaptureEmbeddings {
		t.Error("no config means nothing was opted into")
	}
}

// Rendering an unparseable config as defaults would invite saving over it, and
// the file holds the only record of what the user meant.
func TestGetSettingsOnAnUnparseableConfig(t *testing.T) {
	settingsHome(t, "cortex = \"c\"\ncache_threshold = oops\n")

	s := (&API{}).GetSettings()
	if s.Readable {
		t.Fatal("reported an unparseable config as readable")
	}
	if !strings.Contains(s.Error, "not readable") {
		t.Errorf("error = %q, want it to say the file is unreadable", s.Error)
	}
	if s.CacheAnswers || s.CapturePayloads {
		t.Error("filled fields in from a config it could not read")
	}
}

func TestSaveSettingsRoundTrips(t *testing.T) {
	settingsHome(t, "cortex = \"claude\"\n")
	a := &API{}

	in := a.GetSettings()
	in.CacheAnswers = true
	in.CacheThreshold = 0.9
	in.CacheBudgetMB = 64
	in.CapturePayloads = true
	in.PIILocalOnly = true
	in.StrictEgress = false
	in.ExploreRate = 0.1

	out := a.SaveSettings(in)
	if out.Error != "" {
		t.Fatalf("save reported %q", out.Error)
	}
	// Read back through a fresh load, not from the returned struct, or the
	// test passes on a value that never reached disk.
	again := a.GetSettings()
	if !again.CacheAnswers || again.CacheThreshold != 0.9 || again.CacheBudgetMB != 64 {
		t.Errorf("cache settings did not persist: %+v", again)
	}
	if !again.CapturePayloads || !again.PIILocalOnly || again.StrictEgress || again.ExploreRate != 0.1 {
		t.Errorf("settings did not persist: %+v", again)
	}
}

// The whole reason #1103 had to land first: a settings surface that writes the
// fields it shows must not delete the ones it does not.
func TestSaveSettingsKeepsWhatItDoesNotShow(t *testing.T) {
	path := settingsHome(t, fullConfig+"\n[[tiers]]\n  name = \"fast\"\n")
	a := &API{}

	in := a.GetSettings()
	in.CacheAnswers = false
	if out := a.SaveSettings(in); out.Error != "" {
		t.Fatalf("save reported %q", out.Error)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Cortex != "claude" {
		t.Errorf("cortex = %q; the view does not write it and must not clear it", cfg.Cortex)
	}
	if len(cfg.Skills) != 2 {
		t.Errorf("skills = %v, want both kept", cfg.Skills)
	}
	if len(cfg.OpenRouter.Normalized()) != 1 {
		t.Errorf("openrouter models = %v, want kept", cfg.OpenRouter.Normalized())
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "[[tiers]]") {
		t.Errorf("dropped the undecodable block:\n%s", raw)
	}
}

// Off has to be writable, or every switch is one-way.
func TestSaveSettingsCanTurnEverythingOff(t *testing.T) {
	settingsHome(t, fullConfig)
	a := &API{}

	in := a.GetSettings()
	in.CacheAnswers, in.CapturePayloads, in.CaptureEmbeddings = false, false, false
	in.PIILocalOnly, in.StrictEgress = false, true
	in.ExploreRate, in.CacheThreshold = 0, 0
	if out := a.SaveSettings(in); out.Error != "" {
		t.Fatalf("save reported %q", out.Error)
	}

	got := a.GetSettings()
	if got.CacheAnswers || got.CapturePayloads || got.CaptureEmbeddings || got.PIILocalOnly {
		t.Errorf("a switch would not turn off: %+v", got)
	}
	if !got.StrictEgress || got.ExploreRate != 0 {
		t.Errorf("strict/explore did not take: %+v", got)
	}
}

// A pii policy saying something else was written by hand; turning this switch
// off is not a request to delete it.
func TestSaveSettingsLeavesAPiiPolicyItDidNotWrite(t *testing.T) {
	settingsHome(t, "cortex = \"c\"\n\n[policies.pii]\n  action = \"budget-cap\"\n")
	a := &API{}

	in := a.GetSettings()
	if in.PIILocalOnly {
		t.Fatal("a budget-cap rule is not local-only")
	}
	if out := a.SaveSettings(in); out.Error != "" {
		t.Fatalf("save reported %q", out.Error)
	}

	cfg, _ := config.Load()
	if cfg.Policies["pii"].Action != "budget-cap" {
		t.Errorf("pii policy = %+v, want untouched", cfg.Policies["pii"])
	}
}

func TestSaveSettingsRefusesToOverwriteAnUnparseableConfig(t *testing.T) {
	path := settingsHome(t, "cortex = [not toml\n")

	out := (&API{}).SaveSettings(Settings{CacheAnswers: true})
	if out.Error == "" {
		t.Fatal("want a refusal")
	}
	if !strings.Contains(out.Error, "refusing to overwrite") {
		t.Errorf("error = %q, want it to say it refused", out.Error)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "not toml") {
		t.Errorf("overwrote it anyway:\n%s", raw)
	}
}

// internal/cache ignores a threshold outside (0,1] rather than clamping it, so
// accepting one would show a number the router does not use.
func TestSaveSettingsRefusesValuesTheRouterWouldIgnore(t *testing.T) {
	for _, c := range []struct {
		name string
		in   Settings
		want string
	}{
		{"threshold above one", Settings{CacheThreshold: 1.5}, "cache threshold"},
		{"negative threshold", Settings{CacheThreshold: -0.5}, "cache threshold"},
		{"explore rate above one", Settings{ExploreRate: 2}, "explore rate"},
		{"negative explore rate", Settings{ExploreRate: -1}, "explore rate"},
		{"negative payload budget", Settings{PayloadBudgetMB: -1}, "payload budget"},
		{"negative embed budget", Settings{EmbedBudgetMB: -1}, "embedding budget"},
		{"negative cache budget", Settings{CacheBudgetMB: -1}, "cache budget"},
	} {
		t.Run(c.name, func(t *testing.T) {
			settingsHome(t, "cortex = \"c\"\n")
			out := (&API{}).SaveSettings(c.in)
			if out.Error == "" {
				t.Fatalf("accepted %+v", c.in)
			}
			if !strings.Contains(out.Error, c.want) {
				t.Errorf("error = %q, want it to name %q", out.Error, c.want)
			}
			cfg, _ := config.Load()
			if cfg.CacheThreshold != 0 || cfg.ExploreRate != 0 {
				t.Error("a refused save still wrote")
			}
		})
	}
}

// Zero threshold means "use the default", which is a legitimate value and must
// not be refused along with the out-of-range ones.
func TestSaveSettingsAcceptsZeroAsTheDefault(t *testing.T) {
	settingsHome(t, "cortex = \"c\"\ncache_threshold = 0.9\n")

	a := &API{}
	in := a.GetSettings()
	in.CacheThreshold = 0
	if out := a.SaveSettings(in); out.Error != "" {
		t.Fatalf("refused zero: %q", out.Error)
	}
	if got := a.GetSettings(); got.CacheThreshold != 0 {
		t.Errorf("threshold = %v, want 0", got.CacheThreshold)
	}
}
