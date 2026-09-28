// SPDX-License-Identifier: MIT

// Package budget tracks per-model context window utilisation and enforces
// the global 70%/75%/80% budget rules defined in CLAUDE.md.
package budget

import (
	"gopkg.in/yaml.v3"

	"github.com/ankit373/hydra/registry"
)

const (
	fallbackCloud = 200_000
	// localDefaultCtx is what a local server allocates when nothing asks for
	// more, measured as 4096 on Ollama 0.33.2 and assumed for any other local
	// server (LM Studio reports no ceiling either) since a smaller assumption
	// is the safe direction for a governor. Hydra never asks: the executor
	// sends only model, prompt and stream, and OLLAMA_CONTEXT_LENGTH lives in
	// the server's environment rather than ours. The 32768 that used to sit
	// here was unreachable, and real lookups fell through to fallbackCloud, so
	// a 4096-token head was budgeted at 200000 and the governor could never
	// fire for one (#764).
	localDefaultCtx = 4_096
)

type modelEntry struct {
	ID            string `yaml:"id"`
	Name          string `yaml:"name"`
	Provider      string `yaml:"provider"`
	ModelFlag     string `yaml:"model_flag"`
	ContextWindow int    `yaml:"context_window"`
}

type modelsFile struct {
	Models []modelEntry `yaml:"models"`
}

// parseModels reads models.yaml, preferring an on-disk copy under home over the
// embedded one. An unreadable or malformed file yields no entries rather than
// an error: a broken registry must not stop a dispatch.
func parseModels(home string) []modelEntry {
	raw, err := registry.Read(home, "models.yaml")
	if err != nil {
		return nil
	}
	var mf modelsFile
	if err := yaml.Unmarshal(raw, &mf); err != nil {
		return nil
	}
	return mf.Models
}

// windowOf is an entry's declared window, or its provider's default when it
// declares none.
func windowOf(m modelEntry) int {
	if m.ContextWindow > 0 {
		return m.ContextWindow
	}
	if m.Provider == "ollama" {
		return localDefaultCtx
	}
	return fallbackCloud
}

// windowFor returns the context window for a model ID, with fallback.
func windowFor(windows map[string]int, modelID string) int {
	if w, ok := windows[modelID]; ok {
		return w
	}
	return fallbackCloud
}

// loadDeclarations reads models.yaml into the three indexes a head can name an
// entry by. First entry wins on a repeated key, so a duplicated name resolves
// in file order rather than by map iteration.
func loadDeclarations(home string) declarations {
	d := declarations{
		byID:   map[string]int{},
		byFlag: map[string]int{},
		byName: map[string]int{},
	}
	put := func(m map[string]int, key string, w int) {
		if key == "" {
			return
		}
		if _, seen := m[key]; !seen {
			m[key] = w
		}
	}
	for _, m := range parseModels(home) {
		w := windowOf(m)
		put(d.byID, m.ID, w)
		put(d.byName, m.Name, w)
		// How a port-discovered head is identified, and how internal/cost
		// already resolves one: provider/model_flag.
		if m.Provider != "" && m.ModelFlag != "" {
			put(d.byFlag, m.Provider+"/"+m.ModelFlag, w)
		}
	}
	return d
}
