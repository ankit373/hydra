// SPDX-License-Identifier: MIT

package stats

import (
	"fmt"
	"math"
)

// betaShapes refuses what no Beta distribution has: a shape that is not a finite number above 0.
func betaShapes(a, b float64) error {
	if !(a > 0 && b > 0) || math.IsInf(a, 1) || math.IsInf(b, 1) {
		return fmt.Errorf("stats: no Beta distribution has a=%v and b=%v", a, b)
	}
	return nil
}

// logBeta is ln B(a, b), kept so a bisection over x pays for it once.
func logBeta(a, b float64) float64 {
	la, _ := math.Lgamma(a)
	lb, _ := math.Lgamma(b)
	lab, _ := math.Lgamma(a + b)
	return la + lb - lab
}

// BetaCDF is the regularized incomplete beta I_x(a, b).
func BetaCDF(x, a, b float64) (float64, error) {
	if err := betaShapes(a, b); err != nil {
		return 0, err
	}
	return betaCDF(x, a, b, logBeta(a, b))
}

// betaCDF takes the fraction from the other tail above the mean, as 1 − I_{1−x}(b, a),
// where it would otherwise converge slowly.
func betaCDF(x, a, b, lbeta float64) (float64, error) {
	switch {
	case math.IsNaN(x):
		return 0, fmt.Errorf("stats: Beta CDF at x=NaN")
	case x <= 0:
		return 0, nil
	case x >= 1:
		return 1, nil
	}
	if x > (a+1)/(a+b+2) {
		v, err := betaTail(1-x, b, a, lbeta)
		return 1 - v, err
	}
	return betaTail(x, a, b, lbeta)
}

// betaTail is I_x(a, b) by the modified Lentz method on the incomplete beta's continued
// fraction (Numerical Recipes §6.4). It takes O(√max(a, b)) terms below the mean.
func betaTail(x, a, b, lbeta float64) (float64, error) {
	const (
		maxTerms = 10000
		eps      = 1e-15
		tiny     = 1e-300
	)
	front := math.Exp(a*math.Log(x)+b*math.Log1p(-x)-lbeta) / a

	clampTiny := func(v float64) float64 {
		if math.Abs(v) < tiny {
			return tiny
		}
		return v
	}
	c, d := 1.0, 1/clampTiny(1-(a+b)*x/(a+1))
	f := d
	for m := 1; m <= maxTerms; m++ {
		fm := float64(m)
		even := fm * (b - fm) * x / ((a + 2*fm - 1) * (a + 2*fm))
		odd := -(a + fm) * (a + b + fm) * x / ((a + 2*fm) * (a + 2*fm + 1))
		for _, coef := range [2]float64{even, odd} {
			d = 1 / clampTiny(1+coef*d)
			c = clampTiny(1 + coef/c)
			f *= c * d
		}
		if math.Abs(c*d-1) < eps {
			return math.Min(front*f, 1), nil
		}
	}
	return 0, fmt.Errorf("stats: Beta CDF at x=%v did not converge for a=%v b=%v", x, a, b)
}

// BetaQuantile is the x at which I_x(a, b) = p, bisected on (0, 1) to adjacent floats
// the way ClopperPearsonUpper is.
func BetaQuantile(p, a, b float64) (float64, error) {
	if err := betaShapes(a, b); err != nil {
		return 0, err
	}
	if !(p > 0 && p < 1) {
		return 0, fmt.Errorf("stats: p %v is outside (0, 1)", p)
	}
	lbeta := logBeta(a, b)
	lo, hi := 0.0, 1.0
	for {
		mid := lo + (hi-lo)/2
		if mid <= lo || mid >= hi {
			return hi, nil
		}
		v, err := betaCDF(mid, a, b, lbeta)
		if err != nil {
			return 0, err
		}
		if v < p {
			lo = mid
		} else {
			hi = mid
		}
	}
}
