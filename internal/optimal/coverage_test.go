// SPDX-License-Identifier: MIT

package optimal

import (
	"math"
	"testing"
)

// Agents answers "how many agents should fan out", which `hyctl graph parallel`
// prints as advice. A nonsense answer there wastes money on agents that only
// coordinate with each other.

// k ≤ 0 means coordination is free, so there is no finite optimum — the model
// returns the Amdahl ceiling rather than a made-up agent count.
func TestAgents_FreeCoordinationHasNoFiniteOptimum(t *testing.T) {
	for _, k := range []float64{0, -0.1, -1} {
		n, speedup := Agents(0.1, k)
		if n != 0 {
			t.Errorf("Agents(s=0.1, k=%v) = %d agents; with no coordination cost there "+
				"is no finite optimum and the count must not be invented", k, n)
		}
		if want := 1 / 0.1; math.Abs(speedup-want) > 1e-9 {
			t.Errorf("speedup = %v, want the Amdahl ceiling %v", speedup, want)
		}
	}
	// s = 0 and k = 0 together: unbounded speedup, which must be +Inf rather
	// than a NaN that would render as "NaN×".
	_, sp := Agents(0, 0)
	if !math.IsInf(sp, 1) {
		t.Errorf("Agents(0,0) speedup = %v, want +Inf", sp)
	}
}

// The serial fraction is a fraction. Values outside [0,1] come from a bad
// graph or a bad flag and must be clamped, not propagated into a sqrt.
func TestAgents_ClampsTheSerialFraction(t *testing.T) {
	for _, s := range []float64{-1, -0.001, 1.001, 5} {
		n, speedup := Agents(s, 0.1)
		if n < 1 {
			t.Errorf("Agents(s=%v) = %d agents; the count must be at least 1", s, n)
		}
		if math.IsNaN(speedup) || math.IsInf(speedup, 0) {
			t.Errorf("Agents(s=%v) speedup = %v", s, speedup)
		}
	}
}

// n* is at least one agent: "fan out to zero agents" is not advice.
func TestAgents_NeverAdvisesFewerThanOne(t *testing.T) {
	// Huge coordination cost drives n* below 1 before clamping.
	for _, k := range []float64{1, 10, 1000} {
		n, _ := Agents(0.5, k)
		if n < 1 {
			t.Errorf("Agents(s=0.5, k=%v) = %d", k, n)
		}
	}
}

// The documented relationship: n* = sqrt((1-s)/k), rounded.
func TestAgents_MatchesTheClosedForm(t *testing.T) {
	cases := []struct{ s, k float64 }{
		{0.1, 0.02}, {0.2, 0.05}, {0.05, 0.3}, {0.5, 0.1},
	}
	for _, c := range cases {
		want := int(math.Round(math.Sqrt((1 - c.s) / c.k)))
		if want < 1 {
			want = 1
		}
		got, _ := Agents(c.s, c.k)
		if got != want {
			t.Errorf("Agents(%v, %v) = %d, want %d from sqrt((1-s)/k)", c.s, c.k, got, want)
		}
	}
}

// Speedup's hard ceiling. The model is Amdahl-with-linear-coordination and
// cannot represent superlinear speedup — the benchmark measured S > n, which
// this form falsifiably does not cover. A test that let S > n through would
// hide exactly that limitation.
func TestSpeedup_CeilingIsNeverExceeded(t *testing.T) {
	for _, s := range []float64{0, 0.01, 0.1, 0.5, 0.9, 1} {
		for _, k := range []float64{0, 0.001, 0.02, 0.3, 1} {
			for _, n := range []float64{1, 2, 4, 8, 16, 64} {
				got := Speedup(s, k, n)
				if math.IsNaN(got) {
					t.Fatalf("Speedup(%v,%v,%v) = NaN", s, k, n)
				}
				if got > n+1e-9 {
					t.Errorf("Speedup(%v,%v,%v) = %v, above the hard ceiling of n — "+
						"this model cannot represent superlinear speedup", s, k, n, got)
				}
			}
		}
	}
}

// Zero or negative agents is not a configuration; it must return the no-op
// speedup rather than divide by zero.
func TestSpeedup_NonPositiveAgentsIsUnity(t *testing.T) {
	for _, n := range []float64{0, -1, -100} {
		if got := Speedup(0.1, 0.02, n); got != 1 {
			t.Errorf("Speedup(n=%v) = %v, want 1", n, got)
		}
	}
}

// More agents helps up to n* and hurts after it — that peak is the entire point
// of computing n*, so it must actually be a peak.
func TestSpeedup_PeaksAtTheAdvisedAgentCount(t *testing.T) {
	const s, k = 0.1, 0.02
	nStar, atStar := Agents(s, k)

	below := Speedup(s, k, float64(nStar)-1)
	above := Speedup(s, k, float64(nStar)+1)

	if atStar < below || atStar < above {
		t.Errorf("speedup at n*=%d is %v, but %v at n-1 and %v at n+1 — n* is not "+
			"the optimum it claims to be", nStar, atStar, below, above)
	}
}

func TestAmdahlCeiling(t *testing.T) {
	if got := amdahlCeiling(0); !math.IsInf(got, 1) {
		t.Errorf("amdahlCeiling(0) = %v, want +Inf", got)
	}
	if got := amdahlCeiling(-1); !math.IsInf(got, 1) {
		t.Errorf("amdahlCeiling(-1) = %v, want +Inf", got)
	}
	if got := amdahlCeiling(0.25); math.Abs(got-4) > 1e-9 {
		t.Errorf("amdahlCeiling(0.25) = %v, want 4", got)
	}
}
