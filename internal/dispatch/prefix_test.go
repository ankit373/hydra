// SPDX-License-Identifier: MIT

package dispatch

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/policy"
	"github.com/ankit373/hydra/internal/pricing"
	"github.com/ankit373/hydra/internal/provider"
)

// A provider cache only hits when the start of the request is byte-identical,
// so nothing per-run (ids, timestamps, map order) may reach the request body.
func TestDispatch_SamePromptBuildsIdenticalPrefix(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	var mu sync.Mutex
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, b)
		mu.Unlock()
		_, _ = io.WriteString(w, `{"model":"m","content":[{"type":"text","text":"ok"}],`+
			`"stop_reason":"end_turn","usage":{"input_tokens":9,"output_tokens":2}}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("ANTHROPIC_MODEL", "claude-test-1")

	handoff := filepath.Join(t.TempDir(), "handoff.json")
	if err := os.WriteFile(handoff, []byte(`{"from":"planner","task":"draft","files":["notes/q3.md"],`+
		`"context":"board meeting minutes","prior_output":"outline agreed"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	d := &Dispatcher{
		cfg: &config.Config{}, policy: policy.New(policy.DefaultRules(false)), pricing: pricing.Load(),
		heads: []provider.Head{{ID: "anthropic", Name: "Claude", Provider: "anthropic",
			Source: "env", AuthReady: true, CapScore: 90, Meta: map[string]string{}}},
	}
	for _, run := range []string{"run-a", "run-b"} {
		if _, err := d.Dispatch(context.Background(), "summarise the minutes", Options{
			RunID: run, TaskID: "task-" + run, System: "You write plain summaries.", A2AFile: handoff,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if len(bodies) != 2 {
		t.Fatalf("%d requests reached the provider, want 2", len(bodies))
	}
	if string(bodies[0]) != string(bodies[1]) {
		t.Errorf("two dispatches of the same work sent different requests:\n%s\n%s", bodies[0], bodies[1])
	}
}
