// SPDX-License-Identifier: MIT

package api

import (
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/cost"
	"github.com/ankit373/hydra/registry"
)

// Model is one routable head as the registry declares it.
//
// Complexity is the band the registry assigns this model, and it is the honest
// answer to "how hard can this one think", Hydra has no thinking-depth dial;
// depth is a property of which model you pick (Opus Thinking vs Sonnet
// Thinking, Pro High vs Pro Low), so the band is what a picker should show.
type Model struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Tier     int    `json:"tier"`
	Provider string `json:"provider"`
	Pool     string `json:"pool"`

	ComplexityMin int `json:"complexityMin"`
	ComplexityMax int `json:"complexityMax"`

	// Qualitative registry labels ("slow", "very_high"), not measurements.
	Speed    string `json:"speed"`
	Accuracy string `json:"accuracy"`

	ContextWindow int `json:"contextWindow"`

	// Enabled false means the registry has it switched off, still listed, so a
	// picker can show it greyed rather than pretending it does not exist.
	Enabled bool `json:"enabled"`
}

// Pool is a shared quota and the models drawing from it.
//
// Members of a shared pool compete for one allocation: opus-thinking and
// sonnet-thinking both draw from agy_claude, so choosing one spends what the
// other will need. That consequence is the whole reason this type exists
// instead of a flat model list (#553).
type Pool struct {
	Name string `json:"name"`
	// Shared false means this pool has one member, or the members do not
	// actually contend, spending here costs nothing elsewhere.
	Shared bool   `json:"shared"`
	Note   string `json:"note,omitempty"`

	Models []Model `json:"models"`

	// ObservedCalls/ObservedCostUSD are what Hydra itself logged against this
	// pool, NOT a provider-reported balance. Hydra cannot see Antigravity's
	// real remaining allocation, so these are named for what they are: our own
	// spend, which is a floor on usage and never a quota reading.
	ObservedCalls   int     `json:"observedCalls"`
	ObservedCostUSD float64 `json:"observedCostUsd"`
	ObservedTokens  int     `json:"observedTokens"`
}

// ModelRegistry is every pool, and anything the registry declared without one.
type ModelRegistry struct {
	// Found is false when models.yaml could not be read or parsed at all, an
	// empty list then means "could not look", not "no models".
	Found bool   `json:"found"`
	Error string `json:"error,omitempty"`

	Pools []Pool `json:"pools"`
}

// registryFile mirrors the parts of registry/models.yaml this needs. Declared
// here rather than shared with internal/provider/agy because that one reads a
// deliberately narrower set of fields for discovery; widening it would make
// discovery depend on presentation concerns.
type registryFile struct {
	TokenPools map[string]struct {
		Members []string `yaml:"members"`
		Shared  bool     `yaml:"shared"`
		Note    string   `yaml:"note"`
	} `yaml:"token_pools"`

	Models []struct {
		Tier          int    `yaml:"tier"`
		ID            string `yaml:"id"`
		Name          string `yaml:"name"`
		Provider      string `yaml:"provider"`
		TokenPool     string `yaml:"token_pool"`
		Enabled       bool   `yaml:"enabled"`
		ContextWindow int    `yaml:"context_window"`
		Speed         string `yaml:"speed"`
		Accuracy      string `yaml:"accuracy"`
		ComplexityMin int    `yaml:"complexity_min"`
		ComplexityMax int    `yaml:"complexity_max"`
	} `yaml:"models"`
}

// GetModels returns the model registry grouped by token pool, with the spend
// Hydra has logged against each pool.
//
// Reads through registry.Read so an operator's $HYDRA_HOME/registry/models.yaml
// wins over the embedded copy, same as every other consumer (#238).
func (a *API) GetModels() ModelRegistry {
	raw, err := registry.Read(config.ScriptHome(), "models.yaml")
	if err != nil {
		return ModelRegistry{Error: fmt.Sprintf("cannot read models.yaml: %v", err)}
	}

	var reg registryFile
	if err := yaml.Unmarshal(raw, &reg); err != nil {
		return ModelRegistry{Error: fmt.Sprintf("malformed models.yaml: %v", err)}
	}

	out := ModelRegistry{Found: true}

	// Spend per pool. A missing or unreadable cost log is not an error here:
	// a machine that has never dispatched still has a registry worth showing.
	spend := map[string]cost.GroupRow{}
	if rows, err := cost.ByPool(); err == nil {
		for _, r := range rows {
			spend[r.Key] = r
		}
	}

	// Pool order follows the registry's own declaration order via the models
	// list, so the strongest tiers lead, map iteration would shuffle it.
	seen := map[string]int{}
	for _, m := range reg.Models {
		model := Model{
			ID:            m.ID,
			Name:          m.Name,
			Tier:          m.Tier,
			Provider:      m.Provider,
			Pool:          m.TokenPool,
			ComplexityMin: m.ComplexityMin,
			ComplexityMax: m.ComplexityMax,
			Speed:         m.Speed,
			Accuracy:      m.Accuracy,
			ContextWindow: m.ContextWindow,
			Enabled:       m.Enabled,
		}

		key := m.TokenPool
		if key == "" {
			// A model with no declared pool draws from nothing shared. Group
			// them together rather than dropping them.
			key = "unpooled"
		}

		idx, ok := seen[key]
		if !ok {
			meta := reg.TokenPools[m.TokenPool]
			s := spend[key]
			out.Pools = append(out.Pools, Pool{
				Name:            key,
				Shared:          meta.Shared,
				Note:            meta.Note,
				ObservedCalls:   s.Calls,
				ObservedCostUSD: s.EstCostUSD,
				ObservedTokens:  s.PromptTokens + s.ResponseTokens,
			})
			idx = len(out.Pools) - 1
			seen[key] = idx
		}
		out.Pools[idx].Models = append(out.Pools[idx].Models, model)
	}

	return out
}
