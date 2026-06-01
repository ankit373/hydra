// Package rank provides ordering and deduplication for discovered Heads.
package rank

import (
	"sort"

	"github.com/ankit373/hydra/internal/provider"
)

// sourceWeight determines priority when two heads from the same provider
// have equal capability scores. CLI is preferred (no network, self-auth).
var sourceWeight = map[string]int{"cli": 3, "env": 2, "port": 1}

// ByCapScore deduplicates heads by provider (keeping the best-scoring entry
// per provider, preferring CLI source on ties) then sorts descending by score.
// Local heads (LocalOnly=true) are never deduplicated against remote heads
// because they serve a different purpose.
func ByCapScore(heads []provider.Head) []provider.Head {
	best := map[string]provider.Head{}

	for _, h := range heads {
		// Local models get unique keys so they're never merged with remote ones.
		key := dedupeKey(h)
		existing, ok := best[key]
		if !ok {
			best[key] = h
			continue
		}
		if h.CapScore > existing.CapScore ||
			(h.CapScore == existing.CapScore && sourceWeight[h.Source] > sourceWeight[existing.Source]) {
			best[key] = h
		}
	}

	ranked := make([]provider.Head, 0, len(best))
	for _, h := range best {
		ranked = append(ranked, h)
	}

	sort.Slice(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		if a.CapScore != b.CapScore {
			return a.CapScore > b.CapScore
		}
		return sourceWeight[a.Source] > sourceWeight[b.Source]
	})

	return ranked
}

func dedupeKey(h provider.Head) string {
	if h.LocalOnly {
		return h.ID // each local model is unique
	}
	return h.Provider // one entry per cloud provider
}
