// SPDX-License-Identifier: MIT

package pricing

import (
	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/registry"
	"gopkg.in/yaml.v3"
)

type fallbackYAML struct {
	Tiers map[int]TierPrice `yaml:"tiers"`
}

// loadFallbackTiers reads tier pricing from registry/pricing.yaml — an on-disk
// copy if one exists, otherwise the copy embedded in the binary.
//
// This genuinely is always available now. It used to claim that while reading
// only from disk, so every installed binary fell through to $0.00 for every
// tier — which is what prices the CLI-agent heads that never appear in
// OpenRouter's catalog (#238).
func loadFallbackTiers() (map[int]TierPrice, error) {
	raw, err := registry.Read(config.ScriptHome(), "pricing.yaml")
	if err != nil {
		return nil, err
	}
	var f fallbackYAML
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, err
	}
	return f.Tiers, nil
}
