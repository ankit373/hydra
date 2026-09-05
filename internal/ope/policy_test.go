// SPDX-License-Identifier: MIT

package ope

import "testing"

func TestParsePolicy_KnownNames(t *testing.T) {
	tiers := TiersIn([]int{1, 3, 4, 8})
	cases := []struct{ spec, name string }{
		{"", "1 tier(s) cheaper"},
		{"cheaper", "1 tier(s) cheaper"},
		{"cheaper-by-2", "2 tier(s) cheaper"},
		{"stronger", "1 tier(s) stronger"},
		{"local", "local heads only"},
		{"tier:4", "always tier 4"},
		{"model:qwen3", "always qwen3"},
		{"  CHEAPER  ", "1 tier(s) cheaper"},
	}
	for _, c := range cases {
		p, err := ParsePolicy(c.spec, tiers)
		if err != nil {
			t.Errorf("ParsePolicy(%q): %v", c.spec, err)
			continue
		}
		if p.Name() != c.name {
			t.Errorf("ParsePolicy(%q).Name() = %q, want %q", c.spec, p.Name(), c.name)
		}
	}
}

// An unknown name must list what is accepted. "unknown policy" alone leaves
// someone guessing at a closed set.
func TestParsePolicy_UnknownNameListsTheAlternatives(t *testing.T) {
	_, err := ParsePolicy("wishful", TiersIn([]int{1}))
	if err == nil {
		t.Fatal("ParsePolicy accepted an unknown policy")
	}
	for _, want := range PolicyNames() {
		if !contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestParsePolicy_MalformedTierAndModel(t *testing.T) {
	for _, spec := range []string{"tier:", "tier:abc", "model:"} {
		if _, err := ParsePolicy(spec, TiersIn([]int{1})); err == nil {
			t.Errorf("ParsePolicy(%q) returned no error", spec)
		}
	}
}

// A shift off the end of the ladder has no target, so no row supports it. The
// alternative — mapping onto a tier that does not exist — would silently
// evaluate a policy nobody could run.
func TestTierShift_ShiftOffTheLadderSupportsNothing(t *testing.T) {
	p := TierShift{Shift: 1, Tiers: TiersIn([]int{3, 4})}
	if got := p.Would(Decision{Tier: 4}); got != 1 {
		t.Errorf("tier 4 should be what a 1-cheaper policy picks from tier 3, got %v", got)
	}
	if got := p.Would(Decision{Tier: 3}); got != 0 {
		t.Errorf("tier 3 is not reachable by shifting from any observed tier, got %v", got)
	}
	if got := p.Would(Decision{Tier: 9}); got != 0 {
		t.Errorf("tier 9 was never observed, got %v", got)
	}
}

func TestPinnedTierAndModel(t *testing.T) {
	if got := (PinnedTier{Tier: 8}).Would(Decision{Tier: 8}); got != 1 {
		t.Errorf("PinnedTier(8).Would(tier 8) = %v, want 1", got)
	}
	if got := (PinnedTier{Tier: 8}).Would(Decision{Tier: 3}); got != 0 {
		t.Errorf("PinnedTier(8).Would(tier 3) = %v, want 0", got)
	}
	// Model names arrive from a lowercased command line, so matching has to be
	// case-insensitive or `model:Claude-Sonnet-5` silently matches nothing.
	if got := (PinnedModel{Model: "claude-sonnet-5"}).Would(Decision{Model: "Claude-Sonnet-5"}); got != 1 {
		t.Errorf("model matching is case-sensitive, so a capitalised log entry never matches")
	}
}

func TestLocalOnly_RecognisesLocalRowsByExecutorOrPool(t *testing.T) {
	p := LocalOnly{}
	if got := p.Would(Decision{Executor: "ollama"}); got != 1 {
		t.Errorf("ollama executor = %v, want 1", got)
	}
	if got := p.Would(Decision{Pool: "local"}); got != 1 {
		t.Errorf("local pool = %v, want 1", got)
	}
	if got := p.Would(Decision{Executor: "http", Pool: "api"}); got != 0 {
		t.Errorf("an API row = %v, want 0", got)
	}
}

func TestSortedTiers(t *testing.T) {
	got := SortedTiers(TiersIn([]int{8, 1, 4, 1}))
	want := []int{1, 4, 8}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
