// SPDX-License-Identifier: MIT

// Package rank provides ordering and deduplication for discovered Heads.
package rank

import (
	"sort"
	"strconv"

	"github.com/ankit373/hydra/internal/provider"
)

// sourceWeight determines priority when two heads from the same provider
// have equal capability scores. CLI is preferred (no network, self-auth).
var sourceWeight = map[string]int{"cli": 3, "env": 2, "port": 1}

// ByCapScore deduplicates heads by provider (keeping the best-scoring entry
// per provider, preferring CLI source on ties) then sorts descending by score.
// Local heads (LocalOnly=true) are never deduplicated against remote heads
// because they serve a different purpose.
//
// Nothing is suppressed. A special case here dropped the bare "ollama" runtime
// binary whenever a port-discovered model existed, keyed on `Provider ==
// "ollama"`, which the port provider has never stamped, so it never fired
// (#820). Restoring it would be wrong anyway: executor.Unroutable already
// answers that head with "start its local server", which is the one actionable
// line a user with no server running needs to see (#248).
func ByCapScore(heads []provider.Head) []provider.Head {
	best := map[string]provider.Head{}

	for _, h := range heads {
		key := dedupeKey(h)
		existing, ok := best[key]
		if !ok {
			best[key] = h
			continue
		}
		if rankLess(h, existing) {
			best[key] = h
		}
	}

	ranked := make([]provider.Head, 0, len(best))
	for _, h := range best {
		ranked = append(ranked, h)
	}

	sort.Slice(ranked, func(i, j int) bool { return rankLess(ranked[i], ranked[j]) })

	return ranked
}

// rankLess reports whether a should rank ahead of b, as a total order: score,
// then source, then bits per weight, then id.
//
// Total on purpose. Score and source alone left two quants of one model
// incomparable, and `best` above is a map, whose iteration order Go
// randomizes, so the unstable sort had a different input every call and picked
// between them by coin flip: 23 of 40 real `probe` runs ranked the Q4 first,
// 17 the Q8 (#765). The dedupe tie uses the same predicate so the two passes
// cannot disagree about which of two heads is better.
func rankLess(a, b provider.Head) bool {
	if a.CapScore != b.CapScore {
		return a.CapScore > b.CapScore
	}
	if sourceWeight[a.Source] != sourceWeight[b.Source] {
		return sourceWeight[a.Source] > sourceWeight[b.Source]
	}
	if qa, qb := quantRank(a), quantRank(b); qa != qb {
		return qa > qb
	}
	return a.ID < b.ID
}

func dedupeKey(h provider.Head) string {
	if h.LocalOnly || h.Provider == "antigravity" {
		return h.ID // each local model or antigravity tier is unique
	}
	// A head that names its own model is one of several from the same
	// provider, so its ID is its identity, the same reason a local model's
	// is. Keying it on the provider collapsed a three-model OpenRouter
	// allowlist to whichever scored highest, and the other two were silently
	// gone from probe, status and routing (#752).
	if h.Meta["model"] != "" {
		return h.ID
	}
	return h.Provider // one entry per cloud provider
}

// UITier converts a Head's CapScore to the 1-10 tier integer used in cost
// estimation and logging. Registry heads with an explicit "tier" meta key take
// priority; otherwise the CapScore thresholds apply.
func UITier(h provider.Head) int {
	if h.Source == "registry" {
		if t := h.Meta["tier"]; t != "" {
			if n, err := strconv.Atoi(t); err == nil {
				return n
			}
		}
	}
	// A local head costs nothing to run, so for cost routing it belongs at the
	// cheapest tier regardless of how capable it is. Without this, Ollama's
	// score of exactly 60 landed it at tier 9, one short of the bottom, and
	// `--enum GRUNT` degraded past it to a *paid* cloud head, which is the
	// inverse of the point (#248). It also makes CLAUDE.md's promise that
	// tier 10 is the always-available terminal fallback actually true.
	if h.LocalOnly {
		return 10
	}
	switch {
	case h.CapScore >= 95:
		return 1
	case h.CapScore >= 90:
		return 2
	case h.CapScore >= 85:
		return 3
	case h.CapScore >= 80:
		return 4
	case h.CapScore >= 78:
		return 5
	case h.CapScore >= 72:
		return 6
	case h.CapScore >= 70:
		return 7
	case h.CapScore >= 65:
		return 8
	default:
		// The bottom tier is the free floor, and only local heads belong on it.
		// A weak *paid* head fell through to 10 as well, where routing.yaml
		// sends `GRUNT` and pricing.yaml charges $0.00 ("local, no $ cost"), so
		// it was both preferred over a free local head and costed as if it were
		// one. Reachable in practice only once a single provider could offer
		// many models (#752), a 1b model on OpenRouter scores 55.
		return 9
	}
}
