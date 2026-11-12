// SPDX-License-Identifier: MIT

package bench

import (
	"fmt"
	"sort"
	"time"

	"github.com/BurntSushi/toml"
)

// StaticFallback is the arm that moves down a model list on transport errors.
const StaticFallback = "static-fallback"

// Config is bench/configs/<tier>.toml.
type Config struct {
	Tier   string   `toml:"tier"`
	Track  string   `toml:"track"`
	Suites []string `toml:"suites"`
	NTest  int      `toml:"n_test"`
	Seed   uint64   `toml:"seed"`
	// MaxUSD caps each suite's run; there is no "unlimited" spelling.
	MaxUSD float64 `toml:"max_usd"`
	// Arms is the rotation order; each is a [models] key or static-fallback.
	Arms     []string          `toml:"arms"`
	Models   map[string]string `toml:"models"` // arm → "<provider>:<model id>"
	Fallback *struct {
		Arms      []string `toml:"arms"` // [models] keys, in fallback order
		FaultRate float64  `toml:"fault_rate"`
	} `toml:"static_fallback"`
	PriceRefs map[string]string        `toml:"price_refs"` // model id → pricing.DB key
	Prices    map[string]PriceOverride `toml:"prices"`     // model id → a dated list price
	Cutoffs   map[string]string        `toml:"cutoffs"`    // model id → stated training cutoff
}

// PriceOverride is a list price stated with its source, for a model pricing.DB lacks.
type PriceOverride struct {
	InPerM  float64 `toml:"in_per_mtok"`
	OutPerM float64 `toml:"out_per_mtok"`
	Source  string  `toml:"source"`
	Date    string  `toml:"date"` // YYYY-MM-DD
}

// ReadConfig parses a tier config and refuses one a run could not account for.
func ReadConfig(path string) (*Config, error) {
	var c Config
	md, err := toml.DecodeFile(path, &c)
	if err != nil {
		return nil, fmt.Errorf("bench: %s: %w", path, err)
	}
	if u := md.Undecoded(); len(u) > 0 {
		return nil, fmt.Errorf("bench: %s: unknown key %s", path, u[0])
	}
	if c.Tier == "" || c.Track == "" || len(c.Suites) == 0 || c.NTest < 1 || len(c.Arms) == 0 {
		return nil, fmt.Errorf("bench: %s needs tier, track, suites, n_test and arms", path)
	}
	if _, err := c.ArmConfigs(); err != nil {
		return nil, fmt.Errorf("bench: %s: %w", path, err)
	}
	for _, m := range c.ModelIDs() {
		if c.Cutoffs[m] == "" {
			return nil, fmt.Errorf("bench: %s: [cutoffs] states nothing for %s (write \"unstated\" if the vendor does not)", path, m)
		}
	}
	return &c, nil
}

// ArmConfigs resolves every arm in Arms to the models it calls.
func (c *Config) ArmConfigs() (map[string]ArmConfig, error) {
	out := map[string]ArmConfig{}
	for _, name := range c.Arms {
		if _, dup := out[name]; dup {
			return nil, fmt.Errorf("arm %s is listed twice", name)
		}
		if name == StaticFallback {
			if c.Fallback == nil || len(c.Fallback.Arms) < 2 {
				return nil, fmt.Errorf("%s needs [static_fallback] arms with at least two models", name)
			}
			if !(c.Fallback.FaultRate >= 0 && c.Fallback.FaultRate <= 1) {
				return nil, fmt.Errorf("%s fault_rate %v is outside [0, 1]", name, c.Fallback.FaultRate)
			}
			ac := ArmConfig{Kind: "direct", FaultRate: c.Fallback.FaultRate}
			for _, a := range c.Fallback.Arms {
				if c.Models[a] == "" {
					return nil, fmt.Errorf("%s names %s, which [models] does not define", name, a)
				}
				ac.Models = append(ac.Models, c.Models[a])
			}
			out[name] = ac
			continue
		}
		spec := c.Models[name]
		if spec == "" {
			return nil, fmt.Errorf("arm %s has no [models] entry", name)
		}
		out[name] = ArmConfig{Kind: "direct", Models: []string{spec}}
	}
	for _, ac := range out {
		for _, spec := range ac.Models {
			if _, _, err := ParseModel(spec); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// ModelIDs is every model id the config can call, sorted.
func (c *Config) ModelIDs() []string {
	seen := map[string]bool{}
	for _, spec := range c.Models {
		if _, m, err := ParseModel(spec); err == nil {
			seen[m] = true
		}
	}
	out := make([]string, 0, len(seen))
	for m := range seen {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// Overrides turns [prices] into snapshot rows, refusing one without a source or date.
func (c *Config) Overrides() (map[string]Price, error) {
	out := map[string]Price{}
	for m, o := range c.Prices {
		d, err := time.Parse("2006-01-02", o.Date)
		if err != nil || o.Source == "" {
			return nil, fmt.Errorf("bench: [prices.%q] needs a source and a YYYY-MM-DD date", m)
		}
		out[m] = Price{InPerM: o.InPerM, OutPerM: o.OutPerM, Source: "config:" + o.Source, FetchedAt: d}
	}
	return out, nil
}
