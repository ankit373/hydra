// SPDX-License-Identifier: MIT

package pricing

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// ── cost math ─────────────────────────────────────────────────────────────────

func TestCost(t *testing.T) {
	cases := []struct {
		inputPerM, outputPerM float64
		inTok, outTok         int
		want                  float64
	}{
		// 1M tokens each at $15/$75 per million → $90
		{15.0, 75.0, 1_000_000, 1_000_000, 90.0},
		// 100 in × $15/1M = $0.0015; 50 out × $75/1M = $0.00375
		{15.0, 75.0, 100, 50, 0.0015 + 0.00375},
		{0, 0, 999999, 999999, 0},
		// 500 × $3/1M = $0.0015; 200 × $15/1M = $0.003
		{3.0, 15.0, 500, 200, 0.0015 + 0.003},
	}
	for _, c := range cases {
		got := round6(cost(c.inputPerM, c.outputPerM, c.inTok, c.outTok))
		want := round6(c.want)
		if got != want {
			t.Errorf("cost(%v,%v,%v,%v) = %v, want %v",
				c.inputPerM, c.outputPerM, c.inTok, c.outTok, got, want)
		}
	}
}

// ── DB.EstimateCost ───────────────────────────────────────────────────────────

func TestDB_EstimateCost_FallsBackToTier10(t *testing.T) {
	db := &DB{
		models: map[string]ModelPrice{},
		tiers:  map[int]TierPrice{10: {InputPerMillion: 0, OutputPerMillion: 0}},
	}
	got := db.EstimateCost(99, 1000, 500)
	if got != 0 {
		t.Fatalf("want 0 for local tier fallback, got %v", got)
	}
}

func TestDB_EstimateCost_ZeroWhenTier10Missing(t *testing.T) {
	// CRITICAL case: both requested tier and tier 10 are absent.
	// Must return 0, not panic.
	db := &DB{
		models: map[string]ModelPrice{},
		tiers:  map[int]TierPrice{}, // completely empty
	}
	got := db.EstimateCost(1, 1_000_000, 1_000_000)
	if got != 0 {
		t.Fatalf("want 0 when tiers map empty, got %v", got)
	}
	if db.HasTiers() {
		t.Fatal("HasTiers should be false for empty tiers map")
	}
}

func TestDB_EstimateCost_KnownTier(t *testing.T) {
	db := &DB{
		models: map[string]ModelPrice{},
		tiers:  map[int]TierPrice{1: {InputPerMillion: 15, OutputPerMillion: 75}},
	}
	got := db.EstimateCost(1, 1_000_000, 1_000_000)
	if got != 90.0 {
		t.Fatalf("got %v, want 90.0", got)
	}
}

// ── DB.CostForModel ───────────────────────────────────────────────────────────

func TestDB_CostForModel_PrefersModelToTier(t *testing.T) {
	db := &DB{
		models: map[string]ModelPrice{
			"anthropic/claude-opus-4": {InputPerMillion: 15, OutputPerMillion: 75},
		},
		tiers: map[int]TierPrice{1: {InputPerMillion: 999, OutputPerMillion: 999}},
	}
	got := db.CostForModel("anthropic/claude-opus-4", 1, 1_000_000, 0)
	if got != round6(15.0) {
		t.Fatalf("CostForModel: got %v, want 15.0", got)
	}
}

func TestDB_CostForModel_CaseInsensitive(t *testing.T) {
	db := &DB{
		models: map[string]ModelPrice{
			"anthropic/claude-opus-4": {InputPerMillion: 15, OutputPerMillion: 75},
		},
		tiers: map[int]TierPrice{},
	}
	got := db.CostForModel("Anthropic/Claude-Opus-4", 1, 1_000_000, 0)
	if got == 0 {
		t.Fatal("case-insensitive lookup failed")
	}
}

// ── DB.Models sort order ──────────────────────────────────────────────────────

func TestDB_Models_Sorted(t *testing.T) {
	db := &DB{
		models: map[string]ModelPrice{
			"z/model": {},
			"a/model": {},
			"m/model": {},
		},
		tiers: map[int]TierPrice{},
	}
	got := db.Models()
	want := []string{"a/model", "m/model", "z/model"}
	for i, v := range got {
		if v != want[i] {
			t.Fatalf("Models() not sorted: got %v, want %v", got, want)
		}
	}
}

// ── OpenRouter fetch ──────────────────────────────────────────────────────────

func TestFetchFromOpenRouter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{
					"id": "anthropic/claude-opus-4",
					"pricing": map[string]string{
						"prompt":     "0.000015",
						"completion": "0.000075",
					},
				},
				{
					// Negative price → skipped
					"id":      "bad/negative",
					"pricing": map[string]string{"prompt": "-1", "completion": "-1"},
				},
				{
					// Parse error → skipped
					"id":      "bad/parse",
					"pricing": map[string]string{"prompt": "NaN", "completion": "0"},
				},
				{
					// Empty string → skipped
					"id":      "bad/empty",
					"pricing": map[string]string{"prompt": "", "completion": ""},
				},
				{
					// "0" price = free model → included with $0 rates
					"id":      "free/model",
					"pricing": map[string]string{"prompt": "0", "completion": "0"},
				},
			},
		})
	}))
	defer srv.Close()

	orig := openRouterModelsURL
	t.Cleanup(func() { openRouterModelsURL = orig })
	openRouterModelsURL = srv.URL

	models, err := fetchFromOpenRouter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Expect 2: the paid model + the free model. Negative/empty/parse-error are skipped.
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d: %v", len(models), models)
	}
	p := models["anthropic/claude-opus-4"]
	if p.InputPerMillion != 15.0 {
		t.Fatalf("input price: got %v, want 15.0", p.InputPerMillion)
	}
	if p.OutputPerMillion != 75.0 {
		t.Fatalf("output price: got %v, want 75.0", p.OutputPerMillion)
	}
	free := models["free/model"]
	if free.InputPerMillion != 0 || free.OutputPerMillion != 0 {
		t.Fatalf("free model should have $0 rates, got %+v", free)
	}
}

func TestFetchFromOpenRouter_Non200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	orig := openRouterModelsURL
	t.Cleanup(func() { openRouterModelsURL = orig })
	openRouterModelsURL = srv.URL

	_, err := fetchFromOpenRouter(context.Background())
	if err == nil {
		t.Fatal("expected error on HTTP 503")
	}
}

// ── Cache ─────────────────────────────────────────────────────────────────────

func TestCacheRoundtrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HYDRA_CONFIG_DIR", dir)

	c := &priceCache{
		FetchedAt: time.Now().UTC(),
		Source:    "test",
		Models:    map[string]ModelPrice{"test/model": {1.0, 2.0}},
	}
	if err := writeCache(c); err != nil {
		t.Fatal(err)
	}
	got, err := readCache()
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != "test" {
		t.Fatalf("source: got %q, want %q", got.Source, "test")
	}
	if got.Models["test/model"].InputPerMillion != 1.0 {
		t.Fatal("model price not preserved through roundtrip")
	}
}

func TestReadCache_EmptySentinel(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HYDRA_CONFIG_DIR", dir)

	// Write a valid but empty cache.
	c := &priceCache{FetchedAt: time.Now(), Models: map[string]ModelPrice{}}
	if err := writeCache(c); err != nil {
		t.Fatal(err)
	}
	_, err := readCache()
	if !errors.Is(err, errEmptyCache) {
		t.Fatalf("expected errEmptyCache, got %v", err)
	}
	// Must NOT return os.ErrNotExist for a file that exists.
	if errors.Is(err, os.ErrNotExist) {
		t.Fatal("errEmptyCache must not alias os.ErrNotExist")
	}
}

func TestCacheStale(t *testing.T) {
	fresh := &priceCache{FetchedAt: time.Now()}
	if isCacheStale(fresh) {
		t.Fatal("fresh cache should not be stale")
	}
	old := &priceCache{FetchedAt: time.Now().Add(-25 * time.Hour)}
	if !isCacheStale(old) {
		t.Fatal("25h old cache should be stale")
	}
}

func TestCacheTTLEnvOverride(t *testing.T) {
	t.Setenv("HYDRA_PRICING_TTL_HOURS", "1")
	c := &priceCache{FetchedAt: time.Now().Add(-90 * time.Minute)}
	if !isCacheStale(c) {
		t.Fatal("90m old cache should be stale with 1h TTL")
	}
}

// ── parsePerToken ─────────────────────────────────────────────────────────────

func TestParsePerToken(t *testing.T) {
	cases := []struct {
		s       string
		want    float64
		wantErr bool
	}{
		{"0.000015", 0.000015, false},
		{"0", 0, false}, // free model — included, not skipped
		{"", 0, true},
		{"-1", 0, true},
		{"abc", 0, true},
		{"NaN", 0, true},
	}
	for _, c := range cases {
		got, err := parsePerToken(c.s)
		if (err != nil) != c.wantErr {
			t.Errorf("parsePerToken(%q): err=%v wantErr=%v", c.s, err, c.wantErr)
		}
		if err == nil && got != c.want {
			t.Errorf("parsePerToken(%q) = %v, want %v", c.s, got, c.want)
		}
	}
}

// Hydra is a cost router, so a tier that prices at $0.00 is not a missing
// feature — it is a wrong number presented as a real one. registry/pricing.yaml
// used to be read from disk only, and no install path ships it, so every
// installed binary logged a WARNING and then estimated every CLI-agent head at
// zero (#238). Those heads never appear in OpenRouter's catalog, so the tier
// table is the only thing that prices them.
//
// HYDRA_HOME points at an empty dir to reproduce an installed machine: no
// registry on disk, nothing to walk up to.
func TestLoadFallbackTiers_NonZeroOnAMachineWithNoRegistryOnDisk(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())

	tiers, err := loadFallbackTiers()
	if err != nil {
		t.Fatalf("tier pricing unreadable with no on-disk registry: %v", err)
	}
	if len(tiers) == 0 {
		t.Fatal("no tiers loaded — every cost estimate would be $0.00")
	}
	// Tier 10 is the local Ollama head and is genuinely free — asserting
	// non-zero across the board would encode a wrong expectation, not a
	// stronger guarantee. Every paid tier must be priced.
	const localTier = 10
	paid := 0
	for tier, tp := range tiers {
		if tier == localTier {
			continue
		}
		if tp.InputPerMillion <= 0 && tp.OutputPerMillion <= 0 {
			t.Errorf("tier %d prices at $0.00 in and out", tier)
			continue
		}
		paid++
	}
	if paid == 0 {
		t.Fatal("no paid tier carries a price — every API head would estimate at $0.00")
	}
}
