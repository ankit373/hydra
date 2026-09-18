// SPDX-License-Identifier: MIT

package retrieve

import (
	"context"
	"time"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/embed"
	"github.com/ankit373/hydra/internal/provider"
)

// Mode says which scorers actually contributed to a result set, so a caller can
// report "lexical only" rather than implying a dense half that was not there.
type Mode struct {
	Lexical bool `json:"lexical"`
	Dense   bool `json:"dense"`
}

// String names the mode for a human.
func (m Mode) String() string {
	switch {
	case m.Lexical && m.Dense:
		return "hybrid"
	case m.Dense:
		return "dense"
	case m.Lexical:
		return "lexical"
	}
	return "none"
}

// Searcher scores a query against past prompts, lexically and, where a model
// exists, densely as well.
type Searcher struct {
	ix  *Index
	emb embed.Embedder
	vec *embed.Store
}

// NewSearcher builds a searcher over an index and an optional vector store.
func NewSearcher(ix *Index, emb embed.Embedder, vec *embed.Store) *Searcher {
	return &Searcher{ix: ix, emb: emb, vec: vec}
}

// Enabled reports whether capture was opted into.
//
// Deliberately internal/embed's gate rather than a second one: a bag of words
// and a vector are both derived from the prompt and carry the same decision, so
// two switches would let someone turn off the one they had heard of.
func Enabled(cfg *config.Config) bool { return embed.Enabled(cfg) }

// Budget resolves the index's byte budget, reusing the embedding budget rather
// than adding a knob. It bounds each store separately; a bag of words is about
// an order of magnitude smaller than a vector, so this is never the binding
// constraint in practice.
func Budget(cfg *config.Config) int64 {
	if cfg != nil && cfg.EmbedBudgetMB > 0 {
		return int64(cfg.EmbedBudgetMB) << 20
	}
	return DefaultBudgetBytes
}

// Open resolves the searcher from config and discovered heads.
//
// The lexical half needs nothing, so it is present whenever capture is on. The
// dense half joins it only when a model does, which is the whole point: a fresh
// install retrieves rather than waiting for someone to pull a model.
func Open(cfg *config.Config, heads []provider.Head) (*Searcher, error) {
	if !Enabled(cfg) {
		return nil, nil
	}
	ix, err := OpenIndex(Dir())
	if err != nil {
		return nil, err
	}
	ix.SetBudget(Budget(cfg))

	emb, vec, err := embed.Open(cfg, heads)
	if err != nil {
		// A vector store that will not open must not cost the lexical half,
		// which is the one that works everywhere.
		return NewSearcher(ix, embed.Unavailable{}, nil), nil
	}
	return NewSearcher(ix, emb, vec), nil
}

// Index exposes the lexical index for writing.
func (s *Searcher) Index() *Index { return s.ix }

// Search returns the best k documents, fusing whichever scorers are available.
func (s *Searcher) Search(ctx context.Context, query string, k int) ([]Result, Mode) {
	if s == nil || s.ix == nil {
		return nil, Mode{}
	}
	var mode Mode
	var lists [][]Result

	// Over-fetch each list: fusion reorders, so a document that is k+1 in one
	// list can be top-k after fusing, and cutting before the fuse would lose it.
	over := k * 4
	if over <= 0 {
		over = 0
	}

	if lex := s.ix.Lexical(query, over); len(lex) > 0 {
		mode.Lexical = true
		lists = append(lists, lex)
	}
	if den := s.dense(ctx, query, over); len(den) > 0 {
		mode.Dense = true
		lists = append(lists, den)
	}

	switch len(lists) {
	case 0:
		return nil, mode
	case 1:
		// Fuse of one list is that list's order, but returning it directly
		// keeps the original scores rather than replacing them with a
		// reciprocal rank nobody asked about.
		return Top(lists[0], k), mode
	default:
		return Top(Fuse(lists...), k), mode
	}
}

// dense ranks by cosine against the stored vectors.
//
// A missing or failing embedder yields nothing rather than an error: the
// lexical half is still a correct answer, and degrading to it is the design.
func (s *Searcher) dense(ctx context.Context, query string, k int) []Result {
	if s.emb == nil || !s.emb.Available() || s.vec == nil {
		return nil
	}
	q, err := s.emb.Embed(ctx, query)
	if err != nil {
		return nil
	}

	var out []Result
	err = s.vec.Each(func(id string, _ time.Time, v []float32) bool {
		if sim := embed.Cosine(q, v); sim > 0 {
			out = append(out, Result{DocID: id, Score: sim})
		}
		return true
	})
	if err != nil {
		return nil
	}
	sortResults(out)
	return Top(out, k)
}
