// SPDX-License-Identifier: MIT

package dispatch

import (
	"context"
	"math"
	"testing"

	"github.com/ankit373/hydra/internal/pricing"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/testutil"
)

// Rates per million as OpenRouter's /api/v1/models listed them on 2026-11-15.
// deepseek-chat lists no cache rate there, so its cached tokens stay at input.
var cacheRates = map[string]map[string]float64{
	"anthropic/claude-sonnet-4.5": {"input_per_mtok": 3, "output_per_mtok": 15,
		"cache_read_per_mtok": 0.3, "cache_write_per_mtok": 3.75},
	"openai/gpt-4o": {"input_per_mtok": 2.5, "output_per_mtok": 10, "cache_read_per_mtok": 1.25},
	"google/gemini-2.5-pro": {"input_per_mtok": 1.25, "output_per_mtok": 10,
		"cache_read_per_mtok": 0.125, "cache_write_per_mtok": 0.375},
	"deepseek/deepseek-chat": {"input_per_mtok": 0.2574, "output_per_mtok": 1.0287},
}

func modelHead(model string) provider.Head {
	return provider.Head{ID: model, CapScore: 90, Meta: map[string]string{"model": model}}
}

func TestCallCost_CacheHitIsCheaper(t *testing.T) {
	testutil.NewSandbox(t)
	db := pricedModels(t, cacheRates)
	h := modelHead("anthropic/claude-sonnet-4.5")
	hit := CallCost(db, h, pricing.Usage{In: 1000, Out: 100, CacheRead: 900})
	miss := CallCost(db, h, pricing.Usage{In: 1000, Out: 100})
	// 100 uncached at $3 and 900 read at $0.30, against all 1000 at $3.
	if want := (100*3.0 + 900*0.3 + 100*15.0) / 1e6; math.Abs(hit-want) > 1e-9 {
		t.Errorf("cache hit cost %v, want %v", hit, want)
	}
	if hit >= miss {
		t.Errorf("a cache hit cost %v, no less than the miss %v", hit, miss)
	}
}

func TestCallCost_NoCacheUnchanged(t *testing.T) {
	testutil.NewSandbox(t)
	db := pricedModels(t, cacheRates)
	for model := range cacheRates {
		got := CallCost(db, modelHead(model), pricing.Usage{In: 1234, Out: 567})
		if want := db.CostForModel(model, 1, 1234, 567); got != want {
			t.Errorf("%s: no-cache call cost %v, want today's %v", model, got, want)
		}
	}
}

// One table per provider shape: Anthropic reads and writes, OpenAI and DeepSeek
// report reads only, Gemini reports reads (its writes are an explicit API).
func TestCallCost_PerProviderWithAndWithoutCache(t *testing.T) {
	testutil.NewSandbox(t)
	db := pricedModels(t, cacheRates)
	cases := []struct {
		model string
		hit   pricing.Usage
		want  float64 // price of hit
	}{
		{"anthropic/claude-sonnet-4.5", pricing.Usage{In: 10_000, Out: 500, CacheRead: 6_000, CacheWrite: 3_000},
			(1_000*3 + 6_000*0.3 + 3_000*3.75 + 500*15.0) / 1e6},
		{"openai/gpt-4o", pricing.Usage{In: 10_000, Out: 500, CacheRead: 8_000},
			(2_000*2.5 + 8_000*1.25 + 500*10.0) / 1e6},
		{"google/gemini-2.5-pro", pricing.Usage{In: 10_000, Out: 500, CacheRead: 8_000},
			(2_000*1.25 + 8_000*0.125 + 500*10.0) / 1e6},
		{"deepseek/deepseek-chat", pricing.Usage{In: 10_000, Out: 500, CacheRead: 8_000},
			(10_000*0.2574 + 500*1.0287) / 1e6},
	}
	for _, c := range cases {
		h := modelHead(c.model)
		got := CallCost(db, h, c.hit)
		if math.Abs(got-c.want) > 1e-6 {
			t.Errorf("%s with cache: %v, want %v", c.model, got, c.want)
		}
		miss := CallCost(db, h, pricing.Usage{In: c.hit.In, Out: c.hit.Out})
		if want := (float64(c.hit.In)*cacheRates[c.model]["input_per_mtok"] +
			float64(c.hit.Out)*cacheRates[c.model]["output_per_mtok"]) / 1e6; math.Abs(miss-want) > 1e-6 {
			t.Errorf("%s without cache: %v, want %v", c.model, miss, want)
		}
	}
}

// The logged cost is the cache-aware one: a dispatch whose reply reports 900
// cached of 1000 prompt tokens costs what CallCost says for that usage.
func TestDispatch_LogsCacheAwareCost(t *testing.T) {
	testutil.NewSandbox(t)
	body := `{"model":"gpt-4o","choices":[{"message":{"content":"hi"}}],
  "usage":{"prompt_tokens":1000,"completion_tokens":10,"prompt_tokens_details":{"cached_tokens":900}}}`
	h := servingHeadWith(t, body)
	h.Meta = map[string]string{"model": "openai/gpt-4o"}
	d := liveDispatcher(h)
	d.pricing = pricedModels(t, cacheRates)

	res, err := d.Dispatch(context.Background(), "hello", Options{RunID: "run-c", TaskID: "task-c"})
	if err != nil {
		t.Fatal(err)
	}
	want := CallCost(d.pricing, h, pricing.Usage{In: 1000, Out: 10, CacheRead: 900})
	rows := costRows(t)
	if len(rows) != 1 || res.CostUSD != want || rows[0]["est_cost_usd"] != want {
		t.Fatalf("Result %v, rows %v, want both at the cache-aware %v", res.CostUSD, rows, want)
	}
	if d.PriceCall(h, pricing.Usage{In: 1000, Out: 10, CacheRead: 900}) != want {
		t.Error("PriceCall, what swarm prices with, disagrees with CallCost")
	}
}
