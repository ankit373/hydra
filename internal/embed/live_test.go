// SPDX-License-Identifier: MIT

//go:build ollama

// Behind a build tag rather than t.Skip: the suite caps skips, and a test that
// needs a model server is not a skip, it is a different suite. Run with
// `go test -tags ollama ./internal/embed/`.
package embed

import (
	"context"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/probe"

	// Providers register themselves in init(). Only cmd/hydra imports them, so
	// a test driving discovery has to pull the one it needs in itself.
	_ "github.com/ankit373/hydra/internal/provider/port"
)

func TestLive_OllamaEmbeds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	e := Resolve(probe.Run(ctx).Heads, "")
	if !e.Available() {
		t.Fatal("no embedding model discovered; pull one with `ollama pull nomic-embed-text`")
	}

	start := time.Now()
	a, err := e.Embed(ctx, "how do I delete this branch")
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	t.Logf("model=%s dim=%d first-call=%v", e.Model(), len(a), time.Since(start).Round(time.Millisecond))

	same, err := e.Embed(ctx, "how do I delete this branch")
	if err != nil {
		t.Fatal(err)
	}
	near, err := e.Embed(ctx, "how do I delete this repo")
	if err != nil {
		t.Fatal(err)
	}
	far, err := e.Embed(ctx, "write a haiku about the sea")
	if err != nil {
		t.Fatal(err)
	}

	// The pair #906 names as the cache's failure mode, measured rather than
	// asserted, so the threshold that phase picks has a real number behind it.
	t.Logf("identical      cos=%.4f", Cosine(a, same))
	t.Logf("branch vs repo cos=%.4f", Cosine(a, near))
	t.Logf("unrelated      cos=%.4f", Cosine(a, far))

	if got := Cosine(a, same); got < 0.999 {
		t.Fatalf("the same text must embed to the same vector, got %.4f", got)
	}
	if Cosine(a, near) <= Cosine(a, far) {
		t.Fatal("a near-miss must score above an unrelated prompt")
	}
}
