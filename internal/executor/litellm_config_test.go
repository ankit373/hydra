// SPDX-License-Identifier: MIT

package executor

import (
	"testing"

	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/testutil"
)

func litellmHead() provider.Head {
	return provider.Head{
		ID:       "litellm/local-qwen",
		Source:   "port",
		Provider: "litellm",
		Endpoint: "http://localhost:4000",
		Meta:     map[string]string{"model": "local-qwen"},
	}
}

// A LiteLLM proxy routes on the alias it publishes, which is not derivable from
// the head id, and a proxy with a master key refuses an unauthenticated
// request: measured against 1.80, that is a 500 rather than a 401.
func TestOpenAICompatConfig_LiteLLMSendsTheAliasAndTheKey(t *testing.T) {
	s := testutil.NewSandbox(t)
	s.SetKey(t, "LITELLM_PROXY_API_KEY", "sk-proxy")

	cfg, err := openAICompatConfigFor(litellmHead())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "local-qwen" {
		t.Errorf("Model = %q, want the alias the proxy routes on", cfg.Model)
	}
	if cfg.BaseURL != "http://localhost:4000" {
		t.Errorf("BaseURL = %q, want the address it was discovered at", cfg.BaseURL)
	}
	if got := cfg.Headers["Authorization"]; got != "Bearer sk-proxy" {
		t.Errorf("Authorization = %q, want the proxy key; without it the proxy refuses the call", got)
	}
}

// The local servers that need no key must not acquire a header, and their model
// still comes from the head id.
func TestOpenAICompatConfig_ALocalServerIsUnchanged(t *testing.T) {
	testutil.NewSandbox(t)

	cfg, err := openAICompatConfigFor(provider.Head{
		ID: "ollama/qwen3:0.6b", Source: "port", Provider: "local",
		Endpoint: "http://localhost:11434",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "qwen3:0.6b" {
		t.Errorf("Model = %q, want the id with its prefix trimmed", cfg.Model)
	}
	if len(cfg.Headers) != 0 {
		t.Errorf("Headers = %v, want none: Ollama takes no credential", cfg.Headers)
	}
}

// Least privilege is per provider: a LiteLLM head must not be handed another
// provider's credential, and must be handed its own.
func TestHeadEnv_LiteLLMSeesOnlyItsOwnKey(t *testing.T) {
	s := testutil.NewSandbox(t)
	s.SetKey(t, "LITELLM_PROXY_API_KEY", "sk-proxy")
	s.SetKey(t, "OPENAI_API_KEY", "sk-openai")

	var sawProxy, sawOpenAI bool
	for _, kv := range headEnv(litellmHead()) {
		switch {
		case kv == "LITELLM_PROXY_API_KEY=sk-proxy":
			sawProxy = true
		case kv == "OPENAI_API_KEY=sk-openai":
			sawOpenAI = true
		}
	}
	if !sawProxy {
		t.Error("a LiteLLM head was not given the proxy key it needs")
	}
	if sawOpenAI {
		t.Error("a LiteLLM head was handed OpenAI's credential")
	}
}
