// Package agy discovers Antigravity-backed model tiers by reading
// registry/models.yaml from the Hydra script home directory.
// Each enabled agy tier becomes an individual Go Head so the dispatcher
// can route to specific models (Opus Thinking, Flash Thinking, Gemini Pro, etc.)
// rather than treating all of Antigravity as one undifferentiated head.
package agy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/provider"
)

func init() {
	provider.Register(&Provider{})
}

// Provider reads registry/models.yaml and emits one Head per enabled agy tier.
type Provider struct{}

func (p *Provider) ID() string { return "agy" }

func (p *Provider) Discover(_ context.Context) ([]provider.Head, error) {
	registryPath := filepath.Join(config.ScriptHome(), "registry", "models.yaml")
	data, err := os.ReadFile(registryPath)
	if err != nil {
		// No registry file — binary running without the full repo. Silently return nothing.
		return nil, nil
	}

	var reg struct {
		Models []struct {
			Tier      int    `yaml:"tier"`
			ID        string `yaml:"id"`
			Name      string `yaml:"name"`
			Executor  string `yaml:"executor"`
			ModelFlag string `yaml:"model_flag"`
			TokenPool string `yaml:"token_pool"`
			Enabled   bool   `yaml:"enabled"`
		} `yaml:"models"`
	}
	if err := yaml.Unmarshal(data, &reg); err != nil {
		return nil, nil // malformed YAML — don't crash, just skip
	}

	var heads []provider.Head
	for _, m := range reg.Models {
		if m.Executor != "agy" || !m.Enabled || m.ModelFlag == "" || m.ModelFlag == "null" {
			continue
		}
		heads = append(heads, provider.Head{
			ID:       m.ID,
			Name:     m.Name,
			Provider: "antigravity",
			Source:   "registry",
			CapScore: tierScore(m.Tier),
			Meta: map[string]string{
				"model_flag": m.ModelFlag,
				"token_pool": m.TokenPool,
				"tier":       fmt.Sprintf("%d", m.Tier),
			},
		})
	}
	return heads, nil
}

// tierScore maps the registry tier number to a Go capability score.
// Tier 2 = Opus Thinking (highest); tier 9 = Flash Low (lowest agy tier).
func tierScore(tier int) int {
	scores := map[int]int{
		2: 92, 3: 88, 4: 82, 5: 80,
		6: 78, 7: 72, 8: 70, 9: 68,
	}
	if s, ok := scores[tier]; ok {
		return s
	}
	return 60
}
