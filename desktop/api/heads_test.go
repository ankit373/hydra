// SPDX-License-Identifier: MIT

package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/testutil"
)

// A machine with nothing installed must still answer, and the empty list must
// marshal as [] rather than null, types.ts declares heads as Head[].
func TestGetHeads_EmptyMachineAnswersWithAList(t *testing.T) {
	testutil.NewSandbox(t)

	got := New().GetHeads()
	if got.Heads == nil {
		t.Error("Heads is nil, the bridge must send [] for an empty list")
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"heads":null`) {
		t.Errorf("heads marshalled as null: %s", raw)
	}
	if got.Routable > len(got.Heads) {
		t.Errorf("Routable=%d exceeds the %d heads discovered", got.Routable, len(got.Heads))
	}
}

// The count must agree with the flags, or a view saying "3 of 12 reachable"
// contradicts the rows underneath it.
func TestGetHeads_RoutableCountMatchesTheRows(t *testing.T) {
	testutil.NewSandbox(t)

	got := New().GetHeads()
	n := 0
	for _, h := range got.Heads {
		if h.Routable {
			n++
		}
		// The two must never both be set: a reason is why it is *not* routable.
		if h.Routable && h.Reason != "" {
			t.Errorf("head %s is routable but carries a reason %q", h.ID, h.Reason)
		}
		if !h.Routable && h.Reason == "" {
			t.Errorf("head %s is unroutable with no reason, the user learns nothing", h.ID)
		}
	}
	if n != got.Routable {
		t.Errorf("Routable=%d but %d rows are marked routable", got.Routable, n)
	}
}

// The mapping, against heads chosen to be routable and not, rather than
// whatever the host machine happens to have.
func TestHeadsFrom_MarksRoutabilityAndSaysWhyNot(t *testing.T) {
	// Source "registry" is the agy path: AgyExecutor drives it, so routable.
	routable := provider.Head{ID: "opus", Name: "Opus", Provider: "agy", Source: "registry", CapScore: 98}
	// An embedding-only head is discovered and shown but never dispatched.
	embedding := provider.Head{
		ID: "embed", Name: "Embedder", Provider: "openai", Source: "env",
		Meta: map[string]string{"embedding_only": "true"},
	}
	// A CLI-sourced head for a provider with no template and no endpoint: no
	// executor can drive it. Deliberately not an HTTP head with a missing key
	//, Hydra speaks OpenAI-compat to any endpoint, so that is routable, and
	// asserting otherwise made the fixture wrong rather than the code.
	noExec := provider.Head{ID: "mystery", Name: "Mystery", Provider: "nobody-at-all", Source: "cli"}

	got := headsFrom([]provider.Head{routable, embedding, noExec})
	if len(got.Heads) != 3 {
		t.Fatalf("want 3 heads, got %d", len(got.Heads))
	}
	if got.Routable != 1 {
		t.Errorf("Routable = %d, want 1", got.Routable)
	}

	by := map[string]Head{}
	for _, h := range got.Heads {
		by[h.ID] = h
	}
	if !by["opus"].Routable || by["opus"].Reason != "" {
		t.Errorf("the agy head should be routable with no reason: %+v", by["opus"])
	}
	// Discovered and shown, never dispatched, the user must be told which.
	if by["embed"].Routable {
		t.Error("an embedding-only head must not be marked routable")
	}
	if !strings.Contains(by["embed"].Reason, "embeddings only") {
		t.Errorf("reason = %q, want it to name embeddings", by["embed"].Reason)
	}
	if by["mystery"].Routable {
		t.Error("a head no executor can drive must not be marked routable")
	}
	if by["mystery"].Reason == "" {
		t.Error("an unroutable head with no reason teaches the user nothing")
	}
}

// A routable head must never carry a reason, and an unroutable one must always
// carry one, the view renders the reason instead of the tier.
func TestHeadsFrom_ReasonAndRoutableAreNeverBothSet(t *testing.T) {
	got := headsFrom([]provider.Head{
		{ID: "a", Source: "registry"},
		{ID: "b", Source: "cli", Provider: "nobody-at-all"},
	})
	for _, h := range got.Heads {
		if h.Routable && h.Reason != "" {
			t.Errorf("%s: routable with a reason %q", h.ID, h.Reason)
		}
		if !h.Routable && h.Reason == "" {
			t.Errorf("%s: unroutable with no reason", h.ID)
		}
	}
}

func TestHeadsFrom_EmptyIsAListNotNull(t *testing.T) {
	got := headsFrom(nil)
	if got.Heads == nil {
		t.Error("Heads is nil, types.ts declares Head[]")
	}
	if got.Routable != 0 {
		t.Errorf("Routable = %d with no heads", got.Routable)
	}
}
