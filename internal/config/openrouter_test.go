// SPDX-License-Identifier: MIT

// External test package: internal/testutil imports internal/config, so an
// in-package test importing testutil is an import cycle.
package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/testutil"
)

func TestOpenRouterNormalized_DropsBlanksAndDuplicates(t *testing.T) {
	got := config.OpenRouter{Models: []string{
		"anthropic/claude-sonnet-4.5",
		"  google/gemini-2.5-pro  ",
		"",
		"   ",
		"ANTHROPIC/CLAUDE-SONNET-4.5", // the same model, differently typed
	}}.Normalized()

	want := []string{"anthropic/claude-sonnet-4.5", "google/gemini-2.5-pro"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// The string is sent as the API's model id, so lowercasing it would be
// inventing a normalization OpenRouter never promised.
func TestOpenRouterNormalized_KeepsTheUsersSpelling(t *testing.T) {
	got := config.OpenRouter{Models: []string{"Vendor/Model-V2"}}.Normalized()
	if len(got) != 1 || got[0] != "Vendor/Model-V2" {
		t.Errorf("got %v, want the spelling as written", got)
	}
}

func TestOpenRouterNormalized_EmptyIsEmpty(t *testing.T) {
	if got := (config.OpenRouter{}).Normalized(); len(got) != 0 {
		t.Errorf("got %v from no models", got)
	}
}

func TestOpenRouterModels_ReadsTheConfiguredList(t *testing.T) {
	testutil.NewSandbox(t)
	if err := config.Save(&config.Config{OpenRouter: config.OpenRouter{Models: []string{"google/gemini-2.5-pro"}}}); err != nil {
		t.Fatal(err)
	}

	got := config.OpenRouterModels()
	if len(got) != 1 || got[0] != "google/gemini-2.5-pro" {
		t.Fatalf("got %v, want the saved list", got)
	}
	// The whole point of a round trip: the wizard and the provider must agree.
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.OpenRouter.Models) != 1 {
		t.Errorf("the [openrouter] section did not survive Save/Load: %+v", cfg.OpenRouter)
	}
}

// Discovery runs on machines that never wrote a config. Neither absence nor
// corruption may stop it: routing as before beats discovering nothing.
func TestOpenRouterModels_MissingOrBrokenConfigIsAnEmptyList(t *testing.T) {
	s := testutil.NewSandbox(t)

	if got := config.OpenRouterModels(); len(got) != 0 {
		t.Errorf("got %v with no config file at all", got)
	}
	if err := os.WriteFile(filepath.Join(s.HydraHome, "config.toml"), []byte("not [[[ toml"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := config.OpenRouterModels(); len(got) != 0 {
		t.Errorf("got %v from an unparseable config", got)
	}
	// And the reason it is empty is still reportable to anyone who asks.
	if _, err := config.Load(); err == nil {
		t.Error("Load() succeeded on an unparseable config, so nothing can report it")
	}
}

// A config written before [openrouter] existed must read as no allowlist, not
// as an error, or an upgrade silently stops discovering OpenRouter.
func TestOpenRouterModels_PreexistingConfigHasNoAllowlist(t *testing.T) {
	s := testutil.NewSandbox(t)
	body := "cortex = \"claude\"\nskills = [\"review\"]\n"
	if err := os.WriteFile(filepath.Join(s.HydraHome, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("a config predating [openrouter] failed to load: %v", err)
	}
	if cfg.Cortex != "claude" {
		t.Errorf("Cortex = %q, want the existing value preserved", cfg.Cortex)
	}
	if got := config.OpenRouterModels(); len(got) != 0 {
		t.Errorf("got %v, want no allowlist", got)
	}
}

// Save must not write an [openrouter] section nobody asked for: a config full
// of empty sections is how users stop being able to read their own file.
func TestSave_OmitsAnEmptyOpenRouterSection(t *testing.T) {
	testutil.NewSandbox(t)
	if err := config.Save(&config.Config{Cortex: "claude"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(config.Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "models") {
		t.Errorf("an empty allowlist was written out:\n%s", raw)
	}
}
