// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/cache"
	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/testutil"
)

// Off is the default, and the report has to say so plainly rather than print a
// table of zeroes that reads like a cache doing nothing useful.
func TestTraceCache_ReportsThatItIsOff(t *testing.T) {
	testutil.NewSandbox(t)

	out, _, err := run(t, "trace", "cache")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "off") || !strings.Contains(out, "cache_answers") {
		t.Errorf("the report does not say the cache is off and how to turn it on:\n%s", out)
	}
}

// Refusals are reported with the same weight as hits: a cache is trustworthy
// in proportion to what it declines, so a report showing only hits would make
// a reckless one look good.
func TestTraceCache_ReportsRefusalsBesideHits(t *testing.T) {
	testutil.NewSandbox(t)
	if err := config.Save(&config.Config{CacheAnswers: true}); err != nil {
		t.Fatal(err)
	}
	seedCache(t)

	out, _, err := run(t, "trace", "cache")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"1 answer", "served", "refused by the content gate", "evicted"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report is missing %q:\n%s", want, out)
		}
	}
	// Two lookups, one served: the denominator has to be what the cache was
	// asked, or every cache reads 100%.
	if !strings.Contains(out, "1 of 2") {
		t.Errorf("the hit rate has no denominator:\n%s", out)
	}
}

func TestTraceCache_JSONCarriesTheThresholdAndStats(t *testing.T) {
	testutil.NewSandbox(t)
	if err := config.Save(&config.Config{CacheAnswers: true, CacheThreshold: 0.97}); err != nil {
		t.Fatal(err)
	}
	seedCache(t)

	out, _, err := run(t, "trace", "cache", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Enabled   bool    `json:"enabled"`
		Threshold float64 `json:"threshold"`
		Present   bool    `json:"present"`
		Stats     struct {
			Entries int   `json:"entries"`
			Hits    int64 `json:"hits"`
			Misses  int64 `json:"misses"`
		} `json:"stats"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unparseable json: %v\n%s", err, out)
	}
	if !got.Enabled || got.Threshold != 0.97 || !got.Present {
		t.Errorf("enabled=%v threshold=%v present=%v", got.Enabled, got.Threshold, got.Present)
	}
	if got.Stats.Entries != 1 || got.Stats.Hits != 1 || got.Stats.Misses != 1 {
		t.Errorf("stats = %+v, want one entry, one hit and one miss", got.Stats)
	}
}

// seedCache stores one answer and asks two questions of it, one of which it
// holds. Driven through the real store so the report reads what a dispatch
// would have written.
func seedCache(t *testing.T) {
	t.Helper()
	st, err := cache.OpenDir(cache.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Put(cache.Entry{
		Prompt: "rotate the signing key", Response: "use hyctl edit",
		Head: "ollama/qwen3:4b", CostUSD: 0.002,
	}); err != nil {
		t.Fatal(err)
	}
	st.Record(st.Lookup("rotate the signing key", nil, cache.DefaultThreshold))
	st.Record(st.Lookup("something else entirely", nil, cache.DefaultThreshold))
}

// The label is what tells a reader the answer was not produced just now, so it
// has to carry how it matched, how old it is, and who produced it.
func TestCacheLabel_SaysHowItMatchedAndHowOld(t *testing.T) {
	exact := cacheLabel(&cache.Hit{
		Entry:      cache.Entry{Head: "ollama/qwen3:4b", CostUSD: 0.0125, TS: time.Now().Add(-90 * time.Second)},
		Similarity: 1, Exact: true,
	})
	for _, want := range []string{"same prompt", "ollama/qwen3:4b", "saved", "ago"} {
		if !strings.Contains(exact, want) {
			t.Errorf("the exact label is missing %q: %s", want, exact)
		}
	}

	near := cacheLabel(&cache.Hit{
		Entry:      cache.Entry{Head: "h1", TS: time.Now()},
		Similarity: 0.9712,
	})
	if !strings.Contains(near, "97.1% similar") {
		t.Errorf("a near match does not report its similarity: %s", near)
	}
	// Nothing was spent producing a free head's answer, so nothing is claimed
	// saved by reusing it.
	if strings.Contains(near, "saved") {
		t.Errorf("a free answer claimed a saving: %s", near)
	}
}

// A result no cache served must print nothing at all, or every ordinary
// dispatch grows a line about a cache it never touched.
func TestPrintCacheHit_WritesNothingWithoutAHit(t *testing.T) {
	out := captureStdout(t, func() { printCacheHit(&dispatch.Result{Output: "hello"}) })
	if out != "" {
		t.Errorf("printed %q for a result with no cache hit", out)
	}

	out = captureStdout(t, func() {
		printCacheHit(&dispatch.Result{
			Output: "hello",
			Cache: &cache.Hit{
				Entry: cache.Entry{Head: "h1", TS: time.Now()}, Similarity: 1, Exact: true,
			},
		})
	})
	if !strings.Contains(out, "from cache") || !strings.Contains(out, "hello") {
		t.Errorf("the served answer was not labelled:\n%s", out)
	}
}

// Turning the cache off does not delete what it already holds, and a report
// that said nothing about it would leave answers on disk unaccounted for.
func TestTraceCache_OffStillNamesAStoreLeftBehind(t *testing.T) {
	testutil.NewSandbox(t)
	seedCache(t)

	out, _, err := run(t, "trace", "cache")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "earlier run") || !strings.Contains(out, "1 answer") {
		t.Errorf("the report hides a store left on disk:\n%s", out)
	}
}
