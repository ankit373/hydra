// SPDX-License-Identifier: MIT

//go:build ollama

// Behind a build tag rather than t.Skip: the suite caps skips, and a test that
// needs a model server is a different suite. Run with
// `go test -tags ollama ./internal/retrieve/`.
package retrieve

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/embed"
	"github.com/ankit373/hydra/internal/probe"

	// Providers register in init(); only cmd/hydra imports them.
	_ "github.com/ankit373/hydra/internal/provider/port"
)

// The measurement the issue asks for: lexical, dense and fused over the same
// real corpus, so the claim that a lexical floor is worth having is a number
// rather than an argument.
func TestLive_LexicalVsDenseVsFused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	emb := embed.Resolve(probe.Run(ctx).Heads, "")
	if !emb.Available() {
		t.Fatal("no embedding model; pull one with `ollama pull nomic-embed-text`")
	}

	f, err := os.Open("testdata/corpus.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	ix := newIndex(t)
	vec, err := embed.OpenStore(t.TempDir(), emb.Model())
	if err != nil {
		t.Fatal(err)
	}

	var docs []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		id := fmt.Sprintf("%016x", len(docs))
		if err := ix.Add(id, line); err != nil {
			t.Fatal(err)
		}
		v, err := emb.Embed(ctx, line)
		if err != nil {
			t.Fatalf("embedding the corpus: %v", err)
		}
		if err := vec.Put(id, v); err != nil {
			t.Fatal(err)
		}
		docs = append(docs, line)
	}
	t.Logf("corpus=%d model=%s dim=%d", len(docs), emb.Model(), vec.Dim())

	s := NewSearcher(ix, emb, vec)

	var lex1, den1, fus1, lex5, den5, fus5, asked int
	for i, text := range docs {
		q := everyOtherWord(text)
		if len(Tokenize(q)) < 3 {
			continue
		}
		asked++
		want := fmt.Sprintf("%016x", i)

		l := ids(ix.Lexical(q, 5))
		d := ids(s.dense(ctx, q, 5))
		fu := ids(Top(Fuse(ix.Lexical(q, 20), s.dense(ctx, q, 20)), 5))

		for _, c := range []struct {
			got      []string
			at1, at5 *int
		}{{l, &lex1, &lex5}, {d, &den1, &den5}, {fu, &fus1, &fus5}} {
			if len(c.got) > 0 && c.got[0] == want {
				*c.at1++
			}
			if slices.Contains(c.got, want) {
				*c.at5++
			}
		}
	}

	pct := func(n int) float64 { return float64(n) / float64(asked) * 100 }
	t.Logf("queries=%d", asked)
	t.Logf("  lexical  p@1=%5.1f%%  recall@5=%5.1f%%", pct(lex1), pct(lex5))
	t.Logf("  dense    p@1=%5.1f%%  recall@5=%5.1f%%", pct(den1), pct(den5))
	t.Logf("  fused    p@1=%5.1f%%  recall@5=%5.1f%%", pct(fus1), pct(fus5))
	t.Logf("  random   p@1=%5.2f%%", 100/float64(len(docs)))
}

// The pair #906 names, measured both ways: two prompts differing on one token.
func TestLive_IdentifierPairIsWhereLexicalWins(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	emb := embed.Resolve(probe.Run(ctx).Heads, "")
	if !emb.Available() {
		t.Fatal("no embedding model")
	}

	pairs := [][2]string{
		{"how do I delete this branch", "how do I delete this repo"},
		{"add --max-cost to dispatch", "add --max-heads to dispatch"},
		{"fix the nil panic in internal/awsconf", "fix the nil panic in internal/executor"},
	}
	for _, p := range pairs {
		a, err := emb.Embed(ctx, p[0])
		if err != nil {
			t.Fatal(err)
		}
		b, err := emb.Embed(ctx, p[1])
		if err != nil {
			t.Fatal(err)
		}

		ix := newIndex(t)
		if err := ix.Add("want", p[0]); err != nil {
			t.Fatal(err)
		}
		if err := ix.Add("other", p[1]); err != nil {
			t.Fatal(err)
		}
		lex := ix.Lexical(p[0], 2)

		var margin float64
		if len(lex) == 2 {
			margin = (lex[0].Score - lex[1].Score) / lex[0].Score
		}
		t.Logf("%-38q vs %-38q  cosine=%.4f  lexical margin=%.1f%%",
			p[0], p[1], embed.Cosine(a, b), margin*100)

		if len(lex) == 0 || lex[0].DocID != "want" {
			t.Errorf("lexical failed to separate the pair: %v", ids(lex))
		}
	}
}
