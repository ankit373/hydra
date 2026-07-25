// SPDX-License-Identifier: MIT

// Package pricing provides live model cost data fetched from OpenRouter,
// cached locally with a 24-hour TTL, falling back to the static
// registry/pricing.yaml when offline or on first run.
package pricing

import (
	"log"
	"math"
	"sort"
	"strings"
)

// ModelPrice holds per-million-token rates for one model.
type ModelPrice struct {
	InputPerMillion  float64 `json:"input_per_mtok"`
	OutputPerMillion float64 `json:"output_per_mtok"`
}

// TierPrice holds per-million-token rates for one tier (from pricing.yaml).
type TierPrice struct {
	InputPerMillion  float64 `yaml:"input_per_million"`
	OutputPerMillion float64 `yaml:"output_per_million"`
}

// DB is the live pricing store. Load it once at startup with Load().
//
// Concurrency: DB is read-only after Load() returns. No fields are written
// after construction, so all methods are safe for concurrent reads. Do NOT
// store a *DB and mutate it — if you need a refresh, call Load() again.
type DB struct {
	// model name (lowercase) → price; populated from cache or OpenRouter fetch
	models map[string]ModelPrice
	// tier (1-10) → price; populated from pricing.yaml fallback
	tiers map[int]TierPrice
}

// Load builds a DB: loads tier fallback immediately, then overlays live model
// data from the local cache. A background refresh is started when the cache is
// stale (>24h). On first run with no cache, a background fetch is kicked off
// and tier-based pricing is used until the cache is warm.
//
// Never returns nil — worst case returns a DB with only tier data.
func Load() *DB {
	db := &DB{
		models: make(map[string]ModelPrice),
		tiers:  make(map[int]TierPrice),
	}

	// Tier fallback is always loaded first — it's always available locally.
	if tp, err := loadFallbackTiers(); err == nil {
		db.tiers = tp
	} else {
		// pricing.yaml is missing. EstimateCost will return 0 for unknown tiers.
		// This poisons cost.jsonl — warn loudly so the operator notices.
		log.Printf("hydra/pricing: WARNING — could not load registry/pricing.yaml: %v. All tier cost estimates will be $0.00.", err)
	}

	// Overlay live model-specific prices from cache.
	if cache, err := readCache(); err == nil {
		for k, v := range cache.Models {
			db.models[strings.ToLower(k)] = v
		}
		if isCacheStale(cache) {
			// Stale but usable — refresh in background, don't block caller.
			go func() {
				if err := refreshCache(); err != nil {
					log.Printf("hydra/pricing: background refresh failed: %v", err)
				}
			}()
		}
	} else {
		// No valid cache — kick off background fetch; use tier pricing until warm.
		go func() {
			if err := refreshCache(); err != nil {
				log.Printf("hydra/pricing: initial background fetch failed: %v", err)
			}
		}()
	}

	return db
}

// Refresh forces a synchronous fetch from OpenRouter and updates the cache.
// Returns the number of models fetched and any error.
func Refresh() (int, error) {
	cache, err := fetchAndSave()
	if err != nil {
		return 0, err
	}
	return len(cache.Models), nil
}

// CostForModel returns the estimated USD cost for a specific model name.
// Falls back to tier-based pricing if the model is not in the OpenRouter data.
func (db *DB) CostForModel(modelName string, tier, inputTokens, outputTokens int) float64 {
	if p, ok := db.models[strings.ToLower(modelName)]; ok {
		return round6(cost(p.InputPerMillion, p.OutputPerMillion, inputTokens, outputTokens))
	}
	return db.EstimateCost(tier, inputTokens, outputTokens)
}

// EstimateCost returns the estimated USD cost using tier-based pricing.
// Implements the swarm.PricingReader interface.
// Returns 0 if neither the requested tier nor tier 10 exist in pricing.yaml —
// callers should treat 0 as "pricing unavailable" not "free".
func (db *DB) EstimateCost(tier, inputTokens, outputTokens int) float64 {
	tp, ok := db.tiers[tier]
	if !ok {
		// Requested tier not in pricing.yaml — fall back to tier 10 (local/cheapest).
		// If tier 10 is also missing (broken install), zero is returned.
		tp = db.tiers[10]
	}
	return round6(cost(tp.InputPerMillion, tp.OutputPerMillion, inputTokens, outputTokens))
}

// Models returns all model names in the live pricing data, sorted alphabetically.
func (db *DB) Models() []string {
	out := make([]string, 0, len(db.models))
	for k := range db.models {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ModelPrice returns the price entry for a model, and whether it was found.
func (db *DB) ModelPrice(modelName string) (ModelPrice, bool) {
	p, ok := db.models[strings.ToLower(modelName)]
	return p, ok
}

// TierPrice returns the tier price entry.
func (db *DB) TierPrice(tier int) (TierPrice, bool) {
	p, ok := db.tiers[tier]
	return p, ok
}

// HasTiers reports whether tier pricing was loaded successfully.
// Returns false when pricing.yaml was missing or unreadable.
func (db *DB) HasTiers() bool {
	return len(db.tiers) > 0
}

func cost(inputPerM, outputPerM float64, inputTokens, outputTokens int) float64 {
	return float64(inputTokens)/1_000_000*inputPerM +
		float64(outputTokens)/1_000_000*outputPerM
}

func round6(f float64) float64 {
	return math.Round(f*1_000_000) / 1_000_000
}
