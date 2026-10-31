// SPDX-License-Identifier: MIT

package agy

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/ankit373/hydra/internal/testutil"
)

// This package turns registry/models.yaml into routable heads. It was at 0%,
// and it is the layer #238 was about: it read disk only, so every installed
// binary discovered no agy heads at all.

func writeModels(t *testing.T, hydraHome, body string) {
	t.Helper()
	dir := filepath.Join(hydraHome, "registry")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "models.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func discover(t *testing.T) []headLite {
	t.Helper()
	heads, err := (&Provider{}).Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover returned an error: %v", err)
	}
	out := make([]headLite, 0, len(heads))
	for _, h := range heads {
		out = append(out, headLite{h.ID, h.Name, h.Provider, h.Source, h.CapScore, h.Meta})
	}
	return out
}

type headLite struct {
	ID, Name, Provider, Source string
	CapScore                   int
	Meta                       map[string]string
}

// The #238 contract: with no on-disk registry, the embedded copy must still
// yield heads. Anything else means a brew/npm/pip install can never route to
// agy.
func TestDiscover_UsesTheEmbeddedRegistryWhenNothingIsOnDisk(t *testing.T) {
	testutil.NewSandbox(t)

	heads := discover(t)
	if len(heads) == 0 {
		t.Fatal("no agy heads from the embedded registry, this is what every " +
			"installed binary sees (#238)")
	}
	for _, h := range heads {
		if h.Provider != "antigravity" {
			t.Errorf("%s: Provider = %q, want antigravity", h.ID, h.Provider)
		}
		if h.Source != "registry" {
			t.Errorf("%s: Source = %q, want registry", h.ID, h.Source)
		}
		if h.Meta["model_flag"] == "" {
			t.Errorf("%s has no model_flag; the executor cannot select a model", h.ID)
		}
		if h.Meta["tier"] == "" {
			t.Errorf("%s has no tier meta; rank.UITier falls back to CapScore thresholds", h.ID)
		}
		if _, err := strconv.Atoi(h.Meta["tier"]); err != nil {
			t.Errorf("%s tier meta %q is not a number", h.ID, h.Meta["tier"])
		}
	}
}

// An on-disk registry overrides the embedded one, which is the whole point of
// $HYDRA_HOME: retune routing without a rebuild.
func TestDiscover_OnDiskRegistryWins(t *testing.T) {
	s := testutil.NewSandbox(t)
	writeModels(t, s.HydraHome, `
models:
  - tier: 3
    id: only-one
    name: Only One
    executor: agy
    model_flag: "--model=x"
    token_pool: pool-a
    enabled: true
`)

	heads := discover(t)
	if len(heads) != 1 || heads[0].ID != "only-one" {
		t.Fatalf("got %+v, want just the on-disk entry", heads)
	}
	if heads[0].CapScore != 88 {
		t.Errorf("tier 3 scored %d, want 88", heads[0].CapScore)
	}
	if heads[0].Meta["token_pool"] != "pool-a" {
		t.Errorf("token_pool = %q", heads[0].Meta["token_pool"])
	}
}

// Every exclusion rule, stated. A head that should be filtered but is not gets
// dispatched to and fails at the point of use.
func TestDiscover_SkipsEntriesThatCannotBeRoutedTo(t *testing.T) {
	s := testutil.NewSandbox(t)
	writeModels(t, s.HydraHome, `
models:
  - {tier: 3, id: good,          name: Good,      executor: agy,   model_flag: "--m=1", enabled: true}
  - {tier: 3, id: disabled,      name: Disabled,  executor: agy,   model_flag: "--m=2", enabled: false}
  - {tier: 3, id: other-exec,    name: Other,     executor: ollama, model_flag: "--m=3", enabled: true}
  - {tier: 3, id: no-flag,       name: NoFlag,    executor: agy,   model_flag: "",      enabled: true}
  - {tier: 3, id: null-flag,     name: NullFlag,  executor: agy,   model_flag: "null",  enabled: true}
`)

	heads := discover(t)
	if len(heads) != 1 {
		t.Fatalf("got %d heads, want only the routable one: %+v", len(heads), heads)
	}
	if heads[0].ID != "good" {
		t.Errorf("kept %q, want %q", heads[0].ID, "good")
	}
}

// Tier → CapScore is what puts agy heads in the right place in the ladder.
func TestTierScore(t *testing.T) {
	want := map[int]int{2: 92, 3: 88, 4: 82, 5: 80, 6: 78, 7: 72, 8: 70, 9: 68}
	for tier, score := range want {
		if got := tierScore(tier); got != score {
			t.Errorf("tierScore(%d) = %d, want %d", tier, got, score)
		}
	}
	// Unknown tiers get a mid fallback rather than 0, which would sort them
	// below every local model.
	for _, unknown := range []int{0, 1, 10, 99, -1} {
		if got := tierScore(unknown); got != 60 {
			t.Errorf("tierScore(%d) = %d, want the 60 fallback", unknown, got)
		}
	}
}

// The score ladder must be monotonic in the tier: a stronger tier (lower
// number) must never score below a weaker one, or routing inverts.
func TestTierScore_IsMonotonicAcrossTheLadder(t *testing.T) {
	prev := 1 << 30
	for tier := 2; tier <= 9; tier++ {
		got := tierScore(tier)
		if got >= prev {
			t.Errorf("tier %d scored %d, not below tier %d's %d, the ladder inverts",
				tier, got, tier-1, prev)
		}
		prev = got
	}
}

// Malformed YAML must degrade to "no agy heads", never to an error that stops
// the whole probe or to a partial head with missing routing metadata.
func TestDiscover_MalformedYAMLYieldsNoHeadsAndNoError(t *testing.T) {
	s := testutil.NewSandbox(t)
	writeModels(t, s.HydraHome, "models: [this is not: valid: yaml")

	heads, err := (&Provider{}).Discover(context.Background())
	if err != nil {
		t.Errorf("malformed models.yaml returned an error (%v); it must degrade quietly "+
			"so one bad file does not stop the probe", err)
	}
	if len(heads) != 0 {
		t.Errorf("malformed models.yaml produced heads: %+v", heads)
	}
}

func TestDiscover_EmptyRegistryYieldsNoHeads(t *testing.T) {
	s := testutil.NewSandbox(t)
	writeModels(t, s.HydraHome, "models: []\n")

	if heads := discover(t); len(heads) != 0 {
		t.Errorf("got %+v from an empty models list", heads)
	}
}

func TestProvider_ID(t *testing.T) {
	if got := (&Provider{}).ID(); got != "agy" {
		t.Errorf("ID() = %q, want agy", got)
	}
}
