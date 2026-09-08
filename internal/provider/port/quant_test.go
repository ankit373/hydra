// SPDX-License-Identifier: MIT

package port

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ankit373/hydra/internal/executor"
)

// tagsServer answers /api/tags with body and 404s everything else, so a probe
// that asks the wrong path fails loudly instead of matching by accident.
func tagsServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The payload is the shape a live Ollama actually returns, details object and
// all, so the field names are verified against the server not against docs.
func TestOllama_CarriesTheQuantizationOfTheLoadedWeights(t *testing.T) {
	srv := tagsServer(t, `{"models":[
		{"name":"qwen2.5-coder:7b","details":{"format":"gguf","family":"qwen2",
		 "parameter_size":"7.6B","quantization_level":"Q4_K_M"}},
		{"name":"qwen2.5-coder:7b-q8_0","details":{"format":"gguf","family":"qwen2",
		 "parameter_size":"7.6B","quantization_level":"Q8_0"}},
		{"name":"nomic-embed-text:latest","details":{"parameter_size":"137M",
		 "quantization_level":"F16"}}
	]}`)

	heads, err := (&ollamaService{base: srv.URL}).probe(context.Background(), caps(t))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][2]string{}
	for _, h := range heads {
		got[h.ID] = [2]string{h.Meta["model_quant"], h.Meta["model_params"]}
	}
	want := map[string][2]string{
		"ollama/qwen2.5-coder:7b":        {"Q4_K_M", "7.6B"},
		"ollama/qwen2.5-coder:7b-q8_0":   {"Q8_0", "7.6B"},
		"ollama/nomic-embed-text:latest": {"F16", "137M"},
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s: got quant/params %v, want %v", id, got[id], w)
		}
	}
	// Two quants of one model have to be distinguishable, which is the whole
	// point: otherwise trust averages a lossy quant with a near-lossless one.
	a, b := got["ollama/qwen2.5-coder:7b"][0], got["ollama/qwen2.5-coder:7b-q8_0"][0]
	if a == b {
		t.Errorf("both quants of the same model read as %q, the tradeoff is invisible", a)
	}
}

// An older server sends no details at all, and a newer one can send a blank
// field. A value invented there would be read as measured, so the keys stay
// absent, and neither case may cost the head its routability.
func TestOllama_NoDetailsInventsNoQuant(t *testing.T) {
	srv := tagsServer(t, `{"models":[
		{"name":"llama3.2:3b"},
		{"name":"qwen3:0.6b","details":{"quantization_level":"  ","parameter_size":""}}
	]}`)

	heads, err := (&ollamaService{base: srv.URL}).probe(context.Background(), caps(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(heads) != 2 {
		t.Fatalf("got %d heads, want 2: a missing details must not drop a head", len(heads))
	}
	for _, h := range heads {
		if q, ok := h.Meta["model_quant"]; ok {
			t.Errorf("%s: invented quant %q from a server that reported none", h.ID, q)
		}
		if p, ok := h.Meta["model_params"]; ok {
			t.Errorf("%s: invented params %q from a server that reported none", h.ID, p)
		}
		if _, ok := h.Meta["model_ctx_max"]; ok {
			t.Errorf("%s: invented a context ceiling from a server that reported none", h.ID)
		}
		if !executor.Supports(h) {
			t.Errorf("%s became unroutable: %s", h.ID, executor.Unroutable(h))
		}
	}
}

// The ceiling the budget governor caps a declared window by. Values are the
// ones a live Ollama 0.33.2 reports; the same models run at the server's 4096
// default, which is why this is a ceiling and not the window itself (#764).
func TestOllama_CarriesTheArchitecturalContextCeiling(t *testing.T) {
	srv := tagsServer(t, `{"models":[
		{"name":"qwen3:0.6b","details":{"quantization_level":"Q4_K_M",
		 "parameter_size":"751.63M","context_length":40960}},
		{"name":"nomic-embed-text:latest","details":{"quantization_level":"F16",
		 "parameter_size":"137M","context_length":2048}},
		{"name":"noctx:1b","details":{"quantization_level":"Q4_K_M"}}
	]}`)

	heads, err := (&ollamaService{base: srv.URL}).probe(context.Background(), caps(t))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, h := range heads {
		got[h.ID] = h.Meta["model_ctx_max"]
	}
	for id, want := range map[string]string{
		"ollama/qwen3:0.6b":              "40960",
		"ollama/nomic-embed-text:latest": "2048",
		// Reported nothing, so nothing is recorded: a guessed ceiling would
		// cap a window against a number no server ever gave.
		"ollama/noctx:1b": "",
	} {
		if got[id] != want {
			t.Errorf("%s: ctx ceiling %q, want %q", id, got[id], want)
		}
	}
}
