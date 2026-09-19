// SPDX-License-Identifier: MIT

package port

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/ankit373/hydra/internal/capabilities"
	"github.com/ankit373/hydra/internal/provider"
)

// overlaid builds a capability DB carrying one user entry, which is what
// `hyctl models add` writes.
func overlaid(t *testing.T, id string, score int) *capabilities.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "models.json")
	if _, err := capabilities.AddModel(path, capabilities.Entry{
		ID: id, Name: id, Provider: "ollama", CapScore: score,
	}); err != nil {
		t.Fatal(err)
	}
	db, err := capabilities.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// Every local model server scored its heads off family patterns alone, so the
// runtime overlay, the one documented way to retune a model without a rebuild,
// reached every head except the local ones. `hyctl models list` reported the
// user's number and `hyctl probe` reported the pattern's (#989).
func TestLocalServers_UseTheOverlayScoreForADiscoveredHead(t *testing.T) {
	t.Run("ollama", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/tags" {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(`{"models":[{"name":"qwen3:8b"}]}`))
		}))
		defer srv.Close()

		svc := &ollamaService{base: srv.URL}
		heads, err := svc.probe(context.Background(), overlaid(t, "ollama/qwen3:8b", 91))
		if err != nil {
			t.Fatal(err)
		}
		assertScored(t, heads, "ollama/qwen3:8b", 91)
	})

	t.Run("lmstudio", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/models" {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"qwen3-8b"}]}`))
		}))
		defer srv.Close()

		svc := &lmStudioService{base: srv.URL}
		heads, err := svc.probe(context.Background(), overlaid(t, "lmstudio/qwen3-8b", 92))
		if err != nil {
			t.Fatal(err)
		}
		assertScored(t, heads, "lmstudio/qwen3-8b", 92)
	})

	t.Run("litellm", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/health/liveliness":
				_, _ = w.Write([]byte(`"I'm alive!"`))
			case "/model/info":
				_, _ = w.Write([]byte(`{"data":[{"model_name":"fast",
					"litellm_params":{"model":"ollama/qwen3:8b"},
					"model_info":{"litellm_provider":"ollama","mode":"chat"}}]}`))
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()

		svc := &litellmService{base: srv.URL}
		heads, err := svc.probe(context.Background(), overlaid(t, "litellm/fast", 93))
		if err != nil {
			t.Fatal(err)
		}
		assertScored(t, heads, "litellm/fast", 93)
	})

	t.Run("llamacpp", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/props":
				_, _ = w.Write([]byte(`{"build_info":"b4321","model_path":"/m/qwen3-8b.gguf",
					"default_generation_settings":{"n_ctx":8192}}`))
			case "/v1/models":
				_, _ = w.Write([]byte(`{"data":[{"id":"/m/qwen3-8b.gguf"}]}`))
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()

		svc := &llamaCppService{base: srv.URL}
		heads, err := svc.probe(context.Background(), overlaid(t, "llamacpp/qwen3-8b", 94))
		if err != nil {
			t.Fatal(err)
		}
		assertScored(t, heads, "llamacpp/qwen3-8b", 94)
	})
}

// assertScored checks the head carries the overlay's score and says where the
// number came from, since a head reporting "builtin" for a number the user
// chose is the same lie in the provenance field.
func assertScored(t *testing.T, heads []provider.Head, id string, want int) {
	t.Helper()
	for _, h := range heads {
		if h.ID != id {
			continue
		}
		if h.CapScore != want {
			t.Errorf("%s: CapScore = %d, want the overlay's %d; the family pattern is not what the user asked for",
				id, h.CapScore, want)
		}
		if h.Meta["model_source"] != "user" {
			t.Errorf("%s: model_source = %q, want \"user\"", id, h.Meta["model_source"])
		}
		return
	}
	t.Fatalf("head %q was not discovered; heads: %+v", id, heads)
}
