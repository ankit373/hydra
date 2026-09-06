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
		{"registry → agy", provider.Head{Source: "registry", Executable: "/usr/bin/agy", Provider: "anthropic"}, &AgyExecutor{}},
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
	if !Supports(provider.Head{Source: "registry", Executable: "/usr/bin/agy", Provider: "anthropic"}) {
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
// told the user to go look at probe. The reason must be actionable, "no
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
		t.Error("Supports disagrees with Unroutable, the two must never diverge")
	}
}

// An embedding-only model (marked by the port provider, #532) is discovered
// but never routable, and the reason wins over every source-based branch, so
// no discovery path can accidentally re-admit one.
func TestUnroutable_EmbeddingOnlyIsNeverRouted(t *testing.T) {
	head := provider.Head{
		ID: "ollama/nomic-embed-text", Provider: "local", Source: "port",
		Endpoint: "http://localhost:11434", LocalOnly: true, AuthReady: true,
		Meta: map[string]string{"embedding_only": "true"},
	}
	why := Unroutable(head)
	if why == "" {
		t.Fatal("an embedding-only model is routable; every dispatch to it fails (#532)")
	}
	if !strings.Contains(why, "embeddings only") {
		t.Errorf("reason %q does not say the model is embeddings-only", why)
	}
	// The marker must dominate even a shape that would otherwise route.
	head.Source = "registry"
	if Supports(head) {
		t.Error("a registry-shaped embedding-only head slipped past the marker")
	}
}

// Supports is defined as Unroutable == "", so this holds by construction today.
// It is pinned because the previous shape, two functions each walking the same
// branches, is exactly how a listing surface and a routing surface drift apart.
func TestSupports_AlwaysAgreesWithUnroutable(t *testing.T) {
	heads := []provider.Head{
		{ID: "opus-thinking", Provider: "agy", Source: "registry", Executable: "/usr/bin/agy"},
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

// The same invariant over the whole shape space rather than five hand-picked
// heads. #248 was the listing surface and the routing surface disagreeing about
// what could be dispatched to; the fix made Supports derive from Unroutable so
// they structurally cannot. That guarantee is only worth what it is checked
// over, a handful of examples can miss exactly the combination that breaks it.
//
// Two properties, for every combination:
//   - Supports(h) ⟺ Unroutable(h) == ""
//   - an unroutable head's reason is non-empty and actionable, because probe
//     prints it verbatim as the explanation of why the head is skipped
func TestSupportsAndUnroutable_AgreeOverEveryHeadShape(t *testing.T) {
	sources := []string{"registry", "cli", "env", "port", "", "unknown-source"}
	providers := []string{"agy", "anthropic", "openai", "local", "nobody", ""}
	endpoints := []string{"", "http://localhost:11434"}

	checked := 0
	for _, src := range sources {
		for _, prov := range providers {
			for _, ep := range endpoints {
				for _, local := range []bool{false, true} {
					for _, auth := range []bool{false, true} {
						h := provider.Head{
							ID: prov + "/" + src, Provider: prov, Source: src,
							Endpoint: ep, LocalOnly: local, AuthReady: auth,
						}
						why := Unroutable(h)
						if Supports(h) != (why == "") {
							t.Errorf("%+v: Supports=%v but Unroutable=%q", h, Supports(h), why)
						}
						if why != "" && len(strings.TrimSpace(why)) < 10 {
							t.Errorf("%+v: Unroutable reason %q is too terse to act on, "+
								"probe prints it verbatim as the reason the head is skipped", h, why)
						}
						checked++
					}
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no combinations checked")
	}
	t.Logf("agreement held over %d head shapes", checked)
}

// The reported failure (#688): with agy missing, eight registry heads were
// still advertised as healthy and each was dispatched to in turn, failing
// identically. Discovery resolves the binary; without one there is nothing to
// drive the head and it must say so.
func TestUnroutable_RegistryHeadWithoutAResolvedAgy(t *testing.T) {
	why := Unroutable(provider.Head{ID: "opus-thinking", Provider: "antigravity", Source: "registry"})
	if why == "" {
		t.Fatal("a registry head with no agy binary was reported routable")
	}
	if !strings.Contains(why, "agy") {
		t.Errorf("reason = %q, want it to name the missing CLI", why)
	}
}

func TestUnroutable_RegistryHeadWithAResolvedAgy(t *testing.T) {
	h := provider.Head{ID: "opus-thinking", Provider: "antigravity", Source: "registry", Executable: "/usr/bin/agy"}
	if why := Unroutable(h); why != "" {
		t.Errorf("Unroutable = %q, want routable", why)
	}
}
