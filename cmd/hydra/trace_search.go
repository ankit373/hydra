// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/probe"
	"github.com/ankit373/hydra/internal/retrieve"
)

// cmdTraceSearch finds past dispatches resembling a query.
//
// The read side of the capture store. Without it the index is write-only, and
// a store nobody can query is indistinguishable from one that does not work.
func cmdTraceSearch() *cobra.Command {
	var jsonOut bool
	var limit int
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Find past dispatches whose prompt resembles this one",
		Args:  cobra.MinimumNArgs(1),
		Long: `hyctl trace search ranks past prompts against a query.

Two scorers over the same documents, fused by rank: a lexical BM25 index that
needs no model at all, and cosine over the stored vectors when an embedding
model is installed. The lexical half is why a fresh install can still search.

Capture is off unless it was chosen at ` + "`hyctl init`" + ` or set with
capture_embeddings in config.toml. Results name the span, so
` + "`hyctl trace view --span <id>`" + ` is what shows the run behind one.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				cfg = &config.Config{}
			}
			if !retrieve.Enabled(cfg) {
				fmt.Println("Capture is off, so there is nothing to search.")
				fmt.Println("Turn it on with capture_embeddings = true in config.toml.")
				return nil
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()

			s, err := retrieve.Open(cfg, probe.Run(ctx).Heads)
			if err != nil {
				return err
			}
			query := strings.Join(args, " ")
			hits, mode := s.Search(ctx, query, limit)

			if jsonOut {
				raw, _ := json.MarshalIndent(map[string]any{
					"query": query, "mode": mode.String(),
					"lexical": mode.Lexical, "dense": mode.Dense,
					"results": hits, "indexed": s.Index().Stat().Docs,
				}, "", "  ")
				fmt.Println(string(raw))
				return nil
			}

			st := s.Index().Stat()
			if st.Docs == 0 {
				fmt.Println("Nothing indexed yet. Run a dispatch with capture on.")
				return nil
			}
			if len(hits) == 0 {
				fmt.Printf("  no match in %d indexed prompt%s\n", st.Docs, plural(st.Docs))
				return nil
			}

			fmt.Printf("  %s\n", dimStyle.Render(fmt.Sprintf(
				"%s over %d indexed prompt%s", mode, st.Docs, plural(st.Docs))))
			for i, h := range hits {
				fmt.Printf("  %2d. %s  %s\n", i+1, h.DocID,
					dimStyle.Render(fmt.Sprintf("score %.4f", h.Score)))
			}
			fmt.Printf("  %s\n", dimStyle.Render("hyctl trace view --span <id> shows the run behind one"))
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable output")
	cmd.Flags().IntVar(&limit, "limit", 10, "how many results to show")
	return cmd
}
