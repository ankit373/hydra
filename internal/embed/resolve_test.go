// SPDX-License-Identifier: MIT

package embed

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/provider"
)

func TestEnabled(t *testing.T) {
	if Enabled(nil) {
		t.Error("no config must not read as opted in")
	}
	if Enabled(&config.Config{}) {
		t.Error("the zero config must not read as opted in")
	}
	if !Enabled(&config.Config{CaptureEmbeddings: true}) {
		t.Error("an explicit opt-in was not honoured")
	}
}

func TestBudget(t *testing.T) {
	if got := Budget(nil); got != DefaultBudgetBytes {
		t.Errorf("no config: want the default, got %d", got)
	}
	if got := Budget(&config.Config{}); got != DefaultBudgetBytes {
		t.Errorf("unset: want the default, got %d", got)
	}
	if got := Budget(&config.Config{EmbedBudgetMB: 8}); got != 8<<20 {
		t.Errorf("want 8 MB, got %d", got)
	}
	// A negative budget is nonsense, not an instruction to disable the bound.
	if got := Budget(&config.Config{EmbedBudgetMB: -1}); got != DefaultBudgetBytes {
		t.Errorf("negative: want the default, got %d", got)
	}
}

// The three outcomes a caller has to tell apart.
func TestOpen_ThreeStates(t *testing.T) {
	embedding := []provider.Head{{
		ID: "ollama/nomic-embed-text", Endpoint: "http://127.0.0.1:11434",
		Meta: map[string]string{"embedding_only": "true"},
	}}

	t.Run("capture off", func(t *testing.T) {
		e, st, err := Open(&config.Config{}, embedding)
		if err != nil || st != nil || e.Available() {
			t.Fatalf("want off with no store, got %v %v %v", e.Available(), st, err)
		}
	})

	t.Run("on with no model", func(t *testing.T) {
		e, st, err := Open(&config.Config{CaptureEmbeddings: true}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if e.Available() || st != nil {
			t.Fatalf("want unavailable with no store, got %v %v", e.Available(), st)
		}
	})

	t.Run("ready", func(t *testing.T) {
		t.Setenv("HYDRA_HOME", t.TempDir())
		cfg := &config.Config{CaptureEmbeddings: true, EmbedBudgetMB: 4}
		e, st, err := Open(cfg, embedding)
		if err != nil {
			t.Fatal(err)
		}
		if !e.Available() || st == nil {
			t.Fatalf("want a ready store, got %v %v", e.Available(), st)
		}
		if got := st.Stat().Budget; got != 4<<20 {
			t.Errorf("the configured budget was not applied: %d", got)
		}
		if st.Model() != e.Model() {
			t.Errorf("store %q and embedder %q disagree about the model", st.Model(), e.Model())
		}
	})
}

func TestDir_IsUnderTheConfigDir(t *testing.T) {
	t.Setenv("HYDRA_HOME", filepath.Join("x", "y"))
	if got := Dir(); !strings.HasSuffix(got, filepath.Join("x", "y", "embeddings")) {
		t.Errorf("unexpected store dir %q", got)
	}
}

func TestUnavailable_ReportsItself(t *testing.T) {
	var e Embedder = Unavailable{}
	if e.Model() != "" || e.Available() {
		t.Error("Unavailable must report no model")
	}
}

func TestStore_ReportsItsIdentity(t *testing.T) {
	s := newTestStore(t, "nomic-embed-text")
	if s.Model() != "nomic-embed-text" || s.Dim() != 0 {
		t.Fatalf("model=%q dim=%d", s.Model(), s.Dim())
	}
	if err := s.Put(spanID(1), vec(12, 1)); err != nil {
		t.Fatal(err)
	}
	if s.Dim() != 12 {
		t.Fatalf("dimension not learned from the first vector: %d", s.Dim())
	}
}
