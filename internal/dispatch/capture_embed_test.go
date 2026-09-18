// SPDX-License-Identifier: MIT

package dispatch

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/embed"
	"github.com/ankit373/hydra/internal/provider"
)

// An embedding is not plaintext, but inversion recovers approximate text from
// one. Nothing may be vectorised unless someone opted in.
func TestCaptureEmbedding_WritesNothingWhenNotOptedIn(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())

	d := &Dispatcher{cfg: &config.Config{}} // CaptureEmbeddings false
	d.captureEmbedding("0123456789abcdef", "a secret prompt")
	d.Close()

	if _, err := os.Stat(filepath.Join(embed.Dir(), "vectors.dat")); !os.IsNotExist(err) {
		t.Fatalf("a vector store was created without an opt-in: %v", err)
	}
}

// Opted in on a machine with no embedding model is the common case, and it must
// be silent rather than an error on every dispatch.
func TestCaptureEmbedding_OptedInWithNoModelIsSilent(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())

	d := &Dispatcher{
		cfg:   &config.Config{CaptureEmbeddings: true},
		heads: []provider.Head{{ID: "ollama/llama3", Endpoint: "http://127.0.0.1:1", Meta: map[string]string{}}},
	}
	d.captureEmbedding("0123456789abcdef", "a prompt")
	d.Close()

	if _, err := os.Stat(filepath.Join(embed.Dir(), "vectors.dat")); !os.IsNotExist(err) {
		t.Fatalf("a store appeared with no embedding model: %v", err)
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
