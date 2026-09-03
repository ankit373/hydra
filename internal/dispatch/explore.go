// SPDX-License-Identifier: MIT

package dispatch

import (
	"math/rand/v2"

	"github.com/ankit373/hydra/internal/provider"
)

// pick chooses which candidate to try first and reports the probability it was
// chosen.
//
// Argmax routing gives every head but the winner a selection probability of
// zero, so a log of it can never answer "what would another head have done" —
// that evidence is absent rather than sparse, and no amount of stored data
// recovers it. A small exploration rate is what makes the question answerable.
//
// The returned slice is candidates reordered so the chosen head is first; the
// rest keep their ranking order and remain the fallback chain.
func (d *Dispatcher) pick(candidates []provider.Head, opts Options) ([]provider.Head, float64) {
	eps := d.exploreRate()
	if !explorable(candidates, opts, eps) {
		return candidates, 1
	}
	n := float64(len(candidates))
	greedyProb := 1 - eps + eps/n
	if rand.Float64() >= eps {
		return candidates, greedyProb
	}
	i := rand.IntN(len(candidates))
	if i == 0 {
		return candidates, greedyProb
	}
	reordered := make([]provider.Head, 0, len(candidates))
	reordered = append(reordered, candidates[i])
	reordered = append(reordered, candidates[:i]...)
	reordered = append(reordered, candidates[i+1:]...)
	return reordered, eps / n
}

// SelectionProb is the probability pick would choose the candidate at index i
// out of n under exploration rate eps. Exported so a reader of the logs can
// check a recorded propensity against the policy that produced it.
func SelectionProb(i, n int, eps float64) float64 {
	if n <= 0 || i < 0 || i >= n {
		return 0
	}
	if eps <= 0 || eps > 1 || n == 1 {
		if i == 0 {
			return 1
		}
		return 0
	}
	if i == 0 {
		return 1 - eps + eps/float64(n)
	}
	return eps / float64(n)
}

// explorable reports whether this dispatch may deviate from argmax.
//
// Two guards the issue asks for are already free by construction: selectHeads
// filters to the requested tier or cheaper, so exploring inside candidates can
// never escalate cost, and the per-candidate ledger check still runs in the
// dispatch loop, so exploration cannot walk past a policy gate.
func explorable(candidates []provider.Head, opts Options, eps float64) bool {
	switch {
	case eps <= 0 || eps > 1:
		return false
	case len(candidates) < 2:
		return false
	case opts.DryRun:
		return false // --dry-run must predict real routing, not sample it
	case opts.Resource != "":
		return false // a dispatch that writes a file is not a place to gamble
	}
	return true
}

func (d *Dispatcher) exploreRate() float64 {
	if d.cfg == nil {
		return 0
	}
	return d.cfg.ExploreRate
}
