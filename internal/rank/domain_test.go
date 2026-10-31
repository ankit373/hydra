// SPDX-License-Identifier: MIT

package rank

import (
	"math"
	"math/rand"
	"testing"

	"github.com/ankit373/hydra/internal/provider"
)

// With nothing measured anywhere, the domain ranking must return the declared
// score untouched, exactly as the pooled one does. That is the property that
// makes this safe to put in the routing path.
func TestEffectiveScoreIn_NoEvidenceIsExactlyDeclared(t *testing.T) {
	for declared := 0; declared <= 100; declared++ {
		if got := EffectiveScoreIn(declared, Measurement{}); got != declared {
			t.Errorf("declared %d: got %d, want it untouched", declared, got)
		}
	}
}

// The flaw this bound exists for, measured end to end before it was added: a
// head with 500 judged answers in other domains and none at all here outranked
// one with 40 here at the same rate, 98 to 89. Borrowing that accumulates
// without limit makes the score very nearly domain-independent, which is the
// pooled ranking with extra steps.
func TestEffectiveScoreIn_InDomainEvidenceBeatsBorrowedEvidence(t *testing.T) {
	const declared = 70
	elsewhereOnly := Measurement{Correct: 495, Total: 500}                              // 99%, none here
	here := Measurement{Correct: 39, Total: 40, InDomainCorrect: 39, InDomainTotal: 40} // 97.5%, all here

	borrowed, direct := EffectiveScoreIn(declared, elsewhereOnly), EffectiveScoreIn(declared, here)
	if direct <= borrowed {
		t.Errorf("borrowed %d, direct %d: evidence from a domain nobody asked about outranked evidence from this one",
			borrowed, direct)
	}
}

// A head's own domain rows must not also serve as their own prior. Counting
// them twice makes it look more consistent than it is, and the error grows
// exactly where the evidence is thickest.
func TestEffectiveScoreIn_DomainEvidenceIsNotItsOwnPrior(t *testing.T) {
	const declared = 60
	// Everything is in this domain, so there is nothing left to borrow: the
	// prior must still be the declared score. 40/40 against 60 at strength 20
	// is (40+12)/60 = 87.
	all := Measurement{Correct: 40, Total: 40, InDomainCorrect: 40, InDomainTotal: 40}
	if got := EffectiveScoreIn(declared, all); got != 87 {
		t.Errorf("got %d, want 87: the domain's own rows were counted as their prior too", got)
	}
}

// Thin domain evidence must borrow rather than be judged on a handful of runs.
// That is what pooling is for, and it is the half the bound must not break.
func TestEffectiveScoreIn_ThinDomainBorrowsFromTheOthers(t *testing.T) {
	const declared = 60
	thin := Measurement{Correct: 90, Total: 100, InDomainCorrect: 3, InDomainTotal: 3}
	none := Measurement{Correct: 87, Total: 97} // the same record, with nothing here

	if got, want := EffectiveScoreIn(declared, thin), EffectiveScoreIn(declared, none); got != want {
		t.Errorf("got %d, want %d: three runs moved the score on their own", got, want)
	}
	if borrowed := EffectiveScoreIn(declared, none); borrowed <= declared {
		t.Fatalf("test is inert: the other domains did not move the score off %d", declared)
	}
}

// Once the domain has its own history it must dominate, or a head strong
// overall keeps being routed work it is measurably bad at.
func TestEffectiveScoreIn_ThickDomainOverridesTheOthers(t *testing.T) {
	const declared = 80
	// Excellent elsewhere (190/200), poor here (5/100).
	m := Measurement{Correct: 195, Total: 300, InDomainCorrect: 5, InDomainTotal: 100}

	got := EffectiveScoreIn(declared, m)
	if got >= 50 {
		t.Errorf("got %d, want it well below 50: 5 of 100 here barely moved the score", got)
	}
	if elsewhere := EffectiveScoreIn(declared, Measurement{Correct: 190, Total: 200}); got >= elsewhere {
		t.Errorf("got %d, borrowed alone says %d: the domain evidence changed nothing", got, elsewhere)
	}
}

// Inconsistent counts are a caller mistake, not evidence. A negative
// leave-one-out remainder must not be read as a run of failures.
func TestEffectiveScoreIn_InconsistentCountsAreNotReadAsFailures(t *testing.T) {
	const declared = 70
	m := Measurement{Correct: 5, Total: 10, InDomainCorrect: 30, InDomainTotal: 40}
	got := EffectiveScoreIn(declared, m)
	if got < 0 || got > 100 {
		t.Fatalf("got %d, want a score in range", got)
	}
	if want := posterior(declared, 30, 40); got != want {
		t.Errorf("got %d, want %d: the domain's own rows against the declared prior", got, want)
	}
}

// The bound limits how loud borrowed evidence is, never what it says.
func TestAtMost_KeepsTheRate(t *testing.T) {
	got := atMost(495, 500, MinCommitments)
	if got.Total != MinCommitments {
		t.Errorf("got total %d, want %d", got.Total, MinCommitments)
	}
	if want := int(math.Round(0.99 * float64(MinCommitments))); got.Correct != want {
		t.Errorf("got %d correct, want %d: the rate changed", got.Correct, want)
	}
	// Under the bound nothing is touched, or thin evidence would be rescaled
	// into a precision it does not have.
	if small := atMost(3, 5, MinCommitments); small.Correct != 3 || small.Total != 5 {
		t.Errorf("got %+v, want 3/5 untouched", small)
	}
}

// SortMeasured is what a dispatch reorders its candidates with. It must not
// deduplicate: dropping a candidate silently shortens a fallback chain.
func TestSortMeasured_KeepsEveryCandidate(t *testing.T) {
	// Two heads of one cloud provider, which ByMeasured would collapse.
	heads := []provider.Head{
		{ID: "openrouter/a", Name: "a", Provider: "openrouter", Source: "env", CapScore: 70},
		{ID: "openrouter/b", Name: "b", Provider: "openrouter", Source: "env", CapScore: 60},
	}
	sorted, _ := SortMeasured(heads, nil)
	if len(sorted) != 2 {
		t.Fatalf("got %d candidates from %d: SortMeasured deduplicated", len(sorted), len(heads))
	}
	if deduped, _ := ByMeasured(heads, nil); len(deduped) != 1 {
		t.Fatalf("ByMeasured kept %d, so this test no longer proves the two differ", len(deduped))
	}
}

// The domain key must not reopen #765: a new sort key that leaves two heads
// incomparable puts the coin flip back.
func TestSortMeasured_OrderIsTotalAndRepeatable(t *testing.T) {
	heads := []provider.Head{
		{ID: "ollama/a", Name: "a", Provider: "local", Source: "port", CapScore: 66, LocalOnly: true},
		{ID: "ollama/b", Name: "b", Provider: "local", Source: "port", CapScore: 66, LocalOnly: true},
		{ID: "ollama/c", Name: "c", Provider: "local", Source: "port", CapScore: 66, LocalOnly: true},
	}
	// a and b land on the same effective score by different routes.
	lookup := func(id string) Measurement {
		switch id {
		case "ollama/a":
			return Measurement{Correct: 30, Total: 40, InDomainCorrect: 24, InDomainTotal: 30}
		case "ollama/b":
			return Measurement{Correct: 24, Total: 30, InDomainCorrect: 24, InDomainTotal: 30}
		}
		return Measurement{}
	}

	first, _ := SortMeasured(heads, lookup)
	want := make([]string, len(first))
	for i, h := range first {
		want[i] = h.ID
	}

	rng := rand.New(rand.NewSource(7))
	for i := range 200 {
		shuffled := append([]provider.Head(nil), heads...)
		rng.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
		got, _ := SortMeasured(shuffled, lookup)
		for j := range got {
			if got[j].ID != want[j] {
				t.Fatalf("run %d position %d: got %s, want %s", i, j, got[j].ID, want[j])
			}
		}
	}
}

// Scores has to report the in-domain count separately, or a caller cannot tell
// a head measured here from one borrowing its score from elsewhere.
func TestScores_SeparatesInDomainEvidence(t *testing.T) {
	heads := []provider.Head{
		{ID: "here", Name: "here", Provider: "local", Source: "port", CapScore: 60, LocalOnly: true},
		{ID: "elsewhere", Name: "elsewhere", Provider: "local", Source: "port", CapScore: 60, LocalOnly: true},
	}
	got := Scores(heads, func(id string) Measurement {
		if id == "here" {
			return Measurement{Correct: 40, Total: 50, InDomainCorrect: 25, InDomainTotal: 30}
		}
		return Measurement{Correct: 40, Total: 50, InDomainCorrect: 1, InDomainTotal: 2}
	})

	if s := got["here"]; s.N != 50 || s.InDomain != 30 {
		t.Errorf("here: got n=%d in-domain=%d, want 50 and 30", s.N, s.InDomain)
	}
	if s := got["elsewhere"]; s.N != 50 || s.InDomain != 2 {
		t.Errorf("elsewhere: got n=%d in-domain=%d, want 50 and 2", s.N, s.InDomain)
	}
}

// A head measured at exactly its declared rate was still measured. Reporting
// no evidence for it would be a zero that means two different things.
func TestScores_EvidenceIsReportedEvenWhenItMovesNothing(t *testing.T) {
	h := provider.Head{ID: "flat", Name: "flat", Provider: "local", Source: "port", CapScore: 60, LocalOnly: true}
	// 60/100 against a prior of 60 lands back on 60.
	got := Scores([]provider.Head{h}, func(string) Measurement {
		return Measurement{Correct: 60, Total: 100}
	})
	sc := got["flat"]
	if sc.Effective != 60 {
		t.Fatalf("test is inert: effective %d, wanted it to land back on the declared 60", sc.Effective)
	}
	if sc.N != 100 {
		t.Errorf("got n=%d, want 100: a measurement that confirms the score is still a measurement", sc.N)
	}
}
