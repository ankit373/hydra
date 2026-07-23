// Package config defines Hydra's runtime configuration: Cortex, Heads, policies, skills.
package config

import (
	"fmt"
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

// ScriptHome returns the directory that contains registry/ (the YAML config root).
// Resolution order:
//  1. $HYDRA_HOME env var (set by Homebrew formula or install.sh)
//  2. Auto-detect: walk up from binary location looking for registry/routing.yaml
//  3. ~/.hydra (standalone install copies the registry here)
func ScriptHome() string {
	if h := os.Getenv("HYDRA_HOME"); h != "" {
		return filepath.Clean(h)
	}
	// Walk up from executable looking for registry/routing.yaml (dev / repo layout).
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		for i := 0; i < 5; i++ {
			if _, err := os.Stat(filepath.Join(dir, "registry", "routing.yaml")); err == nil {
				return dir
			}
			dir = filepath.Dir(dir)
		}
	}
	return Dir()
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

// Save writes cfg to the config file atomically (temp file + rename).
func Save(cfg *Config) error {
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(Dir(), ".config-*.toml")
	if err != nil {
		return fmt.Errorf("config save: %w", err)
	}
	tmpName := tmp.Name()
	if err := toml.NewEncoder(tmp).Encode(cfg); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, Path())
}
