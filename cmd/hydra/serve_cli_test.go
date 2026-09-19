// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/serve"
	"github.com/ankit373/hydra/internal/testutil"
)

// The client's model field is the routing instruction, so what it resolves to
// is what gets spent.
func TestRouteToDispatch_ResolvesEachRoutingKey(t *testing.T) {
	if _, _, head, err := routeToDispatch(serve.Route{Head: "ollama/x"}, "STANDARD"); err != nil || head != "ollama/x" {
		t.Errorf("head pin = %q, %v", head, err)
	}
	if tier, _, _, err := routeToDispatch(serve.Route{Tier: "4"}, "STANDARD"); err != nil || tier != "4" {
		t.Errorf("tier pin = %q, %v", tier, err)
	}
	tier, enum, _, err := routeToDispatch(serve.Route{}, "SIMPLE")
	if err != nil || enum != "SIMPLE" || tier == "" {
		t.Errorf("default = %q/%q, %v", tier, enum, err)
	}
	if _, enum, _, err = routeToDispatch(serve.Route{Enum: "HARD"}, "STANDARD"); err != nil || enum != "HARD" {
		t.Errorf("named enum = %q, %v", enum, err)
	}
}

// "local" is an alias for GRUNT rather than an enum of its own. Recording it
// would put a routing key on the cost row that does not exist (#832).
func TestRouteToDispatch_AnAliasRoutesButRecordsNoEnum(t *testing.T) {
	tier, enum, _, err := routeToDispatch(serve.Route{Enum: "LOCAL"}, "STANDARD")
	if err != nil {
		t.Fatal(err)
	}
	if tier == "" {
		t.Fatal("the alias did not resolve to a tier")
	}
	if enum != "" {
		t.Errorf("enum = %q: the cost row would name a routing key that does not exist", enum)
	}
}

// An unresolvable key is the caller's mistake, and has to read as one or the
// client reports Hydra as broken.
func TestRouteToDispatch_UnknownKeyIsTheCallersMistake(t *testing.T) {
	_, _, _, err := routeToDispatch(serve.Route{Enum: "NOT_A_KEY"}, "STANDARD")
	if err == nil {
		t.Fatal("an unknown routing key was accepted")
	}
	if !errors.Is(err, serve.ErrBadRequest) {
		t.Errorf("err does not mark a bad request, so it would answer 502: %v", err)
	}
	if !strings.Contains(err.Error(), "head id") {
		t.Errorf("the refusal does not say what is accepted: %v", err)
	}
}

// The whole conversation is what the router classifies, not just the last turn.
// An agent loop's earlier turns carry tool results, which for a code-review
// client are the user's own source, and the ledger and egress gate read this.
func TestFlattenConversation_CarriesEveryTurnPastThePolicyGate(t *testing.T) {
	got := flattenConversation([]executor.Message{
		{Role: "system", Content: "you review code"},
		{Role: "user", Content: "review this"},
		{Role: "assistant", Content: ""},
		{Role: "tool", Content: "AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI"},
	})
	for _, want := range []string{"you review code", "review this", "wJalrXUtnFEMI"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q never reached the prompt the policy gate reads:\n%s", want, got)
		}
	}
	if strings.Contains(got, "assistant:\n\n") {
		t.Error("an empty turn was rendered, padding the prompt with nothing")
	}
}

// "N of M can carry tools" is the honest answer to "will my agent work here",
// since most dialects cannot carry them yet.
func TestPrintServeBanner_SaysHowManyHeadsCanCarryTools(t *testing.T) {
	var buf bytes.Buffer
	printServeBanner(&buf, "127.0.0.1:8787", "STANDARD", false, true, []provider.Head{
		{ID: "ollama/a", Provider: "local", Source: "port", Endpoint: "http://127.0.0.1:11434"},
		{ID: "claude", Provider: "anthropic", Source: "cli"},
	})
	out := buf.String()
	for _, want := range []string{"127.0.0.1:8787", "STANDARD", "1 of 2", "local only", "loopback only"} {
		if !strings.Contains(out, want) {
			t.Errorf("the banner is missing %q:\n%s", want, out)
		}
	}
}

func TestPrintServeBanner_SaysWhenATokenIsRequired(t *testing.T) {
	var buf bytes.Buffer
	printServeBanner(&buf, "0.0.0.0:8787", "HARD", true, false, nil)
	if !strings.Contains(buf.String(), "bearer token required") {
		t.Errorf("an exposed endpoint did not say it is authenticated:\n%s", buf.String())
	}
}

// The adapter is the only place internal/serve and the router meet, so what it
// drops is invisible everywhere else: an answer with no head cannot be
// attributed, and tokens it forgets never reach the client's usage block.
func TestServeRouter_CarriesTheAnswerHeadAndTokensBack(t *testing.T) {
	dispatchable(t, "the head's answer")

	ctx := context.Background()
	d, err := dispatch.New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	ans, err := serveRouter{d: d, runID: "run-1", defaultEnum: "MODERATE"}.
		Chat(ctx, serve.Request{Messages: []executor.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ans.Output, "the head's answer") {
		t.Errorf("the answer did not survive the adapter: %q", ans.Output)
	}
	if ans.Head == "" {
		t.Error("no head recorded, so the reply cannot say what answered")
	}
	if ans.InputTokens == 0 && ans.OutputTokens == 0 {
		t.Error("no tokens carried back, so the client's usage block reads as a free call")
	}
}

// A request the router cannot serve has to surface as an error, not as an empty
// answer the client would render as the model saying nothing.
func TestServeRouter_ARoutingFailureIsAnError(t *testing.T) {
	dispatchable(t, "unused")

	ctx := context.Background()
	d, err := dispatch.New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	_, err = serveRouter{d: d, runID: "run-2", defaultEnum: "MODERATE"}.
		Chat(ctx, serve.Request{
			Messages: []executor.Message{{Role: "user", Content: "hi"}},
			Route:    serve.Route{Enum: "NOT_A_KEY"},
		})
	if err == nil {
		t.Fatal("an unknown routing key produced an answer")
	}
	if !errors.Is(err, serve.ErrBadRequest) {
		t.Errorf("the client would be told 502 for its own mistake: %v", err)
	}
}

// /v1/models is what a client's model picker reads, so it has to carry the
// routing keys as well as the heads, or the picker shows no way to route.
func TestServeRouter_ModelsAdvertisesRoutingKeysAndHeads(t *testing.T) {
	dispatchable(t, "unused")

	ctx := context.Background()
	d, err := dispatch.New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	ids := map[string]bool{}
	for _, m := range (serveRouter{d: d}).Models() {
		ids[m.ID] = true
	}
	for _, want := range []string{"hydra", "hydra/hard", "cody"} {
		if !ids[want] {
			t.Errorf("the listing is missing %q: %v", want, ids)
		}
	}
}

// A garbage default enum must be refused before the listener is ever bound.
func TestCmdServe_GarbageEnumIsRefusedBeforeBinding(t *testing.T) {
	testutil.NewSandbox(t)
	_, _, err := run(t, "serve", "--enum", "NOT_AN_ENUM")
	if err == nil {
		t.Fatal("a garbage enum was accepted")
	}
	if !strings.Contains(err.Error(), "NOT_AN_ENUM") {
		t.Errorf("the refusal does not name the enum: %v", err)
	}
}

// Only a delta reaches the client. An attempt starting or failing is the chain
// doing its job, and forwarding either would open the stream on a head that has
// produced nothing, which is what makes an ordinary fallback invisible.
func TestStreamBridge_ForwardsOnlyDeltas(t *testing.T) {
	var got []serve.Delta
	stopped := false
	bridge := streamBridge(
		func(d serve.Delta) error { got = append(got, d); return nil },
		func() { stopped = true },
	)

	head := provider.Head{ID: "ollama/qwen3:4b", Name: "Qwen3 4B"}
	bridge(dispatch.StreamEvent{Kind: dispatch.StreamAttemptStarted, Head: head})
	bridge(dispatch.StreamEvent{Kind: dispatch.StreamDelta, Head: head, Text: "hello"})
	bridge(dispatch.StreamEvent{Kind: dispatch.StreamAttemptFailed, Head: head, Reason: "boom"})

	if len(got) != 1 || got[0].Text != "hello" {
		t.Fatalf("forwarded %+v, want the one delta", got)
	}
	// The client's model field is a routing instruction, so the chunks have to
	// name the head that answered rather than echo the key back.
	if got[0].Head != "ollama/qwen3:4b" || got[0].Model != "Qwen3 4B" {
		t.Errorf("the head did not travel with the text: %+v", got[0])
	}
	if stopped {
		t.Error("a failed attempt cancelled the run, so the fallback chain cannot do its job")
	}
}

// Refusing a delta means bytes are already on the wire and this run's answer
// can no longer be used. Carrying on spends money on it anyway.
func TestStreamBridge_ARefusedDeltaStopsTheRun(t *testing.T) {
	stopped := false
	bridge := streamBridge(
		func(serve.Delta) error { return errors.New("committed to another head") },
		func() { stopped = true },
	)
	bridge(dispatch.StreamEvent{Kind: dispatch.StreamDelta, Text: "x"})

	if !stopped {
		t.Error("the run was left to finish an answer nobody will receive")
	}
}

// The streamed path must deliver the same answer the whole-body one does, or
// one request reports two different things depending on how it was asked.
func TestServeRouter_ChatStreamDeliversTheAnswerAsDeltas(t *testing.T) {
	dispatchable(t, "the head's answer")

	ctx := context.Background()
	d, err := dispatch.New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	var streamed strings.Builder
	r := serveRouter{d: d, runID: "run-3", defaultEnum: "MODERATE"}
	ans, err := r.ChatStream(ctx,
		serve.Request{Messages: []executor.Message{{Role: "user", Content: "hi"}}},
		func(delta serve.Delta) error {
			if delta.Head == "" {
				t.Error("a delta with no head cannot name what is answering")
			}
			streamed.WriteString(delta.Text)
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	// executor.Stream delivers a head that cannot stream as one whole delta,
	// which is what lets this path be written once.
	if !strings.Contains(streamed.String(), "the head's answer") {
		t.Errorf("the answer never reached the client as deltas: %q", streamed.String())
	}
	if !strings.Contains(ans.Output, "the head's answer") || ans.Head == "" {
		t.Errorf("the returned answer lost the output or the head: %+v", ans)
	}
}

// A routing key the router cannot resolve has to fail before anything is
// streamed, or the client is sent a 200 carrying an error it does not look for.
func TestServeRouter_ChatStreamRefusesBeforeAnyDelta(t *testing.T) {
	dispatchable(t, "unused")

	ctx := context.Background()
	d, err := dispatch.New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	sent := 0
	_, err = serveRouter{d: d, runID: "run-4", defaultEnum: "MODERATE"}.
		ChatStream(ctx, serve.Request{
			Messages: []executor.Message{{Role: "user", Content: "hi"}},
			Route:    serve.Route{Enum: "NOT_A_KEY"},
		}, func(serve.Delta) error { sent++; return nil })

	if !errors.Is(err, serve.ErrBadRequest) {
		t.Errorf("the client would be told 502 for its own mistake: %v", err)
	}
	if sent != 0 {
		t.Errorf("%d deltas were sent before the refusal, so the status line was already gone", sent)
	}
}
