// SPDX-License-Identifier: MIT

package api

import (
	"errors"
	"fmt"
	"math"

	"github.com/ankit373/hydra/internal/config"
)

// Settings is what the app may change about this machine.
//
// A deliberately narrow slice of config.Config: the switches internal/config
// argues should be decided rather than inherited, plus the numbers that bound
// them. Everything else in the file is read by something and written by nobody,
// so putting it here would be a surface with no reason behind it.
type Settings struct {
	// Path is where these live, shown because the file is still editable by
	// hand and people need to know which one the app is writing.
	Path string `json:"path"`

	// Readable is false when a config exists and does not parse. The app then
	// shows Error and refuses to save, because writing over it destroys the
	// only record of what the user meant.
	Readable bool   `json:"readable"`
	Error    string `json:"error,omitempty"`

	// Exists separates "never configured" from "configured", which read the
	// same once both render as defaults.
	Exists bool `json:"exists"`

	Cortex string   `json:"cortex"`
	Skills []string `json:"skills"`

	PIILocalOnly bool `json:"piiLocalOnly"`
	StrictEgress bool `json:"strictEgress"`

	CapturePayloads bool `json:"capturePayloads"`
	PayloadBudgetMB int  `json:"payloadBudgetMb"`

	CaptureEmbeddings bool   `json:"captureEmbeddings"`
	EmbedModel        string `json:"embedModel"`
	EmbedBudgetMB     int    `json:"embedBudgetMb"`

	CacheAnswers   bool    `json:"cacheAnswers"`
	CacheThreshold float64 `json:"cacheThreshold"`
	CacheBudgetMB  int     `json:"cacheBudgetMb"`

	ExploreRate float64 `json:"exploreRate"`

	// OpenRouterModels is read-only here: the allowlist is a list of catalogue
	// ids, and editing one needs the catalogue in front of you, which is the
	// Models view's job rather than this one's.
	OpenRouterModels []string `json:"openRouterModels"`
}

// GetSettings reads the config the app is allowed to change.
//
// A missing config is not an error: a machine that has never run `hyctl init`
// has every default, and reporting plumbing instead of the answer is what
// #1106 was. A config that exists and does not parse is reported as unreadable
// rather than rendered as defaults, which would invite saving over it.
func (a *API) GetSettings() Settings {
	s := Settings{Path: config.Path(), Readable: true, Exists: true}

	cfg, err := config.Load()
	switch {
	case errors.Is(err, config.ErrNotFound):
		s.Exists = false
		cfg = &config.Config{}
	case err != nil:
		s.Readable, s.Error = false, err.Error()
		return s
	}

	s.Cortex = cfg.Cortex
	s.Skills = cfg.Skills
	s.PIILocalOnly = cfg.Policies["pii"].Action == "local-only"
	s.StrictEgress = cfg.StrictEgress()
	s.CapturePayloads = cfg.CapturePayloads
	s.PayloadBudgetMB = cfg.PayloadBudgetMB
	s.CaptureEmbeddings = cfg.CaptureEmbeddings
	s.EmbedModel = cfg.EmbedModel
	s.EmbedBudgetMB = cfg.EmbedBudgetMB
	s.CacheAnswers = cfg.CacheAnswers
	s.CacheThreshold = cfg.CacheThreshold
	s.CacheBudgetMB = cfg.CacheBudgetMB
	s.ExploreRate = cfg.ExploreRate
	s.OpenRouterModels = cfg.OpenRouter.Normalized()
	return s
}

// SaveSettings overlays s onto the config on disk and writes it back.
//
// Overlay, never replace: config.Save carries over what the struct cannot
// decode (#1103), and this carries over what Settings does not name. Between
// them, saving from the app cannot delete a setting it never showed.
func (a *API) SaveSettings(s Settings) Settings {
	if err := validateSettings(s); err != nil {
		out := a.GetSettings()
		out.Error = err.Error()
		return out
	}

	cfg, err := config.Load()
	switch {
	case errors.Is(err, config.ErrNotFound):
		cfg = &config.Config{}
	case err != nil:
		// The same refusal `hyctl init` makes: a file that is there and does
		// not parse holds the only record of what was meant.
		return Settings{Path: config.Path(), Readable: false, Exists: true,
			Error: fmt.Sprintf("refusing to overwrite %s: %v", config.Path(), err)}
	}

	// Cortex and skills are the wizard's, not this view's: changing a Cortex
	// means picking from discovered heads, which is `hyctl init`'s whole first
	// screen. Writing them from here would mean accepting any string.
	if s.PIILocalOnly {
		if cfg.Policies == nil {
			cfg.Policies = map[string]config.Policy{}
		}
		cfg.Policies["pii"] = config.Policy{Action: "local-only"}
	} else if cfg.Policies["pii"].Action == "local-only" {
		// Only the rule this switch owns, so a pii policy set to something else
		// by hand is not collateral.
		delete(cfg.Policies, "pii")
	}

	strict := s.StrictEgress
	cfg.Egress.Strict = &strict
	cfg.CapturePayloads = s.CapturePayloads
	cfg.PayloadBudgetMB = s.PayloadBudgetMB
	cfg.CaptureEmbeddings = s.CaptureEmbeddings
	cfg.EmbedModel = s.EmbedModel
	cfg.EmbedBudgetMB = s.EmbedBudgetMB
	cfg.CacheAnswers = s.CacheAnswers
	cfg.CacheThreshold = s.CacheThreshold
	cfg.CacheBudgetMB = s.CacheBudgetMB
	cfg.ExploreRate = s.ExploreRate

	if err := config.Save(cfg); err != nil {
		out := a.GetSettings()
		out.Error = err.Error()
		return out
	}
	return a.GetSettings()
}

// validateSettings refuses values the rest of Hydra would silently ignore.
//
// A cache threshold outside (0,1] is ignored by internal/cache rather than
// clamped, so accepting one here would show a number the router does not use.
// A negative budget is the same shape of lie. Refusing at the edge is what
// keeps the view's reading of the config honest.
func validateSettings(s Settings) error {
	if s.CacheThreshold != 0 && (s.CacheThreshold <= 0 || s.CacheThreshold > 1 || math.IsNaN(s.CacheThreshold)) {
		return fmt.Errorf("cache threshold must be between 0 and 1, got %v", s.CacheThreshold)
	}
	if s.ExploreRate < 0 || s.ExploreRate > 1 || math.IsNaN(s.ExploreRate) {
		return fmt.Errorf("explore rate must be between 0 and 1, got %v", s.ExploreRate)
	}
	for _, b := range []struct {
		name string
		v    int
	}{{"payload budget", s.PayloadBudgetMB}, {"embedding budget", s.EmbedBudgetMB}, {"cache budget", s.CacheBudgetMB}} {
		if b.v < 0 {
			return fmt.Errorf("%s cannot be negative, got %d MB", b.name, b.v)
		}
	}
	return nil
}
