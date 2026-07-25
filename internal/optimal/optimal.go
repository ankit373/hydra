// SPDX-License-Identifier: MIT

// Package optimal computes the provably-best number of parallel agents for a
// task, per Manifesto Law 4. Over-spawning on tightly-coupled code is slower
// than a smaller fleet: coordination cost grows linearly with agent count while
// the parallelizable work shrinks.
package optimal

import "math"

// DefaultSerialFraction is the share of work that cannot be parallelized (human
// review, integration). Manifesto assumption: s = 0.2.
const DefaultSerialFraction = 0.2

// Agents returns the optimal parallel agent count n* and its speedup S(n*) for a
// serial fraction s and per-agent coordination cost k, maximizing the
// Amdahl-with-coordination model:
//
//	S(n) = 1 / ( s + (1-s)/n + k*n )
//	n*   = sqrt( (1-s) / k )
//	S(n*) = 1 / ( s + 2*sqrt( k*(1-s) ) )
//
// n* is rounded to the nearest whole agent and clamped to ≥1.
func Agents(s, k float64) (nStar int, speedup float64) {
	if k <= 0 {
		// No coordination cost → parallelism only limited by the serial
		// fraction; there is no finite optimum, so return a sensible large-ish
		// cap and the Amdahl ceiling.
		return 0, amdahlCeiling(s)
	}
	if s < 0 {
		s = 0
	}
	if s > 1 {
		s = 1
	}
	nReal := math.Sqrt((1 - s) / k)
	nStar = int(math.Round(nReal))
	if nStar < 1 {
		nStar = 1
	}
	speedup = Speedup(s, k, float64(nStar))
	return nStar, speedup
}

// Speedup evaluates S(n) for the given parameters.
//
// Domain of validity: this Amdahl-with-linear-coordination form models
// coordination-BOUND work and has a hard ceiling S(n) ≤ n for every s ≥ 0, k ≥ 0
// (equality only at s = k = 0). It therefore cannot represent *superlinear*
// speedup. The real optimal-parallelism benchmark measured superlinear speedup —
// S = 2.33 at n=2 and 9.3 at n=4 — driven by per-agent context dilution (each of
// n agents holds ~1/n of the context and generates faster), a regime this model
// falsifiably does not cover: no (s, k) can fit S > n, which is why the grid-fit
// pinned to the boundary with high residual. A context/entropy-aware successor is
// data-gated future work. See TRUST_CONTROL_PLANE_BENCHMARK_FINDINGS.md §2 and
// TestSpeedup_CeilingIsN_NeverSuperlinear.
func Speedup(s, k, n float64) float64 {
	if n <= 0 {
		return 1
	}
	return 1 / (s + (1-s)/n + k*n)
}

// amdahlCeiling is the classic 1/s limit when coordination is free.
func amdahlCeiling(s float64) float64 {
	if s <= 0 {
		return math.Inf(1)
	}
	return 1 / s
}
