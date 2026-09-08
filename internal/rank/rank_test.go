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
// ladder put at tier 9, one short of the bottom, so `--enum GRUNT` degraded
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
		ID: "qwen-grunt", Source: "registry", Executable: "/usr/bin/agy", LocalOnly: true, CapScore: 60,
		Meta: map[string]string{"tier": "7"},
	}
	if got := UITier(h); got != 7 {
		t.Errorf("UITier = %d, want 7 from the registry meta", got)
	}
}

// Deduping keyed every non-local head on its provider, "one entry per cloud
// provider". That is right for a provider offering one head, and silently
// wrong for one offering several: a three-model OpenRouter allowlist arrived
// as whichever scored highest, and the other two were gone from probe, status
// and routing alike (#752).
func TestByCapScore_KeepsEveryHeadThatNamesItsOwnModel(t *testing.T) {
	named := func(model string, score int) provider.Head {
		return provider.Head{
			ID: "openrouter/" + model, Provider: "openrouter", Source: "env",
			CapScore: score, Meta: map[string]string{"model": model},
		}
	}
	heads := []provider.Head{
		named("anthropic/claude-opus-4.1", 92),
		named("google/gemini-2.5-flash", 78),
		named("meta-llama/llama-3.2-1b", 55),
		// The single key-derived head names no model, so one per provider is
		// still right for it and for every other API provider.
		{ID: "env/anthropic", Provider: "anthropic", Source: "env", CapScore: 95},
		{ID: "claude", Provider: "anthropic", Source: "cli", CapScore: 95},
	}

	ranked := ByCapScore(heads)

	got := map[string]bool{}
	for _, h := range ranked {
		got[h.ID] = true
	}
	for _, want := range []string{
		"openrouter/anthropic/claude-opus-4.1",
		"openrouter/google/gemini-2.5-flash",
		"openrouter/meta-llama/llama-3.2-1b",
	} {
		if !got[want] {
			t.Errorf("%s was deduped away: %+v", want, ranked)
		}
	}
	// And the two anthropic heads naming no model still collapse to one.
	anthropic := 0
	for _, h := range ranked {
		if h.Provider == "anthropic" {
			anthropic++
		}
	}
	if anthropic != 1 {
		t.Errorf("got %d anthropic heads, want 1: per-provider dedupe was lost for heads that name no model", anthropic)
	}
}

// Tier 10 is the free floor: #248 put local heads there because they cost
// nothing, routing.yaml sends GRUNT there, and pricing.yaml charges $0.00 for
// it. A weak paid head fell through to it too, so it was preferred over a free
// local head and costed as if it were one. Only reachable in practice once one
// provider could offer many models (#752).
func TestUITier_OnlyLocalHeadsReachTheFreeFloor(t *testing.T) {
	paid := provider.Head{ID: "openrouter/meta-llama/llama-3.2-1b", Provider: "openrouter",
		Source: "env", CapScore: 55, Meta: map[string]string{"model": "meta-llama/llama-3.2-1b"}}
	if got := UITier(paid); got == 10 {
		t.Error("a paid head reached tier 10, where GRUNT routes and pricing.yaml charges $0.00")
	} else if got != 9 {
		t.Errorf("UITier = %d, want 9, the cheapest tier a paid head may occupy", got)
	}

	// A local head still does, whatever it scores: that is what makes tier 10
	// the always-available terminal fallback.
	for _, score := range []int{0, 55, 60, 66, 99} {
		local := provider.Head{ID: "ollama/x", Provider: "ollama", Source: "port",
			CapScore: score, LocalOnly: true}
		if got := UITier(local); got != 10 {
			t.Errorf("a local head scoring %d landed at tier %d, want 10", score, got)
		}
	}

	// The scale above the floor is untouched.
	for score, want := range map[int]int{95: 1, 90: 2, 85: 3, 80: 4, 78: 5, 72: 6, 70: 7, 65: 8, 60: 9} {
		h := provider.Head{ID: "env/x", Provider: "x", Source: "env", CapScore: score}
		if got := UITier(h); got != want {
			t.Errorf("UITier(score %d) = %d, want %d", score, got, want)
		}
	}
}
