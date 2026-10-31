// SPDX-License-Identifier: MIT

package security

import (
	"testing"

	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/testutil"
)

func ollamaHead(id, digest string) provider.Head {
	h := provider.Head{ID: id, Provider: "local", Source: "port", Endpoint: "http://localhost:11434", LocalOnly: true}
	if digest != "" {
		h.Meta = map[string]string{"model_digest": digest}
	}
	return h
}

// Head binaries were fingerprinted and model weights were not, so a swapped
// model silently owned every dispatch that routed to it. Ollama pulls unsigned
// weights from a public registry, which is exactly the rug-pull shape
// internal/mcpregistry already watches for in MCP servers.
func TestFingerprintHeads_DetectsASwappedModel(t *testing.T) {
	testutil.NewSandbox(t)

	first := FingerprintHeads([]provider.Head{ollamaHead("ollama/qwen3", "sha256:aaa")})
	if len(first.Binaries) != 1 {
		t.Fatalf("model was not fingerprinted at all: %+v", first)
	}
	if !first.Binaries[0].New || first.New != 1 {
		t.Errorf("a first sighting must be a baseline, not a finding: %+v", first.Binaries[0])
	}

	// Same digest: a re-probe is not a finding.
	same := FingerprintHeads([]provider.Head{ollamaHead("ollama/qwen3", "sha256:aaa")})
	if same.Changed != 0 || same.New != 0 {
		t.Errorf("an unchanged model was reported as moved: %+v", same)
	}

	// Different weights behind the same name is the whole point.
	swapped := FingerprintHeads([]provider.Head{ollamaHead("ollama/qwen3", "sha256:bbb")})
	if swapped.Changed != 1 {
		t.Fatalf("a swapped model was not detected: %+v", swapped)
	}
	if got := swapped.Binaries[0].Previous; got != "sha256:aaa" {
		t.Errorf("Previous = %q, want the digest it replaced", got)
	}
}

// A head with no binary and no digest has no local artifact at all (an
// API-key provider), and must not be counted as unfingerprintable, which
// would read as a failure to check rather than nothing to check.
func TestFingerprintHeads_IgnoresHeadsWithNoArtifact(t *testing.T) {
	testutil.NewSandbox(t)

	sc := FingerprintHeads([]provider.Head{
		{ID: "openai/gpt-5", Provider: "openai", Source: "env"},
		ollamaHead("ollama/no-digest", ""),
	})
	if len(sc.Binaries) != 0 || sc.New != 0 || sc.Unfingerprintable != 0 {
		t.Errorf("a head with nothing to fingerprint was counted: %+v", sc)
	}
}

// A model digest and a binary path share one store, so the namespacing has to
// hold or one artifact overwrites the other's baseline.
func TestFingerprintHeads_ModelAndBinaryStoresDoNotCollide(t *testing.T) {
	testutil.NewSandbox(t)

	if got := modelKey("ollama/qwen3"); got == "ollama/qwen3" {
		t.Error("model keys are not namespaced away from filesystem paths")
	}
}
