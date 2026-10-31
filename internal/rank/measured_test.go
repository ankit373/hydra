// SPDX-License-Identifier: MIT

package rank

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/ankit373/hydra/internal/provider"
)

// noEvidence is what every head looks like on a fresh install.
func noEvidence(string) Measurement { return Measurement{} }

// The property the whole design rests on: with nothing measured the posterior
// is the prior, so a fresh machine ranks exactly as it did before there was a
// measurement at all. Swept rather than spot-checked, because a rounding slip
// would move some scores and not others.
func TestEffectiveScore_NoEvidenceIsExactlyDeclared(t *testing.T) {
	for declared := 0; declared <= 100; declared++ {
		if got := EffectiveScore(declared, Measurement{}); got != declared {
			t.Errorf("declared %d with no evidence: got %d, want it untouched", declared, got)
		}
	}
}

// Thin data must not move a score at all, however lopsided it is. Without the
// floor, five straight failures would knock 13 points off a declared 66.
func TestEffectiveScore_BelowTheFloorDoesNotMove(t *testing.T) {
	for n := range MinCommitments {
		worst := Measurement{Correct: 0, Total: n}
		best := Measurement{Correct: n, Total: n}
		if got := EffectiveScore(66, worst); got != 66 {
			t.Errorf("n=%d all wrong: got %d, want 66 until the floor is met", n, got)
		}
		if got := EffectiveScore(66, best); got != 66 {
			t.Errorf("n=%d all right: got %d, want 66 until the floor is met", n, got)
		}
	}
}

// The declared score is worth exactly MinCommitments observations, so at the
// floor the measurement and the catalogue weigh the same: the result is the
// midpoint. This is the one constant in the file, asserted rather than assumed.
func TestEffectiveScore_AtTheFloorTheDeclaredScoreWeighsEqually(t *testing.T) {
	const declared = 63
	cases := []struct {
		name string
		m    Measurement
		want int // midpoint of declared and the measured rate
	}{
		{"every answer held up", Measurement{Correct: MinCommitments, Total: MinCommitments}, 82},
		{"none held up", Measurement{Correct: 0, Total: MinCommitments}, 32},
		{"half held up", Measurement{Correct: MinCommitments / 2, Total: MinCommitments}, 57},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EffectiveScore(declared, tc.m); got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
}

// Evidence has to keep earning its weight, or the floor would be a cliff the
// score never moves past.
func TestEffectiveScore_EvidenceDominatesAsItAccumulates(t *testing.T) {
	const declared = 63
	prev := declared
	for _, n := range []int{20, 40, 100, 400, 2000} {
		got := EffectiveScore(declared, Measurement{Correct: n / 10, Total: n}) // a 10% head
		if got >= prev {
			t.Fatalf("n=%d: got %d, want it below the previous %d as evidence accumulates", n, got, prev)
		}
		prev = got
	}
	if prev > 12 {
		t.Errorf("with 2000 observations at 10%%, got %d, want it near the measured rate", prev)
	}
}

// measuredHead is a local head, so dedupeKey keys on its ID and two quants of
// one model both survive to be ranked against each other.
func measuredHead(id, quant string, score int) provider.Head {
	return provider.Head{
		ID: id, Name: id, Provider: "local", Source: "port",
		CapScore: score, LocalOnly: true, AuthReady: true,
		Meta: map[string]string{"model_source": "ollama", "model_quant": quant},
	}
}

// The defect #815 names: quantization was only a tiebreak, so it decided
// nothing between heads that were not otherwise identical. A 2-bit quant of a
// higher-scoring family outranked an 8-bit quant of a lower-scoring one, which
// is where accuracy actually falls off a cliff.
func TestByMeasured_AWorseQuantDoesNotOutrankABetterOneOnceMeasured(t *testing.T) {
	q2 := measuredHead("ollama/strong-family:7b-q2_K", "Q2_K", 70)
	q8 := measuredHead("ollama/weaker-family:7b-q8_0", "Q8_0", 63)

	declaredOnly, _ := ByMeasured([]provider.Head{q8, q2}, noEvidence)
	if declaredOnly[0].ID != q2.ID {
		t.Fatalf("with no evidence the higher declared score must still win: got %s", declaredOnly[0].ID)
	}

	lookup := func(id string) Measurement {
		switch id {
		case q2.ID:
			return Measurement{Correct: 6, Total: 30} // 20% once verified
		case q8.ID:
			return Measurement{Correct: 24, Total: 30} // 80% once verified
		}
		return Measurement{}
	}
	for _, in := range [][]provider.Head{{q2, q8}, {q8, q2}} {
		ranked, scores := ByMeasured(in, lookup)
		if ranked[0].ID != q8.ID {
			t.Errorf("input %s first: ranked %s ahead of the measured-better head %s",
				in[0].ID, ranked[0].ID, q8.ID)
		}
		if scores[q2.ID].Effective >= scores[q8.ID].Effective {
			t.Errorf("Q2 scored %d, Q8 %d: the measurement did not separate them",
				scores[q2.ID].Effective, scores[q8.ID].Effective)
		}
	}
}

// #765 was a coin flip because the comparator was not a total order and the
// dedupe pass iterates a map. Adding a key must not reopen that: the scores it
// ranks on come from a map too, so this runs enough times that randomization
// would surface.
func TestByMeasured_OrderIsTotalAndRepeatable(t *testing.T) {
	heads := []provider.Head{
		measuredHead("ollama/a:7b", "Q4_K_M", 66),
		measuredHead("ollama/b:7b", "Q8_0", 66),
		measuredHead("ollama/c:7b", "Q4_K_M", 66),
		measuredHead("ollama/d:7b", "", 66),
	}
	// Two heads land on the same effective score from different directions, so
	// the lower keys have to settle them.
	lookup := func(id string) Measurement {
		switch id {
		case "ollama/a:7b":
			return Measurement{Correct: 24, Total: 30}
		case "ollama/b:7b":
			return Measurement{Correct: 24, Total: 30}
		}
		return Measurement{}
	}

	first, _ := ByMeasured(heads, lookup)
	want := make([]string, len(first))
	for i, h := range first {
		want[i] = h.ID
	}

	rng := rand.New(rand.NewSource(1))
	for i := range 200 {
		shuffled := append([]provider.Head(nil), heads...)
		rng.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
		got, _ := ByMeasured(shuffled, lookup)
		for j, h := range got {
			if h.ID != want[j] {
				t.Fatalf("run %d: position %d is %s, want %s (order depends on input order)", i, j, h.ID, want[j])
			}
		}
	}
}

// AC: a head with insufficient evidence ranks exactly as today. Asserted
// against ByCapScore itself so the two can never drift apart.
func TestByMeasured_ThinEvidenceRanksExactlyLikeByCapScore(t *testing.T) {
	var heads []provider.Head
	for i := range 12 {
		heads = append(heads, measuredHead(fmt.Sprintf("ollama/m%02d:7b", i), "Q4_K_M", 60+i%4))
	}
	thin := func(string) Measurement { return Measurement{Correct: 3, Total: MinCommitments - 1} }

	want := ByCapScore(heads)
	got, scores := ByMeasured(heads, thin)
	for i := range want {
		if got[i].ID != want[i].ID {
			t.Fatalf("position %d: measured ranking gives %s, ByCapScore gives %s", i, got[i].ID, want[i].ID)
		}
	}
	for id, sc := range scores {
		if sc.Adjusted() {
			t.Errorf("%s: effective %d differs from declared %d on evidence below the floor", id, sc.Effective, sc.Declared)
		}
		if sc.N != 0 {
			t.Errorf("%s: reports n=%d behind an adjustment that did not happen", id, sc.N)
		}
	}
}

// The scores returned have to explain the order returned, or a caller
// rendering them would describe a ranking nobody got.
func TestByMeasured_ScoresExplainTheOrderReturned(t *testing.T) {
	heads := []provider.Head{
		measuredHead("ollama/a:7b", "Q4_K_M", 66),
		measuredHead("ollama/b:7b", "Q8_0", 50),
	}
	lookup := func(id string) Measurement {
		if id == "ollama/b:7b" {
			return Measurement{Correct: 38, Total: 40}
		}
		return Measurement{}
	}
	ranked, scores := ByMeasured(heads, lookup)

	if len(scores) != len(heads) {
		t.Fatalf("got %d scores for %d heads", len(scores), len(heads))
	}
	for i := 1; i < len(ranked); i++ {
		if scores[ranked[i-1].ID].Effective < scores[ranked[i].ID].Effective {
			t.Errorf("position %d ranks above %d but scores lower", i-1, i)
		}
	}
	for _, h := range heads {
		if scores[h.ID].Declared != h.CapScore {
			t.Errorf("%s: declared %d, want the head's own %d", h.ID, scores[h.ID].Declared, h.CapScore)
		}
	}
	if b := scores["ollama/b:7b"]; !b.Adjusted() || b.N != 40 {
		t.Errorf("the measured head reports %+v, want an adjustment backed by 40 commitments", b)
	}
}

// A nil lookup is the declared-only path every existing caller takes.
func TestByMeasured_NilLookupRanksOnDeclaredScores(t *testing.T) {
	heads := []provider.Head{
		measuredHead("ollama/a:7b", "Q4_K_M", 40),
		measuredHead("ollama/b:7b", "Q8_0", 80),
	}
	ranked, scores := ByMeasured(heads, nil)
	if ranked[0].ID != "ollama/b:7b" {
		t.Errorf("got %s first, want the higher declared score", ranked[0].ID)
	}
	for id, sc := range scores {
		if sc.Adjusted() {
			t.Errorf("%s was adjusted with no lookup at all", id)
		}
	}
}
