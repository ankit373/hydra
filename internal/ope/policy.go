// SPDX-License-Identifier: MIT

package ope

import (
	"fmt"
	"sort"
	"strings"
)

// A counterfactual policy says what it would have done in a logged context.
//
// Deterministic policies are the useful ones here, "always one tier cheaper",
// "always local", so Would returns a probability that is 1 or 0 rather than a
// distribution. The signature keeps room for a stochastic policy without
// changing the estimator, which already takes TargetProb as a probability.

// Decision is the part of a logged dispatch a policy gets to see. Deliberately
// narrow: a policy that could read the outcome would be cheating.
type Decision struct {
	Enum     string
	Tier     int
	Model    string
	Executor string
	Pool     string
	// Head is the action itself, the head that ran, and Domain the calibration
	// domain the router chose it for. A row written before either was logged
	// carries neither, and a policy that needs them abstains rather than
	// guessing what the context was.
	Head   string
	Domain string
}

// Policy scores how likely it is to have taken the logged action in a context.
type Policy interface {
	// Name is what the report calls it.
	Name() string
	// Would returns π_target(logged action | context), in [0,1].
	Would(d Decision) float64
}

// TierShift is "route everything n tiers away from where it actually went".
// A positive shift is cheaper (tier numbers rise as capability falls).
type TierShift struct {
	Shift int
	// Tiers is the set of tiers the log actually contains, so a shift off the
	// end of the ladder is recognised as unroutable rather than silently
	// mapping onto a tier that does not exist.
	Tiers map[int]bool
}

func (p TierShift) Name() string {
	if p.Shift > 0 {
		return fmt.Sprintf("%d tier(s) cheaper", p.Shift)
	}
	return fmt.Sprintf("%d tier(s) stronger", -p.Shift)
}

// Would treats a logged row as on-policy when its tier is what the shifted
// policy would have chosen for *some* logged context, that is, when this row's
// tier equals another observed tier plus the shift.
func (p TierShift) Would(d Decision) float64 {
	if p.Tiers[d.Tier-p.Shift] {
		return 1
	}
	return 0
}

// PinnedTier is "always route to tier n".
type PinnedTier struct{ Tier int }

func (p PinnedTier) Name() string { return fmt.Sprintf("always tier %d", p.Tier) }

func (p PinnedTier) Would(d Decision) float64 {
	if d.Tier == p.Tier {
		return 1
	}
	return 0
}

// PinnedModel is "always route to this model".
type PinnedModel struct{ Model string }

func (p PinnedModel) Name() string { return "always " + p.Model }

func (p PinnedModel) Would(d Decision) float64 {
	if strings.EqualFold(d.Model, p.Model) {
		return 1
	}
	return 0
}

// LocalOnly is "never leave the machine". Pool is the discriminator the cost
// log already carries, so this needs no provider lookup at evaluation time.
type LocalOnly struct{}

func (LocalOnly) Name() string { return "local heads only" }

func (LocalOnly) Would(d Decision) float64 {
	if strings.EqualFold(d.Executor, "ollama") || strings.EqualFold(d.Pool, "local") {
		return 1
	}
	return 0
}

// Constraint is "route to the cheapest head measured competent at this row's
// domain", the policy internal/dispatch applies when no tier is pinned.
//
// Chosen answers what that head is today, which is the honest limit of this
// estimate and has to be read with it: the constraint's choice moves as
// calibration accumulates, so this asks what the policy *now* would have done
// with the contexts the log recorded, not what it would have done at the time.
// A domain it cannot answer for abstains, which drops the row rather than
// asserting the policy would have routed somewhere else.
type Constraint struct {
	Chosen func(domain string) (headID string, ok bool)
}

func (Constraint) Name() string { return "cheapest head measured competent in-domain" }

func (p Constraint) Would(d Decision) float64 {
	if p.Chosen == nil || d.Domain == "" || d.Head == "" {
		return 0
	}
	id, ok := p.Chosen(d.Domain)
	if !ok || !strings.EqualFold(id, d.Head) {
		return 0
	}
	return 1
}

// Env is what a policy needs besides the log row: the tiers the log contains,
// and the machine's own routing for the constraint policy.
type Env struct {
	Tiers  map[int]bool
	Chosen func(domain string) (headID string, ok bool)
}

// ParsePolicy resolves a policy name from the command line.
func ParsePolicy(spec string, env Env) (Policy, error) {
	tiers := env.Tiers
	spec = strings.TrimSpace(strings.ToLower(spec))
	switch {
	case spec == "constraint":
		if env.Chosen == nil {
			return nil, fmt.Errorf("ope: the constraint policy needs this machine's heads and calibration, which are not available here")
		}
		return Constraint{Chosen: env.Chosen}, nil
	case spec == "" || spec == "cheaper":
		return TierShift{Shift: 1, Tiers: tiers}, nil
	case spec == "cheaper-by-2":
		return TierShift{Shift: 2, Tiers: tiers}, nil
	case spec == "stronger":
		return TierShift{Shift: -1, Tiers: tiers}, nil
	case spec == "local":
		return LocalOnly{}, nil
	case strings.HasPrefix(spec, "tier:"):
		var n int
		if _, err := fmt.Sscanf(spec, "tier:%d", &n); err != nil {
			return nil, fmt.Errorf("ope: %q is not tier:<n>", spec)
		}
		return PinnedTier{Tier: n}, nil
	case strings.HasPrefix(spec, "model:"):
		name := strings.TrimPrefix(spec, "model:")
		if name == "" {
			return nil, fmt.Errorf("ope: model: needs a model name")
		}
		return PinnedModel{Model: name}, nil
	}
	return nil, fmt.Errorf("ope: unknown policy %q, try %s", spec, strings.Join(PolicyNames(), ", "))
}

// PolicyNames lists what ParsePolicy accepts, for help text and errors.
func PolicyNames() []string {
	return []string{"cheaper", "cheaper-by-2", "stronger", "local", "constraint", "tier:<n>", "model:<name>"}
}

// TiersIn collects the tiers a log actually contains.
func TiersIn(tiers []int) map[int]bool {
	out := map[int]bool{}
	for _, t := range tiers {
		out[t] = true
	}
	return out
}

// SortedTiers is TiersIn's inverse, for display.
func SortedTiers(set map[int]bool) []int {
	out := make([]int, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Ints(out)
	return out
}
