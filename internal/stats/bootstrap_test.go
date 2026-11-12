// SPDX-License-Identifier: MIT

package stats

import (
	"math"
	"math/rand/v2"
	"slices"
	"testing"
)

func resample(t *testing.T, stat Stat, resamples int, seed uint64) Bootstrap {
	t.Helper()
	b, err := Resample(stat, resamples, seed)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestResample_DegenerateSampleHasNoSpread(t *testing.T) {
	d := []float64{0.3, 0.3, 0.3, 0.3, 0.3, 0.3, 0.3, 0.3}
	b := resample(t, PairedMeanDiff(d), 1000, 1)
	for _, q := range []float64{0, 0.05, 0.5, 0.95, 1} {
		if got := b.Quantile(q); got != 0.3 {
			t.Errorf("Quantile(%v) = %v, want 0.3 exactly", q, got)
		}
	}
}

func TestResample_IsDeterministicInTheSeed(t *testing.T) {
	d := []float64{1, 0, -1, 0, 0, 1, 1, 0, -1, 0, 1, 0}
	a := resample(t, PairedMeanDiff(d), 500, 42)
	b := resample(t, PairedMeanDiff(d), 500, 42)
	c := resample(t, PairedMeanDiff(d), 500, 43)
	if !slices.Equal(a.Values, b.Values) {
		t.Error("one seed gave two samples")
	}
	if slices.Equal(a.Values, c.Values) {
		t.Error("two seeds gave one sample")
	}
	if !slices.IsSorted(a.Values) {
		t.Error("Values are not sorted")
	}
}

// Recorded from the first implementation (scope §6, E7.1-4): a change here means the
// RNG stream or the resampling order moved, and every pinned interval moves with it.
func TestResample_RegressionFixture(t *testing.T) {
	d := []float64{
		1, 0, 0, -1, 0, 1, 1, 0, 0, 0, -1, 0, 1, 0, 0, 0, 1, -1, 0, 0, 0, 0, 1, 0, 0,
		0, -1, 0, 0, 1, 0, 0, 0, 0, 1, 0, -1, -1, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, -1, 0,
	}
	b := resample(t, PairedMeanDiff(d), 10000, 7)
	near(t, "Quantile(0.05)", b.Quantile(0.05), -0.08, exact)
	if b.Dropped != 0 || len(b.Values) != 10000 {
		t.Errorf("kept %d, dropped %d", len(b.Values), b.Dropped)
	}
}

// With 2,000 items the percentile bound must sit where the normal approximation puts it.
func TestResample_PairedLowerBoundMatchesTheNormalApproximation(t *testing.T) {
	rng := rand.New(rand.NewPCG(2026, 11))
	d := make([]float64, 2000)
	for i := range d {
		switch u := rng.Float64(); {
		case u < 0.045: // B: p_disc = 0.10, Δ = −0.01
			d[i] = 1
		case u < 0.10:
			d[i] = -1
		}
	}
	var mean, ss float64
	for _, v := range d {
		mean += v
	}
	mean /= float64(len(d))
	for _, v := range d {
		ss += (v - mean) * (v - mean)
	}
	sd := math.Sqrt(ss / float64(len(d)-1))
	b := resample(t, PairedMeanDiff(d), 10000, 3)
	near(t, "bootstrap LB", b.Quantile(0.05), mean-NormalQuantile(0.95)*sd/math.Sqrt(float64(len(d))), 0.002)
}

func TestResample_DropsAndCountsUndefinedResamples(t *testing.T) {
	cost := []float64{1, 1, 1, 1, 1, 1, 1, 1}
	accA := []bool{true, false, false, false, false, false, false, false}
	accR := []bool{true, true, true, true, true, true, true, true}
	stat, err := CostPerAcceptedDiff(cost, cost, accA, accR)
	if err != nil {
		t.Fatal(err)
	}
	b := resample(t, stat, 2000, 5)
	if b.Dropped == 0 || len(b.Values)+b.Dropped != 2000 {
		t.Errorf("kept %d, dropped %d of 2000", len(b.Values), b.Dropped)
	}
	empty := resample(t, PairedMeanDiff(nil), 10, 1)
	if empty.Dropped != 10 || empty.Quantile(0.5) != 0 {
		t.Errorf("an empty sample gave %+v", empty)
	}
}

func TestQuantile_ReadsTheFloorRank(t *testing.T) {
	b := Bootstrap{Values: []float64{10, 20, 30, 40, 50}}
	for q, want := range map[float64]float64{0: 10, 0.24: 10, 0.25: 20, 0.74: 30, 0.99: 40, 1: 50, -1: 10, 2: 50} {
		if got := b.Quantile(q); got != want {
			t.Errorf("Quantile(%v) = %v, want %v", q, got, want)
		}
	}
}

func TestStatistics_OnTheIdentityResample(t *testing.T) {
	all := []int{0, 1, 2}
	cpa, err := CostPerAcceptedDiff([]float64{1, 2, 3}, []float64{2, 2, 2},
		[]bool{true, false, true}, []bool{true, true, true})
	if err != nil {
		t.Fatal(err)
	}
	v, ok := cpa.eval(all)
	if !ok {
		t.Fatal("undefined")
	}
	near(t, "cost per accepted diff", v, 6.0/2-6.0/3, exact)
	if whole, ok := cpa.All(); !ok || whole != v {
		t.Errorf("All = %v, %v; want %v from the identity resample", whole, ok, v)
	}
	none, _ := CostPerAcceptedDiff([]float64{1}, []float64{1}, []bool{false}, []bool{true})
	if _, ok := none.All(); ok {
		t.Error("All gave a cost per accepted answer for an arm that accepted nothing")
	}
	if _, ok := (Stat{}).All(); ok {
		t.Error("All evaluated a statistic no constructor built")
	}

	wm, err := WeightedMean([]float64{1, 4, 10}, []float64{1, 2, 0})
	if err != nil {
		t.Fatal(err)
	}
	v, ok = wm.eval(all)
	if !ok {
		t.Fatal("undefined")
	}
	near(t, "weighted mean", v, 3, exact)
	whole, err := WeightedMeanAll([]float64{1, 4, 10}, []float64{1, 2, 0})
	if err != nil || whole != v {
		t.Errorf("WeightedMeanAll = %v, %v; want %v from the identity resample", whole, err, v)
	}

	zero, _ := WeightedMean([]float64{1, 4}, []float64{0, 0})
	if _, ok := zero.eval([]int{0, 1}); ok {
		t.Error("a zero total weight gave a mean")
	}
	if _, err := WeightedMeanAll([]float64{1, 4}, []float64{0, 0}); err == nil {
		t.Error("WeightedMeanAll gave a mean for a zero total weight")
	}
}

func TestBootstrap_RefusesRatherThanPanics(t *testing.T) {
	for _, resamples := range []int{0, -1} {
		if _, err := Resample(PairedMeanDiff([]float64{1}), resamples, 1); err == nil {
			t.Errorf("Resample answered %d resamples", resamples)
		}
	}
	if _, err := Resample(Stat{}, 10, 1); err == nil {
		t.Error("Resample ran a statistic nothing built")
	}
	if _, err := WeightedMean([]float64{1, 2}, []float64{1}); err == nil {
		t.Error("WeightedMean paired slices of different lengths")
	}
	if _, err := WeightedMeanAll([]float64{1}, []float64{1, 2}); err == nil {
		t.Error("WeightedMeanAll paired slices of different lengths")
	}
	c, b := []float64{1, 2}, []bool{true, true}
	for i, args := range [][4]int{{1, 2, 2, 2}, {2, 1, 2, 2}, {2, 2, 1, 2}, {2, 2, 2, 1}} {
		if _, err := CostPerAcceptedDiff(c[:args[0]], c[:args[1]], b[:args[2]], b[:args[3]]); err == nil {
			t.Errorf("case %d: CostPerAcceptedDiff paired slices of different lengths", i)
		}
	}
}

// A Stat carries its own length, so a 2-item statistic is resampled at n = 2 and no
// caller can ask for n = 10 and index past its data.
func TestResample_DrawsOnlyWithinTheStatsOwnData(t *testing.T) {
	inner := PairedMeanDiff([]float64{1, -1})
	spy := Stat{n: inner.n, eval: func(idx []int) (float64, bool) {
		if len(idx) != 2 {
			t.Fatalf("drew %d indices from 2 items", len(idx))
		}
		for _, j := range idx {
			if j < 0 || j >= 2 {
				t.Fatalf("drew index %d from 2 items", j)
			}
		}
		return inner.eval(idx)
	}}
	if b := resample(t, spy, 200, 1); len(b.Values) != 200 {
		t.Errorf("kept %d of 200", len(b.Values))
	}
}
