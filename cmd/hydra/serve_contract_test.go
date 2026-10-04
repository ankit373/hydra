// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/serve"
)

// /v1/models is a client's model picker. Offering a head the router refuses by
// design makes the picker lie: nomic-embed-text has no completion API, so
// choosing it could only ever fail, and it was advertised anyway (#1145).
func TestAdvertisable_SkipsHeadsTheRouterWouldRefuse(t *testing.T) {
	// Shaped like what the port provider really emits, or the "routable" case
	// is not one and the test passes by dropping everything.
	routable := provider.Head{
		ID: "ollama/Qwen2.5-Coder:7b", Provider: "ollama", Source: "port",
		Endpoint: "http://127.0.0.1:11434/v1", LocalOnly: true,
	}
	if why := executor.Unroutable(routable); why != "" {
		t.Fatalf("the fixture's routable head is not routable (%s), so this would pass vacuously", why)
	}
	heads := []provider.Head{
		routable,
		{ID: "ollama/nomic-embed-text:latest", Provider: "ollama", Source: "port",
			Endpoint: "http://127.0.0.1:11434/v1", LocalOnly: true,
			Meta: map[string]string{"embedding_only": "true"}},
		{ID: "env/openrouter/ghost", Provider: "openrouter", Source: "env",
			Meta: map[string]string{"unroutable_reason": "not in the catalogue"}},
		// The bare binary: discovered on $PATH, nothing can drive it.
		{ID: "ollama", Provider: "ollama", LocalOnly: true},
	}

	got := map[string]bool{}
	for _, m := range advertisable(heads) {
		got[m.ID] = true
	}
	if !got["ollama/Qwen2.5-Coder:7b"] {
		t.Error("a routable head was dropped from the picker")
	}
	for _, unwanted := range []string{"ollama/nomic-embed-text:latest", "env/openrouter/ghost", "ollama"} {
		if got[unwanted] {
			t.Errorf("the picker offers %q, which the router refuses", unwanted)
		}
	}
}

// A head the client named is the client's choice, so its failure is a 4xx. 5xx
// is in the OpenAI SDKs' retry class, and retrying a model that does not exist
// can never succeed, so the client burns its budget and then blames Hydra.
func TestCallerError_GradesANamedHeadsFailureAsTheClientsOwn(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		grade error
	}{
		{"unknown id", fmt.Errorf("%w: x", dispatch.ErrHeadUnknown), serve.ErrNotFound},
		{"unusable head", fmt.Errorf("%w: x", dispatch.ErrHeadUnusable), serve.ErrBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := callerError(tc.err)
			if !errors.Is(got, tc.grade) {
				t.Errorf("not graded %v: %v", tc.grade, got)
			}
			// The message is what the operator reads and is already right, so
			// grading must not prepend "bad request: " or similar to it.
			if got.Error() != tc.err.Error() {
				t.Errorf("grading rewrote the message:\n got %q\nwant %q", got.Error(), tc.err.Error())
			}
		})
	}

	// A head that genuinely failed stays a 5xx, or the grading is vacuous.
	real := errors.New("upstream returned 500")
	if g := callerError(real); errors.Is(g, serve.ErrBadRequest) || errors.Is(g, serve.ErrNotFound) {
		t.Errorf("a real head failure was graded as the client's mistake: %v", g)
	}
}

// pinHead is where all three come from, so the kinds have to be distinguishable
// there or the grading above has nothing to read.
func TestPinHead_SeparatesUnknownFromUnusable(t *testing.T) {
	if errors.Is(dispatch.ErrHeadUnknown, dispatch.ErrHeadUnusable) {
		t.Error("the two kinds are the same error, so they cannot be graded apart")
	}
	for _, e := range []error{dispatch.ErrHeadUnknown, dispatch.ErrHeadUnusable} {
		if errors.Is(e, dispatch.ErrNoHeads) {
			t.Errorf("%v already is ErrNoHeads; the wrapper is what must carry both", e)
		}
	}
}

// The banner is what an agent author reads to decide whether their loop will
// work here, so its denominator has to be heads this server can actually
// reach. Before #1147 it counted all 16 on a --local endpoint, ten of which
// no request could route to, and the embedding-only head among them.
func TestServeBanner_CountsOnlyReachableHeads(t *testing.T) {
	heads := []provider.Head{
		{ID: "ollama/chat", Provider: "ollama", Source: "port",
			Endpoint: "http://127.0.0.1:11434/v1", LocalOnly: true},
		{ID: "ollama/embed", Provider: "ollama", Source: "port",
			Endpoint: "http://127.0.0.1:11434/v1", LocalOnly: true,
			Meta: map[string]string{"embedding_only": "true"}},
		{ID: "cloud/one", Provider: "anthropic", Source: "registry", Executable: "/usr/bin/agy"},
	}

	var local, all bytes.Buffer
	printServeBanner(&local, "127.0.0.1:1", "STANDARD", false, true, heads)
	printServeBanner(&all, "127.0.0.1:1", "STANDARD", false, false, heads)

	// --local: only the one routable local chat head is reachable.
	if !strings.Contains(local.String(), "of 1 reachable") {
		t.Errorf("--local did not drop the unreachable heads:\n%s", local.String())
	}
	// Without it, the cloud head joins; the embedding-only one never does.
	if !strings.Contains(all.String(), "of 2 reachable") {
		t.Errorf("the embedding-only head was counted as reachable:\n%s", all.String())
	}
	// And the line must not read as a promise about the model.
	if !strings.Contains(local.String(), "accept tool definitions") {
		t.Errorf("the banner states a capability it cannot know:\n%s", local.String())
	}
}
