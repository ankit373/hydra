// SPDX-License-Identifier: MIT

package optimal

import (
	"math"
	"testing"
)

// Manifesto Law 4 numbers (s = 0.2):
//
//	independent  k=0.02 → n*≈6.3→6
//	moderate     k=0.08 → n*≈3.2→3
//	heavy        k=0.30 → n*≈1.6→2
func TestAgents_MatchesLaw4(t *testing.T) {
	cases := []struct {
		name  string
		k     float64
		wantN int
	}{
		{"independent", 0.02, 6},
		{"moderate", 0.08, 3},
		{"heavy overlap", 0.30, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n, _ := Agents(DefaultSerialFraction, c.k)
			if n != c.wantN {
				t.Errorf("Agents(0.2, %v) n* = %d, want %d", c.k, n, c.wantN)
			}
		})
	}

	// Independent work has a real (>1) speedup; heavy coupling drives the
	// absolute S(n*) below 1 — the model correctly says "don't parallelize this."
	if _, s := Agents(0.2, 0.02); s <= 1 {
		t.Errorf("independent speedup = %v, want > 1", s)
	}
	if _, s := Agents(0.2, 0.30); s >= 1 {
		t.Errorf("heavy-overlap speedup = %v, want < 1 (parallelism doesn't pay)", s)
	}
}

// n* must actually maximize S(n): S(n*) ≥ S(n*±1).
func TestAgents_IsOptimum(t *testing.T) {
	s, k := 0.2, 0.05
	n, peak := Agents(s, k)
	if lower := Speedup(s, k, float64(n-1)); n > 1 && lower > peak {
		t.Errorf("S(n*-1)=%v > S(n*)=%v — not optimal", lower, peak)
	}
	if higher := Speedup(s, k, float64(n+1)); higher > peak {
		t.Errorf("S(n*+1)=%v > S(n*)=%v — not optimal", higher, peak)
	}
}

// More coordination cost ⇒ fewer optimal agents.
func TestAgents_MonotoneInK(t *testing.T) {
	nLow, _ := Agents(0.2, 0.02)
	nHigh, _ := Agents(0.2, 0.30)
	if nHigh >= nLow {
		t.Errorf("heavier coupling should reduce n*: k=0.02→%d, k=0.30→%d", nLow, nHigh)
	}
}

func TestSpeedup_PeakFormula(t *testing.T) {
	s, k := 0.2, 0.08
	_, got := Agents(s, k)
	// closed form S(n*) = 1 / (s + 2*sqrt(k*(1-s)))
	want := 1 / (s + 2*math.Sqrt(k*(1-s)))
	// Agents rounds n*, so allow a small gap from the continuous peak.
	if math.Abs(got-want) > 0.05 {
		t.Errorf("S(n*) = %v, continuous peak = %v", got, want)
	}
}

func TestAgents_ZeroK(t *testing.T) {
	n, speedup := Agents(0.2, 0)
	if n != 0 {
		t.Errorf("k=0 has no finite optimum; want sentinel 0, got %d", n)
	}
	if speedup != 1/0.2 {
		t.Errorf("k=0 speedup should be Amdahl ceiling 1/s = 5, got %v", speedup)
	}
}
