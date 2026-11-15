// SPDX-License-Identifier: MIT

package stats

import (
	"math"
	"testing"
)

func mustBetaCDF(t *testing.T, x, a, b float64) float64 {
	t.Helper()
	v, err := BetaCDF(x, a, b)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func mustBetaQuantile(t *testing.T, p, a, b float64) float64 {
	t.Helper()
	v, err := BetaQuantile(p, a, b)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestBetaCDF_ClosedForms(t *testing.T) {
	for _, x := range []float64{1e-9, 0.01, 0.2, 0.5, 0.73, 0.9, 0.999} {
		near(t, "Beta(1,1)", mustBetaCDF(t, x, 1, 1), x, exact)
		for _, s := range []float64{0.4, 2, 7.5, 30} {
			near(t, "Beta(a,1)", mustBetaCDF(t, x, s, 1), math.Pow(x, s), exact)
			near(t, "Beta(1,b)", mustBetaCDF(t, x, 1, s), -math.Expm1(s*math.Log1p(-x)), exact)
		}
	}
	near(t, "below 0", mustBetaCDF(t, -0.5, 3, 4), 0, exact)
	near(t, "above 1", mustBetaCDF(t, 1.5, 3, 4), 1, exact)
}

// ClopperPearsonUpper(k, n, α) is the 1 − α quantile of Beta(k + 1, n − k).
func TestBetaQuantile_MatchesClopperPearson(t *testing.T) {
	for _, n := range []int{1, 2, 5, 13, 20, 50, 97, 200} {
		for k := 0; k < n; k += 1 + n/20 {
			for _, alpha := range []float64{0.05, 0.5, 0.9} {
				cp, err := ClopperPearsonUpper(k, n, alpha)
				if err != nil {
					t.Fatal(err)
				}
				got := mustBetaQuantile(t, 1-alpha, float64(k+1), float64(n-k))
				near(t, "Beta quantile vs Clopper–Pearson", got, cp, iterative)
			}
		}
	}
}

// References are scipy's beta.ppf(p, a, b), from testdata/gold.py.
func TestBetaQuantile_FractionalShapes(t *testing.T) {
	cases := []struct{ p, a, b, want float64 }{
		{0.05, 17.4, 2.6, 0.7311065887388601},
		{0.5, 17.4, 2.6, 0.8823497593502702},
		{0.95, 17.4, 2.6, 0.9664918060849692},
		{0.05, 0.3, 0.7, 7.660738053881493e-05},
		{0.05, 2.5, 40.5, 0.013790663333097839},
		{0.05, 118, 9, 0.8883569865943286},
	}
	for _, c := range cases {
		q := mustBetaQuantile(t, c.p, c.a, c.b)
		near(t, "quantile", q, c.want, iterative)
		near(t, "CDF(quantile)", mustBetaCDF(t, q, c.a, c.b), c.p, iterative)
	}
}

func TestBetaQuantile_RefusesBadInput(t *testing.T) {
	nan, inf := math.NaN(), math.Inf(1)
	for _, c := range []struct{ p, a, b float64 }{
		{0.5, 0, 1}, {0.5, 1, 0}, {0.5, -2, 3}, {0.5, nan, 1}, {0.5, 1, nan}, {0.5, inf, 1},
		{0, 2, 3}, {1, 2, 3}, {-0.1, 2, 3}, {1.1, 2, 3}, {nan, 2, 3},
	} {
		if _, err := BetaQuantile(c.p, c.a, c.b); err == nil {
			t.Errorf("BetaQuantile(%v, %v, %v) gave no error", c.p, c.a, c.b)
		}
	}
	for _, c := range []struct{ x, a, b float64 }{{nan, 2, 3}, {0.5, 0, 3}, {0.5, 2, inf}} {
		if _, err := BetaCDF(c.x, c.a, c.b); err == nil {
			t.Errorf("BetaCDF(%v, %v, %v) gave no error", c.x, c.a, c.b)
		}
	}
}
