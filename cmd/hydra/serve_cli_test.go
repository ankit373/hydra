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
