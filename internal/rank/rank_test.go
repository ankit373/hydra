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
