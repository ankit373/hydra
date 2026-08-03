// SPDX-License-Identifier: MIT

package executor

import (
	"strings"
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

// The PATH-discovered Ollama head is the case that made probe and dispatch
// disagree (#248): probe listed it, dispatch filtered it out, and the error
// told the user to go look at probe. The reason must be actionable — "no
// executor" would leave them no better off than before.
func TestUnroutable_LocalBinaryWithoutAServerSaysWhatToDo(t *testing.T) {
	// Exactly how internal/provider/cli registers it: {"ollama", "local", true}.
	head := provider.Head{ID: "ollama", Name: "Ollama", Provider: "local", Source: "cli", LocalOnly: true}

	why := Unroutable(head)
	if why == "" {
		t.Fatal("Unroutable = \"\" for a PATH-only ollama head; dispatch cannot drive it")
	}
	if !strings.Contains(why, "server") {
		t.Errorf("reason %q does not mention the server the user needs to start", why)
	}
	if Supports(head) {
		t.Error("Supports disagrees with Unroutable — the two must never diverge")
	}
}

// Supports is defined as Unroutable == "", so this holds by construction today.
// It is pinned because the previous shape — two functions each walking the same
// branches — is exactly how a listing surface and a routing surface drift apart.
func TestSupports_AlwaysAgreesWithUnroutable(t *testing.T) {
	heads := []provider.Head{
		{ID: "opus-thinking", Provider: "agy", Source: "registry"},
		{ID: "ollama", Provider: "local", Source: "cli", LocalOnly: true},
		{ID: "claude", Provider: "anthropic", Source: "cli"},
		{ID: "mystery", Provider: "nobody", Source: "cli"},
		{ID: "env/anthropic", Provider: "anthropic", Source: "env"},
	}
	for _, h := range heads {
		if Supports(h) != (Unroutable(h) == "") {
			t.Errorf("%s: Supports=%v but Unroutable=%q", h.ID, Supports(h), Unroutable(h))
		}
	}
}
