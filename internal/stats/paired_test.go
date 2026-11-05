// SPDX-License-Identifier: MIT

package stats

import (
	"math"
	"testing"
)

// Golden values come from testdata/gold.py (scipy 1.18.1, statsmodels 0.15.0), or
// are printed in Tango (1998) Stat Med 17:891, where the test says so.
const (
	exact     = 1e-12
	iterative = 1e-9
)

func near(t *testing.T, what string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %.17g, want %.17g (±%g)", what, got, want, tol)
	}
}

func TestPairedDiscordant_CountsAndDifferences(t *testing.T) {
	a := []bool{true, true, false, false, true}
	r := []bool{false, true, true, false, false}
	d, diff, err := PairedDiscordant(a, r)
	if err != nil {
		t.Fatal(err)
	}
	if d != (Discordant{N: 5, B: 2, C: 1}) {
		t.Errorf("counts = %+v", d)
	}
	want := []float64{1, 0, -1, 0, 1}
	for i := range want {
		if diff[i] != want[i] {
			t.Errorf("d[%d] = %v, want %v", i, diff[i], want[i])
		}
	}
	near(t, "Delta", d.Delta(), 0.2, exact)
	if _, _, err := PairedDiscordant(a, r[:4]); err == nil {
		t.Error("vectors of different lengths were paired")
	}
}

// Both two-sided values agree with scipy's binomtest and statsmodels' mcnemar(exact=True).
func TestMcNemarExact_Golden(t *testing.T) {
	cases := []struct {
		b, c     int
		one, two float64
	}{
		{10, 2, 0.019287109375, 0.03857421875}, // 79/4096 one-sided
		{6, 8, 0.78802490234375, 0.79052734375},
		{3, 9, 0.980712890625, 0.14599609375},
		{40, 45, 0.7422871340580498, 0.664645490413214},
		{600, 500, 0.0014097724957182138, 0.0028195449914364293}, // 2^1100 overflows a float64
		{0, 0, 1, 1}, // all concordant: no evidence either way
		{0, 60, 1, 2 * math.Pow(2, -60)},
		{60, 0, math.Pow(2, -60), 2 * math.Pow(2, -60)},
	}
	for _, c := range cases {
		one, two := McNemarExact(Discordant{N: 2000, B: c.b, C: c.c})
		near(t, "one-sided", one, c.one, exact)
		near(t, "two-sided", two, c.two, exact)
		if one > 1 || two > 1 {
			t.Errorf("B=%d C=%d: p-values %v, %v exceed 1", c.b, c.c, one, two)
		}
	}
}

func TestTangoNI_Golden(t *testing.T) {
	cases := []struct {
		d      Discordant
		margin float64
		z, p   float64
	}{
		{Discordant{200, 6, 8}, 0.02, 0.5264247260767538, 0.29929656985372144},
		{Discordant{1500, 40, 45}, 0.02, 2.6057938891628436, 0.004583080034926135},
		{Discordant{100, 10, 2}, 0.02, 2.7454601989185186, 0.0030213048301711565},
		// All concordant: q21 = margin and v = N(margin − margin²), so z = 2/1.4.
		{Discordant{100, 0, 0}, 0.02, 2 / 1.4, 0.07656372550983474},
		// The discriminant is exactly 0 here and rounds below it, which gave NaN before the clamp.
		{Discordant{21, 0, 2}, 0.05, -0.9511897312113418, 0.8292459648586155},
		{Discordant{42, 0, 4}, 0.05, -1.3451854182690983, 0.9107173180177863},
		{Discordant{55, 0, 10}, 0.1, -2.0225995873897262, 0.9784427766084624},
		{Discordant{63, 0, 6}, 0.05, -1.6475089420958278, 0.9502732601258456},
		{Discordant{77, 0, 14}, 0.1, -2.393172105652397, 0.9916482980764193},
		// All concordant at margin 0: the data sit on H0, so no evidence either way.
		{Discordant{100, 0, 0}, 0, 0, 0.5},
	}
	for _, c := range cases {
		z, p, err := TangoNI(c.d, c.margin)
		if err != nil {
			t.Fatal(err)
		}
		near(t, "z", z, c.z, exact)
		near(t, "p", p, c.p, exact)
	}
}

// At θ₀ = 0 Tango's statistic is McNemar's (B − C)/√(B + C), whatever N is.
func TestTangoNI_AtZeroMarginIsMcNemarZ(t *testing.T) {
	for _, d := range []Discordant{{100, 10, 2}, {32, 9, 3}, {1500, 40, 45}} {
		z, _, err := TangoNI(d, 0)
		if err != nil {
			t.Fatal(err)
		}
		near(t, "z", z, float64(d.B-d.C)/math.Sqrt(float64(d.B+d.C)), exact)
	}
	z, _, _ := TangoNI(Discordant{100, 10, 2}, 0)
	near(t, "z", z, 2.3094010767585034, exact)
}

// Transcribed from Tango (1998): §6.1 (soft contact lenses, Table V), Table I, and
// §6.2 (Karacan et al., the marijuana example). Printed to 2 or 3 decimals.
func TestTango_PublishedWorkedExamples(t *testing.T) {
	lenses := Discordant{N: 44, B: 0, C: 1}
	z, p, err := TangoNI(lenses, 0.1)
	if err != nil {
		t.Fatal(err)
	}
	near(t, "§6.1 Z", z, 1.709, 0.0005)
	near(t, "§6.1 p", p, 0.044, 0.0005)
	lb, err := TangoLowerBound(lenses, 0.05)
	if err != nil {
		t.Fatal(err)
	}
	near(t, "§6.1 90% lower limit", lb, -0.096, 0.0005)

	// Table I: b = 0, Δ = 0.1, Z and the 90% interval, for n = 30, 50, 80 and c = 0, 1, 2.
	table := []struct {
		n, c         int
		z, low, high float64
	}{
		{30, 0, 1.83, -0.083, 0.083}, {30, 1, 1.22, -0.136, 0.052}, {30, 2, 0.61, -0.183, 0.022},
		{50, 0, 2.36, -0.051, 0.051}, {50, 1, 1.89, -0.085, 0.032}, {50, 2, 1.41, -0.114, 0.013},
		{80, 0, 2.98, -0.033, 0.033}, {80, 1, 2.61, -0.054, 0.021}, {80, 2, 2.24, -0.073, 0.009},
	}
	for _, row := range table {
		d := Discordant{N: row.n, B: 0, C: row.c}
		z, _, err := TangoNI(d, 0.1)
		if err != nil {
			t.Fatal(err)
		}
		near(t, "Table I Z", z, row.z, 0.005)
		lo, err := TangoLowerBound(d, 0.05)
		if err != nil {
			t.Fatal(err)
		}
		near(t, "Table I lower", lo, row.low, 0.0005)
		// The upper limit is the lower limit of the reversed comparison, negated.
		swapped, err := TangoLowerBound(Discordant{N: row.n, B: row.c, C: 0}, 0.05)
		if err != nil {
			t.Fatal(err)
		}
		near(t, "Table I upper", -swapped, row.high, 0.0005)
	}

	karacan := Discordant{N: 32, B: 9, C: 3}
	z, _, _ = TangoNI(karacan, 0)
	near(t, "§6.2 Z", z, 1.73, 0.005)
	lo, _ := TangoLowerBound(karacan, 0.025)
	hi, _ := TangoLowerBound(Discordant{N: 32, B: 3, C: 9}, 0.025)
	near(t, "§6.2 95% lower", lo, -0.027, 0.0005)
	near(t, "§6.2 95% upper", -hi, 0.390, 0.0005)
}

func TestTangoLowerBound_InvertsTheTest(t *testing.T) {
	crit := NormalQuantile(0.95)
	for _, d := range []Discordant{{200, 6, 8}, {1500, 40, 45}, {100, 10, 2}, {100, 0, 0}, {44, 0, 1}, {21, 0, 2}, {77, 0, 14}} {
		var prev float64 = -2
		for _, alpha := range []float64{0.01, 0.05, 0.1} {
			lb, err := TangoLowerBound(d, alpha)
			if err != nil {
				t.Fatal(err)
			}
			if lb <= prev {
				t.Errorf("%+v: bound %v at α=%v is not above %v at the smaller α", d, lb, alpha, prev)
			}
			prev = lb
			z, err := tangoZ(d, lb)
			if err != nil {
				t.Fatal(err)
			}
			near(t, "p at the bound", NormalCDF(-z), alpha, iterative)
		}
		// Where the bound is a non-inferiority margin, TangoNI itself sits on α there.
		if lb, _ := TangoLowerBound(d, 0.05); lb <= 0 {
			z, p, err := TangoNI(d, -lb)
			if err != nil {
				t.Fatal(err)
			}
			near(t, "TangoNI p at −LB", p, 0.05, iterative)
			near(t, "TangoNI z at −LB", z, crit, 1e-6)
		}
	}
	// Every item lost: Δ = −1, and no margin inside (−1, 0] is ever rejected.
	lb, err := TangoLowerBound(Discordant{N: 10, B: 0, C: 10}, 0.05)
	if err != nil || lb != -1 {
		t.Errorf("all-lost bound = %v, %v; want −1", lb, err)
	}
}

func TestTango_RefusesWhatItCannotAnswer(t *testing.T) {
	bad := []struct {
		d      Discordant
		margin float64
	}{
		{Discordant{0, 0, 0}, 0.02},   // n = 0
		{Discordant{100, 1, 1}, -0.1}, // margin outside [0, 1)
		{Discordant{100, 1, 1}, 1},
		{Discordant{100, 1, 1}, math.NaN()},
	}
	for _, c := range bad {
		if _, _, err := TangoNI(c.d, c.margin); err == nil {
			t.Errorf("TangoNI(%+v, %v) answered", c.d, c.margin)
		}
	}
	for _, alpha := range []float64{0, 0.5, -1, math.NaN()} {
		if _, err := TangoLowerBound(Discordant{100, 1, 1}, alpha); err == nil {
			t.Errorf("TangoLowerBound accepted α=%v", alpha)
		}
	}
	if _, err := TangoLowerBound(Discordant{}, 0.05); err == nil {
		t.Error("TangoLowerBound answered with n = 0")
	}
}

func TestNormal_Golden(t *testing.T) {
	near(t, "Φ(1.959964)", NormalCDF(1.959964), 0.9750000009035577, exact)
	// Quantiles against mpmath at 40 digits (gold.py prints scipy's, which agree).
	near(t, "Φ⁻¹(0.95)", NormalQuantile(0.95), 1.6448536269514727, exact)
	near(t, "Φ⁻¹(0.975)", NormalQuantile(0.975), 1.9599639845400542, exact)
	near(t, "Φ⁻¹(0.01)", NormalQuantile(0.01), -2.3263478740408411, exact)
	near(t, "Φ⁻¹(1e-10)", NormalQuantile(1e-10), -6.3613409024040562, 2e-8)
	for _, x := range []float64{-5, -1, 0, 0.3, 2.5} {
		near(t, "Φ⁻¹(Φ(x))", NormalQuantile(NormalCDF(x)), x, 1e-9)
	}
}
