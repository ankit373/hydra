// Package rank provides ordering and deduplication for discovered Heads.
package rank

import (
	"fmt"
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
//
// Special case: the generic "ollama" CLI head (the runtime binary) is suppressed
// when any port-discovered Ollama model exists — the named models are strictly
// more useful than the bare runtime as a dispatchable head.
func ByCapScore(heads []provider.Head) []provider.Head {
	// Check if Ollama port models are present before deduping.
	hasOllamaPortModels := false
	for _, h := range heads {
		if h.Provider == "ollama" && h.Source == "port" {
			hasOllamaPortModels = true
			break
		}
	}

	best := map[string]provider.Head{}

	for _, h := range heads {
		// Suppress the generic ollama CLI binary when named port models exist.
		if hasOllamaPortModels && h.ID == "ollama" && h.Source == "cli" {
			continue
		}
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
	if h.LocalOnly || h.Provider == "antigravity" {
		return h.ID // each local model or antigravity tier is unique
	}
	return h.Provider // one entry per cloud provider
}

// UITier converts a Head's CapScore to the 1-10 tier integer used in cost
// estimation and logging. Registry heads with an explicit "tier" meta key take
// priority; otherwise the CapScore thresholds apply.
func UITier(h provider.Head) int {
	if h.Source == "registry" {
		if t := h.Meta["tier"]; t != "" {
			var n int
			if _, err := fmt.Sscanf(t, "%d", &n); err == nil {
				return n
			}
		}
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
	case h.CapScore >= 60:
		return 9
	default:
		return 10
	}
}
