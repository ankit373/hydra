// SPDX-License-Identifier: MIT

package dispatch

import (
	"context"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/payload"
	"github.com/ankit373/hydra/internal/policy"
	"github.com/ankit373/hydra/internal/pricing"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/runlog"
)

func captureDispatcher(t *testing.T, cfg *config.Config) *Dispatcher {
	t.Helper()
	return &Dispatcher{
		cfg: cfg, heads: []provider.Head{servingHead(t)},
		policy: policy.New(policy.DefaultRules(false)), pricing: pricing.Load(),
	}
}

// Payloads are verbatim prompts and source, the only trace class with real
// privacy risk. Nothing may be written unless someone opted in.
func TestCapture_WritesNothingWhenNotOptedIn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	d := captureDispatcher(t, &config.Config{}) // CapturePayloads false
	if _, err := d.Dispatch(context.Background(), "a secret prompt", Options{
		RunID: "run-off", TaskID: "task-off",
	}); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	store, err := payload.Open(payload.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if store.Len() != 0 {
		t.Fatalf("capture is off but %d blobs were stored", store.Len())
	}
	events, err := runlog.Load("run-off")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.InputRef != "" || e.OutputRef != "" {
			t.Fatalf("a span carries payload refs with capture off: %+v", e)
		}
	}
}

// The defect this phase exists to fix: the store had no writers at all, so the
// opt-in set a flag nothing read.
func TestCapture_StoresThePromptAndResponseWhenOptedIn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	const prompt = "explain the routing table"
	const system = "You are a careful engineer."
	d := captureDispatcher(t, &config.Config{CapturePayloads: true})
	if _, err := d.Dispatch(context.Background(), prompt, Options{
		RunID: "run-on", TaskID: "task-on", System: system,
	}); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	events, err := runlog.Load("run-on")
	if err != nil {
		t.Fatal(err)
	}
	var in, out string
	for _, e := range events {
		if e.Kind == runlog.KindDispatchFinished {
			in, out = e.InputRef, e.OutputRef
		}
	}
	if in == "" {
		t.Fatal("the span carries no input ref, so the prompt is unreachable")
	}
	if out == "" {
		t.Fatal("the span carries no output ref, so the response is unreachable")
	}

	store, err := payload.Open(payload.Dir())
	if err != nil {
		t.Fatal(err)
	}
	gotIn, err := store.Load(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotIn, prompt) || !strings.Contains(gotIn, system) {
		t.Fatalf("the stored prompt is missing its parts:\n%s", gotIn)
	}
	gotOut, err := store.Load(out)
	if err != nil {
		t.Fatal(err)
	}
	if gotOut != "hello from the stub" {
		t.Fatalf("stored response = %q", gotOut)
	}

	// The labels are what let a viewer show a system prompt apart from the task.
	segs, err := store.LoadSegments(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 2 || segs[0].Label != "system" || segs[1].Label != "prompt" {
		t.Fatalf("segments lost their labels: %+v", segs)
	}
}

// Redaction runs before the content is addressed, so a secret never reaches
// disk and never leaves a fingerprint in the index either.
func TestCapture_RedactsBeforeStoring(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	const secret = "AKIAIOSFODNN7EXAMPLE"
	d := captureDispatcher(t, &config.Config{CapturePayloads: true})
	if _, err := d.Dispatch(context.Background(), "deploy with "+secret, Options{
		RunID: "run-pii", TaskID: "task-pii",
	}); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	events, err := runlog.Load("run-pii")
	if err != nil {
		t.Fatal(err)
	}
	var in string
	for _, e := range events {
		if e.Kind == runlog.KindDispatchFinished {
			in = e.InputRef
		}
	}
	store, err := payload.Open(payload.Dir())
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, secret) {
		t.Fatalf("the secret was stored verbatim:\n%s", got)
	}
	if !strings.Contains(got, "REDACTED") {
		t.Fatalf("the secret vanished without a label, so the finding is lost:\n%s", got)
	}
}

// A capture failure must never fail the work being captured.
func TestCapture_FailureDoesNotFailTheDispatch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	// A budget this small evicts as fast as it writes, which is the closest a
	// test gets to a store that cannot hold anything.
	d := captureDispatcher(t, &config.Config{CapturePayloads: true, PayloadBudgetMB: 0})
	r, err := d.Dispatch(context.Background(), strings.Repeat("x", 1000), Options{
		RunID: "run-fail", TaskID: "task-fail",
	})
	if err != nil {
		t.Fatalf("dispatch failed because of capture: %v", err)
	}
	if r.Output == "" {
		t.Fatal("the dispatch answered but the result is empty")
	}
}
