// SPDX-License-Identifier: MIT

package trust

import (
	"errors"
	"math"
	"math/rand"
	"testing"

	"github.com/ankit373/hydra/internal/reliability"
)

// randomSeries draws n values in [0,1) with positive weights, unsorted on purpose.
func randomSeries(rng *rand.Rand, n int) (y, w []float64) {
	y = make([]float64, n)
	w = make([]float64, n)
	for i := range y {
		y[i] = rng.Float64()
		w[i] = 1 + float64(rng.Intn(5))
	}
	return y, w
}

func TestPAV_OutputIsMonotone(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for trial := 0; trial < 200; trial++ {
		y, w := randomSeries(rng, 1+rng.Intn(60))
		fit := pav(y, w)
		for i := 1; i < len(fit); i++ {
			if fit[i] < fit[i-1] {
				t.Fatalf("trial %d: fit[%d]=%v < fit[%d]=%v", trial, i, fit[i], i-1, fit[i-1])
			}
		}
	}
}

// Fitting an already monotone fit must leave it where it is.
func TestPAV_IsIdempotent(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	for trial := 0; trial < 200; trial++ {
		y, w := randomSeries(rng, 1+rng.Intn(60))
		once := pav(y, w)
		twice := pav(once, w)
		for i := range once {
			if math.Abs(once[i]-twice[i]) > 1e-12 {
				t.Fatalf("trial %d: refit moved point %d from %v to %v", trial, i, once[i], twice[i])
			}
		}
	}
}

// PAV is the weighted least-squares projection, so pooled blocks take their weighted mean.
func TestPAV_PoolsViolatorsToWeightedMean(t *testing.T) {
	tests := []struct {
		name string
		y, w []float64
		want []float64
	}{
		{
			name: "already sorted",
			y:    []float64{0.1, 0.5, 0.9},
			w:    []float64{1, 1, 1},
			want: []float64{0.1, 0.5, 0.9},
		},
		{
			name: "one violator",
			y:    []float64{0.2, 0.8, 0.4},
			w:    []float64{1, 1, 1},
			want: []float64{0.2, 0.6, 0.6},
		},
		{
			name: "weighted pool",
			y:    []float64{1, 0},
			w:    []float64{1, 3},
			want: []float64{0.25, 0.25},
		},
		{
			name: "cascade",
			y:    []float64{0.9, 0.5, 0.1},
			w:    []float64{1, 1, 1},
			want: []float64{0.5, 0.5, 0.5},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := pav(tc.y, tc.w)
			for i := range tc.want {
				if math.Abs(got[i]-tc.want[i]) > 1e-12 {
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

func TestFitCorrection_IsMonotone(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	c, err := FitCorrection(overconfidentSource(rng, 500))
	if err != nil {
		t.Fatal(err)
	}
	prev := c.Apply(0)
	for step := 1; step <= 1000; step++ {
		stated := float64(step) / 1000
		got := c.Apply(stated)
		if got < prev {
			t.Fatalf("Apply(%v) = %v, below Apply at a lower stated value (%v)", stated, got, prev)
		}
		prev = got
	}
}

// The measurement the issue asks for: ECE on a held-out split, before and after.
func TestFitCorrection_ShrinksHeldOutErrorOnOverconfidentSource(t *testing.T) {
	rng := rand.New(rand.NewSource(4))
	train := overconfidentSource(rng, 2000)
	heldOut := overconfidentSource(rng, 2000)

	c, err := FitCorrection(train)
	if err != nil {
		t.Fatal(err)
	}
	corrected := make([]reliability.Observation, len(heldOut))
	for i, o := range heldOut {
		corrected[i] = reliability.Observation{Predicted: c.Apply(o.Predicted), Correct: o.Correct}
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

// Below the floor nothing is fitted, so every stated value passes through bit for bit.
func TestFitCorrection_BelowFloorIsIdentity(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	few := overconfidentSource(rng, reliability.MinObservations-1)
	c, err := FitCorrection(few)
	if !errors.Is(err, reliability.ErrTooFew) {
		t.Fatalf("err = %v, want ErrTooFew", err)
	}
	for _, stated := range []float64{0, 1e-9, 0.3, 0.5, 0.97, 1} {
		if got := c.Apply(stated); math.Float64bits(got) != math.Float64bits(stated) {
			t.Errorf("Apply(%v) = %v, want it unchanged", stated, got)
		}
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
			if _, err := FitCorrection(obs); err == nil {
				t.Fatalf("stated %v was accepted", tc.stated)
			}
		})
	}
}

// A block that went all-right must not become unbounded evidence.
func TestFitCorrection_BoundsCertainBlocks(t *testing.T) {
	obs := make([]reliability.Observation, reliability.MinObservations)
	for i := range obs {
		obs[i] = reliability.Observation{Predicted: 0.99, Correct: true}
	}
	c, err := FitCorrection(obs)
	if err != nil {
		t.Fatal(err)
	}
	n := float64(len(obs))
	if got, want := c.Apply(0.99), (n+1)/(n+2); got != want {
		t.Errorf("Apply(0.99) = %v, want the rule-of-succession bound %v", got, want)
	}
}

// Corrections share the calibration store's key, so "" and "default" are one cell.
func TestCorrections_KeyedLikeCalibration(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	var cs Corrections
	if err := cs.Fit("head-a", "", overconfidentSource(rng, 1000)); err != nil {
		t.Fatal(err)
	}
	raw := logit(0.95)
	corrected := cs.LLR("head-a", DefaultDomain, 0.95)
	if corrected >= raw {
		t.Errorf("fitted cell LLR = %v, want it below the raw %v for an overconfident source", corrected, raw)
	}
	if got := cs.LLR("head-b", DefaultDomain, 0.95); got != raw {
		t.Errorf("unfitted source LLR = %v, want the raw %v", got, raw)
	}
	if got := cs.LLR("head-a", "go", 0.95); got != raw {
		t.Errorf("other domain LLR = %v, want the raw %v", got, raw)
	}
}

func TestCorrections_NonProbabilityCarriesNoEvidence(t *testing.T) {
	var cs Corrections
	for _, stated := range []float64{-0.5, 1.2, math.NaN()} {
		if got := cs.LLR("head-a", "go", stated); got != 0 {
			t.Errorf("LLR(%v) = %v, want 0", stated, got)
		}
	}
}
