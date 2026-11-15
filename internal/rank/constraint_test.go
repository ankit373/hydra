// SPDX-License-Identifier: MIT

package rank

import (
	"math"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/stats"
)

// heads used across these tests: a cheap local head with a low declared score
// and an expensive one with a high declared score, which is the whole question
// the constraint answers.
func pair() []provider.Head {
	return []provider.Head{
		{ID: "expensive", CapScore: 95},
		{ID: "cheap", CapScore: 55, LocalOnly: true},
	}
}

func fixedPrice(prices map[string]float64) Price {
	return func(h provider.Head) (float64, bool) {
		usd, ok := prices[h.ID]
		return usd, ok
	}
}

var listPrice = fixedPrice(map[string]float64{"expensive": 0.015, "cheap": 0})

// measured builds the lookup for a head with a perfect in-domain record.
func measured(id string, correct, total int) Lookup {
	return func(headID string) Measurement {
		if headID != id {
			return Measurement{}
		}
		return Measurement{
			Correct: correct, Total: total,
			InDomainCorrect: correct, InDomainTotal: total,
		}
	}
}

// The point of the whole exercise: once the cheap head has been measured
// competent at this domain, it runs, and the expensive one becomes the
// fallback rather than the default.
func TestCheapest_MeasuredCheapHeadBeatsTheStrongerOne(t *testing.T) {
	heads := pair()
	// 200 of 200 on a prior of 55: mean 96, lower bound 93. At 100 of 100 the bound is 88.
	scores := Scores(heads, measured("cheap", 200, 200))
	got, out := Cheapest(heads, scores, listPrice, 0.90)

	if got[0].ID != "cheap" {
		t.Fatalf("ran %s first, want cheap: %d in-domain at %d did not displace the expensive head",
			got[0].ID, out["cheap"].InDomain, out["cheap"].Effective)
	}
	if got[1].ID != "expensive" {
		t.Errorf("fallback chain = %v, want the expensive head still in it", order(got))
	}
	if !out["cheap"].Clears || out["expensive"].Clears {
		t.Errorf("clearance: cheap=%v expensive=%v, want only the measured one",
			out["cheap"].Clears, out["expensive"].Clears)
	}
}

// The floor that makes this safe to ship on by default. Below MinCommitments
// in this domain there is no measurement to route on, whatever the head has
// done elsewhere, so the order that arrived is the order that leaves.
func TestCheapest_NoInDomainEvidenceRoutesExactlyAsBefore(t *testing.T) {
	heads := pair()
	// A flawless record, all of it somewhere else.
	elsewhere := func(id string) Measurement {
		if id != "cheap" {
			return Measurement{}
		}
		return Measurement{Correct: 500, Total: 500}
	}
	scores := Scores(heads, elsewhere)
	got, out := Cheapest(heads, scores, listPrice, 0.90)

	if order(got) != "expensive,cheap" {
		t.Errorf("order = %s, want it untouched: no head was measured in this domain", order(got))
	}
	if out["cheap"].Clears {
		t.Error("a head with no in-domain evidence cleared an in-domain requirement")
	}
}

// Just under the floor is still under it. 19 judged answers is not 20, and the
// boundary is the only place a floor can be wrong.
func TestCheapest_TheFloorIsExact(t *testing.T) {
	heads := pair()
	for _, n := range []int{MinCommitments - 1, MinCommitments} {
		// Perfect at every count, so only the floor can decide this.
		scores := Scores(heads, measured("cheap", n, n))
		_, out := Cheapest(heads, scores, listPrice, 0.50)
		want := n >= MinCommitments
		if out["cheap"].Clears != want {
			t.Errorf("%d in-domain: Clears = %v, want %v", n, out["cheap"].Clears, want)
		}
	}
}

// A head measured and found wanting is not chosen, which is the half that
// stops this from being "always use the cheapest head".
func TestCheapest_MeasuredAndShortOfTheBarStaysWhereItWas(t *testing.T) {
	heads := pair()
	// 60 of 80 is 75%, well measured and well short of 90.
	scores := Scores(heads, measured("cheap", 60, 80))
	got, out := Cheapest(heads, scores, listPrice, 0.90)

	if got[0].ID != "expensive" {
		t.Errorf("ran %s first: a head measured at %d took work needing 90", got[0].ID, out["cheap"].Effective)
	}
	if out["cheap"].InDomain < MinCommitments {
		t.Errorf("in-domain count = %d, the test meant to measure it", out["cheap"].InDomain)
	}
}

// Pricing that cannot answer must not read as free. EstimateCost returns 0
// both for a local head and for a broken pricing file, and an unpriced head
// treated as $0 would be the cheapest thing on the machine.
func TestCheapest_AnUnpricedHeadIsNeverTheCheapest(t *testing.T) {
	heads := pair()
	scores := Scores(heads, measured("cheap", 100, 100))
	unpriced := fixedPrice(map[string]float64{"expensive": 0.015})

	got, out := Cheapest(heads, scores, unpriced, 0.90)
	if out["cheap"].Clears {
		t.Error("a head nothing could price cleared the constraint")
	}
	if got[0].ID != "expensive" {
		t.Errorf("ran %s first, want the priced head", got[0].ID)
	}
}

// Two clearing heads sort on price alone: once a head is competent enough for
// the task, the only question left is what it costs.
func TestCheapest_ClearingHeadsSortOnPrice(t *testing.T) {
	heads := []provider.Head{
		{ID: "best", CapScore: 95},
		{ID: "middling", CapScore: 80},
	}
	both := func(string) Measurement {
		return Measurement{Correct: 200, Total: 200, InDomainCorrect: 200, InDomainTotal: 200}
	}
	scores := Scores(heads, both)
	got, _ := Cheapest(heads, scores, fixedPrice(map[string]float64{"best": 0.02, "middling": 0.001}), 0.90)

	if got[0].ID != "middling" {
		t.Errorf("ran %s first: both cleared, so the cheaper one wins", got[0].ID)
	}
}

// Which head runs must not depend on the order the candidates arrived in.
// Probe order is not stable across machines or runs, so a comparator that
// leaves ties to the input silently routes two identical machines differently.
//
// Asking the same slice twice does not test this: the input to the sort is a
// slice here, not a map, so a comparator that answers false for everything
// still returns the order it was given, and the guard passes under the bug.
// Permuting the input is what separates the two.
func TestCheapest_TheChoiceDoesNotDependOnInputOrder(t *testing.T) {
	heads := []provider.Head{
		{ID: "a", CapScore: 90}, {ID: "b", CapScore: 90},
		{ID: "c", CapScore: 90}, {ID: "d", CapScore: 90},
	}
	all := func(string) Measurement {
		return Measurement{Correct: 100, Total: 100, InDomainCorrect: 100, InDomainTotal: 100}
	}
	// Every head at the same price and the same score, so nothing but the
	// tiebreak can decide, which is where a non-total comparator shows up.
	free := func(provider.Head) (float64, bool) { return 0, true }

	want, _ := Cheapest(heads, Scores(heads, all), free, 0.90)
	rng := rand.New(rand.NewPCG(1, 2))
	for i := 0; i < 50; i++ {
		shuffled := append([]provider.Head(nil), heads...)
		rng.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })

		got, _ := Cheapest(shuffled, Scores(shuffled, all), free, 0.90)
		if order(got) != order(want) {
			t.Fatalf("input %s chose %s, input %s chose %s",
				order(shuffled), order(got), order(heads), order(want))
		}
	}
}

// No requirement, no price, no heads: each leaves the list exactly as it was,
// which is what makes every caller that cannot supply one safe.
func TestCheapest_MissingInputsChangeNothing(t *testing.T) {
	heads := pair()
	scores := Scores(heads, measured("cheap", 100, 100))

	if got, _ := Cheapest(heads, scores, nil, 0.90); order(got) != "expensive,cheap" {
		t.Errorf("no pricing: order = %s, want it untouched", order(got))
	}
	if got, _ := Cheapest(heads, scores, listPrice, 0); order(got) != "expensive,cheap" {
		t.Errorf("no requirement: order = %s, want it untouched", order(got))
	}
	if got, _ := Cheapest(nil, scores, listPrice, 0.90); len(got) != 0 {
		t.Errorf("no heads: got %d", len(got))
	}
}

// Cheapest must not write into the map it was handed: the caller holds it to
// explain the order, and an annotation appearing in a map nobody asked to have
// annotated is how two readings of one ranking start to disagree.
func TestCheapest_DoesNotMutateTheScoresItWasGiven(t *testing.T) {
	heads := pair()
	scores := Scores(heads, measured("cheap", 100, 100))
	Cheapest(heads, scores, listPrice, 0.90)

	if scores["cheap"].Clears || scores["cheap"].CostUSD != 0 {
		t.Error("the input map was annotated in place")
	}
}

// order is the candidate chain as one comparable string.
func order(heads []provider.Head) string { return strings.Join(ids(heads), ",") }

// trialClears draws n results at rate p for each of heads heads declared 90, and reports
// whether any clears bar on the lower bound, and whether any would have on the mean.
func trialClears(rng *rand.Rand, heads, n int, p, bar float64) (lower, mean bool) {
	hs := make([]provider.Head, heads)
	got := make(map[string]Measurement, heads)
	for i := range hs {
		hs[i] = provider.Head{ID: string(rune('a' + i)), CapScore: 90}
		k := 0
		for range n {
			if rng.Float64() < p {
				k++
			}
		}
		got[hs[i].ID] = Measurement{Correct: k, Total: n, InDomainCorrect: k, InDomainTotal: n}
	}
	free := func(provider.Head) (float64, bool) { return 0, true }
	_, out := Cheapest(hs, Scores(hs, func(id string) Measurement { return got[id] }), free, bar)
	for _, sc := range out {
		lower = lower || sc.Clears
		mean = mean || float64(sc.Effective) >= bar*100
	}
	return lower, mean
}

// Five heads truly at 0.85 against a 0.90 bar, 20 results each: on the mean the
// luckiest one clears about 93% of the time. On the lower bound none ever does.
func TestCheapest_LuckyHeadsDoNotClear(t *testing.T) {
	rng := rand.New(rand.NewPCG(1461, 1537))
	const trials = 2000
	var byLower, byMean int
	for range trials {
		lower, mean := trialClears(rng, 5, 20, 0.85, 0.90)
		if lower {
			byLower++
		}
		if mean {
			byMean++
		}
	}
	if byLower != 0 {
		t.Errorf("a head truly at 0.85 cleared 0.90 in %d of %d trials", byLower, trials)
	}
	t.Run("the mean gate fails this", func(t *testing.T) {
		if byMean < trials*85/100 {
			t.Errorf("the mean let a lucky head clear in only %d of %d trials, so this test proves nothing", byMean, trials)
		}
	})
}

// A head truly at 0.97 with 200 results clears 0.90 almost always. At 100 results
// the 95% bound needs 95 of them right, which happens only about 92% of the time.
func TestCheapest_StrongHeadStillClears(t *testing.T) {
	rng := rand.New(rand.NewPCG(1461, 1537))
	const trials = 2000
	cleared := 0
	for range trials {
		if lower, _ := trialClears(rng, 1, 200, 0.97, 0.90); lower {
			cleared++
		}
	}
	if cleared < trials*99/100 {
		t.Errorf("a head truly at 0.97 cleared 0.90 in %d of %d trials, want at least 99%%", cleared, trials)
	}
}

// Lower and Effective describe one posterior: prior 85 borrowed from 36 of 40 elsewhere
// (scaled to 18 of 20), then 45 of 50 in-domain, is Beta(17+45, 3+5).
func TestScore_LowerBoundUsesTheSamePrior(t *testing.T) {
	m := Measurement{Correct: 81, Total: 90, InDomainCorrect: 45, InDomainTotal: 50}
	sc := Scores([]provider.Head{{ID: "h", CapScore: 80}}, func(string) Measurement { return m })["h"]
	a, b := 17.0+45, 3.0+5
	if want := int(math.Round(100 * a / (a + b))); sc.Effective != want {
		t.Fatalf("Effective = %d, want %d: the prior is not the one this test built", sc.Effective, want)
	}
	q, err := stats.BetaQuantile(1-ClearLevel, a, b)
	if err != nil {
		t.Fatal(err)
	}
	if want := int(math.Floor(100 * q)); sc.Lower != want {
		t.Errorf("Lower = %d, want %d", sc.Lower, want)
	}
}

// Lower is reported only for a measured head, and never above its mean.
func TestScore_LowerOnlyWhenMeasured(t *testing.T) {
	heads := pair()
	for _, n := range []int{MinCommitments - 1, MinCommitments, 500} {
		sc := Scores(heads, measured("cheap", n, n))["cheap"]
		if (sc.Lower > 0) != sc.Measured() || sc.Lower > sc.Effective {
			t.Errorf("%d in-domain: Lower = %d, Effective = %d, Measured = %v", n, sc.Lower, sc.Effective, sc.Measured())
		}
	}
}

// A posterior with no failures on a prior of 100, or no successes on a prior of 0,
// is a point mass: its bound is the point, not a refused quantile.
func TestScore_LowerAtAPointMass(t *testing.T) {
	for _, c := range []struct{ declared, correct, want int }{{100, 20, 100}, {0, 0, 0}} {
		heads := []provider.Head{{ID: "h", CapScore: c.declared}}
		if got := Scores(heads, measured("h", c.correct, 20))["h"].Lower; got != c.want {
			t.Errorf("declared %d, %d of 20: Lower = %d, want %d", c.declared, c.correct, got, c.want)
		}
	}
}
