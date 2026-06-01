// Package config defines Hydra's runtime configuration: Cortex, Heads, policies, skills.
package config

import (
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Tier groups one or more Head IDs under a named capability level.
type Tier struct {
	Name    string   `toml:"name"`
	Heads   []string `toml:"heads"`
	Policy  string   `toml:"policy,omitempty"`
}

// Policy is a named routing rule applied before dispatch.
type Policy struct {
	Action string `toml:"action"` // "local-only", "budget-cap", etc.
}

// Config is the root Hydra configuration.
type Config struct {
	Cortex   string            `toml:"cortex"`            // Head ID acting as the brain
	Tiers    []Tier            `toml:"tiers"`             // ordered by capability (high → low)
	Skills   []string          `toml:"skills"`            // enabled skill IDs
	Policies map[string]Policy `toml:"policies,omitempty"`
}

// Dir returns the Hydra config directory (~/.hydra).
func Dir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".hydra")
}

// Path returns the full path to the config file.
func Path() string { return filepath.Join(Dir(), "config.toml") }

// Exists reports whether a config file already exists.
func Exists() bool {
	_, err := os.Stat(Path())
	return err == nil
}

// Load reads and parses the config file.
func Load() (*Config, error) {
	var cfg Config
	if _, err := toml.DecodeFile(Path(), &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Save writes cfg to the config file, creating the directory if needed.
func Save(cfg *Config) error {
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return err
	}
	f, err := os.Create(Path())
	if err != nil {
		return err
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(cfg)
}
