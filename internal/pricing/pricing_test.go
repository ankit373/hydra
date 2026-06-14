package pricing

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ── unit tests for cost math ──────────────────────────────────────────────────

func TestCost(t *testing.T) {
	cases := []struct {
		inputPerM, outputPerM float64
		inTok, outTok         int
		want                  float64
	}{
		// 1M tokens each at $15/$75 per million → $15 + $75 = $90
		{15.0, 75.0, 1_000_000, 1_000_000, 90.0},
		// 100 in-tokens × $15/1M = $0.0015; 50 out-tokens × $75/1M = $0.00375
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

func TestDB_EstimateCost_FallsBackToTier10(t *testing.T) {
	db := &DB{
		models: map[string]ModelPrice{},
		tiers: map[int]TierPrice{
			10: {InputPerMillion: 0, OutputPerMillion: 0},
		},
	}
	// Tier 99 doesn't exist — should use tier 10 (free local).
	got := db.EstimateCost(99, 1000, 500)
	if got != 0 {
		t.Fatalf("want 0 for local tier fallback, got %v", got)
	}
}

func TestDB_CostForModel_PrefersModeToTier(t *testing.T) {
	db := &DB{
		models: map[string]ModelPrice{
			"anthropic/claude-opus-4": {InputPerMillion: 15, OutputPerMillion: 75},
		},
		tiers: map[int]TierPrice{
			1: {InputPerMillion: 999, OutputPerMillion: 999}, // wrong — should not be used
		},
	}
	got := db.CostForModel("anthropic/claude-opus-4", 1, 1_000_000, 0)
	want := round6(15.0)
	if got != want {
		t.Fatalf("CostForModel: got %v, want %v", got, want)
	}
}

func TestDB_CostForModel_CaseInsensitive(t *testing.T) {
	db := &DB{
		models: map[string]ModelPrice{
			"anthropic/claude-opus-4": {InputPerMillion: 15, OutputPerMillion: 75},
		},
		tiers: map[int]TierPrice{},
	}
	// Lookup with mixed case.
	got := db.CostForModel("Anthropic/Claude-Opus-4", 1, 1_000_000, 0)
	if got == 0 {
		t.Fatal("case-insensitive lookup failed")
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
					"id": "free/model",
					"pricing": map[string]string{
						"prompt":     "-1", // negative → skipped
						"completion": "-1",
					},
				},
				{
					"id": "broken/model",
					"pricing": map[string]string{
						"prompt":     "not-a-number",
						"completion": "0",
					},
				},
			},
		})
	}))
	defer srv.Close()

	// Temporarily override the URL constant by monkey-patching via test helper.
	orig := openRouterModelsURL
	t.Cleanup(func() { openRouterModelsURL = orig })
	openRouterModelsURL = srv.URL

	models, err := fetchFromOpenRouter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 {
		t.Fatalf("expected 1 model (skipping negative and broken), got %d", len(models))
	}
	p := models["anthropic/claude-opus-4"]
	if p.InputPerMillion != 15.0 {
		t.Fatalf("input price: got %v, want 15.0", p.InputPerMillion)
	}
	if p.OutputPerMillion != 75.0 {
		t.Fatalf("output price: got %v, want 75.0", p.OutputPerMillion)
	}
}

// ── Cache read/write/TTL ──────────────────────────────────────────────────────

func TestCacheRoundtrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HYDRA_CONFIG_DIR", dir) // override config.Dir() via env

	c := &priceCache{
		FetchedAt: time.Now().UTC(),
		Source:    "test",
		Models: map[string]ModelPrice{
			"test/model": {InputPerMillion: 1.0, OutputPerMillion: 2.0},
		},
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
		{"0", 0, false},
		{"", 0, true},
		{"-1", 0, true},
		{"abc", 0, true},
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

// cachePath uses config.Dir() which reads HYDRA_CONFIG_DIR env or defaults.
// We need the test cache to land in t.TempDir().
func init() {
	_ = filepath.Join // ensure filepath is used
	_ = os.Getenv    // ensure os is used
}
