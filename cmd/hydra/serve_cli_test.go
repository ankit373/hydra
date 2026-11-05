// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/config"
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
	// LocalOnly is what the port provider really stamps on a discovered local
	// head, and what pinHead reads to refuse one on a --local run. Without it
	// this fixture was a "local" head the router itself would not route to, so
	// the count it asserted could not happen (#1147).
	heads := []provider.Head{
		{ID: "ollama/a", Provider: "local", Source: "port",
			Endpoint: "http://127.0.0.1:11434", LocalOnly: true},
		{ID: "claude", Provider: "anthropic", Source: "cli"},
	}

	var local bytes.Buffer
	printServeBanner(&local, "127.0.0.1:8787", "STANDARD", false, true, heads)
	out := local.String()
	// Under --local the cloud head is not reachable, so it is not counted.
	for _, want := range []string{"127.0.0.1:8787", "STANDARD", "1 of 1", "local only", "loopback only"} {
		if !strings.Contains(out, want) {
			t.Errorf("the banner is missing %q:\n%s", want, out)
		}
	}

	var all bytes.Buffer
	printServeBanner(&all, "127.0.0.1:8787", "STANDARD", false, false, heads)
	if !strings.Contains(all.String(), "1 of 2") {
		t.Errorf("without --local both heads are reachable:\n%s", all.String())
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

	ans, err := serveRouter{d: d, defaultEnum: "MODERATE"}.
		Chat(ctx, serve.Request{
			RequestID: "run-1",
			Messages:  []executor.Message{{Role: "user", Content: "hi"}},
		})
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

	_, err = serveRouter{d: d, defaultEnum: "MODERATE"}.
		Chat(ctx, serve.Request{
			RequestID: "run-2",
			Messages:  []executor.Message{{Role: "user", Content: "hi"}},
			Route:     serve.Route{Enum: "NOT_A_KEY"},
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

// The endpoint cannot import dispatch, so this adapter is the only place the
// two event vocabularies meet, and a kind mapped wrong here is a fallback the
// stream never learns about.
func TestStreamEvents_MapsEachKind(t *testing.T) {
	if streamEvents(nil) != nil {
		t.Fatal("a client that did not ask for a stream must leave OnStream nil, " +
			"or every dispatch takes the streaming executor path")
	}

	var got []serve.Event
	on := streamEvents(func(e serve.Event) { got = append(got, e) })
	head := provider.Head{ID: "h1", Name: "model-1"}

	on(dispatch.StreamEvent{Kind: dispatch.StreamAttemptStarted, Head: head})
	on(dispatch.StreamEvent{Kind: dispatch.StreamDelta, Text: "hi", Head: head, SpanID: "s1"})
	on(dispatch.StreamEvent{Kind: dispatch.StreamAttemptFailed, Reason: "EOF", Head: head, SpanID: "s1"})

	if len(got) != 2 {
		t.Fatalf("got %d events, want the delta and the failure: %+v", len(got), got)
	}
	if got[0].Kind != serve.EventDelta || got[0].Text != "hi" || got[0].Model != "model-1" {
		t.Errorf("delta mapped as %+v", got[0])
	}
	if got[1].Kind != serve.EventAttemptFailed || got[1].Reason != "EOF" || got[1].SpanID != "s1" {
		t.Errorf("failure mapped as %+v", got[1])
	}
}

// stubOllamaHeads serves three models from one Ollama stub: the names in fail answer 503,
// the rest answer. It returns the models asked, in order, so a test can see who was tried.
func stubOllamaHeads(t *testing.T, fail ...string) func() []string {
	t.Helper()
	var (
		mu    sync.Mutex
		asked []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = w.Write([]byte(`{"models":[{"name":"alpha:1b","capabilities":["completion"]},` +
				`{"name":"beta:1b","capabilities":["completion"]},{"name":"gamma:1b","capabilities":["completion"]}]}`))
		case "/v1/chat/completions":
			var body struct{ Model string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			asked = append(asked, body.Model)
			mu.Unlock()
			if slices.Contains(fail, body.Model) {
				http.Error(w, `{"error":{"message":"overloaded"}}`, http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte(`{"model":"` + body.Model + `","choices":[{"message":{"role":"assistant","content":"from ` +
				body.Model + `"}}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OLLAMA_HOST", srv.URL)
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(asked)
	}
}

// G24: with --heads set, the router can neither choose, fall back to, nor advertise a head
// outside the list. The listed heads fail, so the excluded one is the only way to answer.
func TestServeHeads_NoHeadOutsideTheList(t *testing.T) {
	testutil.NewSandbox(t)
	if err := config.Save(&config.Config{}); err != nil {
		t.Fatal(err)
	}
	asked := stubOllamaHeads(t, "alpha:1b", "beta:1b")
	listed := []string{"ollama/alpha:1b", "ollama/beta:1b"}
	const excluded = "ollama/gamma:1b"

	ctx := context.Background()
	d, err := dispatch.New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := restrictServeHeads(d, listed); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(serve.Handler(serveRouter{d: d, defaultEnum: "GRUNT"}, nil, ""))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	var models struct{ Data []serve.Model }
	err = json.NewDecoder(resp.Body).Decode(&models)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	advertised := map[string]bool{}
	for _, m := range models.Data {
		advertised[m.ID] = true
	}
	if advertised[excluded] {
		t.Errorf("--heads %v, yet %s was %s", listed, excluded, "advertised by /v1/models")
	}
	for _, id := range listed {
		if !advertised[id] {
			t.Errorf("--heads %v, yet %s was %s", listed, id, "missing from /v1/models")
		}
	}

	for _, model := range []string{"hydra", excluded} {
		resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json",
			strings.NewReader(`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Errorf("--heads %v, yet %s was %s", listed, excluded, "what answered model "+model)
		}
	}
	got := asked()
	if slices.Contains(got, "gamma:1b") {
		t.Errorf("--heads %v, yet %s was %s", listed, excluded, fmt.Sprintf("sent a request (asked %v)", got))
	}
	if !slices.Contains(got, "alpha:1b") || !slices.Contains(got, "beta:1b") {
		t.Errorf("the listed heads were not both tried, so nothing fell back: asked %v", got)
	}
}

// serveUntil runs `hyctl serve args...` until it returns or the deadline cancels it, so a
// refusal that never happens fails the test instead of serving for ever.
func serveUntil(t *testing.T, args ...string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := cmdServe()
	cmd.SetArgs(append([]string{"--addr", "127.0.0.1:0"}, args...))
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	var err error
	captureStdout(t, func() { err = cmd.ExecuteContext(ctx) })
	return err
}

// Both spellings of the list refuse an id discovery did not find, before the server binds.
func TestServeHeads_AnUnknownIdStopsStartup(t *testing.T) {
	for _, spelling := range []string{"--heads", "[serve] heads"} {
		t.Run(spelling, func(t *testing.T) {
			testutil.NewSandbox(t)
			stubOllamaHeads(t)
			cfg := &config.Config{}
			var args []string
			if spelling == "--heads" {
				args = []string{"--heads", "ollama/alpha:1b,ollama/nope"}
			} else {
				cfg.Serve.Heads = []string{"ollama/nope"}
			}
			if err := config.Save(cfg); err != nil {
				t.Fatal(err)
			}
			err := serveUntil(t, args...)
			if err == nil || !strings.Contains(err.Error(), `"ollama/nope"`) {
				t.Errorf("%s naming an unknown head started a server: %v", spelling, err)
			}
		})
	}
}
