// SPDX-License-Identifier: MIT

// Package recipe is what the router chooses for a request: which head, how
// many samples, which checks pick among them, when to stop and what runs next.
package recipe

import (
	"fmt"

	"github.com/ankit373/hydra/internal/rank"
)

// Stop says when a recipe stops sampling.
type Stop string

const (
	// StopNone takes the one answer as it comes; there is no check to stop on.
	StopNone Stop = "none"
	// StopFirstPass stops at the first sample whose checks all pass.
	StopFirstPass Stop = "first_pass"
	// StopAll runs every sample and keeps the best by its checks.
	StopAll Stop = "all"
	// StopBudget stops once MaxUSD is spent.
	StopBudget Stop = "budget"
	// StopCertified is reserved for stopping on certified evidence (#1549).
	StopCertified Stop = "certified"
)

// Recipe is one way to get an answer. Checks are the same spec strings
// `hyctl dispatch --check` parses, so a logged recipe can be run again.
type Recipe struct {
	Name        string         `json:"name" yaml:"name"`
	Head        string         `json:"head,omitempty" yaml:"head"`           // empty lets the ranking pick
	Objective   rank.Objective `json:"objective,omitempty" yaml:"objective"` // with no Head: quality or cost
	Samples     int            `json:"samples" yaml:"samples"`
	Temperature *float64       `json:"temperature,omitempty" yaml:"temperature"`
	Checks      []string       `json:"checks,omitempty" yaml:"checks"`
	Stop        Stop           `json:"stop" yaml:"stop"`
	Fallback    string         `json:"fallback,omitempty" yaml:"fallback"` // next recipe's name, empty = none
	MaxUSD      float64        `json:"max_usd,omitempty" yaml:"max_usd"`
}

// Default is the one call dispatch already makes: one sample, and when checks
// are given, the answer stands only if they pass.
func Default(checks []string) Recipe {
	r := Recipe{Name: "single", Samples: 1, Stop: StopNone}
	if len(checks) > 0 {
		r.Checks = checks
		r.Stop = StopFirstPass
	}
	return r
}

// Validate refuses a recipe that cannot work as written.
func (r Recipe) Validate() error {
	if _, err := rank.ParseObjective(string(r.Objective)); err != nil {
		return fmt.Errorf("recipe %q: %w", r.Name, err)
	}
	switch {
	case r.Samples < 1:
		return fmt.Errorf("recipe %q: samples is %d, want at least 1", r.Name, r.Samples)
	case r.Samples > 1 && len(r.Checks) == 0:
		// With nothing to pick between, k samples is a majority vote, which plateaus.
		return fmt.Errorf("recipe %q: %d samples with no check to pick between them", r.Name, r.Samples)
	}
	switch r.Stop {
	case StopNone:
		if r.Samples > 1 {
			return fmt.Errorf("recipe %q: stop none with %d samples says nothing about which to keep", r.Name, r.Samples)
		}
	case StopFirstPass, StopAll:
		if len(r.Checks) == 0 {
			return fmt.Errorf("recipe %q: stop %s needs a check", r.Name, r.Stop)
		}
	case StopBudget:
		if r.MaxUSD <= 0 {
			return fmt.Errorf("recipe %q: stop budget needs max_usd above 0", r.Name)
		}
	case StopCertified:
		return fmt.Errorf("recipe %q: stop certified is reserved and not supported yet", r.Name)
	default:
		return fmt.Errorf("recipe %q: unknown stop %q", r.Name, r.Stop)
	}
	return nil
}
