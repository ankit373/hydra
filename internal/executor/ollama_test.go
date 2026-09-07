// SPDX-License-Identifier: MIT

package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ankit373/hydra/internal/provider"
)

// Ollama reports prompt-eval duration, which is the work before the first
// output token. Hydra parsed it into a field nothing read until #719.
func TestOllamaExecute_ReportsTimeToFirstToken(t *testing.T) {
	var probed bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// ensureRunning health-checks the host before generating.
		if r.URL.Path != "/api/generate" {
			probed = true
			_, _ = w.Write([]byte(`{"models":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"model":"qwen","response":"hi",
		  "prompt_eval_count":11,"eval_count":7,
		  "prompt_eval_duration":380000000}`))
	}))
	defer srv.Close()
	// The executor resolves its host from OLLAMA_HOST, not from Head.Endpoint.
	t.Setenv("OLLAMA_HOST", srv.URL)

	resp, err := (&OllamaExecutor{}).Execute(context.Background(), Request{
		Prompt: "hi",
		Head:   provider.Head{ID: "qwen", Provider: "ollama", Source: "ollama"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.TTFT.Milliseconds(); got != 380 {
		t.Fatalf("TTFT = %d ms, want 380 as the provider reported", got)
	}
	if !probed {
		t.Log("health check did not run; the generate path was still exercised")
	}
}
