// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/embed"
	"github.com/ankit373/hydra/internal/probe"
	"github.com/ankit373/hydra/internal/retrieve"
)

// cmdTraceEmbeddings reports the opt-in vector store.
//
// Three states, and they are reported apart: capture off, capture on with no
// model on the machine, and ready. The middle one is the whole reason the
// command exists, since it is silent everywhere else.
func cmdTraceEmbeddings() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "embeddings",
		Short: "Vector store: whether embedding is available, and what it holds",
		Long: `hyctl trace embeddings reports the opt-in store of one vector per dispatch.

Capture is off unless it was chosen at ` + "`hyctl init`" + ` or set with
capture_embeddings in config.toml. Vectors come from an embedding model already
running on the machine, usually under Ollama; with none, capture is off and
every feature that reads a vector is off with it.

Text is redacted before it is embedded, and the model is part of the store's
key: changing it invalidates the store rather than mixing two vector spaces.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// No config means capture was never opted into, which is the answer,
			// not a plumbing error to report in place of it.
			cfg, err := config.Load()
			if err != nil {
				cfg = &config.Config{}
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			defer cancel()
			res := probe.Run(ctx)

			emb, store, err := embed.Open(cfg, res.Heads)
			if err != nil {
				return err
			}
			var st embed.Stats
			if store != nil {
				st = store.Stat()
			}

			if jsonOut {
				raw, _ := json.MarshalIndent(map[string]any{
					"capture_enabled": embed.Enabled(cfg),
					"available":       emb.Available(),
					"model":           emb.Model(),
					"budget_bytes":    embed.Budget(cfg),
					"stats":           st,
					"lexical":         lexStat(cfg),
				}, "", "  ")
				fmt.Println(string(raw))
				return nil
			}

			if !embed.Enabled(cfg) {
				fmt.Println("Embedding capture is off.")
				fmt.Println("Turn it on with capture_embeddings = true in config.toml.")
				return nil
			}
			lex := lexStat(cfg)
			fmt.Printf("  lexical index: %d prompt%s, %d term%s in %s\n",
				lex.Docs, plural(lex.Docs), lex.Terms, plural(lex.Terms), humanBytes(lex.Bytes))

			if !emb.Available() {
				fmt.Println("  no embedding model found, so search is lexical only.")
				fmt.Printf("  %s\n", dimStyle.Render(
					"pull one, e.g. `ollama pull nomic-embed-text`, or name it with embed_model in config.toml"))
				return nil
			}

			fmt.Printf("  model %s · %d dimensions\n", emb.Model(), st.Dim)
			if st.Count == 0 {
				fmt.Printf("  %s\n", dimStyle.Render("no vectors stored yet"))
				return nil
			}
			fmt.Printf("  %d vector%s in %s\n", st.Count, plural(st.Count), humanBytes(st.Bytes))
			fmt.Printf("  %s\n", dimStyle.Render(fmt.Sprintf(
				"budget %s, oldest dropped past it · holds about %d more",
				humanBytes(st.Budget), remaining(st))))
			if st.Oldest != nil && st.Newest != nil {
				fmt.Printf("  %s\n", dimStyle.Render(fmt.Sprintf(
					"oldest %s ago · newest %s ago",
					since(*st.Oldest), since(*st.Newest))))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable output")
	return cmd
}

// remaining is how many more vectors fit before eviction starts.
func remaining(st embed.Stats) int64 {
	if st.Count == 0 || st.Bytes == 0 {
		return 0
	}
	per := st.Bytes / int64(st.Count)
	if per == 0 || st.Budget <= st.Bytes {
		return 0
	}
	return (st.Budget - st.Bytes) / per
}

func since(t time.Time) string {
	d := time.Since(t).Round(time.Second)
	if d < 0 {
		return "0s"
	}
	return d.String()
}

// lexStat reads the lexical index's size without creating it, so reporting
// cannot make its own answer true.
func lexStat(cfg *config.Config) retrieve.Stats {
	ix, err := retrieve.OpenIndex(retrieve.Dir())
	if err != nil {
		return retrieve.Stats{}
	}
	ix.SetBudget(retrieve.Budget(cfg))
	return ix.Stat()
}
