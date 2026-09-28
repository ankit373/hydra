// SPDX-License-Identifier: MIT

package retrieve

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/embed"
)

// stubEmbedder maps text to a vector by hashing its terms into buckets, so two
// texts sharing terms land near each other. Enough to exercise the dense path
// deterministically without a model.
type stubEmbedder struct{ dim int }

func (s stubEmbedder) Available() bool { return true }
func (s stubEmbedder) Model() string   { return "stub" }
func (s stubEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	v := make([]float32, s.dim)
	for _, tok := range Tokenize(text) {
		h := 0
		for _, r := range tok {
			h = h*31 + int(r)
		}
		v[((h%s.dim)+s.dim)%s.dim] += 1
	}
	return v, nil
}

func newSearcher(t *testing.T, emb embed.Embedder, withVectors bool) *Searcher {
	t.Helper()
	ix := newIndex(t)
	var vec *embed.Store
	if withVectors {
		var err error
		vec, err = embed.OpenStore(t.TempDir(), "stub")
		if err != nil {
			t.Fatal(err)
		}
	}
	return NewSearcher(ix, emb, vec)
}

// The acceptance criterion this issue turns on: with no embedder, the fused
// result must be the lexical result, element for element and score for score.
func TestSearch_WithNoEmbedderIsExactlyLexical(t *testing.T) {
	s := newSearcher(t, embed.Unavailable{}, false)
	for i, text := range []string{
		"rotate the signing key in internal/auth/token.go",
		"add pagination to the results list",
		"how do I delete this branch",
		"how do I delete this repo",
		"rotate the signing key again",
	} {
		mustAdd(t, s.Index(), fmt.Sprintf("doc%d", i), text)
	}

	const q = "rotate the signing key"
	want := s.Index().Lexical(q, 5)
	got, mode := s.Search(context.Background(), q, 5)

	if mode.Dense || !mode.Lexical || mode.String() != "lexical" {
		t.Fatalf("want lexical-only mode, got %+v (%s)", mode, mode)
	}
	if len(got) != len(want) {
		t.Fatalf("length differs: %d vs %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("at %d: fused %+v, lexical %+v", i, got[i], want[i])
		}
	}
}

func TestSearch_ModeNamesWhatContributed(t *testing.T) {
	ctx := context.Background()

	s := newSearcher(t, embed.Unavailable{}, false)
	mustAdd(t, s.Index(), "a", "rotate the signing key")
	if _, m := s.Search(ctx, "signing", 5); m.String() != "lexical" {
		t.Errorf("want lexical, got %s", m)
	}
	if _, m := s.Search(ctx, "kubernetes", 5); m.String() != "none" {
		t.Errorf("no match must report none, got %s", m)
	}

	// A vector store with an embedder but nothing stored still has no dense
	// list, and must not claim one.
	h := newSearcher(t, stubEmbedder{dim: 32}, true)
	mustAdd(t, h.Index(), "a", "rotate the signing key")
	if _, m := h.Search(ctx, "signing", 5); m.Dense {
		t.Errorf("claimed a dense half with no vectors stored: %s", m)
	}
}

func TestSearch_HybridWhenBothHalvesHaveSomething(t *testing.T) {
	ctx := context.Background()
	emb := stubEmbedder{dim: 32}
	s := newSearcher(t, emb, true)

	texts := []string{
		"rotate the signing key in internal/auth/token.go",
		"add pagination to the results list",
		"how do I delete this branch",
	}
	for i, text := range texts {
		id := spanID(i)
		mustAdd(t, s.Index(), id, text)
		v, err := emb.Embed(ctx, text)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.vec.Put(id, v); err != nil {
			t.Fatal(err)
		}
	}

	got, mode := s.Search(ctx, "rotate the signing key", 3)
	if !mode.Lexical || !mode.Dense || mode.String() != "hybrid" {
		t.Fatalf("want hybrid, got %s", mode)
	}
	if len(got) == 0 || got[0].DocID != spanID(0) {
		t.Fatalf("want the signing-key document first, got %v", ids(got))
	}
}

// A failing embedder must degrade to the lexical answer, not to no answer.
func TestSearch_FailingEmbedderDegradesToLexical(t *testing.T) {
	s := newSearcher(t, failingEmbedder{}, true)
	mustAdd(t, s.Index(), "a", "rotate the signing key")
	got, mode := s.Search(context.Background(), "signing key", 5)
	if len(got) != 1 || got[0].DocID != "a" {
		t.Fatalf("want the lexical answer, got %v", ids(got))
	}
	if mode.Dense {
		t.Error("a failed embed must not be reported as a dense contribution")
	}
}

type failingEmbedder struct{}

func (failingEmbedder) Available() bool { return true }
func (failingEmbedder) Model() string   { return "failing" }
func (failingEmbedder) Embed(context.Context, string) ([]float32, error) {
	return nil, fmt.Errorf("model gone")
}

func TestSearch_NilSearcherAnswersEmpty(t *testing.T) {
	var s *Searcher
	if got, m := s.Search(context.Background(), "anything", 5); got != nil || m.String() != "none" {
		t.Fatalf("got %v %s", ids(got), m)
	}
}

func TestOpen_OffUnlessOptedIn(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())
	s, err := Open(&config.Config{}, nil)
	if err != nil || s != nil {
		t.Fatalf("want no searcher when capture is off, got %v %v", s, err)
	}
}

// The lexical half needs no model, so opting in must give a working searcher on
// a machine with nothing installed. That is the entire point of this issue.
func TestOpen_LexicalWorksWithNoModel(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())
	s, err := Open(&config.Config{CaptureEmbeddings: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s == nil {
		t.Fatal("want a searcher with no model installed")
	}
	mustAdd(t, s.Index(), "a", "rotate the signing key")
	got, mode := s.Search(context.Background(), "signing key", 5)
	if len(got) != 1 || mode.String() != "lexical" {
		t.Fatalf("got %v %s", ids(got), mode)
	}
}

func TestBudget_SharesTheEmbeddingKnob(t *testing.T) {
	if got := Budget(nil); got != DefaultBudgetBytes {
		t.Errorf("want the default, got %d", got)
	}
	if got := Budget(&config.Config{EmbedBudgetMB: 8}); got != 8<<20 {
		t.Errorf("want 8 MB, got %d", got)
	}
}

func TestEnabled_IsTheEmbeddingGate(t *testing.T) {
	if Enabled(&config.Config{}) {
		t.Error("off by default")
	}
	if !Enabled(&config.Config{CaptureEmbeddings: true}) {
		t.Error("the shared opt-in was not honoured")
	}
}

// Known-item retrieval over this repo's own commit subjects: index all of them,
// then query each with half its words and see whether it comes back first.
//
// Real text rather than invented prompts, and no hand labels: the document a
// query was built from is its own ground truth. Random ranking over this corpus
// would score about 1/N, which is what makes the number mean something.
func TestMeasure_KnownItemPrecisionOnRealCommitSubjects(t *testing.T) {
	ix := newIndex(t)
	docs := loadCorpus(t, ix)
	if len(docs) < 50 {
		t.Fatalf("corpus too small to measure: %d", len(docs))
	}

	var at1, at5, asked int
	for i, text := range docs {
		q := everyOtherWord(text)
		if len(Tokenize(q)) < 3 {
			continue
		}
		asked++
		want := fmt.Sprintf("doc%d", i)
		got := ids(ix.Lexical(q, 5))
		if len(got) > 0 && got[0] == want {
			at1++
		}
		if slices.Contains(got, want) {
			at5++
		}
	}

	p1 := float64(at1) / float64(asked) * 100
	p5 := float64(at5) / float64(asked) * 100
	t.Logf("corpus=%d queries=%d  lexical p@1=%.1f%%  recall@5=%.1f%%  (random p@1 would be %.2f%%)",
		len(docs), asked, p1, p5, 100/float64(len(docs)))

	// A floor, not the measurement. Half the words of a real commit subject
	// should find it far more often than not; well below this means the
	// tokenizer or the scoring regressed, not that the corpus got harder.
	if p1 < 60 {
		t.Errorf("known-item p@1 fell to %.1f%%", p1)
	}
}

// everyOtherWord keeps alternate whitespace-separated words, so the query is a
// real subset of the document rather than the whole of it.
func everyOtherWord(s string) string {
	fields := strings.Fields(s)
	var out []string
	for i := 0; i < len(fields); i += 2 {
		out = append(out, fields[i])
	}
	return strings.Join(out, " ")
}

// spanID builds a 16-character document id. Zero-padded decimal rather than
// random-looking hex: a high-entropy literal beside a quoted string is what a
// secret scanner is built to flag, and it flagged this file.
func spanID(n int) string { return fmt.Sprintf("%016d", n) }
