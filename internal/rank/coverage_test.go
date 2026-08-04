// SPDX-License-Identifier: MIT

package rank

import (
	"testing"

	"github.com/ankit373/hydra/internal/provider"
)

// UITier maps a head to the 1–10 ladder used for cost estimation, logging and
// `--tier` pinning. Every threshold is a routing decision, so the whole ladder
// is walked rather than sampled.
func TestUITier_EveryThresholdBoundary(t *testing.T) {
	cases := []struct {
		score int
		want  int
	}{
		{100, 1}, {95, 1}, // ≥95
		{94, 2}, {90, 2}, // ≥90
		{89, 3}, {85, 3}, // ≥85
		{84, 4}, {80, 4}, // ≥80
		{79, 5}, {78, 5}, // ≥78
		{77, 6}, {72, 6}, // ≥72
		{71, 7}, {70, 7}, // ≥70
		{69, 8}, {65, 8}, // ≥65
		{64, 9}, {60, 9}, // ≥60
		{59, 10}, {0, 10}, {-5, 10}, // below the ladder
	}
	for _, tc := range cases {
		h := provider.Head{ID: "h", Provider: "openai", Source: "env", CapScore: tc.score}
		if got := UITier(h); got != tc.want {
			t.Errorf("UITier(CapScore=%d) = %d, want %d", tc.score, got, tc.want)
		}
	}
}

// The ladder must be monotonic: a stronger head can never be assigned a cheaper
// tier than a weaker one, or cost routing inverts.
func TestUITier_IsMonotonicInCapScore(t *testing.T) {
	// Walking the score down, the tier number must never go down with it:
	// a weaker head can only be the same tier or a cheaper (higher-numbered)
	// one. Lower tier number means stronger, so the two run opposite ways —
	// which I had backwards on the first attempt.
	prev := 0
	for score := 100; score >= 0; score-- {
		h := provider.Head{ID: "h", Provider: "openai", Source: "env", CapScore: score}
		got := UITier(h)
		if got < prev {
			t.Errorf("score %d gave tier %d after tier %d at a higher score — a weaker "+
				"head was assigned a stronger tier", score, got, prev)
		}
		if got < 1 || got > 10 {
			t.Errorf("score %d gave tier %d, outside the 1–10 ladder", score, got)
		}
		prev = got
	}
	if prev != 10 {
		t.Errorf("the weakest score lands at tier %d, not the bottom of the ladder", prev)
	}
}

// #248: a local head is tier 10 whatever it scores. Ollama scores exactly 60,
// which the ladder alone puts at tier 9 — one short of the bottom — so GRUNT
// degraded past it to a paid cloud head.
func TestUITier_LocalHeadsAreAlwaysTierTen(t *testing.T) {
	for _, score := range []int{0, 60, 61, 90, 100} {
		h := provider.Head{ID: "ollama/x", Provider: "local", Source: "port",
			CapScore: score, LocalOnly: true}
		if got := UITier(h); got != 10 {
			t.Errorf("a local head scoring %d got tier %d, want 10 — it costs nothing "+
				"to run, so it belongs at the cheapest tier (#248)", score, got)
		}
	}
}

// A registry head's explicit tier wins over the CapScore ladder — that is how
// models.yaml pins an agy tier regardless of its score.
func TestUITier_RegistryMetaTierWins(t *testing.T) {
	h := provider.Head{
		ID: "opus", Provider: "antigravity", Source: "registry",
		CapScore: 10, // would be tier 10 by score alone
		Meta:     map[string]string{"tier": "2"},
	}
	if got := UITier(h); got != 2 {
		t.Errorf("UITier = %d, want the explicit meta tier 2", got)
	}

	// A malformed meta tier falls back to the ladder rather than routing to
	// tier 0, which does not exist.
	bad := h
	bad.Meta = map[string]string{"tier": "not-a-number"}
	if got := UITier(bad); got < 1 || got > 10 {
		t.Errorf("a malformed meta tier gave %d, outside the 1–10 ladder", got)
	}

	// Meta tier is honoured only for registry heads.
	notRegistry := h
	notRegistry.Source = "env"
	if got := UITier(notRegistry); got == 2 {
		t.Error("a non-registry head honoured its meta tier; only registry entries " +
			"carry an authoritative tier")
	}
}

// The ollama CLI binary is suppressed when named port models exist, so a probe
// shows "qwen3:8b" rather than a generic, unroutable "ollama" entry alongside it.
func TestByCapScore_SuppressesTheGenericOllamaCLIWhenPortModelsExist(t *testing.T) {
	heads := []provider.Head{
		{ID: "ollama", Provider: "ollama", Source: "cli", CapScore: 60, LocalOnly: true},
		{ID: "ollama/qwen3:8b", Provider: "ollama", Source: "port", CapScore: 60, LocalOnly: true},
	}
	got := ByCapScore(heads)

	for _, h := range got {
		if h.ID == "ollama" && h.Source == "cli" {
			t.Error("the generic ollama CLI head survived alongside named port models")
		}
	}
	if len(got) != 1 || got[0].ID != "ollama/qwen3:8b" {
		t.Errorf("got %+v, want just the named port model", got)
	}

	// With no port models, the CLI head is the only way to reach ollama and
	// must be kept.
	onlyCLI := ByCapScore([]provider.Head{heads[0]})
	if len(onlyCLI) != 1 {
		t.Errorf("the ollama CLI head was dropped with no port models to replace it: %+v", onlyCLI)
	}
}

// Ties are broken by source weight, so the same model discovered two ways
// resolves deterministically rather than by map order.
func TestByCapScore_TiesBreakBySourceDeterministically(t *testing.T) {
	heads := []provider.Head{
		{ID: "a", Provider: "openai", Source: "env", CapScore: 80},
		{ID: "b", Provider: "openai", Source: "registry", CapScore: 80},
	}
	first := ByCapScore(heads)
	for i := 0; i < 20; i++ {
		again := ByCapScore(heads)
		if len(again) != len(first) || again[0].ID != first[0].ID {
			t.Fatalf("ByCapScore is not deterministic: %+v then %+v", first, again)
		}
	}
}

func TestByCapScore_EmptyInput(t *testing.T) {
	if got := ByCapScore(nil); len(got) != 0 {
		t.Errorf("ByCapScore(nil) = %+v", got)
	}
}
