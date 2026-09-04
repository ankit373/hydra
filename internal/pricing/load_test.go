// SPDX-License-Identifier: MIT

package pricing

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/testutil"
)

// Load is what every command calls at startup. Its job is to never block and
// never return nil, whatever the network and the cache are doing — a nil DB
// panics the caller, and a blocking one hangs the CLI on a dead network.

// stubOpenRouter points the fetcher at a local server returning the given
// models, so no test ever reaches the real API.
func stubOpenRouter(t *testing.T, models map[string]orPricing) *httptest.Server {
	t.Helper()
	var resp orResponse
	for id, p := range models {
		resp.Data = append(resp.Data, orModel{ID: id, Pricing: p})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)

	orig := openRouterModelsURL
	openRouterModelsURL = srv.URL
	t.Cleanup(func() { openRouterModelsURL = orig })
	return srv
}

// writeCacheFixture puts a cache file in place with a chosen age.
func writeCacheFixture(t *testing.T, age time.Duration, models map[string]ModelPrice) {
	t.Helper()
	c := &priceCache{
		FetchedAt: time.Now().UTC().Add(-age),
		Source:    "fixture",
		Models:    models,
	}
	if err := writeCache(c); err != nil {
		t.Fatal(err)
	}
}

// drainBackgroundFetch waits for the goroutine Load() spawns to finish writing.
//
// Load's refresh is fire-and-forget: nothing can wait for it, and it resolves
// the cache path when it runs, not when it is started. A test that returns
// while one is still in flight has it land in the *next* test's sandbox home.
// That is a property of Load, not of the tests, so every test that starts one
// drains it before returning.
func drainBackgroundFetch(t *testing.T, wantModel string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := readCache(); err == nil {
			if _, ok := c.Models[wantModel]; ok {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the background fetch never wrote %q to the cache", wantModel)
}

func TestLoad_NoCacheStillPricesEveryTier(t *testing.T) {
	testutil.NewSandbox(t)
	stubOpenRouter(t, map[string]orPricing{"bg/model": {Prompt: "0.000001", Completion: "0.000002"}})

	db := Load()
	if db == nil {
		t.Fatal("Load() = nil; every caller dereferences this")
	}
	// The tier table is the load-bearing part: it is what prices the CLI-agent
	// heads that never appear in OpenRouter's catalog (#238).
	if !db.HasTiers() {
		t.Fatal("Load() produced no tier pricing, so every CLI-agent head costs $0.00")
	}
	if got := db.EstimateCost(1, 1_000_000, 0); got <= 0 {
		t.Errorf("EstimateCost(tier 1) = %v with no cache, want the fallback rate", got)
	}
	// Models are absent on this first call — the fetch is in the background —
	// and arrive on the next one.
	if len(db.Models()) != 0 {
		t.Errorf("Models() = %v with no cache, want none until the fetch lands", db.Models())
	}
	drainBackgroundFetch(t, "bg/model")
	if _, ok := Load().ModelPrice("bg/model"); !ok {
		t.Error("the second Load did not pick up what the first one fetched")
	}
}

func TestLoad_FreshCacheIsOverlaidAndNotRefetched(t *testing.T) {
	testutil.NewSandbox(t)
	// Any fetch would replace the fixture; make one detectable.
	stubOpenRouter(t, map[string]orPricing{"refetched/model": {Prompt: "0.000001", Completion: "0.000002"}})

	writeCacheFixture(t, time.Hour, map[string]ModelPrice{
		"Vendor/Model": {InputPerMillion: 3, OutputPerMillion: 15},
	})

	db := Load()
	// Keys are lowercased on the way in, so lookups are case-insensitive.
	p, ok := db.ModelPrice("vendor/MODEL")
	if !ok {
		t.Fatalf("cached model missing from the DB: %v", db.Models())
	}
	if p.InputPerMillion != 3 || p.OutputPerMillion != 15 {
		t.Errorf("ModelPrice = %+v, want the cached rates", p)
	}

	// A fresh cache must not trigger a background refresh. Give any goroutine
	// time to land, then confirm the file is untouched.
	time.Sleep(200 * time.Millisecond)
	c, err := readCache()
	if err != nil {
		t.Fatal(err)
	}
	if c.Source != "fixture" {
		t.Errorf("cache Source = %q; a fresh cache was refetched anyway", c.Source)
	}
}

func TestLoad_StaleCacheIsUsedImmediatelyThenRefreshedInBackground(t *testing.T) {
	testutil.NewSandbox(t)
	stubOpenRouter(t, map[string]orPricing{
		"fresh/model": {Prompt: "0.000002", Completion: "0.000004"},
	})

	writeCacheFixture(t, 48*time.Hour, map[string]ModelPrice{
		"stale/model": {InputPerMillion: 1, OutputPerMillion: 1},
	})

	db := Load()
	// The stale data is returned immediately — Load must not block on the network.
	if _, ok := db.ModelPrice("stale/model"); !ok {
		t.Error("stale cache was discarded instead of used while the refresh runs")
	}

	// The background refresh should replace the file.
	drainBackgroundFetch(t, "fresh/model")
	if c, _ := readCache(); c != nil && c.Source != "openrouter" {
		t.Errorf("cache Source = %q after a refresh, want openrouter", c.Source)
	}
}

func TestLoad_CorruptCacheIsReplacedNotFatal(t *testing.T) {
	testutil.NewSandbox(t)
	stubOpenRouter(t, map[string]orPricing{
		"good/model": {Prompt: "0.000003", Completion: "0.000009"},
	})

	if err := os.MkdirAll(filepath.Dir(cachePath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachePath(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	db := Load()
	if !db.HasTiers() {
		t.Error("a corrupt cache took the tier fallback down with it")
	}

	drainBackgroundFetch(t, "good/model")
}

// Refresh is `hyctl pricing refresh` — synchronous, and its count is printed to
// the user, so it must be the number actually written.
func TestRefresh_ReportsWhatItWroteAndSurfacesFailures(t *testing.T) {
	testutil.NewSandbox(t)
	stubOpenRouter(t, map[string]orPricing{
		"a/one":   {Prompt: "0.000001", Completion: "0.000002"},
		"b/two":   {Prompt: "0", Completion: "0"},         // free models are kept
		"c/three": {Prompt: "", Completion: "0.000002"},   // unpriced: skipped
		"d/four":  {Prompt: "-1", Completion: "0.000002"}, // negative: skipped
	})

	n, err := Refresh()
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("Refresh() = %d models, want the 2 priced ones (free kept, "+
			"unpriced and negative skipped)", n)
	}

	c, err := readCache()
	if err != nil {
		t.Fatalf("Refresh reported %d models but wrote no readable cache: %v", n, err)
	}
	if len(c.Models) != n {
		t.Errorf("Refresh() reported %d models but the cache holds %d", n, len(c.Models))
	}
	if c.Source != "openrouter" {
		t.Errorf("cache Source = %q, want openrouter", c.Source)
	}
	if time.Since(c.FetchedAt) > time.Minute {
		t.Errorf("FetchedAt = %v, not stamped at fetch time — the cache would read "+
			"as stale immediately", c.FetchedAt)
	}
}

func TestRefresh_ServerErrorIsReportedNotSwallowed(t *testing.T) {
	testutil.NewSandbox(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	orig := openRouterModelsURL
	openRouterModelsURL = srv.URL
	defer func() { openRouterModelsURL = orig }()

	n, err := Refresh()
	if err == nil {
		t.Fatalf("Refresh() = (%d, nil) against a 503; the user would be told the "+
			"refresh succeeded", n)
	}
	if n != 0 {
		t.Errorf("Refresh() = %d models alongside an error", n)
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("error %q does not say what went wrong", err)
	}
}

// A fetch that succeeds but cannot be persisted must still return the data —
// pricing is more useful than caching it.
func TestFetchAndSave_UnwritableCacheStillReturnsThePrices(t *testing.T) {
	testutil.NewSandbox(t)
	stubOpenRouter(t, map[string]orPricing{"a/one": {Prompt: "0.000001", Completion: "0.000002"}})

	// Dir() is a regular file, so the cache directory cannot be created. The
	// sandbox pre-creates it as an empty directory, so remove that first.
	if err := os.RemoveAll(filepath.Dir(cachePath())); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Dir(cachePath()), []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}

	c, err := fetchAndSave()
	if err != nil {
		t.Fatalf("fetchAndSave failed because the cache was unwritable: %v", err)
	}
	if len(c.Models) != 1 {
		t.Errorf("Models = %v, want the fetched model returned despite the write failure", c.Models)
	}
	if err := writeCache(c); err == nil {
		t.Error("writeCache reported success into an uncreatable directory")
	}
}

// ModelPrice and TierPrice are the lookup surface `hyctl pricing list` renders.
// A miss must be reported as a miss, not as a zero price — zero is a real rate
// for local models.
func TestModelPriceAndTierPrice_ReportMissesAsMisses(t *testing.T) {
	db := &DB{
		models: map[string]ModelPrice{"free/local": {InputPerMillion: 0, OutputPerMillion: 0}},
		tiers:  map[int]TierPrice{10: {InputPerMillion: 0, OutputPerMillion: 0}},
	}

	if p, ok := db.ModelPrice("free/local"); !ok || p.InputPerMillion != 0 {
		t.Errorf("ModelPrice(free/local) = (%+v, %v), want a hit at $0", p, ok)
	}
	if _, ok := db.ModelPrice("never/heard-of-it"); ok {
		t.Error("ModelPrice reported a hit for an unknown model")
	}
	if _, ok := db.ModelPrice("FREE/LOCAL"); !ok {
		t.Error("ModelPrice is case-sensitive; model names arrive in both cases")
	}

	if p, ok := db.TierPrice(10); !ok || p.InputPerMillion != 0 {
		t.Errorf("TierPrice(10) = (%+v, %v), want a hit at $0", p, ok)
	}
	if _, ok := db.TierPrice(3); ok {
		t.Error("TierPrice reported a hit for a tier that is not in the table")
	}
}

// writeCache falls back to a direct write when the rename cannot complete, and
// must never leave its .tmp behind either way.
func TestWriteCache_LeavesNoTempFile(t *testing.T) {
	testutil.NewSandbox(t)

	c := &priceCache{FetchedAt: time.Now().UTC(), Source: "t", Models: map[string]ModelPrice{"m": {1, 2}}}
	if err := writeCache(c); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cachePath() + ".tmp"); err == nil {
		t.Error("the .tmp file survived a successful write")
	}

	// Overwriting an existing cache must work too — this is the every-refresh path.
	c.Source = "second"
	if err := writeCache(c); err != nil {
		t.Fatal(err)
	}
	got, err := readCache()
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != "second" {
		t.Errorf("Source = %q after overwrite, want second", got.Source)
	}
	if _, err := os.Stat(cachePath() + ".tmp"); err == nil {
		t.Error("the .tmp file survived an overwrite")
	}
}

func TestReadCache_MissingFileIsNotExistNotCorruption(t *testing.T) {
	testutil.NewSandbox(t)

	_, err := readCache()
	if err == nil {
		t.Fatal("readCache succeeded with no cache file")
	}
	if !os.IsNotExist(err) {
		t.Errorf("err = %v, want an os.ErrNotExist — Load distinguishes "+
			"\"never fetched\" from \"corrupt\"", err)
	}
}

// The tier table is read through registry.Read, so an on-disk override wins and
// an unparsable one must be an error rather than a silently empty table.
func TestLoadFallbackTiers_OnDiskOverrideWinsAndCorruptIsAnError(t *testing.T) {
	s := testutil.NewSandbox(t)

	regDir := filepath.Join(s.HydraHome, "registry")
	if err := os.MkdirAll(regDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(regDir, "pricing.yaml")
	if err := os.WriteFile(path, []byte("tiers:\n  4:\n    input_per_million: 42\n    output_per_million: 84\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tiers, err := loadFallbackTiers()
	if err != nil {
		t.Fatal(err)
	}
	if got := tiers[4].InputPerMillion; got != 42 {
		t.Errorf("tier 4 input = %v, want the on-disk override's 42 — an operator's "+
			"retuned pricing.yaml was ignored", got)
	}

	if err := os.WriteFile(path, []byte("tiers: [this is not a map"), 0o600); err != nil {
		t.Fatal(err)
	}
	if tiers, err := loadFallbackTiers(); err == nil {
		t.Errorf("loadFallbackTiers() = %v with unparsable YAML, want an error — "+
			"an empty table silently prices everything at $0.00", tiers)
	}
}
