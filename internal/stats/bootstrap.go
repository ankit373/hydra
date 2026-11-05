// SPDX-License-Identifier: MIT

// Package stats is the benchmark harness's statistics, shared with ope and entity
// so each interval has one implementation. Pure maths: deterministic given inputs and seed.
package stats

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"
)

// Bootstrap is a sorted sample of a resampled statistic. Undefined resamples are
// dropped and counted, never replaced with 0.
type Bootstrap struct {
	Values  []float64
	Dropped int
}

// Quantile reads Values[⌊q·(len−1)⌋], clamped to the slice: nearest rank rounded
// down, which assumes nothing about the distribution's shape. An empty sample reads 0.
func (b Bootstrap) Quantile(q float64) float64 {
	if len(b.Values) == 0 {
		return 0
	}
	i := int(q * float64(len(b.Values)-1))
	if i < 0 {
		i = 0
	}
	if i >= len(b.Values) {
		i = len(b.Values) - 1
	}
	return b.Values[i]
}

// Resample draws n indices with replacement, resamples times, and returns the
// sorted values of stat; ok=false drops that resample. Seeded so one input gives
// one interval twice. idx is reused between calls, so stat must not keep it.
func Resample(n, resamples int, seed uint64, stat func(idx []int) (v float64, ok bool)) (Bootstrap, error) {
	if n < 0 || resamples < 1 {
		return Bootstrap{}, fmt.Errorf("stats: cannot resample %d items %d times", n, resamples)
	}
	rng := rand.New(rand.NewPCG(seed, 0x9E3779B97F4A7C15))
	idx := make([]int, n)
	b := Bootstrap{Values: make([]float64, 0, resamples)}
	for r := 0; r < resamples; r++ {
		for i := range idx {
			idx[i] = rng.IntN(n)
		}
		if v, ok := stat(idx); ok {
			b.Values = append(b.Values, v)
		} else {
			b.Dropped++
		}
	}
	sort.Float64s(b.Values)
	return b, nil
}

// weighted accumulates Σwv and Σw in one place, so the resampled and the whole-sample
// means add in the same order and ope's estimates stay bit-identical.
type weighted struct{ num, den float64 }

func (s *weighted) add(v, w float64) {
	s.num += v * w
	s.den += w
}

func sameLength(a, b int) error {
	if a != b {
		return fmt.Errorf("stats: paired slices differ in length, %d against %d", a, b)
	}
	return nil
}

// WeightedMean is the self-normalised estimate Σwv/Σw over idx, undefined when Σw = 0.
func WeightedMean(values, weights []float64) (func(idx []int) (float64, bool), error) {
	if err := sameLength(len(values), len(weights)); err != nil {
		return nil, err
	}
	return func(idx []int) (float64, bool) {
		var s weighted
		for _, j := range idx {
			s.add(values[j], weights[j])
		}
		if s.den > 0 {
			return s.num / s.den, true
		}
		return 0, false
	}, nil
}

// WeightedMeanAll is WeightedMean over every item, without building an index.
func WeightedMeanAll(values, weights []float64) (float64, error) {
	if err := sameLength(len(values), len(weights)); err != nil {
		return 0, err
	}
	var s weighted
	for j := range values {
		s.add(values[j], weights[j])
	}
	if !(s.den > 0) {
		return 0, errors.New("stats: the weights sum to zero, so the weighted mean is undefined")
	}
	return s.num / s.den, nil
}

// PairedMeanDiff is the mean of the paired differences d over idx (H1's Δpass).
func PairedMeanDiff(d []float64) func(idx []int) (float64, bool) {
	return func(idx []int) (float64, bool) {
		if len(idx) == 0 {
			return 0, false
		}
		var sum float64
		for _, j := range idx {
			sum += d[j]
		}
		return sum / float64(len(idx)), true
	}
}

// CostPerAcceptedDiff is Σcost_A/Σacc_A − Σcost_R/Σacc_R over the same items (H3).
// A resample in which either arm accepted nothing has no cost per accepted answer.
func CostPerAcceptedDiff(costA, costR []float64, accA, accR []bool) (func(idx []int) (float64, bool), error) {
	for _, n := range []int{len(costR), len(accA), len(accR)} {
		if err := sameLength(len(costA), n); err != nil {
			return nil, err
		}
	}
	return func(idx []int) (float64, bool) {
		var cA, cR float64
		var nA, nR int
		for _, j := range idx {
			cA += costA[j]
			cR += costR[j]
			if accA[j] {
				nA++
			}
			if accR[j] {
				nR++
			}
		}
		if nA == 0 || nR == 0 {
			return 0, false
		}
		return cA/float64(nA) - cR/float64(nR), true
	}, nil
}
