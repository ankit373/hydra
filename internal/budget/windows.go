// SPDX-License-Identifier: MIT

// Package budget tracks per-model context window utilisation and enforces
// the global 70%/75%/80% budget rules defined in CLAUDE.md.
package budget

import (
	"gopkg.in/yaml.v3"

	"github.com/ankit373/hydra/registry"
)

const (
	fallbackCloud  = 200_000
	fallbackOllama = 32_768
)

type modelEntry struct {
	ID            string `yaml:"id"`
	Provider      string `yaml:"provider"`
	ContextWindow int    `yaml:"context_window"`
}

type modelsFile struct {
	Models []modelEntry `yaml:"models"`
}

// LoadWindows reads registry/models.yaml — an on-disk copy under home if one
// exists, otherwise the copy embedded in the binary — and returns a map of
// model-id → context window size. Missing entries get provider-based fallbacks
// (ollama → 32 768, everything else → 200 000).
//
// home is the Hydra home directory, not the registry directory: Read appends
// "registry" itself so every caller resolves the override the same way.
func LoadWindows(home string) map[string]int {
	out := map[string]int{}

	raw, err := registry.Read(home, "models.yaml")
	if err != nil {
		return out
	}
	var mf modelsFile
	if err := yaml.Unmarshal(raw, &mf); err != nil {
		return out
	}
	for _, m := range mf.Models {
		if m.ID == "" {
			continue
		}
		w := m.ContextWindow
		if w <= 0 {
			if m.Provider == "ollama" {
				w = fallbackOllama
			} else {
				w = fallbackCloud
			}
		}
		out[m.ID] = w
	}
	return out
}

// windowFor returns the context window for a model ID, with fallback.
func windowFor(windows map[string]int, modelID string) int {
	if w, ok := windows[modelID]; ok {
		return w
	}
	return fallbackCloud
}
