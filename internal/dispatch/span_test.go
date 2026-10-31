// SPDX-License-Identifier: MIT

package dispatch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/policy"
	"github.com/ankit373/hydra/internal/pricing"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/runlog"
)

const spanChatBody = `{"model":"gpt-test","choices":[{"message":{"content":"hello from the stub"}}],
  "usage":{"prompt_tokens":11,"completion_tokens":7}}`

// A head that really answers, so the success path is exercised end to end
// rather than only the select-then-fail one.
func servingHead(t *testing.T) provider.Head {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(spanChatBody))
	}))
	t.Cleanup(srv.Close)
	return provider.Head{
		ID: "stub-model", Name: "Stub Model", Provider: "openai", Source: "env",
		Endpoint: srv.URL, AuthReady: true, CapScore: 90,
	}
}

// The point of the v2 schema is that the real dispatch path populates it. A
// declared field nobody writes is the defect this guards against.
func TestDispatch_SpanCarriesTokensAndIdentity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	d := &Dispatcher{
		cfg: &config.Config{}, heads: []provider.Head{servingHead(t)},
		policy: policy.New(policy.DefaultRules(false)), pricing: pricing.Load(),
	}
	if _, err := d.Dispatch(context.Background(), "hello", Options{
		RunID: "run-span", TaskID: "task-span", MaxTokens: 64, Enum: "SIMPLE",
	}); err != nil {
		t.Fatalf("dispatch failed, so there is no success span to inspect: %v", err)
	}

	events, err := runlog.Load("run-span")
	if err != nil {
		t.Fatal(err)
	}

	var selected, finished *runlog.Event
	for i := range events {
		switch events[i].Kind {
		case runlog.KindHeadSelected:
			selected = &events[i]
		case runlog.KindDispatchFinished:
			finished = &events[i]
		}
	}
	if selected == nil || finished == nil {
		t.Fatalf("missing head_selected or dispatch_finished in %d events", len(events))
	}

	// Identity: the selection and its outcome are one span.
	if selected.SpanID == "" {
		t.Fatal("head_selected carries no span id")
	}
	if finished.SpanID != selected.SpanID {
		t.Fatalf("the outcome span %q does not match the selection span %q",
			finished.SpanID, selected.SpanID)
	}
	if got, want := finished.ParentSpan(), runlog.SpanIDFor("task-span"); got != want {
		t.Fatalf("parent span = %q, want the task's derived span %q", got, want)
	}

	// Usage on the span itself, so a timeline needs no join against cost.jsonl.
	if finished.InputTokens != 11 || finished.OutputTokens != 7 {
		t.Fatalf("tokens on the span = %d/%d, want 11/7 as the provider reported",
			finished.InputTokens, finished.OutputTokens)
	}
	if finished.Severity() != runlog.LevelInfo {
		t.Fatalf("a successful dispatch reads as %q", finished.Severity())
	}
	// Model parameters, which had nowhere to go before Meta.
	if finished.Meta["max_tokens"] != float64(64) || finished.Meta["enum"] != "SIMPLE" {
		t.Fatalf("model parameters missing from the span: %+v", finished.Meta)
	}
}

// The cost row and the span must name the same span, or spend cannot be
// attributed to one attempt among several on the same task.
func TestDispatch_CostRowJoinsTheSpan(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	d := &Dispatcher{
		cfg: &config.Config{}, heads: []provider.Head{servingHead(t)},
		policy: policy.New(policy.DefaultRules(false)), pricing: pricing.Load(),
	}
	if _, err := d.Dispatch(context.Background(), "hello", Options{
		RunID: "run-join", TaskID: "task-join",
	}); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	events, err := runlog.Load("run-join")
	if err != nil {
		t.Fatal(err)
	}
	var span string
	for _, e := range events {
		if e.Kind == runlog.KindDispatchFinished {
			span = e.SpanID
		}
	}
	if span == "" {
		t.Fatal("no finished span to join against")
	}

	raw, err := os.ReadFile(filepath.Join(config.Dir(), "logs", "cost.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(raw))
	if line == "" {
		t.Fatal("no cost row was written")
	}
	var row map[string]any
	if err := json.Unmarshal([]byte(line), &row); err != nil {
		t.Fatal(err)
	}
	if row["span_id"] != span {
		t.Fatalf("cost row span_id = %v, want the span %q it paid for", row["span_id"], span)
	}
}

// A candidate that cannot run is part of the run's shape, and it is an error,
// not an ordinary event. Without a level a reader cannot separate the two.
func TestDispatch_FailedCandidateIsLoggedAtErrorLevel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	d := rlDispatcher(rlHead("h1", 90))
	_, _ = d.Dispatch(context.Background(), "hello", Options{
		RunID: "run-lvl", TaskID: "task-lvl",
	})

	events, err := runlog.Load("run-lvl")
	if err != nil {
		t.Fatal(err)
	}
	var sawError bool
	for _, e := range events {
		if e.Kind != runlog.KindError {
			continue
		}
		sawError = true
		if e.Severity() != runlog.LevelError {
			t.Fatalf("a failed candidate reads as %q, want %q", e.Severity(), runlog.LevelError)
		}
		if e.SpanID == "" {
			t.Fatal("the failure is not attached to the span that failed")
		}
	}
	if !sawError {
		t.Fatal("no error event: the fallback chain advanced with nothing recording why")
	}
}
