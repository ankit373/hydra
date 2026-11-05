// SPDX-License-Identifier: MIT

package stats

import (
	"math"
	"testing"
)

// References are scipy's beta.ppf(1 − α, k + 1, n − k); k = 0 also has the closed form 1 − α^{1/n}.
func TestClopperPearsonUpper_Golden(t *testing.T) {
	cases := []struct {
		k, n int
		want float64
	}{
		{0, 100, 0.029513049607039932},
		{3, 50, 0.1478371763641812},
		{35, 500, 0.09174006190473744},
		{64, 910, 0.08588608387949787}, // H2's sized n
		{1, 1000, 0.004734993575499777},
		{7, 7, 1},
	}
	for _, c := range cases {
		got, err := ClopperPearsonUpper(c.k, c.n, 0.05)
		if err != nil {
			t.Fatal(err)
		}
		near(t, "upper", got, c.want, exact)
	}
	near(t, "k=0 closed form", 1-math.Pow(0.05, 1.0/100), 0.029513049607039932, exact)

	// α ≥ ½ puts the bound below k/n, so the search must start at 0, not at k/n.
	high := []struct {
		alpha float64
		k, n  int
		want  float64
	}{
		{0.7, 3, 50, 0.055412788159043706},
		{0.7, 35, 500, 0.065416110641767},
		{0.7, 0, 100, 0.003560396144387363},
		{0.9, 3, 50, 0.03534768016845918},
		{0.9, 35, 500, 0.05748997789637211},
		{0.9, 0, 100, 0.001053050309545618},
	}
	for _, c := range high {
		got, err := ClopperPearsonUpper(c.k, c.n, c.alpha)
		if err != nil {
			t.Fatal(err)
		}
		near(t, "upper at high α", got, c.want, exact)
	}
}

// Moved from internal/trust, which now calls this one implementation.
func TestBinomialCDF_KnownValues(t *testing.T) {
	tests := []struct {
		k, n int
		p    float64
		want float64
	}{
		{k: 0, n: 0, p: 0.3, want: 1},
		{k: 0, n: 10, p: 0.1, want: math.Pow(0.9, 10)},
		{k: 1, n: 2, p: 0.5, want: 0.75},
		{k: 2, n: 4, p: 0.5, want: 11.0 / 16},
		{k: 5, n: 5, p: 0.7, want: 1},
	}
	for _, tc := range tests {
		if got := BinomialCDF(tc.k, tc.n, tc.p); math.Abs(got-tc.want) > 1e-12 {
			t.Errorf("BinomialCDF(%d, %d, %v) = %v, want %v", tc.k, tc.n, tc.p, got, tc.want)
		}
	}
}

// The edges are exact, where the log-space sum would meet log 0.
func TestBinomialCDF_Edges(t *testing.T) {
	tests := []struct {
		k, n int
		p    float64
		want float64
	}{
		{-1, 10, 0.5, 0}, {-1, 0, 0.5, 0},
		{10, 10, 0.5, 1}, {12, 10, 0.5, 1}, {10, 10, 1, 1},
		{3, 10, 0, 1}, {0, 10, 0, 1},
		{3, 10, 1, 0}, {9, 10, 1, 0},
	}
	for _, tc := range tests {
		if got := BinomialCDF(tc.k, tc.n, tc.p); got != tc.want {
			t.Errorf("BinomialCDF(%d, %d, %v) = %v, want %v", tc.k, tc.n, tc.p, got, tc.want)
		}
	}
}

// Rounding can sum the whole distribution past 1; a probability never is.
func TestBinomialCDF_NeverExceedsOne(t *testing.T) {
	for _, n := range []int{60, 500, 1100} {
		for _, p := range []float64{0.1, 0.5, 0.9} {
			if got := BinomialCDF(n, n, p); got > 1 || got < 1-1e-12 {
				t.Errorf("P(X ≤ %d) at n=%d p=%v = %.17g", n, n, p, got)
			}
		}
	}
}

func TestClopperPearsonUpper_RefusesWhatItCannotAnswer(t *testing.T) {
	bad := []struct {
		k, n  int
		alpha float64
	}{
		{0, 0, 0.05}, {-1, 10, 0.05}, {11, 10, 0.05}, {1, 10, 0}, {1, 10, 1}, {1, 10, math.NaN()},
	}
	for _, c := range bad {
		if _, err := ClopperPearsonUpper(c.k, c.n, c.alpha); err == nil {
			t.Errorf("ClopperPearsonUpper(%d, %d, %v) answered", c.k, c.n, c.alpha)
		}
	}
}

func TestWilson_GoldenAndEdges(t *testing.T) {
	lo, hi, err := Wilson(81, 263, 1.959964)
	if err != nil {
		t.Fatal(err)
	}
	near(t, "lo", lo, 0.25528851948901576, exact)
	near(t, "hi", hi, 0.3662095774580194, exact)
	if lo, hi, _ := Wilson(0, 0, 1.959964); lo != 0 || hi != 1 {
		t.Errorf("n = 0 gave [%v, %v], want the whole range", lo, hi)
	}
	if lo, _, _ := Wilson(0, 50, 1.959964); lo != 0 {
		t.Errorf("0 of 50 has lower bound %v", lo)
	}
	_, hi, _ = Wilson(50, 50, 1.959964)
	near(t, "50 of 50 upper", hi, 1, exact)
	for _, c := range [][2]int{{-1, 10}, {11, 10}, {0, -1}} {
		if _, _, err := Wilson(c[0], c[1], 1.959964); err == nil {
			t.Errorf("Wilson(%d, %d) answered", c[0], c[1])
		}
	}
}

func TestPassHatK_Golden(t *testing.T) {
	tasks := []Trials{{4, 4}, {4, 3}, {4, 2}, {4, 0}}
	got, err := PassHatK(tasks, 2)
	if err != nil {
		t.Fatal(err)
	}
	near(t, "pass^2", got, 5.0/12, exact)
	got, _ = PassHatK(tasks, 1)
	near(t, "pass^1 = pass@1", got, 9.0/16, exact)
	// C(999,500)/C(1000,500) = 500/1000; both binomials overflow a float64.
	got, err = PassHatK([]Trials{{1000, 999}}, 500)
	if err != nil {
		t.Fatal(err)
	}
	near(t, "pass^500", got, 0.5, exact)
}

func TestPassHatK_RefusesWhatItCannotAnswer(t *testing.T) {
	bad := []struct {
		tasks []Trials
		k     int
	}{
		{[]Trials{{4, 4}, {1, 1}}, 2}, // N < k
		{nil, 1},
		{[]Trials{{4, 4}}, 0},
		{[]Trials{{4, 5}}, 1},
		{[]Trials{{4, -1}}, 1},
	}
	for _, c := range bad {
		if _, err := PassHatK(c.tasks, c.k); err == nil {
			t.Errorf("PassHatK(%v, %d) answered", c.tasks, c.k)
		}
	}
}
