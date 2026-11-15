// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/testutil"
	"github.com/ankit373/hydra/internal/workflow"
)

// anthropicRecording answers like Anthropic and keeps every request body.
func anthropicRecording(t *testing.T) func() []string {
	t.Helper()
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"claude-test","content":[{"type":"text","text":"done"}],`+
			`"usage":{"input_tokens":3,"output_tokens":4}}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), bodies...)
	}
}

func dispatchBody(t *testing.T, sent func() []string) string {
	t.Helper()
	out, cobraOut, err := run(t, "dispatch", "--system", "You write plain summaries.", "summarise the minutes")
	if err != nil {
		t.Fatalf("dispatch failed: %v\n%s%s", err, out, cobraOut)
	}
	bodies := sent()
	if len(bodies) != 1 {
		t.Fatalf("%d requests reached Anthropic, want 1", len(bodies))
	}
	return bodies[0]
}

// The shipped policy sets prompt_cache: true, so a plain dispatch asks for it.
func TestDispatch_PromptCacheFromPolicy(t *testing.T) {
	cliSandbox(t)
	body := dispatchBody(t, anthropicRecording(t))
	if !strings.Contains(body, `"cache_control":{"type":"ephemeral"}`) {
		t.Errorf("the shipped policy's prompt_cache never reached the request:\n%s", body)
	}
}

func TestDispatch_PromptCacheOffByRule(t *testing.T) {
	s := cliSandbox(t)
	writeWorkflowPolicy(t, s.HydraHome, `version: "1.0"
defaults:
  prompt_cache: true
rules:
  - name: no-cache-for-summaries
    when:
      prompt_contains: summarise
    apply:
      prompt_cache: false
`)
	body := dispatchBody(t, anthropicRecording(t))
	if strings.Contains(body, "cache_control") {
		t.Errorf("a rule set prompt_cache: false and the request still asked for caching:\n%s", body)
	}
}

// Each workflow step dispatches with the policy's prompt_cache.
func TestWorkflowStep_PassesPromptCache(t *testing.T) {
	testutil.NewSandbox(t)
	var got dispatch.Options
	var seen context.Context
	r := dispatchRouter{dispatchFn: capturedDispatch(&got, &seen)}
	if _, err := r.Route(context.Background(), "draft the release note", workflow.Step{Enum: "SIMPLE"}); err != nil {
		t.Fatal(err)
	}
	if !got.PromptCache {
		t.Error("a workflow step dispatched without the shipped policy's prompt_cache")
	}
}
