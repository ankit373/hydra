// SPDX-License-Identifier: MIT

package stats

import (
	"fmt"
	"math"
)

// logChoose is ln C(n, i) for i = 0..k, kept so a bisection over p pays for it once.
func logChoose(k, n int) []float64 {
	lgn, _ := math.Lgamma(float64(n + 1))
	lc := make([]float64, k+1)
	for i := range lc {
		lgi, _ := math.Lgamma(float64(i + 1))
		lgr, _ := math.Lgamma(float64(n - i + 1))
		lc[i] = lgn - lgi - lgr
	}
	return lc
}

// cdf sums P(X ≤ len(lc)−1) in log space, because 2^n and C(n, i) overflow a
// float64 long before n reaches a suite's size. Rounding can pass 1, so it is clamped.
func cdf(lc []float64, n int, p float64) float64 {
	lp, lq := math.Log(p), math.Log1p(-p)
	var sum float64
	for i, c := range lc {
		sum += math.Exp(c + float64(i)*lp + float64(n-i)*lq)
	}
	return math.Min(sum, 1)
}

// BinomialCDF is P(X ≤ k) for X ~ Binomial(n, p). The edges are answered exactly
// rather than through logs, where p = 0 or 1 would make 0·log 0 a NaN.
func BinomialCDF(k, n int, p float64) float64 {
	switch {
	case k < 0:
		return 0
	case k >= n:
		return 1
	case p <= 0:
		return 1
	case p >= 1:
		return 0
	}
	return cdf(logChoose(k, n), n, p)
}

// ClopperPearsonUpper is the exact one-sided upper bound on a binomial rate: the p
// at which P(X ≤ k) = alpha, bisected on (0, 1) to adjacent floats. k == n gives 1.
func ClopperPearsonUpper(k, n int, alpha float64) (float64, error) {
	if n <= 0 || k < 0 || k > n {
		return 0, fmt.Errorf("stats: Clopper–Pearson needs 0 ≤ k ≤ n and n > 0, got k=%d n=%d", k, n)
	}
	if !(alpha > 0 && alpha < 1) {
		return 0, fmt.Errorf("stats: alpha %v is outside (0, 1)", alpha)
	}
	if k == n {
		return 1, nil
	}
	lc := logChoose(k, n)
	lo, hi := 0.0, 1.0
	for {
		mid := lo + (hi-lo)/2
		if mid <= lo || mid >= hi {
			return hi, nil
		}
		if cdf(lc, n, mid) > alpha {
			lo = mid
		} else {
			hi = mid
		}
	}
}

// Wilson is the score interval for k successes in n trials. It stays inside [0, 1]
// where the normal approximation does not, and n = 0, no evidence, is the whole range.
func Wilson(k, n int, z float64) (lo, hi float64, err error) {
	if n < 0 || k < 0 || k > n {
		return 0, 0, fmt.Errorf("stats: Wilson needs 0 ≤ k ≤ n, got k=%d n=%d", k, n)
	}
	if n == 0 {
		return 0, 1, nil
	}
	fn := float64(n)
	p := float64(k) / fn
	d := 1 + z*z/fn
	centre := (p + z*z/(2*fn)) / d
	spread := z * math.Sqrt(p*(1-p)/fn+z*z/(4*fn*fn)) / d
	return math.Max(0, centre-spread), math.Min(1, centre+spread), nil
}

// Trials is one task's repeated attempts, for pass^k (checkpoint B, τ²-bench).
type Trials struct{ N, C int }

// PassHatK is τ-bench's pass^k, the mean over tasks of C(c,k)/C(n,k). The ratio is
// taken as a product of (c−i)/(n−i), so it never overflows.
func PassHatK(tasks []Trials, k int) (float64, error) {
	if len(tasks) == 0 || k < 1 {
		return 0, fmt.Errorf("stats: pass^k needs at least one task and k ≥ 1, got %d tasks and k=%d", len(tasks), k)
	}
	var sum float64
	for _, t := range tasks {
		if t.C < 0 || t.C > t.N {
			return 0, fmt.Errorf("stats: a task has %d successes in %d trials", t.C, t.N)
		}
		if t.N < k {
			return 0, fmt.Errorf("stats: pass^%d needs at least %d trials per task, a task has %d", k, k, t.N)
		}
		r := 1.0
		for i := 0; i < k; i++ {
			r *= float64(t.C-i) / float64(t.N-i)
		}
		sum += r
	}
	return sum / float64(len(tasks)), nil
}
