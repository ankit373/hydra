// SPDX-License-Identifier: MIT

package env

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ankit373/hydra/internal/capabilities"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/testutil"
)

func embedded(t *testing.T) *capabilities.DB {
	t.Helper()
	db, err := capabilities.Load("")
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// writeAllowlist puts an [openrouter] section in the sandbox's config.
func writeAllowlist(t *testing.T, s *testutil.Sandbox, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(s.HydraHome, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func byID(heads []provider.Head) map[string]provider.Head {
	m := make(map[string]provider.Head, len(heads))
	for _, h := range heads {
		m[h.ID] = h
	}
	return m
}

// One head per named model, each carrying the model id to send. Before this,
// OpenRouter was one head routing to one model however many were priced (#752).
func TestOpenRouterHeads_OneRoutableHeadPerNamedModel(t *testing.T) {
	catalogue := []string{"anthropic/claude-sonnet-4.5", "google/gemini-2.5-pro"}
	heads := openRouterHeads(catalogue, catalogue, embedded(t))

	if len(heads) != 2 {
		t.Fatalf("got %d heads, want one per named model: %+v", len(heads), heads)
	}
	h := heads[0]
	if h.ID != "openrouter/anthropic/claude-sonnet-4.5" {
		t.Errorf("ID = %q, want the openrouter/ prefix that keeps it distinct from the direct anthropic head", h.ID)
	}
	if h.Name != "anthropic/claude-sonnet-4.5 (OpenRouter)" {
		t.Errorf("Name = %q, want the (OpenRouter) suffix cost.CanonicalKey resolves", h.Name)
	}
	if h.Provider != "openrouter" {
		t.Errorf("Provider = %q, want openrouter, it decides the base URL and the key", h.Provider)
	}
	if h.Source != "env" {
		t.Errorf("Source = %q, want env, the API key is what discovered it", h.Source)
	}
	if h.Meta["model"] != "anthropic/claude-sonnet-4.5" {
		t.Errorf("Meta[model] = %q, want the exact id to send", h.Meta["model"])
	}
	if h.Meta["unroutable_reason"] != "" {
		t.Errorf("a catalogued model was marked unroutable: %q", h.Meta["unroutable_reason"])
	}
	if h.LocalOnly {
		t.Error("LocalOnly = true, so rank.UITier would price a paid head at tier 10 and the PII policy would hand it secrets")
	}
	if !h.AuthReady {
		t.Error("AuthReady = false, but the key that discovered it is present")
	}
}

// A name the catalogue does not hold is a typo. It stays visible with a reason
// instead of vanishing, which is the #714 defect.
func TestOpenRouterHeads_UnknownModelIsMarkedNotDropped(t *testing.T) {
	heads := openRouterHeads(
		[]string{"anthropic/claud-sonnet-4-5", "google/gemini-2.5-pro"},
		[]string{"anthropic/claude-sonnet-4.5", "google/gemini-2.5-pro"},
		embedded(t),
	)

	if len(heads) != 2 {
		t.Fatalf("got %d heads, want both, the typo included: %+v", len(heads), heads)
	}
	typo := byID(heads)["openrouter/anthropic/claud-sonnet-4-5"]
	if typo.ID == "" {
		t.Fatalf("the misspelled model was dropped: %+v", heads)
	}
	if typo.Meta["unroutable_reason"] == "" {
		t.Error("a model absent from the catalogue is offered as routable")
	}
	if got := byID(heads)["openrouter/google/gemini-2.5-pro"].Meta["unroutable_reason"]; got != "" {
		t.Errorf("one bad name made a good one unroutable: %q", got)
	}
}

// An empty catalogue means the fetch never landed, which is no evidence about
// any model. Refusing everything then would break routing over a cold cache.
func TestOpenRouterHeads_EmptyCatalogueRefusesNothing(t *testing.T) {
	heads := openRouterHeads([]string{"anthropic/claude-sonnet-4.5"}, nil, embedded(t))

	if len(heads) != 1 {
		t.Fatalf("got %d heads: %+v", len(heads), heads)
	}
	if got := heads[0].Meta["unroutable_reason"]; got != "" {
		t.Errorf("an unfetched catalogue was read as proof the model does not exist: %q", got)
	}
}

// The catalogue is stored lowercased. Matching must not depend on how the user
// typed it, and the id sent to the API must be what they typed.
func TestOpenRouterHeads_MatchesCatalogueCaseInsensitively(t *testing.T) {
	heads := openRouterHeads(
		[]string{"Anthropic/Claude-Sonnet-4.5"},
		[]string{"anthropic/claude-sonnet-4.5"},
		embedded(t),
	)

	if got := heads[0].Meta["unroutable_reason"]; got != "" {
		t.Errorf("a differently-cased name failed to match the catalogue: %q", got)
	}
	if got := heads[0].Meta["model"]; got != "Anthropic/Claude-Sonnet-4.5" {
		t.Errorf("Meta[model] = %q, want the user's own spelling, the API is what decides case", got)
	}
	if heads[0].ID != "openrouter/anthropic/claude-sonnet-4.5" {
		t.Errorf("ID = %q, want a lowercased id so spend does not split on capitalisation", heads[0].ID)
	}
}

// Without a score per model the router has nothing to choose on, and a bare
// capabilities lookup returns the same default for every unsynced model.
func TestOpenRouterHeads_ScoresWithoutRequiringASync(t *testing.T) {
	// Vendor names no curated catalogue carries, so this asserts the fallback
	// rather than whatever the embedded data.json happens to know today.
	models := []string{"vendor-x/opus-9", "vendor-x/tiny-1b"}
	heads := openRouterHeads(models, models, embedded(t))

	strong, weak := heads[0].CapScore, heads[1].CapScore
	if strong <= weak {
		t.Errorf("an opus-class model scored %d and a 1b one %d; with no ordering the router cannot rank them", strong, weak)
	}
	if want := capabilities.HeuristicCapScore(models[0]); strong != want {
		t.Errorf("CapScore = %d, want %d, the same score `models sync` would have recorded", strong, want)
	}
}

// A score the user tuned by hand must beat the heuristic, or `models add` on an
// OpenRouter model does nothing.
func TestOpenRouterHeads_UserOverlayScoreWins(t *testing.T) {
	s := testutil.NewSandbox(t)
	overlay := filepath.Join(s.HydraHome, "models.json")
	if _, err := capabilities.AddModel(overlay, capabilities.Entry{
		ID: "anthropic/claude-opus-4-1", Name: "tuned", Provider: "anthropic", CapScore: 41, Source: "user",
	}); err != nil {
		t.Fatal(err)
	}
	db, err := capabilities.Load(overlay)
	if err != nil {
		t.Fatal(err)
	}

	models := []string{"anthropic/claude-opus-4-1"}
	if got := openRouterHeads(models, models, db)[0].CapScore; got != 41 {
		t.Errorf("CapScore = %d, want the hand-tuned 41, not the heuristic %d",
			got, capabilities.HeuristicCapScore(models[0]))
	}
}

// The default must be the behaviour every install already had: one key, one
// head. Naming nothing changes nothing.
func TestDiscover_NoAllowlistKeepsTheSingleOpenRouterHead(t *testing.T) {
	s := testutil.NewSandbox(t)
	s.SetKey(t, "OPENROUTER_API_KEY", "sk-test")

	heads, err := (&Provider{}).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(heads) != 1 || heads[0].ID != "env/openrouter" {
		t.Fatalf("got %+v, want exactly the single env/openrouter head", heads)
	}
}

// The named heads replace that one rather than joining it: two heads on one
// account, one routing to whatever OPENROUTER_MODEL says, splits its spend.
func TestDiscover_AllowlistReplacesTheKeyDerivedHead(t *testing.T) {
	s := testutil.NewSandbox(t)
	s.SetKey(t, "OPENROUTER_API_KEY", "sk-test")
	writeAllowlist(t, s, "[openrouter]\nmodels = [\"anthropic/claude-sonnet-4.5\", \"google/gemini-2.5-pro\"]\n")

	heads, err := (&Provider{}).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(heads) != 2 {
		t.Fatalf("got %d heads, want one per named model: %+v", len(heads), heads)
	}
	if _, ok := byID(heads)["env/openrouter"]; ok {
		t.Error("the key-derived head survives beside the named ones, so the account has two heads")
	}
	for _, want := range []string{"openrouter/anthropic/claude-sonnet-4.5", "openrouter/google/gemini-2.5-pro"} {
		if _, ok := byID(heads)[want]; !ok {
			t.Errorf("%s missing: %+v", want, heads)
		}
	}
}

// An allowlist with no key is not a head. The config names models; the
// credential is still what makes any of them reachable.
func TestDiscover_AllowlistWithoutAKeyFindsNothing(t *testing.T) {
	s := testutil.NewSandbox(t)
	writeAllowlist(t, s, "[openrouter]\nmodels = [\"anthropic/claude-sonnet-4.5\"]\n")

	heads, err := (&Provider{}).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(heads) != 0 {
		t.Errorf("discovered %+v with no OPENROUTER_API_KEY set", heads)
	}
}

// The allowlist must not reach any other provider's head.
func TestDiscover_AllowlistLeavesOtherProvidersAlone(t *testing.T) {
	s := testutil.NewSandbox(t)
	s.SetKey(t, "OPENROUTER_API_KEY", "sk-or")
	s.SetKey(t, "ANTHROPIC_API_KEY", "sk-ant")
	writeAllowlist(t, s, "[openrouter]\nmodels = [\"google/gemini-2.5-pro\"]\n")

	heads, err := (&Provider{}).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m := byID(heads)
	if _, ok := m["env/anthropic"]; !ok {
		t.Errorf("env/anthropic went missing: %+v", heads)
	}
	if _, ok := m["openrouter/google/gemini-2.5-pro"]; !ok {
		t.Errorf("the named OpenRouter model went missing: %+v", heads)
	}
	if len(heads) != 2 {
		t.Errorf("got %d heads, want exactly those two: %+v", len(heads), heads)
	}
}

// A config that cannot be parsed must not stop discovery: routing as before
// beats discovering nothing at all.
func TestDiscover_UnparseableConfigStillDiscovers(t *testing.T) {
	s := testutil.NewSandbox(t)
	s.SetKey(t, "OPENROUTER_API_KEY", "sk-test")
	writeAllowlist(t, s, "this is not toml [[[\n")

	heads, err := (&Provider{}).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(heads) != 1 || heads[0].ID != "env/openrouter" {
		t.Fatalf("got %+v, want the single env/openrouter head", heads)
	}
}
