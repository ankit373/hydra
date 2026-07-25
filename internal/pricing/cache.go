// SPDX-License-Identifier: MIT

package pricing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/ankit373/hydra/internal/config"
)

const defaultCacheTTLHours = 24

// errEmptyCache is returned when the cache file exists and parses correctly
// but contains no model entries. Distinct from os.ErrNotExist.
var errEmptyCache = errors.New("pricing cache is empty")

// priceCache is the on-disk JSON structure.
type priceCache struct {
	FetchedAt time.Time             `json:"fetched_at"`
	Source    string                `json:"source"`
	Models    map[string]ModelPrice `json:"models"`
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

// readCache reads and parses the cache file.
// Returns errEmptyCache when the file is valid but has no model entries.
func readCache() (*priceCache, error) {
	raw, err := os.ReadFile(cachePath())
	if err != nil {
		return nil, err
	}
	var c priceCache
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("pricing cache corrupt: %w", err)
	}
	if len(c.Models) == 0 {
		return nil, errEmptyCache
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

	// Non-fatal if we can't persist — return the data regardless.
	_ = writeCache(c)
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
	// Always clean up the tmp file, even on rename failure (cross-device move).
	defer func() { _ = os.Remove(tmp) }()

	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, cachePath()); err != nil {
		// Rename failed (e.g. cross-device). Fall back to direct write.
		return os.WriteFile(cachePath(), raw, 0o600)
	}
	return nil
}
