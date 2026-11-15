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
	"time"
)

// ModelPrice holds per-million-token rates for one model. A nil cache rate is
// unknown, not free: those tokens are priced at the input rate.
type ModelPrice struct {
	InputPerMillion      float64  `json:"input_per_mtok"`
	OutputPerMillion     float64  `json:"output_per_mtok"`
	CacheReadPerMillion  *float64 `json:"cache_read_per_mtok,omitempty"`
	CacheWritePerMillion *float64 `json:"cache_write_per_mtok,omitempty"`
}

// TierPrice holds per-million-token rates for one tier (from pricing.yaml).
type TierPrice struct {
	ID                   string   `yaml:"id"`
	InputPerMillion      float64  `yaml:"input_per_million"`
	OutputPerMillion     float64  `yaml:"output_per_million"`
	CacheReadPerMillion  *float64 `yaml:"cache_read_per_million"`
	CacheWritePerMillion *float64 `yaml:"cache_write_per_million"`
}

// Usage is one call's token counts. In is the whole prompt, cached tokens
// included; CacheRead and CacheWrite say how much of it was read or written.
type Usage struct {
	In, Out, CacheRead, CacheWrite int
}

// TierEntry is one tier's fallback pricing, identified by its registry id
// (e.g. "claude-core") rather than a tier number, the form callers that
// merge it alongside OpenRouter model rows want.
type TierEntry struct {
	Tier int
	ID   string
	ModelPrice
}

// DB is the live pricing store. Load it once at startup with Load().
//
// Concurrency: DB is read-only after Load() returns. No fields are written
// after construction, so all methods are safe for concurrent reads. Do NOT
// store a *DB and mutate it, if you need a refresh, call Load() again.
type DB struct {
	// model name (lowercase) → price; populated from cache or OpenRouter fetch
	models map[string]ModelPrice
	// tier (1-10) → price; populated from pricing.yaml fallback
	tiers map[int]TierPrice
	// fetchedAt is when the cached model prices were fetched, zero when none were.
	fetchedAt time.Time
}

// Load builds a DB: loads tier fallback immediately, then overlays live model
// data from the local cache. A background refresh is started when the cache is
// stale (>24h). On first run with no cache, a background fetch is kicked off
// and tier-based pricing is used until the cache is warm.
//
// Never returns nil, worst case returns a DB with only tier data.
func Load() *DB {
	db := &DB{
		models: make(map[string]ModelPrice),
		tiers:  make(map[int]TierPrice),
	}

	// Tier fallback is always loaded first, it's always available locally.
	if tp, err := loadFallbackTiers(); err == nil {
		db.tiers = tp
	} else {
		// pricing.yaml is missing. EstimateCost will return 0 for unknown tiers.
		// This poisons cost.jsonl, warn loudly so the operator notices.
		log.Printf("hydra/pricing: WARNING, could not load registry/pricing.yaml: %v. All tier cost estimates will be $0.00.", err)
	}

	// Overlay live model-specific prices from cache.
	if cache, err := readCache(); err == nil {
		db.fetchedAt = cache.FetchedAt
		for k, v := range cache.Models {
			db.models[strings.ToLower(k)] = v
		}
		if isCacheStale(cache) {
			// Stale but usable, refresh in background, don't block caller.
			go func() {
				if err := refreshCache(); err != nil {
					log.Printf("hydra/pricing: background refresh failed: %v", err)
				}
			}()
		}
	} else {
		// No valid cache, kick off background fetch; use tier pricing until warm.
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

// CostForModel prices a call with no cache usage. See CostForUsage.
func (db *DB) CostForModel(modelName string, tier, inputTokens, outputTokens int) float64 {
	return db.CostForUsage(modelName, tier, Usage{In: inputTokens, Out: outputTokens})
}

// CostForUsage returns the USD cost of one call, cache reads and writes at
// their own rates. Falls back to tier pricing when OpenRouter lacks the model.
func (db *DB) CostForUsage(modelName string, tier int, u Usage) float64 {
	p, ok := db.models[strings.ToLower(modelName)]
	if !ok {
		p = db.tierRates(tier)
	}
	return round6(p.cost(u))
}

// EstimateCost returns the estimated USD cost using tier-based pricing.
// Implements the swarm.PricingReader interface.
// Returns 0 if neither the requested tier nor tier 10 exist in pricing.yaml,
// callers should treat 0 as "pricing unavailable" not "free".
func (db *DB) EstimateCost(tier, inputTokens, outputTokens int) float64 {
	return round6(db.tierRates(tier).cost(Usage{In: inputTokens, Out: outputTokens}))
}

// tierRates is a tier's price, or tier 10's when pricing.yaml lacks the tier.
// With tier 10 missing too (broken install) every rate is zero.
func (db *DB) tierRates(tier int) ModelPrice {
	tp, ok := db.tiers[tier]
	if !ok {
		tp = db.tiers[10]
	}
	return tp.rates()
}

// FetchedAt is when the model prices this DB holds were fetched, read once at Load
// so a background refresh cannot change what a benchmark run recorded. Zero: none.
func (db *DB) FetchedAt() time.Time { return db.fetchedAt }

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

// TierEntries returns every tier-based fallback price, sorted by tier number.
// This is what prices the CLI-agent heads (claude-core, opus-thinking, …) that
// never appear in OpenRouter's catalog, `pricing list` merges this in
// alongside Models() so those heads are visible at all (#505).
func (db *DB) TierEntries() []TierEntry {
	out := make([]TierEntry, 0, len(db.tiers))
	for tier, tp := range db.tiers {
		if tp.ID == "" {
			continue
		}
		out = append(out, TierEntry{Tier: tier, ID: tp.ID, ModelPrice: tp.rates()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tier < out[j].Tier })
	return out
}

func (tp TierPrice) rates() ModelPrice {
	return ModelPrice{tp.InputPerMillion, tp.OutputPerMillion, tp.CacheReadPerMillion, tp.CacheWritePerMillion}
}

// cost prices uncached input at the input rate and each cache count at its own
// rate, or at the input rate when that rate is unknown.
func (p ModelPrice) cost(u Usage) float64 {
	// Never negative, even if a provider's In ever leaves its cache counts out.
	uncached := max(u.In-u.CacheRead-u.CacheWrite, 0)
	return cost(p.InputPerMillion, p.OutputPerMillion, uncached, u.Out) +
		perMillion(u.CacheRead, orInput(p.CacheReadPerMillion, p.InputPerMillion)) +
		perMillion(u.CacheWrite, orInput(p.CacheWritePerMillion, p.InputPerMillion))
}

func orInput(rate *float64, input float64) float64 {
	if rate == nil {
		return input
	}
	return *rate
}

func perMillion(tokens int, rate float64) float64 {
	return float64(tokens) / 1_000_000 * rate
}

func cost(inputPerM, outputPerM float64, inputTokens, outputTokens int) float64 {
	return perMillion(inputTokens, inputPerM) + perMillion(outputTokens, outputPerM)
}

func round6(f float64) float64 {
	return math.Round(f*1_000_000) / 1_000_000
}
