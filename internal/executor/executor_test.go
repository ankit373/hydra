// SPDX-License-Identifier: MIT

package executor

import (
	"testing"

	"github.com/ankit373/hydra/internal/provider"
)

func TestFor_SelectsExecutorBySource(t *testing.T) {
	cases := []struct {
		name string
		head provider.Head
		want interface{}
	}{
		{"registry → agy", provider.Head{Source: "registry", Provider: "anthropic"}, &AgyExecutor{}},
		{"ollama source → ollama", provider.Head{Source: "ollama", ID: "ollama/qwen2.5"}, &OllamaExecutor{}},
		{"ollama provider → ollama", provider.Head{Provider: "ollama", ID: "qwen2.5"}, &OllamaExecutor{}},
		{"env → http", provider.Head{Source: "env", Provider: "anthropic", ID: "env/anthropic"}, &HTTPExecutor{}},
		{"env openai → http", provider.Head{Source: "env", Provider: "openai", ID: "env/openai"}, &HTTPExecutor{}},
		{"port → http", provider.Head{Source: "port", Endpoint: "http://localhost:1234"}, &HTTPExecutor{}},
		{"explicit endpoint → http", provider.Head{Source: "cli", Endpoint: "http://x"}, &HTTPExecutor{}},
		{"cli → cli", provider.Head{Source: "cli", Provider: "anthropic", Executable: "/usr/bin/claude"}, &CLIExecutor{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := For(tc.head)
			if typeName(got) != typeName(tc.want) {
				t.Fatalf("For(%+v) = %T, want %T", tc.head, got, tc.want)
			}
		})
	}
}

// Regression guard for issue #75: an env-key head must NOT route to CLIExecutor
// (it has no Executable, so a CLI exec would always fail).
func TestFor_EnvHeadNeverRoutesToCLI(t *testing.T) {
	for _, prov := range []string{"anthropic", "openai", "google", "groq", "mistral", "cohere", "bedrock"} {
		h := provider.Head{Source: "env", Provider: prov, ID: "env/" + prov}
		if _, isCLI := For(h).(*CLIExecutor); isCLI {
			t.Fatalf("env head %q routed to CLIExecutor; want HTTPExecutor", h.ID)
		}
	}
}

func TestSupports_EnvHeadsGateOnKey(t *testing.T) {
	// anthropic gates on ANTHROPIC_API_KEY (SupportsHTTP checks apiKeyFor directly).
	head := provider.Head{Source: "env", Provider: "anthropic", ID: "env/anthropic"}

	t.Setenv("ANTHROPIC_API_KEY", "")
	if Supports(head) {
		t.Fatal("Supports(env/anthropic) = true with no key; want false")
	}

	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	if !Supports(head) {
		t.Fatal("Supports(env/anthropic) = false with key set; want true")
	}
}

func TestSupports_NonEnvUnchanged(t *testing.T) {
	if !Supports(provider.Head{Source: "registry", Provider: "anthropic"}) {
		t.Fatal("registry head should be supported")
	}
	if !Supports(provider.Head{Source: "cli", Provider: "anthropic", Executable: "/usr/bin/claude"}) {
		t.Fatal("cli head with known template should be supported")
	}
	if Supports(provider.Head{Source: "cli", Provider: "unknown-tool", ID: "unknown-tool"}) {
		t.Fatal("cli head with no template should not be supported")
	}
}

func typeName(v interface{}) string {
	switch v.(type) {
	case *AgyExecutor:
		return "agy"
	case *OllamaExecutor:
		return "ollama"
	case *HTTPExecutor:
		return "http"
	case *CLIExecutor:
		return "cli"
	default:
		return "unknown"
	}
}
