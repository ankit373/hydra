// SPDX-License-Identifier: MIT

package registry

import (
	"sync"

	"gopkg.in/yaml.v3"
)

// A token pool is a property of the model, declared in models.yaml. It used to
// reach a cost row only as head metadata, and exactly one provider (agy) set
// that metadata, so every head discovered by the port, env or CLI providers
// wrote an empty pool and its pool card read 0 calls forever. On a real machine
// 72 of 80 rows were affected (#681).
//
// Reading it from the registry instead makes it right for every provider,
// rather than each one having to remember to attach it.

type poolModel struct {
	ID        string `yaml:"id"`
	Provider  string `yaml:"provider"`
	ModelFlag string `yaml:"model_flag"`
	TokenPool string `yaml:"token_pool"`
}

type poolFile struct {
	Models []poolModel `yaml:"models"`
}

var poolOnce struct {
	sync.Once
	byID map[string]string
}

// TokenPoolFor resolves a discovered head's token pool. headID is matched two
// ways, both exact: the registry model's own id, and provider/model_flag, which
// is the shape a port-discovered head carries (ollama/Qwen2.5-Coder:7b for
// model_flag "Qwen2.5-Coder:7b"). No fuzzy matching: a wrong pool would file
// spend against someone else's quota, which is worse than none.
//
// Returns "" when nothing matches, which callers must treat as "not declared"
// rather than substituting a default.
func TokenPoolFor(home, headID string) string {
	if headID == "" {
		return ""
	}
	poolOnce.Do(func() { poolOnce.byID = loadPools(home) })
	return poolOnce.byID[headID]
}

func loadPools(home string) map[string]string {
	out := map[string]string{}
	raw, err := Read(home, "models.yaml")
	if err != nil {
		return out
	}
	var f poolFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return out
	}
	for _, m := range f.Models {
		if m.TokenPool == "" {
			continue
		}
		if m.ID != "" {
			out[m.ID] = m.TokenPool
		}
		if m.Provider != "" && m.ModelFlag != "" {
			out[m.Provider+"/"+m.ModelFlag] = m.TokenPool
		}
	}
	return out
}
