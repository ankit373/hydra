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
		p, err := ParsePolicy(c.spec, Env{Tiers: tiers})
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
	_, err := ParsePolicy("wishful", Env{Tiers: TiersIn([]int{1})})
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
		if _, err := ParsePolicy(spec, Env{Tiers: TiersIn([]int{1})}); err == nil {
			t.Errorf("ParsePolicy(%q) returned no error", spec)
		}
	}
}

// A shift off the end of the ladder has no target, so no row supports it. The
// alternative, mapping onto a tier that does not exist, would silently
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

// The constraint policy needs the machine, not just the log. Refusing without
// it beats scoring every row 0, which reads as "the policy would never do
// this" rather than "nothing here could answer".
func TestParsePolicy_ConstraintNeedsTheMachine(t *testing.T) {
	if _, err := ParsePolicy("constraint", Env{Tiers: TiersIn([]int{1})}); err == nil {
		t.Error("the constraint policy was accepted with no way to say what it would choose")
	}
	chosen := func(string) (string, bool) { return "local", true }
	p, err := ParsePolicy("constraint", Env{Tiers: TiersIn([]int{1}), Chosen: chosen})
	if err != nil {
		t.Fatalf("ParsePolicy(constraint): %v", err)
	}
	if _, ok := p.(Constraint); !ok {
		t.Errorf("ParsePolicy(constraint) returned %T", p)
	}
}

// A row that predates domain logging cannot say what the constraint would have
// done with it. Abstaining drops it from the estimate; scoring it 0 would
// assert the policy routed elsewhere, which is a claim nobody can support.
func TestConstraint_AbstainsOnRowsThatCannotAnswer(t *testing.T) {
	asked := 0
	p := Constraint{Chosen: func(string) (string, bool) { asked++; return "local", true }}

	for _, d := range []Decision{
		{Head: "local"}, // no domain
		{Domain: "go"},  // no head
		{},              // neither
	} {
		if got := p.Would(d); got != 0 {
			t.Errorf("%+v scored %v, want 0", d, got)
		}
	}
	if asked != 0 {
		t.Errorf("asked the machine %d times about rows that carry no context", asked)
	}
}

// The policy agrees with the row exactly when the head it would choose is the
// head that ran, and head ids are compared without regard to case because the
// log and the probe do not always agree on it.
func TestConstraint_MatchesTheHeadItWouldChoose(t *testing.T) {
	p := Constraint{Chosen: func(domain string) (string, bool) {
		if domain != "go" {
			return "", false
		}
		return "ollama/qwen3:4b", true
	}}

	if got := p.Would(Decision{Domain: "go", Head: "Ollama/Qwen3:4b"}); got != 1 {
		t.Errorf("the head it would choose scored %v, want 1", got)
	}
	if got := p.Would(Decision{Domain: "go", Head: "anthropic/claude"}); got != 0 {
		t.Errorf("a head it would not choose scored %v, want 0", got)
	}
	// A domain the machine cannot route for is not a disagreement.
	if got := p.Would(Decision{Domain: "cobol", Head: "ollama/qwen3:4b"}); got != 0 {
		t.Errorf("an unroutable domain scored %v, want 0", got)
	}
}

// A policy that cannot be listed cannot be asked for.
func TestPolicyNames_ListsTheConstraint(t *testing.T) {
	for _, n := range PolicyNames() {
		if n == "constraint" {
			return
		}
	}
	t.Errorf("PolicyNames() = %v, missing the constraint policy", PolicyNames())
}
