// SPDX-License-Identifier: MIT

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
	Name   string   `toml:"name"`
	Heads  []string `toml:"heads"`
	Policy string   `toml:"policy,omitempty"`
}

// Policy is a named routing rule applied before dispatch.
type Policy struct {
	Action string `toml:"action"` // "local-only", "budget-cap", etc.
}

// Config is the root Hydra configuration.
type Config struct {
	Cortex   string            `toml:"cortex"` // Head ID acting as the brain
	Tiers    []Tier            `toml:"tiers"`  // ordered by capability (high → low)
	Skills   []string          `toml:"skills"` // enabled skill IDs
	Policies map[string]Policy `toml:"policies,omitempty"`
}

// Dir returns the Hydra state directory: $HYDRA_HOME if set, else ~/.hydra.
// Every subsystem that persists Hydra state (cost, trust, ledger, security,
// run logs, config.toml) must resolve its path through this function rather
// than calling os.UserHomeDir() directly, or $HYDRA_HOME silently stops being
// an isolation boundary for it (#442).
func Dir() string {
	if h := os.Getenv("HYDRA_HOME"); h != "" {
		return filepath.Clean(h)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".hydra")
}

// ScriptHome returns the directory searched for an on-disk registry/ override.
// Resolution order:
//  1. $HYDRA_HOME env var
//  2. Auto-detect: walk up from the binary looking for registry/routing.yaml
//     (repo checkout / dev layout)
//  3. ~/.hydra
//
// Step 3 used to be documented as "standalone install copies the registry here".
// Nothing has ever done that — not install.sh, not the tap formula, not the npm
// or pip installers — which is why every installed binary ran with no registry
// at all until #238. It is the embedded copy that makes the files always
// available now; this path only decides where an operator's *override* is read
// from, so a miss here is normal rather than a failure.
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
	if err := os.Rename(tmpName, Path()); err != nil {
		// Windows refuses a rename onto a file another process has open, so two
		// concurrent hyctl invocations reliably leave .config-*.toml litter in
		// ~/.hydra. The rename failing is survivable; leaving debris behind on
		// every collision is not.
		os.Remove(tmpName)
		return fmt.Errorf("config save: %w", err)
	}
	return nil
}
