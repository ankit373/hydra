// SPDX-License-Identifier: MIT

package executor

import (
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/provider"
)

// clearProviderKeys blanks every API key env var the HTTP executor consults, so
// a key present in the developer's real environment cannot make a test pass
// that would fail in CI (or vice versa).
func clearProviderKeys(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "OPENROUTER_API_KEY", "GEMINI_API_KEY",
		"GOOGLE_API_KEY", "XAI_API_KEY", "GROQ_API_KEY", "TOGETHER_API_KEY",
		"FIREWORKS_API_KEY", "MISTRAL_API_KEY", "DEEPSEEK_API_KEY", "AZURE_OPENAI_API_KEY",
		"AZURE_OPENAI_ENDPOINT", "AZURE_OPENAI_DEPLOYMENT", "PERPLEXITY_API_KEY",
		"COHERE_API_KEY", "REPLICATE_API_TOKEN", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY",
		"OPENROUTER_MODEL", "HYDRA_MODEL_OPENROUTER",
	} {
		t.Setenv(k, "")
	}
}

func openRouterHead() provider.Head {
	return provider.Head{ID: "env/openrouter", Source: "env", Provider: "openrouter"}
}

// Hydra reads OpenRouter's catalogue for every cost estimate it makes, but
// before #200 could not send it a single token.
func TestOpenRouter_SupportsGatesOnKey(t *testing.T) {
	clearProviderKeys(t)
	h := openRouterHead()

	if Supports(h) {
		t.Fatal("Supports(env/openrouter) = true with no key; want false")
	}

	t.Setenv("OPENROUTER_API_KEY", "sk-or-test")
	if !Supports(h) {
		t.Fatal("Supports(env/openrouter) = false with OPENROUTER_API_KEY set; want true")
	}
}

// An env head has no Executable, so routing it to CLIExecutor guarantees a
// failed exec — the #75 regression, re-asserted for the new provider.
func TestOpenRouter_RoutesToHTTPExecutor(t *testing.T) {
	if _, ok := For(openRouterHead()).(*HTTPExecutor); !ok {
		t.Fatalf("For(env/openrouter) = %T, want *HTTPExecutor", For(openRouterHead()))
	}
}

// The base URL must be the one the OpenAI-compat path can append
// "/v1/chat/completions" to and land on a real endpoint.
func TestOpenRouter_ResolvesToChatCompletionsEndpoint(t *testing.T) {
	clearProviderKeys(t)
	t.Setenv("OPENROUTER_API_KEY", "sk-or-test")

	cfg, err := openAICompatConfigFor(openRouterHead())
	if err != nil {
		t.Fatalf("openAICompatConfigFor: %v", err)
	}

	const want = "https://openrouter.ai/api/v1/chat/completions"
	// Mirrors how executeOpenAICompatible builds the URL.
	if got := strings.TrimRight(cfg.BaseURL, "/") + "/v1/chat/completions"; got != want {
		t.Errorf("endpoint = %q, want %q", got, want)
	}
	if got := cfg.Headers["Authorization"]; got != "Bearer sk-or-test" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer sk-or-test")
	}
}

func TestOpenRouter_ModelIsOverridable(t *testing.T) {
	clearProviderKeys(t)

	if got := defaultModelFor("openrouter"); got == "" {
		t.Fatal("defaultModelFor(openrouter) is empty — SupportsHTTP would reject the head")
	}
	// OpenRouter model IDs are vendor-prefixed; a bare name is a wrong default.
	if got := defaultModelFor("openrouter"); !strings.Contains(got, "/") {
		t.Errorf("default model %q is not vendor-prefixed — OpenRouter IDs look like \"anthropic/claude-…\"", got)
	}

	t.Setenv("OPENROUTER_MODEL", "meta-llama/llama-4-scout")
	if got := defaultModelFor("openrouter"); got != "meta-llama/llama-4-scout" {
		t.Errorf("OPENROUTER_MODEL = %q, want it to win", got)
	}

	t.Setenv("OPENROUTER_MODEL", "")
	t.Setenv("HYDRA_MODEL_OPENROUTER", "google/gemini-2.5-pro")
	if got := defaultModelFor("openrouter"); got != "google/gemini-2.5-pro" {
		t.Errorf("HYDRA_MODEL_OPENROUTER = %q, want it to win", got)
	}
}

// Adding a provider means touching five separate tables — env.knownKeys,
// openAICompatConfigFor, defaultModelFor, apiKeyFor, and capabilities. #200
// existed because OpenRouter was in none of them while pricing depended on it.
// This asserts the tables agree, so the next provider added to one but not the
// others fails here instead of silently discovering an unusable head.
func TestEveryOpenAICompatProviderIsFullyWired(t *testing.T) {
	clearProviderKeys(t)

	// Providers served by the generic OpenAI-compatible path. The bespoke ones
	// (anthropic, google, cohere, azure, bedrock, replicate) have their own
	// Execute branches and their own gates in SupportsHTTP.
	compat := []struct{ id, keyEnv string }{
		{"openai", "OPENAI_API_KEY"},
		{"openrouter", "OPENROUTER_API_KEY"},
		{"xai", "XAI_API_KEY"},
		{"groq", "GROQ_API_KEY"},
		{"together", "TOGETHER_API_KEY"},
		{"fireworks", "FIREWORKS_API_KEY"},
		{"mistral", "MISTRAL_API_KEY"},
		{"deepseek", "DEEPSEEK_API_KEY"},
		{"perplexity", "PERPLEXITY_API_KEY"},
	}

	for _, p := range compat {
		t.Run(p.id, func(t *testing.T) {
			if got := defaultModelFor(p.id); got == "" {
				t.Errorf("defaultModelFor(%q) is empty — SupportsHTTP will always reject this provider", p.id)
			}

			h := provider.Head{ID: "env/" + p.id, Source: "env", Provider: p.id}
			if Supports(h) {
				t.Errorf("Supports(%q) = true with no key set; the key gate is missing from apiKeyFor", p.id)
			}

			t.Setenv(p.keyEnv, "test-key")
			defer t.Setenv(p.keyEnv, "")

			if !Supports(h) {
				t.Errorf("Supports(%q) = false with %s set; provider is missing from apiKeyFor or the configs table", p.id, p.keyEnv)
			}
			cfg, err := openAICompatConfigFor(h)
			if err != nil {
				t.Fatalf("openAICompatConfigFor(%q): %v", p.id, err)
			}
			if !strings.HasPrefix(cfg.BaseURL, "https://") {
				t.Errorf("%q base URL %q is not https", p.id, cfg.BaseURL)
			}
			if cfg.Headers["Authorization"] != "Bearer test-key" {
				t.Errorf("%q Authorization = %q, want %q", p.id, cfg.Headers["Authorization"], "Bearer test-key")
			}
		})
	}
}
