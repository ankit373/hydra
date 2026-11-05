// SPDX-License-Identifier: MIT

package stats

import (
	"errors"
	"fmt"
	"math"
)

// Discordant counts a paired binary comparison of arm A against reference R.
type Discordant struct {
	N int // items both arms answered
	B int // A passed, R failed
	C int // A failed, R passed
}

// validate refuses counts no paired comparison can produce, which would otherwise
// be answered as if they were data.
func (d Discordant) validate() error {
	if d.N < 0 || d.B < 0 || d.C < 0 || d.B+d.C > d.N {
		return fmt.Errorf("stats: no paired comparison has N=%d with B=%d and C=%d", d.N, d.B, d.C)
	}
	return nil
}

// tangoReady is what both Tango functions need: valid counts and at least one item.
func (d Discordant) tangoReady() error {
	if err := d.validate(); err != nil {
		return err
	}
	if d.N == 0 {
		return errors.New("stats: Tango needs at least one paired item")
	}
	return nil
}

// Delta is the paired difference in pass rates. N = 0 has none, so callers check N.
func (d Discordant) Delta() float64 { return float64(d.B-d.C) / float64(d.N) }

// PairedDiscordant aligns two pass vectors item by item and returns the counts
// with each item's difference d_i ∈ {−1, 0, 1}.
func PairedDiscordant(a, r []bool) (Discordant, []float64, error) {
	if err := sameLength(len(a), len(r)); err != nil {
		return Discordant{}, nil, err
	}
	d := Discordant{N: len(a)}
	diff := make([]float64, len(a))
	for i := range a {
		switch {
		case a[i] && !r[i]:
			d.B++
			diff[i] = 1
		case !a[i] && r[i]:
			d.C++
			diff[i] = -1
		}
	}
	return d, diff, nil
}

// TangoNI is Tango's (1998) score test for paired proportions, H0: Δ ≤ −margin.
// It returns the score z and the one-sided p = 1 − Φ(z); all-concordant data at margin 0 give 0, ½.
func TangoNI(d Discordant, margin float64) (z, p float64, err error) {
	if err := d.tangoReady(); err != nil {
		return 0, 0, err
	}
	if !(margin >= 0 && margin < 1) {
		return 0, 0, fmt.Errorf("stats: Tango margin %v is outside [0, 1)", margin)
	}
	if z, err = tangoZ(d, -margin); err != nil {
		return 0, 0, err
	}
	return z, NormalCDF(-z), nil
}

// tangoZ is the score statistic at θ₀ = Δ under H0, taken over the whole of
// (−1, 1) so TangoLowerBound can cross zero when the arm is ahead.
func tangoZ(d Discordant, theta float64) (float64, error) {
	n, b, c := float64(d.N), float64(d.B), float64(d.C)
	num := b - c - n*theta
	if num == 0 {
		// The data sit exactly on H0. At B = C = 0, θ₀ = 0 the variance is 0 too, and
		// that 0/0 is no evidence either way, which is z = 0 rather than an error.
		return 0, nil
	}
	qa := 2 * n
	qb := -b - c + (2*n-b+c)*theta
	qc := -c * theta * (1 - theta)
	// The discriminant is never negative in exact arithmetic; rounding can push it below 0.
	p21 := (-qb + math.Sqrt(math.Max(0, qb*qb-4*qa*qc))) / (2 * qa)
	v := n * (2*p21 + theta - theta*theta)
	if !(v > 0) {
		return 0, fmt.Errorf("stats: Tango's restricted MLE is degenerate at B=%d C=%d θ₀=%v", d.B, d.C, theta)
	}
	return num / math.Sqrt(v), nil
}

// TangoLowerBound inverts TangoNI: the smallest −margin the test does not reject
// at alpha, by bisection to 1e-12 on (−1, Delta()]. z is 0 at Delta(), so that end never rejects.
func TangoLowerBound(d Discordant, alpha float64) (float64, error) {
	if err := d.tangoReady(); err != nil {
		return 0, err
	}
	if !(alpha > 0 && alpha < 0.5) {
		return 0, fmt.Errorf("stats: alpha %v is outside (0, 0.5)", alpha)
	}
	crit := NormalQuantile(1 - alpha)
	lo, hi := -1.0, d.Delta()
	for hi-lo > 1e-12 {
		mid := lo + (hi-lo)/2
		z, err := tangoZ(d, mid)
		if err != nil {
			return 0, err
		}
		if z > crit {
			lo = mid
		} else {
			hi = mid
		}
	}
	return hi, nil
}

// McNemarExact is the conditional binomial test on B of B+C at p = ½. oneSided is
// P(X ≥ B), "A beats R"; twoSided is min(1, 2·min tail). No discordant pair gives 1, 1.
func McNemarExact(d Discordant) (oneSided, twoSided float64, err error) {
	if err := d.validate(); err != nil {
		return 0, 0, err
	}
	m := d.B + d.C
	if m == 0 {
		return 1, 1, nil
	}
	upper := BinomialCDF(d.C, m, 0.5) // P(X ≥ B) = P(X ≤ C) by symmetry at ½, clamped to 1
	lower := BinomialCDF(d.B, m, 0.5)
	return upper, math.Min(1, 2*math.Min(upper, lower)), nil
}

// NormalCDF is Φ(x), through erfc so a far tail keeps its precision.
func NormalCDF(x float64) float64 { return 0.5 * math.Erfc(-x/math.Sqrt2) }

// NormalQuantile is Φ⁻¹(p). Go's Erfinv is good to ~1e-15 at the α a harness uses
// and drifts to ~1e-9 relative by p = 1e-10.
func NormalQuantile(p float64) float64 { return math.Sqrt2 * math.Erfinv(2*p-1) }
