// SPDX-License-Identifier: MIT

// Package config defines Hydra's runtime configuration: Cortex, Heads, policies, skills.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Policy is a named routing rule applied before dispatch.
type Policy struct {
	Action string `toml:"action"` // "local-only", "budget-cap", etc.
}

// Egress governs what may leave the machine once content classifies secret.
type Egress struct {
	// Strict refuses a secret payload when no local head is routable, rather
	// than sending it to one that reaches the network. Absent means on: a
	// config file written before the gate existed must not read as an opt-out.
	Strict *bool `toml:"strict,omitempty"`
}

// OpenRouter admits individual models from OpenRouter's catalogue as routable
// heads. Hydra already prices and scores hundreds of them, but enumerating
// them all would bury `hyctl probe` and `hyctl status`, so admission is the
// user's explicit choice (#752).
type OpenRouter struct {
	// Models is the allowlist of catalogue ids, e.g. "anthropic/claude-sonnet-4-5".
	// Empty means the single key-derived head every install has had, so naming
	// nothing changes nothing.
	Models []string `toml:"models,omitempty"`
}

// Normalized drops blanks and case-insensitive duplicates, keeping the user's
// spelling: the string is what gets sent as the API's model id, and lowercasing
// it would be inventing a normalization the API never promised.
func (o OpenRouter) Normalized() []string {
	seen := make(map[string]bool, len(o.Models))
	out := make([]string, 0, len(o.Models))
	for _, m := range o.Models {
		m = strings.TrimSpace(m)
		key := strings.ToLower(m)
		if m == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, m)
	}
	return out
}

// Config is the root Hydra configuration.
type Config struct {
	// A `[[tiers]]` block written by an older `hyctl init` is ignored rather
	// than rejected: toml leaves unknown keys undecoded. Tier names resolve
	// through registry/routing.yaml now, so a per-install head list cannot
	// disagree with the enum that shares its name (#782).
	Cortex   string            `toml:"cortex"` // Head ID acting as the brain
	Skills   []string          `toml:"skills"` // enabled skill IDs
	Policies map[string]Policy `toml:"policies,omitempty"`
	Egress   Egress            `toml:"egress,omitempty"`

	// ExploreRate is the probability a dispatch tries a head other than the
	// top-ranked one, so the logs carry the counterfactual evidence off-policy
	// evaluation needs. 0 (the default) is pure argmax and changes nothing.
	ExploreRate float64 `toml:"explore_rate,omitempty"`

	// CapturePayloads opts into storing prompt and response text. Off by
	// default and deliberately not inferable from anything else: payloads are
	// verbatim source and prompts, the only trace class with real privacy risk,
	// so capture is a decision someone makes rather than a default they inherit.
	CapturePayloads bool `toml:"capture_payloads,omitempty"`

	// PayloadKeepRate is the probability a payload is admitted when capture is
	// on. A byte budget is what bounds the store now, so the default keeps
	// everything; the rate is still recorded on every blob so a deliberately
	// sampled store stays correctable to the population. 0 means the default.
	PayloadKeepRate float64 `toml:"payload_keep_rate,omitempty"`

	// PayloadBudgetMB bounds the payload store on disk. Past it the oldest
	// packs are dropped, so the store forgets rather than refuses. 0 means the
	// built-in default.
	PayloadBudgetMB int `toml:"payload_budget_mb,omitempty"`

	OpenRouter OpenRouter `toml:"openrouter,omitempty"`
}

// OpenRouterModels is the configured allowlist, or nothing.
//
// A missing or unparseable config yields an empty list rather than an error:
// discovery runs on machines that have never written one, and failing to
// discover anything at all over it would be far worse than routing as before.
func OpenRouterModels() []string {
	cfg, err := Load()
	if err != nil {
		return nil
	}
	return cfg.OpenRouter.Normalized()
}

// StrictEgress reports whether a secret payload is refused when no local head
// is routable. A nil Config, or one with no [egress] section, is strict: the
// safe reading of "not configured" is the one that does not leak.
func (c *Config) StrictEgress() bool {
	if c == nil || c.Egress.Strict == nil {
		return true
	}
	return *c.Egress.Strict
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
// Nothing has ever done that, not install.sh, not the tap formula, not the npm
// or pip installers, which is why every installed binary ran with no registry
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
