//go:build cachebench

// SPDX-License-Identifier: MIT

// Build-tagged: this needs a live Ollama with an embedding model, which CI has
// no business depending on. Run it by hand when the matching rule changes:
//
//	go test ./internal/cache -tags cachebench -run TestFalseHitRate -v
package cache

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/embed"
	"github.com/ankit373/hydra/internal/provider"
)

// Every pair of distinct commit subjects is a pair of distinct questions, so
// any pair the cache would serve is a false hit. That is what makes a real
// corpus usable as ground truth here without anyone labelling it.
func TestFalseHitRate(t *testing.T) {
	raw, err := os.ReadFile("../retrieve/testdata/corpus.txt")
	if err != nil {
		t.Fatal(err)
	}
	var docs []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			docs = append(docs, Normalize(s))
		}
	}

	emb := embed.Resolve([]provider.Head{{
		ID: "ollama/nomic-embed-text:latest", Provider: "ollama", Source: "port",
		Endpoint: "http://localhost:11434",
		Meta:     map[string]string{"model": "nomic-embed-text:latest", "embedding_only": "true"},
	}}, "")
	if !emb.Available() {
		t.Fatal("no embedding model; start ollama and pull nomic-embed-text")
	}

	vecs := make([][]float32, len(docs))
	terms := make([][]string, len(docs))
	for i, d := range docs {
		v, err := emb.Embed(context.Background(), d)
		if err != nil {
			t.Fatalf("embed %d: %v", i, err)
		}
		vecs[i], terms[i] = v, content(d)
	}

	type row struct {
		thr            float64
		cosineOnly     int
		withTokenGate  int
		maxCosine      float64
		worstA, worstB string
	}
	pairs := 0
	rows := []*row{{thr: 0.80}, {thr: 0.85}, {thr: 0.90}, {thr: 0.95}, {thr: 0.98}}
	for i := 0; i < len(docs); i++ {
		for j := i + 1; j < len(docs); j++ {
			pairs++
			sim := embed.Cosine(vecs[i], vecs[j])
			same := sameQuestion(terms[i], terms[j])
			for _, r := range rows {
				if sim >= r.thr {
					r.cosineOnly++
					if sim > r.maxCosine {
						r.maxCosine, r.worstA, r.worstB = sim, docs[i], docs[j]
					}
					if same {
						r.withTokenGate++
					}
				}
			}
		}
	}

	fmt.Printf("\n%d documents, %d distinct pairs, every one of them a pair that must not be served\n\n", len(docs), pairs)
	fmt.Printf("  %-10s %14s %14s %10s\n", "threshold", "cosine alone", "+ token gate", "worst cos")
	for _, r := range rows {
		fmt.Printf("  %-10.2f %8d %5.3f%% %8d %5.3f%% %10.4f\n",
			r.thr,
			r.cosineOnly, 100*float64(r.cosineOnly)/float64(pairs),
			r.withTokenGate, 100*float64(r.withTokenGate)/float64(pairs),
			r.maxCosine)
	}
	for _, r := range rows {
		if r.worstA != "" {
			fmt.Printf("\n  closest pair at %.2f (cos %.4f):\n    %s\n    %s\n", r.thr, r.maxCosine, r.worstA, r.worstB)
			break
		}
	}

	// The true-hit side: a question asked again in a way that changes nothing
	// about what it asks must still be served, or the cache is a hash map.
	perturb := []struct {
		name string
		fn   func(string) string
	}{
		{"case", strings.ToUpper},
		{"trailing punctuation", func(s string) string { return s + "?" }},
		{"whitespace", func(s string) string { return "  " + strings.ReplaceAll(s, " ", "   ") + "\n" }},
		{"filler words", func(s string) string { return "please can you " + s + " for me" }},
	}
	fmt.Printf("\n  %-22s %10s %10s\n", "perturbation", "served", "exact")
	for _, p := range perturb {
		served, exact := 0, 0
		for i, d := range docs {
			q := Normalize(p.fn(d))
			if Key(q) == Key(d) {
				exact++
				served++
				continue
			}
			v, err := emb.Embed(context.Background(), q)
			if err != nil {
				t.Fatal(err)
			}
			if embed.Cosine(v, vecs[i]) >= DefaultThreshold && sameQuestion(content(q), terms[i]) {
				served++
			}
		}
		fmt.Printf("  %-22s %8d %3.0f%% %8d\n", p.name, served, 100*float64(served)/float64(len(docs)), exact)
	}
	fmt.Println()
}
