// SPDX-License-Identifier: MIT

package dispatch

import (
	"fmt"
	"strings"

	"github.com/ankit373/hydra/internal/signals"
	"github.com/ankit373/hydra/internal/trust"
	"github.com/ankit373/hydra/registry"
)

// ErrBlocked reports a dispatch a rule refused. Distinct from a routing
// failure: nothing was tried and nothing would have helped.
type ErrBlocked struct {
	Rule   string
	Reason string
}

func (e *ErrBlocked) Error() string {
	return fmt.Sprintf("blocked by rule %q: %s", e.Rule, e.Reason)
}

// loadRules reads signals.yaml, validating the values only this package can
// resolve. A rules file that will not load stops the dispatch rather than
// being papered over, the same posture routing.yaml already takes (#720): a
// rule that was meant to keep secrets local must not be skipped silently.
func loadRules(home string) (*signals.Engine, error) {
	return signals.Load(home, func(a signals.Action) error {
		if a.Enum != "" {
			tiers, err := registry.EnumTiers(home)
			if err != nil {
				return err
			}
			if _, ok := tiers[strings.ToUpper(a.Enum)]; !ok {
				return fmt.Errorf("enum %q is not in routing.yaml", a.Enum)
			}
		}
		if a.Tier != "" {
			if _, err := ResolveTier(a.Tier); err != nil {
				return fmt.Errorf("tier %q: %w", a.Tier, err)
			}
		}
		return nil
	})
}

// Decide evaluates the routing rules once for this dispatch.
//
// Once is the contract: the result is carried on Options so every fallback
// candidate sees the same decision. Recomputing per candidate would let a rule
// answer differently for two attempts at one task.
//
// blastRadius is the caller's, because the caller is the one that already
// loaded the code graph for --file; nil leaves the signal absent rather than
// zero, and zero dependents is a real reading.
func (d *Dispatcher) Decide(prompt, domain string, blastRadius *int) signals.Decision {
	in := signals.Input{Prompt: prompt, BlastRadius: blastRadius}
	if d != nil && d.cal != nil {
		cal := d.calibratedIn(domain)
		in.Calibrated = &cal
	}
	if d == nil {
		return (*signals.Engine)(nil).Evaluate(in)
	}
	return d.rules.Evaluate(in)
}

// calibratedIn reports whether internal/trust holds any real observation for
// this domain. The prior alone is not evidence, so N must be above zero.
func (d *Dispatcher) calibratedIn(domain string) bool {
	if domain == "" {
		domain = trust.DefaultDomain
	}
	for _, s := range d.cal.Report() {
		if s.Domain == domain && s.N > 0 {
			return true
		}
	}
	return false
}

// applyDecision folds a rule's action into the options this dispatch runs with.
//
// route and block are dispatch's to apply. require_confidence is not: the
// stopping rule lives where --confidence is read, so that action is applied by
// the caller and is a no-op here rather than being silently dropped.
func applyDecision(dec signals.Decision, opts *Options) error {
	switch dec.Action.Type {
	case signals.ActionBlock:
		return &ErrBlocked{Rule: dec.Rule, Reason: dec.Action.Reason}
	case signals.ActionRoute:
		if dec.Action.LocalOnly {
			opts.LocalOnly = true
		}
		// An explicit flag wins over a rule: the rule is a default for the
		// dispatches nobody spoke about, not an override of the ones they did.
		if dec.Action.Tier != "" && opts.TierHint == "" {
			opts.TierHint = dec.Action.Tier
		}
		if dec.Action.Enum != "" && opts.Enum == "" {
			opts.Enum = dec.Action.Enum
		}
	}
	return nil
}
