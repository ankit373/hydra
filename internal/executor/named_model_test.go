// SPDX-License-Identifier: MIT

package executor

import (
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/testutil"
)

func namedHead(model string) provider.Head {
	h := provider.Head{ID: "openrouter/" + model, Source: "env", Provider: "openrouter"}
	if model != "" {
		h.Meta = map[string]string{"model": model}
	}
	return h
}

// One provider emitting several heads that differ only by model is what makes
// routing *within* OpenRouter possible (#752). Without this the head's model
// came from OPENROUTER_MODEL and every such head dispatched to the same model.
func TestOpenAICompatConfig_UsesTheModelTheProviderNamed(t *testing.T) {
	s := testutil.NewSandbox(t)
	s.SetKey(t, "OPENROUTER_API_KEY", "sk-test")

	cfg, err := openAICompatConfigFor(namedHead("google/gemini-2.5-pro"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "google/gemini-2.5-pro" {
		t.Errorf("Model = %q, want the head's own model", cfg.Model)
	}
	if !strings.Contains(cfg.BaseURL, "openrouter.ai") {
		t.Errorf("BaseURL = %q, want OpenRouter's", cfg.BaseURL)
	}
	if cfg.Headers["Authorization"] != "Bearer sk-test" {
		t.Errorf("Authorization = %q; a named model still needs the account's key", cfg.Headers["Authorization"])
	}
}

// Two named heads must not collapse onto one model, which is the whole point.
func TestOpenAICompatConfig_NamedModelsStayDistinct(t *testing.T) {
	s := testutil.NewSandbox(t)
	s.SetKey(t, "OPENROUTER_API_KEY", "sk-test")
	// Set even so, since it is what the single head used to obey.
	t.Setenv("OPENROUTER_MODEL", "anthropic/claude-sonnet-4-5")

	first, err := openAICompatConfigFor(namedHead("google/gemini-2.5-pro"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := openAICompatConfigFor(namedHead("meta-llama/llama-4-scout"))
	if err != nil {
		t.Fatal(err)
	}
	if first.Model == second.Model {
		t.Fatalf("both heads resolved to %q, so OPENROUTER_MODEL still decides", first.Model)
	}
	if first.Model != "google/gemini-2.5-pro" || second.Model != "meta-llama/llama-4-scout" {
		t.Errorf("got %q and %q, want each head's own model", first.Model, second.Model)
	}
}

// A head that names no model is every other env head, and must keep resolving
// through the provider default and its env override.
func TestModelFor_FallsBackToTheProviderDefault(t *testing.T) {
	testutil.NewSandbox(t)

	if got, want := modelFor(namedHead("")), defaultModelFor("openrouter"); got != want {
		t.Errorf("modelFor with no named model = %q, want the provider default %q", got, want)
	}
	t.Setenv("OPENROUTER_MODEL", "meta-llama/llama-4-scout")
	if got := modelFor(namedHead("")); got != "meta-llama/llama-4-scout" {
		t.Errorf("modelFor = %q, want OPENROUTER_MODEL to still win for an unnamed head", got)
	}
	// A named model outranks the env pin: the config named this head, the pin
	// is the fallback for the one head that has no name of its own.
	if got := modelFor(namedHead("google/gemini-2.5-pro")); got != "google/gemini-2.5-pro" {
		t.Errorf("modelFor = %q, want the named model to beat OPENROUTER_MODEL", got)
	}
}

// A provider that knows why its head cannot be driven says so itself, rather
// than this package guessing, or the head silently reading as routable.
func TestUnroutable_HonoursAProviderSuppliedReason(t *testing.T) {
	s := testutil.NewSandbox(t)
	s.SetKey(t, "OPENROUTER_API_KEY", "sk-test")

	h := namedHead("google/gemini-2.5-pro")
	if why := Unroutable(h); why != "" {
		t.Fatalf("a named model with a key set is unroutable: %q", why)
	}

	const reason = "not in the OpenRouter catalogue, check the spelling"
	h.Meta["unroutable_reason"] = reason
	if why := Unroutable(h); why != reason {
		t.Errorf("Unroutable = %q, want the provider's own reason %q", why, reason)
	}
	if Supports(h) {
		t.Error("Supports = true for a head its own provider refused")
	}
}
