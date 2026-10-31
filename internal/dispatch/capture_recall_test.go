// SPDX-License-Identifier: MIT

package dispatch

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/embed"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/retrieve"
)

// An embedding is not plaintext, but inversion recovers approximate text from
// one. Nothing may be vectorised unless someone opted in.
func TestCaptureRecall_WritesNothingWhenNotOptedIn(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())

	d := &Dispatcher{cfg: &config.Config{}} // CaptureEmbeddings false
	d.captureRecall("0123456789abcdef", "a secret prompt")
	d.Close()

	if _, err := os.Stat(filepath.Join(embed.Dir(), "vectors.dat")); !os.IsNotExist(err) {
		t.Fatalf("a vector store was created without an opt-in: %v", err)
	}
	if _, err := os.Stat(filepath.Join(retrieve.Dir(), "docs.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("a lexical index was created without an opt-in: %v", err)
	}
}

// A machine with no embedding model is the common case, and it is exactly the
// one #911 exists for: the lexical half must still index, and only the vector
// half stays absent.
func TestCaptureRecall_OptedInWithNoModelStillIndexes(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())

	d := &Dispatcher{
		cfg:   &config.Config{CaptureEmbeddings: true},
		heads: []provider.Head{{ID: "ollama/llama3", Endpoint: "http://127.0.0.1:1", Meta: map[string]string{}}},
	}
	d.captureRecall("0123456789abcdef", "rotate the signing key")
	d.Close()

	if _, err := os.Stat(filepath.Join(embed.Dir(), "vectors.dat")); !os.IsNotExist(err) {
		t.Fatalf("a vector store appeared with no embedding model: %v", err)
	}

	ix, err := retrieve.OpenIndex(retrieve.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if got := ix.Stat().Docs; got != 1 {
		t.Fatalf("want the prompt indexed lexically, got %d documents", got)
	}
	if hits := ix.Lexical("signing key", 1); len(hits) != 1 {
		t.Fatalf("the indexed prompt is not retrievable: %v", hits)
	}
}

// Close is wired at seven construction sites and drains a background worker, so
// a nil receiver has to be a no-op rather than a panic in a deferred call.
func TestClose_SurvivesANilDispatcher(t *testing.T) {
	var d *Dispatcher
	d.Close()
}

func TestClose_IsIdempotent(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())
	d := &Dispatcher{cfg: &config.Config{CaptureEmbeddings: true}}
	d.Close()
	d.Close()
}
