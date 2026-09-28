// SPDX-License-Identifier: MIT

package env

import (
	"strings"

	"github.com/ankit373/hydra/internal/capabilities"
	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/pricing"
	"github.com/ankit373/hydra/internal/provider"
)

// OpenRouter is priced and scored per model already, and was still one head
// routing to one model. Enumerating its whole catalogue instead would bury
// probe and status, so only models the user names are admitted (#752).

// allowlistedOpenRouter returns one head per configured model, or nil when the
// allowlist is empty, in which case the caller keeps the single key-derived
// head this install has always had.
func allowlistedOpenRouter(caps *capabilities.DB) []provider.Head {
	models := config.OpenRouterModels()
	if len(models) == 0 {
		return nil // nothing named, nothing changes, and no catalogue to read
	}
	return openRouterHeads(models, pricing.Load().Models(), caps)
}

// openRouterHeads builds the heads. The catalogue is a parameter so the
// admission rules are testable without a pricing cache or a network.
func openRouterHeads(models, catalogue []string, caps *capabilities.DB) []provider.Head {
	known := make(map[string]bool, len(catalogue))
	for _, id := range catalogue {
		known[strings.ToLower(id)] = true
	}

	heads := make([]provider.Head, 0, len(models))
	for _, m := range models {
		lower := strings.ToLower(m)
		meta := map[string]string{
			// The id to send as the request's model. Carried rather than derived
			// from the head ID, which is lowercased and prefixed.
			"model":        m,
			"model_source": caps.Source(lower),
		}
		// A name the catalogue does not hold is a typo, and the catalogue is
		// evidence of that only when it loaded at all: an empty one means the
		// fetch never landed, which says nothing about this model. Marked
		// rather than dropped, because a head that silently vanishes is
		// exactly the defect #714 was about.
		if len(known) > 0 && !known[lower] {
			meta["unroutable_reason"] = "not in the OpenRouter catalogue, check the spelling in " + config.Path()
		}
		heads = append(heads, provider.Head{
			ID:        "openrouter/" + lower,
			Name:      m + " (OpenRouter)",
			Provider:  "openrouter",
			Source:    "env",
			CapScore:  openRouterScore(lower, caps),
			AuthReady: true,
			Meta:      meta,
		})
	}
	return heads
}

// openRouterScore prefers what capabilities knows, falling back to the same
// heuristic `hyctl models sync` would have recorded, so ranking does not
// depend on having run a sync: without it every allowlisted model scored the
// identical default and the router had nothing to choose on.
func openRouterScore(lower string, caps *capabilities.DB) int {
	if e, ok := caps.Entry(lower); ok {
		return e.CapScore
	}
	return capabilities.HeuristicCapScore(lower)
}
