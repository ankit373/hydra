// Package pricing provides live model cost data fetched from OpenRouter,
// cached locally with a 24-hour TTL, falling back to the static
// registry/pricing.yaml when offline or on first run.
package pricing

import (
	"math"
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
// All methods are safe for concurrent use after construction.
type DB struct {
	// model name (lowercase) → price; populated from cache or OpenRouter fetch
	models map[string]ModelPrice
	// tier (1-10) → price; always populated from pricing.yaml fallback
	tiers map[int]TierPrice
}

// Load builds a DB: tries the local cache first, falls back to pricing.yaml.
// A background refresh is started when the cache is stale (>24h).
// Never returns nil — worst case it returns a DB with only tier data.
func Load() *DB {
	db := &DB{
		models: make(map[string]ModelPrice),
		tiers:  make(map[int]TierPrice),
	}

	// Always load tier fallback first — it's always available.
	if tp, err := loadFallbackTiers(); err == nil {
		db.tiers = tp
	}

	// Try live model data from cache.
	if cache, err := readCache(); err == nil {
		for k, v := range cache.Models {
			db.models[strings.ToLower(k)] = v
		}
		// Kick off background refresh if stale.
		if isCacheStale(cache) {
			go func() { _ = refreshCache() }()
		}
	} else {
		// No valid cache — fetch synchronously (best-effort, 10s timeout).
		if cache, err := fetchAndSave(); err == nil {
			for k, v := range cache.Models {
				db.models[strings.ToLower(k)] = v
			}
		}
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
func (db *DB) EstimateCost(tier, inputTokens, outputTokens int) float64 {
	tp, ok := db.tiers[tier]
	if !ok {
		// Tier not in pricing.yaml — use tier 10 (cheapest) as floor.
		tp = db.tiers[10]
	}
	return round6(cost(tp.InputPerMillion, tp.OutputPerMillion, inputTokens, outputTokens))
}

// Models returns all model names in the live pricing data.
func (db *DB) Models() []string {
	out := make([]string, 0, len(db.models))
	for k := range db.models {
		out = append(out, k)
	}
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

func cost(inputPerM, outputPerM float64, inputTokens, outputTokens int) float64 {
	return float64(inputTokens)/1_000_000*inputPerM +
		float64(outputTokens)/1_000_000*outputPerM
}

func round6(f float64) float64 {
	return math.Round(f*1_000_000) / 1_000_000
}
