// SPDX-License-Identifier: MIT

package rank

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ankit373/hydra/internal/provider"
)

// Price is what one nominal call to a head costs. A false second result means
// pricing could not answer for this head, which is not the same as free: an
// unpriced head is never chosen as the cheapest, because nothing established
// that it is.
type Price func(h provider.Head) (usd float64, known bool)

// Cheapest orders candidates so the cheapest head measured competent in this
// domain is tried first, and reports the price and verdict it ordered on.
//
// requirement is a probability, the same one trust.RequiredConfidence demands
// of an ensemble, asked here of a single head. Competence is EffectiveScoreIn
// read as what it is: the posterior mean of that head's commitment-correctness
// rate in this domain, which is a probability once there is evidence behind
// it.
//
// "Once there is evidence" is the whole gate. A head with fewer than
// MinCommitments observations *in this domain* is not eligible, whatever it
// has done elsewhere: other-domain work locates its prior and says nothing
// about this one (#885), and a declared catalogue score is a capability index
// rather than a measured rate. Ineligible heads keep the order they arrived
// in, so a machine with no in-domain history routes exactly as before.
//
// Heads that clear it sort on price alone. Picking the *best* clearing head
// would be the ladder again with more steps: once a head is competent enough
// for the task, the only question left is what it costs.
func Cheapest(heads []provider.Head, scores map[string]Score, price Price, requirement float64) ([]provider.Head, map[string]Score) {
	if price == nil || requirement <= 0 || len(heads) == 0 {
		return heads, scores
	}
	out := make(map[string]Score, len(scores))
	for id, sc := range scores {
		out[id] = sc
	}

	bar := requirement * 100
	var cleared, rest []provider.Head
	for _, h := range heads {
		sc := out[h.ID]
		usd, known := price(h)
		if known {
			sc.CostUSD = usd
		}
		sc.Clears = known && sc.Measured() && float64(sc.Effective) >= bar
		out[h.ID] = sc
		if sc.Clears {
			cleared = append(cleared, h)
		} else {
			rest = append(rest, h)
		}
	}
	if len(cleared) == 0 {
		return heads, out
	}
	sort.Slice(cleared, func(i, j int) bool { return cheaperFirst(cleared[i], cleared[j], out) })
	return append(cleared, rest...), out
}

// cheaperFirst is a total order: price, then the measurement, then the id, so
// two heads at the same free tier cannot swap between calls.
func cheaperFirst(a, b provider.Head, scores map[string]Score) bool {
	sa, sb := scores[a.ID], scores[b.ID]
	if sa.CostUSD != sb.CostUSD {
		return sa.CostUSD < sb.CostUSD
	}
	if sa.Effective != sb.Effective {
		return sa.Effective > sb.Effective
	}
	return a.ID < b.ID
}

// Objective is what an unpinned route optimises. The zero value is the cost
// constraint, so a caller that names none routes exactly as before E1.
type Objective string

const (
	Quality Objective = "quality"
	Cost    Objective = "cost"
)

// ParseObjective accepts "", "quality" or "cost" in any case; "" means unset.
func ParseObjective(s string) (Objective, error) {
	switch o := Objective(strings.ToLower(strings.TrimSpace(s))); o {
	case "", Quality, Cost:
		return o, nil
	}
	return "", fmt.Errorf("unknown objective %q; accepted: %s, %s", s, Quality, Cost)
}

// Strongest orders candidates by in-domain competence, then known price (an
// unknown one last), then id: a total order, so equal inputs route equally.
// Nil scores fall back to the declared ones. Fills Score.CostUSD like Cheapest.
func Strongest(heads []provider.Head, scores map[string]Score, price Price) ([]provider.Head, map[string]Score) {
	if scores == nil {
		scores = Scores(heads, nil)
	}
	out := make(map[string]Score, len(scores))
	for id, sc := range scores {
		out[id] = sc
	}
	priced := make(map[string]bool, len(heads))
	for _, h := range heads {
		if price == nil {
			break
		}
		if usd, known := price(h); known {
			sc := out[h.ID]
			sc.CostUSD = usd
			out[h.ID], priced[h.ID] = sc, true
		}
	}
	ordered := append([]provider.Head(nil), heads...)
	sort.Slice(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		sa, sb := out[a.ID], out[b.ID]
		if sa.Effective != sb.Effective {
			return sa.Effective > sb.Effective
		}
		if priced[a.ID] != priced[b.ID] {
			return priced[a.ID]
		}
		if sa.CostUSD != sb.CostUSD {
			return sa.CostUSD < sb.CostUSD
		}
		return a.ID < b.ID
	})
	return ordered, out
}
