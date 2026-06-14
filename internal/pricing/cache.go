package pricing

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/ankit373/hydra/internal/config"
)

const defaultCacheTTLHours = 24

// priceCache is the on-disk JSON structure.
type priceCache struct {
	FetchedAt time.Time              `json:"fetched_at"`
	Source    string                 `json:"source"`
	Models    map[string]ModelPrice  `json:"models"`
}

func cachePath() string {
	return filepath.Join(config.Dir(), "pricing_cache.json")
}

func cacheTTL() time.Duration {
	if s := os.Getenv("HYDRA_PRICING_TTL_HOURS"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			return time.Duration(n) * time.Hour
		}
	}
	return defaultCacheTTLHours * time.Hour
}

// readCache reads and parses the cache file. Returns an error if missing or corrupt.
func readCache() (*priceCache, error) {
	raw, err := os.ReadFile(cachePath())
	if err != nil {
		return nil, err
	}
	var c priceCache
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	if len(c.Models) == 0 {
		return nil, os.ErrNotExist
	}
	return &c, nil
}

// isCacheStale returns true when the cache is older than the TTL.
func isCacheStale(c *priceCache) bool {
	return time.Since(c.FetchedAt) > cacheTTL()
}

// fetchAndSave fetches from OpenRouter and writes the result to the cache file.
func fetchAndSave() (*priceCache, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	models, err := fetchFromOpenRouter(ctx)
	if err != nil {
		return nil, err
	}

	c := &priceCache{
		FetchedAt: time.Now().UTC(),
		Source:    "openrouter",
		Models:    models,
	}

	if err := writeCache(c); err != nil {
		// Non-fatal — return the data even if we couldn't persist it.
		return c, nil
	}
	return c, nil
}

// refreshCache is the background-refresh entry point.
func refreshCache() error {
	_, err := fetchAndSave()
	return err
}

func writeCache(c *priceCache) error {
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(cachePath())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := cachePath() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, cachePath())
}
