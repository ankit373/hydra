// SPDX-License-Identifier: MIT

package api

import (
	"os"
	"path/filepath"
	"testing"
)

// The embedded registry is what every installed binary runs with (#238), so the
// shipped default must produce a usable answer with no files on disk at all.
func TestGetModels_EmbeddedRegistryIsUsable(t *testing.T) {
	sandbox(t)

	r := New().GetModels()
	if !r.Found {
		t.Fatalf("Found = false (%s); the embedded models.yaml must always parse", r.Error)
	}
	if len(r.Pools) == 0 {
		t.Fatal("no pools; the embedded registry declares several")
	}

	var models int
	for _, p := range r.Pools {
		if p.Name == "" {
			t.Error("a pool has no name")
		}
		models += len(p.Models)
		for _, m := range p.Models {
			if m.ID == "" {
				t.Errorf("pool %q has a model with no id", p.Name)
			}
			if m.Tier == 0 {
				t.Errorf("model %q has tier 0; every registry model declares a tier", m.ID)
			}
		}
	}
	if models == 0 {
		t.Fatal("pools exist but contain no models")
	}
}

// The complexity band is what a picker shows in place of a thinking-depth dial
// Hydra does not have, so it has to survive the mapping rather than land as 0-0.
func TestGetModels_CarriesComplexityBand(t *testing.T) {
	sandbox(t)

	for _, p := range New().GetModels().Pools {
		for _, m := range p.Models {
			if m.ComplexityMax == 0 {
				t.Errorf("model %q has no complexity band; the picker would show 0-0", m.ID)
			}
			if m.ComplexityMin > m.ComplexityMax {
				t.Errorf("model %q band is inverted: %d-%d", m.ID, m.ComplexityMin, m.ComplexityMax)
			}
		}
	}
}

// Shared pools are the reason this groups by pool at all: opus and sonnet
// contend for one allocation, so the UI must be able to say so.
func TestGetModels_SharedPoolHasMultipleMembers(t *testing.T) {
	sandbox(t)

	shared := 0
	for _, p := range New().GetModels().Pools {
		if !p.Shared {
			continue
		}
		shared++
		if len(p.Models) < 2 {
			t.Errorf("pool %q is marked shared but has %d member(s), nothing to contend with",
				p.Name, len(p.Models))
		}
	}
	if shared == 0 {
		t.Error("no shared pool found; the embedded registry declares several")
	}
}

// A machine that has never dispatched still has a registry worth showing, a
// missing cost log must not blank the model list.
func TestGetModels_NoCostLogStillReturnsRegistry(t *testing.T) {
	sandbox(t)

	r := New().GetModels()
	if !r.Found || len(r.Pools) == 0 {
		t.Fatal("an absent cost.jsonl suppressed the registry")
	}
	for _, p := range r.Pools {
		if p.ObservedCalls != 0 || p.ObservedCostUSD != 0 {
			t.Errorf("pool %q reports spend with no cost log: %d calls, $%v",
				p.Name, p.ObservedCalls, p.ObservedCostUSD)
		}
	}
}

// Pool spend comes from cost.jsonl's own `pool` field, which dispatch already
// writes on every call. This pins that the join actually lands.
func TestGetModels_AggregatesObservedSpendPerPool(t *testing.T) {
	home := sandbox(t)

	// Two calls against one real pool from the embedded registry, one against
	// another, so a cross-pool leak would show up as the wrong total.
	writeCostRows(t, home, []map[string]any{
		{"ts": "2026-08-20T10:00:00Z", "pool": "agy_claude", "tier": 2,
			"prompt_tokens": 100, "response_tokens": 50, "est_cost_usd": 0.10},
		{"ts": "2026-08-20T10:01:00Z", "pool": "agy_claude", "tier": 3,
			"prompt_tokens": 200, "response_tokens": 100, "est_cost_usd": 0.20},
		{"ts": "2026-08-20T10:02:00Z", "pool": "agy_flash", "tier": 7,
			"prompt_tokens": 10, "response_tokens": 5, "est_cost_usd": 0.01},
	})

	reg := New().GetModels()
	var claude, flash *Pool
	for i := range reg.Pools {
		switch reg.Pools[i].Name {
		case "agy_claude":
			claude = &reg.Pools[i]
		case "agy_flash":
			flash = &reg.Pools[i]
		}
	}
	if claude == nil {
		t.Fatal("agy_claude pool missing from the registry")
	}

	if claude.ObservedCalls != 2 {
		t.Errorf("agy_claude ObservedCalls = %d, want 2", claude.ObservedCalls)
	}
	if got := claude.ObservedTokens; got != 450 {
		t.Errorf("agy_claude ObservedTokens = %d, want 450 (prompt+response)", got)
	}
	if claude.ObservedCostUSD < 0.29 || claude.ObservedCostUSD > 0.31 {
		t.Errorf("agy_claude ObservedCostUSD = %v, want ~0.30", claude.ObservedCostUSD)
	}
	if flash != nil && flash.ObservedCalls != 1 {
		t.Errorf("agy_flash ObservedCalls = %d, want 1, spend leaked across pools",
			flash.ObservedCalls)
	}
}

// The card beside the chat read "0 calls" while the list under it said 26, for
// the same model. The rows were written with pool="" because only the agy
// provider attached token_pool metadata, so local_ollama never matched any
// spend (#681). 90% of a real machine's rows were affected.
func TestGetModels_LocalPoolSpendIsCounted(t *testing.T) {
	home := sandbox(t)

	writeCostRows(t, home, []map[string]any{
		{"ts": "2026-09-05T10:00:00Z", "pool": "local_ollama", "tier": 10,
			"prompt_tokens": 40, "response_tokens": 20, "est_cost_usd": 0},
		{"ts": "2026-09-05T10:01:00Z", "pool": "local_ollama", "tier": 10,
			"prompt_tokens": 60, "response_tokens": 30, "est_cost_usd": 0},
		{"ts": "2026-09-05T10:02:00Z", "pool": "agy_claude", "tier": 2,
			"prompt_tokens": 10, "response_tokens": 5, "est_cost_usd": 0.10},
	})

	var local *Pool
	reg := New().GetModels()
	for i := range reg.Pools {
		if reg.Pools[i].Name == "local_ollama" {
			local = &reg.Pools[i]
		}
	}
	if local == nil {
		t.Fatal("local_ollama pool missing; the registry declares it for the Ollama models")
	}
	if local.ObservedCalls != 2 {
		t.Errorf("local_ollama ObservedCalls = %d, want 2. A free local call is still a call, "+
			"and 0 next to a list saying 2 makes the number worthless", local.ObservedCalls)
	}
	if got := local.ObservedTokens; got != 150 {
		t.Errorf("local_ollama ObservedTokens = %d, want 150", got)
	}
}

// An operator's on-disk registry must win over the embedded copy, or retuning
// routing without a rebuild (the whole point of registry.Read) silently fails.
func TestGetModels_OnDiskRegistryOverridesEmbedded(t *testing.T) {
	home := sandbox(t)

	dir := filepath.Join(home, ".hydra", "registry")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	yaml := `
token_pools:
  only_pool:
    members: [just-one]
    shared: false
    note: "override marker"
models:
  - tier: 4
    id: just-one
    name: "Only Model"
    provider: testing
    token_pool: only_pool
    enabled: true
    context_window: 1234
    complexity_min: 2
    complexity_max: 3
`
	if err := os.WriteFile(filepath.Join(dir, "models.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	r := New().GetModels()
	if !r.Found {
		t.Fatalf("Found = false: %s", r.Error)
	}
	if len(r.Pools) != 1 || len(r.Pools[0].Models) != 1 {
		t.Fatalf("got %d pool(s); the on-disk override declares exactly one", len(r.Pools))
	}
	m := r.Pools[0].Models[0]
	if m.ID != "just-one" {
		t.Errorf("model id = %q, want just-one, the embedded copy won", m.ID)
	}
	if m.ContextWindow != 1234 || m.ComplexityMax != 3 {
		t.Errorf("override fields lost: ctx=%d band=%d-%d", m.ContextWindow, m.ComplexityMin, m.ComplexityMax)
	}
	if r.Pools[0].Note != "override marker" {
		t.Errorf("pool note = %q, want the override's", r.Pools[0].Note)
	}
}

// Unparseable YAML must say so rather than presenting "no models" as fact.
func TestGetModels_MalformedRegistryReportsInsteadOfClaimingEmpty(t *testing.T) {
	home := sandbox(t)

	dir := filepath.Join(home, ".hydra", "registry")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "models.yaml"),
		[]byte("models: [this is not: valid: yaml"), 0o600); err != nil {
		t.Fatal(err)
	}

	r := New().GetModels()
	if r.Found {
		t.Error("Found = true on malformed YAML; an empty list would read as 'no models'")
	}
	if r.Error == "" {
		t.Error("no Error explaining why the registry is empty")
	}
}
