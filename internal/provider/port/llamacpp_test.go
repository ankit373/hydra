// SPDX-License-Identifier: MIT

package port

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/capabilities"
	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/provider"
)

// What a real llama-server (build b11042) answers, trimmed to the fields read
// here and otherwise as it sent them. build_info is the identity; n_ctx is the
// window it actually allocated, which is the -c it was started with.
const (
	llamaPropsFixture = `{
	  "build_info": "b11042-ec9281505",
	  "model_path": "/models/qwen3-0.6b-q4_k_m.gguf",
	  "total_slots": 4,
	  "default_generation_settings": {"n_ctx": 2048, "params": {"temperature": 0.8}}
	}`
	llamaModelsFixture = `{
	  "object": "list",
	  "data": [{"id": "/models/qwen3-0.6b-q4_k_m.gguf", "object": "model"}]
	}`
)

func llamaStub(t *testing.T, props, models string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/props", func(w http.ResponseWriter, _ *http.Request) {
		if props == "" {
			http.NotFound(w, nil)
			return
		}
		_, _ = w.Write([]byte(props))
	})
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(models))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func llamaProbe(t *testing.T, srv *httptest.Server) ([]provider.Head, error) {
	t.Helper()
	caps, err := capabilities.Load("")
	if err != nil {
		t.Fatal(err)
	}
	return (&llamaCppService{base: srv.URL}).probe(context.Background(), caps)
}

func TestLlamaCpp_DiscoversTheServedModel(t *testing.T) {
	heads, err := llamaProbe(t, llamaStub(t, llamaPropsFixture, llamaModelsFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(heads) != 1 {
		t.Fatalf("heads = %v, want the one served model", ids(heads))
	}
	h := heads[0]

	// Named for a person: the server reports the file it loaded.
	if h.ID != "llamacpp/qwen3-0.6b-q4_k_m" {
		t.Errorf("ID = %q, want the model's name rather than its path", h.ID)
	}
	// And addressed by what the server answers to, which is that path.
	if h.Meta["model"] != "/models/qwen3-0.6b-q4_k_m.gguf" {
		t.Errorf("Meta[model] = %q, want the id the server routes on", h.Meta["model"])
	}
	if !h.LocalOnly {
		t.Error("a model served by llama.cpp on this machine is not marked local")
	}
	if reason := executor.Unroutable(h); reason != "" {
		t.Errorf("discovered but not routable: %s", reason)
	}
}

// 8080 is the most common port on a developer's machine, so the service has to
// be identified rather than assumed from the address.
func TestLlamaCpp_AnotherServiceOnThePortIsNotLlamaCpp(t *testing.T) {
	t.Run("serves no props at all", func(t *testing.T) {
		if heads, err := llamaProbe(t, llamaStub(t, "", llamaModelsFixture)); err == nil {
			t.Errorf("probe succeeded with %v against a service with no /props", ids(heads))
		}
	})

	// The harder case: something that speaks OpenAI and answers /props with
	// JSON that is not llama.cpp's. Only build_info settles it.
	t.Run("answers props without build_info", func(t *testing.T) {
		const notLlama = `{"model": "gpt-4o", "object": "props"}`
		heads, err := llamaProbe(t, llamaStub(t, notLlama, llamaModelsFixture))
		if err == nil {
			t.Fatalf("probe succeeded with %v against a service that is not llama.cpp", ids(heads))
		}
		if !strings.Contains(err.Error(), "build_info") {
			t.Errorf("error = %q, want it to name what was missing", err)
		}
	})
}

// The window the server allocated, recorded as a measurement rather than as a
// ceiling: a ceiling can only lower the default, so a server started with a
// large -c would still have been budgeted at the local default.
func TestLlamaCpp_CarriesTheWindowTheServerAllocated(t *testing.T) {
	heads, err := llamaProbe(t, llamaStub(t, llamaPropsFixture, llamaModelsFixture))
	if err != nil {
		t.Fatal(err)
	}
	if got := heads[0].Meta["model_ctx"]; got != "2048" {
		t.Errorf("model_ctx = %q, want the 2048 the server reported", got)
	}
	if _, ok := heads[0].Meta["model_ctx_max"]; ok {
		t.Error("recorded as an architectural ceiling; it is the effective window")
	}
}

// llama.cpp's own variables say where it listens, so a machine already
// configured for it needs nothing new.
func TestLlamaCppHost_ReadsTheServersOwnVariables(t *testing.T) {
	t.Setenv("LLAMA_ARG_HOST", "")
	t.Setenv("LLAMA_ARG_PORT", "")
	if got := llamaCppHost(); got != DefaultLlamaCppHost {
		t.Errorf("host = %q, want %q", got, DefaultLlamaCppHost)
	}

	t.Setenv("LLAMA_ARG_PORT", "9090")
	if got := llamaCppHost(); got != "http://127.0.0.1:9090" {
		t.Errorf("host = %q, want the port the server was told to listen on", got)
	}

	// A bind-any address names every interface; it is not somewhere to connect.
	t.Setenv("LLAMA_ARG_HOST", "0.0.0.0")
	if got := llamaCppHost(); got != "http://127.0.0.1:9090" {
		t.Errorf("host = %q, want loopback: 0.0.0.0 is where it listens, not an address to dial", got)
	}

	t.Setenv("LLAMA_ARG_HOST", "192.168.1.50")
	if got := llamaCppHost(); got != "http://192.168.1.50:9090" {
		t.Errorf("host = %q, want the address it was bound to", got)
	}

	// A typo must not make the server undiscoverable, the same rule OllamaHost
	// applies to its own variable.
	t.Setenv("LLAMA_ARG_PORT", "not-a-port")
	if got := llamaCppHost(); got != DefaultLlamaCppHost {
		t.Errorf("host = %q, want the default for an unusable port", got)
	}
}

func TestLlamaCppModelName_ReadsAsAModelNotAPath(t *testing.T) {
	cases := map[string]string{
		"/models/qwen3-0.6b-q4_k_m.gguf": "qwen3-0.6b-q4_k_m",
		"my-alias":                       "my-alias",
		"/a/b/Model.GGUF":                "Model.GGUF", // only the exact suffix is ours to strip
	}
	for id, want := range cases {
		if got := llamaCppModelName(id); got != want {
			t.Errorf("llamaCppModelName(%q) = %q, want %q", id, got, want)
		}
	}
}
