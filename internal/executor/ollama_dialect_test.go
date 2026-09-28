// SPDX-License-Identifier: MIT

package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/provider"
	_ "github.com/ankit373/hydra/internal/provider/port" // registers the discovery this asserts on
	"github.com/ankit373/hydra/internal/testutil"
)

// For had a branch selecting a native /api/generate executor on Source or
// Provider == "ollama", and no provider has ever stamped either, so every real
// Ollama dispatch went to HTTPExecutor and the dialect was unreachable (#819).
//
// A hand-written head cannot catch that: one built with Provider: "ollama"
// passes while the real one fails, which is how it shipped. So this discovers
// a head the way `hyctl probe` does and asserts on what came back.
func TestFor_ARealOllamaHeadResolvesToHTTP(t *testing.T) {
	testutil.NewSandbox(t)
	srv := stubOllamaServer(t)
	t.Setenv("OLLAMA_HOST", srv.URL)

	heads := discoverPortHeads(t)
	if len(heads) == 0 {
		t.Fatal("the port provider found no Ollama head against a stub that answers /api/tags")
	}

	for _, h := range heads {
		if h.Source == "ollama" || h.Provider == "ollama" {
			t.Fatalf("head %q stamps %q/%q: the dialect branch removed in #819 "+
				"is reachable again and this test is the wrong guard now",
				h.ID, h.Source, h.Provider)
		}
		if _, ok := For(h).(*HTTPExecutor); !ok {
			t.Errorf("For(%q) = %T, want *HTTPExecutor: a local model server is "+
				"driven over its OpenAI-compatible endpoint", h.ID, For(h))
		}
		if h.Endpoint != srv.URL {
			t.Errorf("head %q carries endpoint %q but was found at %q; the "+
				"executor dials the stamped address, so a dispatch would go elsewhere",
				h.ID, h.Endpoint, srv.URL)
		}
	}
}

// discoverPortHeads runs the registered port provider and returns only the
// heads it minted for the stub, so an LM Studio server on the developer's own
// machine cannot change the result.
func discoverPortHeads(t *testing.T) []provider.Head {
	t.Helper()
	var out []provider.Head
	for _, p := range provider.All() {
		if p.ID() != "port" {
			continue
		}
		heads, err := p.Discover(context.Background())
		if err != nil {
			t.Fatalf("port discovery: %v", err)
		}
		for _, h := range heads {
			if strings.HasPrefix(h.ID, "ollama/") {
				out = append(out, h)
			}
		}
	}
	return out
}

func stubOllamaServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			w.WriteHeader(http.StatusOK)
			return
		}
		fmt.Fprint(w, `{"models":[{"name":"qwen3:0.6b","details":{"quantization_level":"Q4_K_M"}}]}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}
