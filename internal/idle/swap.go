// SPDX-License-Identifier: MIT

package idle

import (
	"fmt"
	"slices"
	"time"

	"github.com/ankit373/hydra/internal/pricing"
)

// CacheLifetime is how long a provider keeps a prompt prefix cached after its
// last use: Anthropic's default cache marker. Providers that cache on their
// own are read the same way until pricing carries a lifetime for them.
const CacheLifetime = 5 * time.Minute

// Rates are what writing and reading the provider's prompt cache cost,
// relative to a plain input token. Known is false when pricing has no cache rate.
type Rates struct {
	Write, Read float64
	Known       bool
}

// RatesFor reads the cache rates of one model's price. A missing rate is
// unknown, never free (see pricing.ModelPrice).
func RatesFor(p pricing.ModelPrice) Rates {
	if p.CacheWritePerMillion == nil || p.CacheReadPerMillion == nil || p.InputPerMillion <= 0 {
		return Rates{}
	}
	return Rates{Write: *p.CacheWritePerMillion / p.InputPerMillion, Read: *p.CacheReadPerMillion / p.InputPerMillion, Known: true}
}

// Warm reports whether the cache of the full conversation is still alive, from
// the time since its last request.
func Warm(sinceLast time.Duration) bool { return sinceLast < CacheLifetime }

// ShouldSwap decides whether to send the pack (m tokens) in place of the
// covered turns (n tokens). Cold cache: the next request writes a prefix either
// way, so the smaller one always wins. Warm: the full prefix reads at r while
// the pack must first be written at w, so the pack pays back after
// k > w*m / (r*(n-m)) more requests, and swaps only when turnsLeft exceeds that.
func ShouldSwap(n, m, turnsLeft float64, rates Rates, warm bool) (bool, string) {
	if m >= n {
		return false, "the pack is not smaller than the turns it replaces"
	}
	if !warm {
		return true, "the cache is cold, so the smaller prefix costs less to write"
	}
	if !rates.Known {
		return false, "the cache is warm and its rates are not known"
	}
	if rates.Read*(n-m) <= 0 {
		return false, "the cache is warm and reading it costs nothing, so a rewrite never pays back"
	}
	breakEven := rates.Write * m / (rates.Read * (n - m))
	if turnsLeft > breakEven {
		return true, fmt.Sprintf("the cache is warm; %.1f more requests are expected and the rewrite pays back after %.1f", turnsLeft, breakEven)
	}
	return false, fmt.Sprintf("the cache is warm; %.1f more requests are expected and the rewrite needs more than %.1f", turnsLeft, breakEven)
}

// TurnsLeft is how many requests to expect after a pause of gap, from this
// conversation's own request times: the median length of the bursts that
// followed each earlier pause that long. With no such pause it is fallback.
func TurnsLeft(requests []time.Time, gap time.Duration, fallback float64) float64 {
	ts := slices.Clone(requests)
	slices.SortFunc(ts, func(a, b time.Time) int { return a.Compare(b) })
	var bursts []float64
	count := -1 // requests since the latest pause; -1 before the first one
	for i := 1; i < len(ts); i++ {
		switch {
		case ts[i].Sub(ts[i-1]) >= gap:
			if count > 0 {
				bursts = append(bursts, float64(count))
			}
			count = 1
		case count >= 0:
			count++
		}
	}
	if count > 0 {
		bursts = append(bursts, float64(count))
	}
	if len(bursts) == 0 {
		return fallback
	}
	slices.Sort(bursts)
	mid := len(bursts) / 2
	if len(bursts)%2 == 1 {
		return bursts[mid]
	}
	return (bursts[mid-1] + bursts[mid]) / 2
}
