// SPDX-License-Identifier: MIT

package rank

import (
	"testing"

	"github.com/ankit373/hydra/internal/provider"
)

func TestOllamaCLISuppressedWhenPortModelsExist(t *testing.T) {
	heads := []provider.Head{
		{ID: "claude", Provider: "anthropic", Source: "cli", CapScore: 95},
		{ID: "ollama", Provider: "ollama", Source: "cli", CapScore: 60, LocalOnly: true},
		{ID: "ollama/qwen3:8b", Provider: "ollama", Source: "port", CapScore: 66, LocalOnly: true},
		{ID: "ollama/phi4-mini", Provider: "ollama", Source: "port", CapScore: 64, LocalOnly: true},
	}

	ranked := ByCapScore(heads)

	for _, h := range ranked {
		if h.ID == "ollama" && h.Source == "cli" {
			t.Errorf("generic ollama CLI head should be suppressed when port models exist, but it appeared in results")
		}
	}

	portCount := 0
	for _, h := range ranked {
		if h.Source == "port" {
			portCount++
		}
	}
	if portCount != 2 {
		t.Errorf("expected 2 port heads, got %d", portCount)
	}
}

func TestOllamaCLIKeptWhenNoPortModels(t *testing.T) {
	heads := []provider.Head{
		{ID: "claude", Provider: "anthropic", Source: "cli", CapScore: 95},
		{ID: "ollama", Provider: "ollama", Source: "cli", CapScore: 60, LocalOnly: true},
	}

	ranked := ByCapScore(heads)

	found := false
	for _, h := range ranked {
		if h.ID == "ollama" && h.Source == "cli" {
			found = true
		}
	}
	if !found {
		t.Errorf("ollama CLI head should be kept when no port models exist")
	}
}

// Hydra routes by cost, and a local head costs nothing, so it belongs at the
// cheapest tier however capable it is. Ollama scores exactly 60, which the score
// ladder put at tier 9 — one short of the bottom — so `--enum GRUNT` degraded
// straight past it to a paid cloud head (#248). CLAUDE.md promises tier 10 is
// the always-available terminal fallback; this is what makes that true.
func TestUITier_LocalHeadsAreAlwaysTheCheapestTier(t *testing.T) {
	for _, score := range []int{0, 40, 60, 75, 99} {
		h := provider.Head{ID: "local-head", CapScore: score, LocalOnly: true}
		if got := UITier(h); got != 10 {
			t.Errorf("UITier(local head, score %d) = %d, want 10", score, got)
		}
	}
	// A non-local head at the same score must keep its ladder position, or this
	// would be a blanket downgrade rather than a local-cost rule.
	if got := UITier(provider.Head{ID: "cloud", CapScore: 60}); got == 10 {
		t.Error("a non-local head at score 60 was moved to tier 10")
	}
}

// An explicit registry tier still wins: models.yaml is where an operator states
// intent, and inferring over it would silently ignore their edit.
func TestUITier_ExplicitRegistryTierBeatsTheLocalRule(t *testing.T) {
	h := provider.Head{
		ID: "qwen-grunt", Source: "registry", LocalOnly: true, CapScore: 60,
		Meta: map[string]string{"tier": "7"},
	}
	if got := UITier(h); got != 7 {
		t.Errorf("UITier = %d, want 7 from the registry meta", got)
	}
}
