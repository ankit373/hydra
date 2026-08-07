// SPDX-License-Identifier: MIT

package workspace

import (
	"strings"
	"testing"
	"time"
)

// Benchmarks here are regression guards, not tuning targets. matchGlob runs on
// every scope check, and its failure mode was not "slow" — it was "never
// returns" (see the memoization note in matchSegments). A benchmark that stops
// completing is the signal.

func BenchmarkMatchGlob_Typical(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = matchGlob("internal/executor/http.go", "internal/**")
	}
}

func BenchmarkMatchGlob_DeepPath(b *testing.B) {
	rel := strings.Repeat("a/", 30) + "target.go"
	for i := 0; i < b.N; i++ {
		_ = matchGlob(rel, "**/*.go")
	}
}

// The shape that used to hang: many "**" against a long path, with a tail that
// never matches so the search cannot exit early.
func BenchmarkMatchGlob_ManyDoubleStarsNoMatch(b *testing.B) {
	rel := strings.Repeat("a/", 20) + "x"
	pat := strings.Repeat("**/", 10) + "no-such-segment"
	for i := 0; i < b.N; i++ {
		_ = matchGlob(rel, pat)
	}
}

// A hard ceiling rather than a relative one: exponential blow-up is orders of
// magnitude, so any threshold in this range separates "fixed" from "broken"
// without being brittle about machine speed.
func TestMatchGlob_PathologicalInputIsNotExponential(t *testing.T) {
	cases := []struct{ segments, stars int }{
		{20, 10},
		{30, 15},
		{40, 20},
	}
	for _, tc := range cases {
		rel := strings.Repeat("a/", tc.segments) + "x"
		pat := strings.Repeat("**/", tc.stars) + "no-such-segment"

		start := time.Now()
		done := make(chan bool, 1)
		go func() { done <- matchGlob(rel, pat) }()

		select {
		case <-done:
			if d := time.Since(start); d > 2*time.Second {
				t.Errorf("%d segments / %d '**' took %v — matching is superlinear again",
					tc.segments, tc.stars, d)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%d segments / %d '**' did not terminate — matchSegments has lost "+
				"its memoization and a workspace.yaml pattern can hang every scope check",
				tc.segments, tc.stars)
		}
	}
}
