// SPDX-License-Identifier: MIT

package pricing

import (
	"os"
	"path/filepath"

	"github.com/ankit373/hydra/internal/config"
	"gopkg.in/yaml.v3"
)

type fallbackYAML struct {
	Tiers map[int]TierPrice `yaml:"tiers"`
}

// loadFallbackTiers reads tier pricing from registry/pricing.yaml.
// This is always available — it ships with the binary.
func loadFallbackTiers() (map[int]TierPrice, error) {
	path := filepath.Join(config.ScriptHome(), "registry", "pricing.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f fallbackYAML
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, err
	}
	return f.Tiers, nil
}
