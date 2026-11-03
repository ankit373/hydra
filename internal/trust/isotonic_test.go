// SPDX-License-Identifier: MIT

package trust

import (
	"errors"
	"math"
	"math/rand"
	"testing"

	"github.com/ankit373/hydra/internal/reliability"
)

// randomTallies draws n tallies of one to five verdicts each, unsorted on purpose.
func randomTallies(rng *rand.Rand, n int) []tally {
	points := make([]tally, n)
	for i := range points {
		size := 1 + rng.Intn(5)
		points[i] = tally{hits: float64(rng.Intn(size + 1)), n: float64(size)}
	}
	return points
}

func TestPAV_OutputIsMonotone(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for trial := 0; trial < 200; trial++ {
		blocks := pav(randomTallies(rng, 1+rng.Intn(60)))
		for i := 1; i < len(blocks); i++ {
			if blocks[i].rate() < blocks[i-1].rate() {
				t.Fatalf("trial %d: block %d rate %v < block %d rate %v",
					trial, i, blocks[i].rate(), i-1, blocks[i-1].rate())
			}
		}
	}
}

// A pooled block's rate comes from its own pooled counts, never the total's.
func TestPAV_PoolsViolatorsOnTheirOwnCounts(t *testing.T) {
	tests := []struct {
		name   string
		points []tally
		want   []block
	}{
		{
			name: "already ordered",
			points: []tally{
				{hits: 0, n: 1},
				{hits: 1, n: 1},
			},
			want: []block{
				{tally: tally{hits: 0, n: 1}, first: 0},
				{tally: tally{hits: 1, n: 1}, first: 1},
			},
		},
		{
			name: "one violator",
			points: []tally{
				{hits: 1, n: 1},
				{hits: 0, n: 1},
			},
			want: []block{
				{tally: tally{hits: 1, n: 2}, first: 0},
			},
		},
		{
			name: "cascade",
			points: []tally{
				{hits: 2, n: 2},
				{hits: 1, n: 2},
				{hits: 0, n: 2},
			},
			want: []block{
				{tally: tally{hits: 3, n: 6}, first: 0},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := pav(tc.points)
			if len(got) != len(tc.want) {
				t.Fatalf("pav = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("pav = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// overconfidentSource states p but is right only at 0.5+0.4(p-0.5): it is
// sure of itself at the ends where it is barely better than a coin.
func overconfidentSource(rng *rand.Rand, n int) []reliability.Observation {
	obs := make([]reliability.Observation, n)
	for i := range obs {
		stated := rng.Float64()
		actual := 0.5 + 0.4*(stated-0.5)
		obs[i] = reliability.Observation{Predicted: stated, Correct: rng.Float64() < actual}
	}
	return obs
}

// repeated returns n verdicts at one stated value, the first hits of them right.
func repeated(stated float64, n, hits int) []reliability.Observation {
	obs := make([]reliability.Observation, n)
	for i := range obs {
		obs[i] = reliability.Observation{Predicted: stated, Correct: i < hits}
	}
	return obs
}

func TestFitCorrection_IsMonotone(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	c, err := fitCorrection(overconfidentSource(rng, 500))
	if err != nil {
		t.Fatal(err)
	}
	prev := c.apply(0)
	for step := 1; step <= 1000; step++ {
		stated := float64(step) / 1000
		got := c.apply(stated)
		if got < prev {
			t.Fatalf("apply(%v) = %v, below apply at a lower stated value (%v)", stated, got, prev)
		}
		prev = got
	}
}

// The measurement the issue asks for: ECE on a held-out split, before and after.
func TestFitCorrection_ShrinksHeldOutErrorOnOverconfidentSource(t *testing.T) {
	rng := rand.New(rand.NewSource(4))
	train := overconfidentSource(rng, 2000)
	heldOut := overconfidentSource(rng, 2000)

	c, err := fitCorrection(train)
	if err != nil {
		t.Fatal(err)
	}
	corrected := make([]reliability.Observation, len(heldOut))
	for i, o := range heldOut {
		corrected[i] = reliability.Observation{Predicted: c.apply(o.Predicted), Correct: o.Correct}
	}
	before, err := reliability.Evaluate(heldOut, 0)
	if err != nil {
		t.Fatal(err)
	}
	after, err := reliability.Evaluate(corrected, 0)
	if err != nil {
		t.Fatal(err)
	}
	if after.ECE >= before.ECE/2 {
		t.Errorf("held-out ECE %.4f -> %.4f, want the correction to at least halve it", before.ECE, after.ECE)
	}
}

func TestFitCorrection_BelowFloorIsRefused(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	few := overconfidentSource(rng, reliability.MinObservations-1)
	if _, err := fitCorrection(few); !errors.Is(err, reliability.ErrTooFew) {
		t.Fatalf("err = %v, want ErrTooFew", err)
	}
}

func TestFitCorrection_RefusesNonProbability(t *testing.T) {
	tests := []struct {
		name   string
		stated float64
	}{
		{name: "negative", stated: -0.1},
		{name: "above one", stated: 1.5},
		{name: "NaN", stated: math.NaN()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			obs := overconfidentSource(rand.New(rand.NewSource(6)), reliability.MinObservations)
			obs[3].Predicted = tc.stated
			if _, err := fitCorrection(obs); err == nil {
				t.Fatalf("stated %v was accepted", tc.stated)
			}
		})
	}
}

// Three right answers are worth what three verdicts support, however many sit below them.
func TestFitCorrection_BoundsEachBlockByItsOwnCount(t *testing.T) {
	obs := append(repeated(0.6, 1000, 600), repeated(0.99, 3, 3)...)
	c, err := fitCorrection(obs)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := c.apply(0.99), 4.0/5; got != want {
		t.Errorf("apply(0.99) = %v, want the three-verdict bound %v", got, want)
	}
	if got, wald := c.llr(0.99), math.Log(19); got >= wald {
		t.Errorf("llr(0.99) = %.2f nats, want under the %.2f Wald needs at 95%%", got, wald)
	}
}

// A stated value that says nothing beyond the source's base rate is no evidence,
// or summing votes would count that base rate once per source.
func TestCorrection_LLRExcludesTheBaseRate(t *testing.T) {
	c, err := fitCorrection(repeated(0.9, 100, 80))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.llr(0.9); got != 0 {
		t.Errorf("llr(0.9) = %v, want 0 for a source right 80%% of the time whatever it states", got)
	}
}

// A perfect source stating a different value each time must not count against itself.
func TestFitCorrection_PoolsEqualRates(t *testing.T) {
	obs := make([]reliability.Observation, 0, 200)
	for i := range 200 {
		stated := 0.9 + 0.1*float64(i)/200
		obs = append(obs, reliability.Observation{Predicted: stated, Correct: true})
	}
	c, err := fitCorrection(obs)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.llr(0.99); got < 0 {
		t.Errorf("llr(0.99) = %v for a source right 200 of 200, want >= 0", got)
	}
}

func TestCorrections_UnfittedSourceCarriesNoEvidence(t *testing.T) {
	var cs corrections
	for _, stated := range []float64{0.5, 0.99, 1} {
		if got := cs.llr("head-a", "go", stated); got != 0 {
			t.Errorf("llr(%v) = %v, want 0 with nothing fitted", stated, got)
		}
	}
}

// Corrections share the calibration store's key, so "" and "default" are one cell.
func TestCorrections_KeyedLikeCalibration(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	var cs corrections
	if err := cs.fit("head-a", "", overconfidentSource(rng, 1000)); err != nil {
		t.Fatal(err)
	}
	if got := cs.llr("head-a", DefaultDomain, 0.95); got <= 0 {
		t.Errorf("fitted cell llr = %v, want positive evidence at a high stated value", got)
	}
	if got := cs.llr("head-a", "go", 0.95); got != 0 {
		t.Errorf("other domain llr = %v, want 0", got)
	}
}

func TestCorrections_NonProbabilityCarriesNoEvidence(t *testing.T) {
	rng := rand.New(rand.NewSource(8))
	var cs corrections
	if err := cs.fit("head-a", "go", overconfidentSource(rng, 1000)); err != nil {
		t.Fatal(err)
	}
	for _, stated := range []float64{-0.5, 1.2, math.NaN()} {
		if got := cs.llr("head-a", "go", stated); got != 0 {
			t.Errorf("llr(%v) = %v, want 0", stated, got)
		}
	}
}
