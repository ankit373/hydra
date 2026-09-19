// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ankit373/hydra/internal/cache"
	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/dispatch"
)

// cmdTraceCache reports the opt-in answer cache.
//
// The refusal count is given the same weight as the hit count on purpose. A
// cache is only trustworthy in proportion to what it declines to answer, so a
// report that showed hits alone would make a reckless cache look like a good
// one.
func cmdTraceCache() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Answer cache: what it holds, what it served, and what it refused",
		Long: `hyctl trace cache reports the opt-in store of answers to earlier dispatches.

Caching is off unless it was chosen at ` + "`hyctl init`" + ` or set with
cache_answers in config.toml. An exact match on the normalized prompt is served
outright; anything else has to clear both a cosine threshold and a gate
requiring the two prompts to ask about exactly the same things.

Refusals are prompts the similarity alone would have served and that gate
stopped. That number is the evidence the gate is worth having.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				cfg = &config.Config{}
			}
			st, present := cache.StoredStats(cache.Dir())

			if jsonOut {
				raw, _ := json.MarshalIndent(map[string]any{
					"enabled":      cache.Enabled(cfg),
					"threshold":    cache.Threshold(cfg, ""),
					"budget_bytes": cache.Budget(cfg),
					"present":      present,
					"stats":        st,
				}, "", "  ")
				fmt.Println(string(raw))
				return nil
			}

			if !cache.Enabled(cfg) {
				fmt.Println("The answer cache is off.")
				fmt.Println("Turn it on with cache_answers = true in config.toml.")
				if present {
					fmt.Printf("  %s\n", dimStyle.Render(fmt.Sprintf(
						"a store from an earlier run is still on disk: %d answer%s in %s",
						st.Entries, plural(st.Entries), humanBytes(st.Bytes))))
				}
				return nil
			}

			fmt.Printf("  %d answer%s in %s · threshold %.2f · budget %s\n",
				st.Entries, plural(st.Entries), humanBytes(st.Bytes),
				cache.Threshold(cfg, ""), humanBytes(cache.Budget(cfg)))

			asked := st.Hits + st.Misses
			if asked == 0 {
				fmt.Printf("  %s\n", dimStyle.Render("nothing has been looked up yet"))
				return nil
			}
			fmt.Printf("  %d of %d dispatches served (%.1f%%): %d exact, %d near\n",
				st.Hits, asked, 100*float64(st.Hits)/float64(asked), st.Exact, st.Near)
			fmt.Printf("  %s\n", dimStyle.Render(fmt.Sprintf(
				"$%.4f avoided, at what the same answers cost to produce", st.AvoidedUSD)))
			fmt.Printf("  %d refused by the content gate · %d evicted\n", st.Refused, st.Evicted)
			if st.Oldest != nil && st.Newest != nil {
				fmt.Printf("  %s\n", dimStyle.Render(fmt.Sprintf(
					"oldest %s ago · newest %s ago", since(*st.Oldest), since(*st.Newest))))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable output")
	return cmd
}

// cacheLabel is how a served answer announces itself. Never silent: an answer
// nobody just produced is a different thing from one somebody did, and the
// reader is the one who gets to decide whether that matters here.
func cacheLabel(hit *cache.Hit) string {
	how := fmt.Sprintf("%.1f%% similar", hit.Similarity*100)
	if hit.Exact {
		how = "same prompt"
	}
	saved := ""
	if hit.CostUSD > 0 {
		saved = fmt.Sprintf(", saved ~$%.4f", hit.CostUSD)
	}
	return fmt.Sprintf("%s, answered %s ago by %s%s", how, since(hit.TS), hit.Head, saved)
}

// printCacheHit renders a served answer above the answer itself.
func printCacheHit(r *dispatch.Result) {
	if r.Cache == nil {
		return
	}
	fmt.Println()
	fmt.Printf("  %s %s\n", cortexStyle.Render("⚡ from cache"), dimStyle.Render(cacheLabel(r.Cache)))
	fmt.Println(dimStyle.Render("  " + strings.Repeat("─", 56)))
	fmt.Println()
	fmt.Println(r.Output)
	fmt.Println()
}
