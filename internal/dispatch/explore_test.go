// SPDX-License-Identifier: MIT

package dispatch

import (
	"math"
	"testing"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/provider"
)

func exploreHeads(n int) []provider.Head {
	heads := make([]provider.Head, n)
	for i := range heads {
		heads[i] = provider.Head{ID: string(rune('a' + i)), CapScore: 100 - i}
	}
	return heads
}

func exploreDispatcher(rate float64) *Dispatcher {
	return &Dispatcher{cfg: &config.Config{ExploreRate: rate}}
}

// The default must be a behavioural no-op: shipping exploration on by default
// would silently spend a user's money to buy evidence they never asked for.
func TestPickDefaultRateIsPureArgmax(t *testing.T) {
	d := exploreDispatcher(0)
	in := exploreHeads(5)
	for i := 0; i < 500; i++ {
		got, p := d.pick(in, Options{})
		if p != 1 {
			t.Fatalf("act_prob = %v, want 1 at ExploreRate 0", p)
		}
		for j := range in {
			if got[j].ID != in[j].ID {
				t.Fatalf("order changed at %d: %s != %s", j, got[j].ID, in[j].ID)
			}
		}
	}
}

// A zero-valued Dispatcher must not panic or explore.
func TestPickNilConfig(t *testing.T) {
	got, p := (&Dispatcher{}).pick(exploreHeads(3), Options{})
	if p != 1 || len(got) != 3 {
		t.Fatalf("nil cfg: got prob %v, %d heads; want 1, 3", p, len(got))
	}
}

func TestPickGuardsForceCertainty(t *testing.T) {
	cases := []struct {
		name  string
		rate  float64
		heads int
		opts  Options
	}{
		{"dry run predicts real routing", 1, 5, Options{DryRun: true}},
		{"resource-targeted dispatch", 1, 5, Options{Resource: "internal/auth/token.go"}},
		{"single candidate", 1, 1, Options{}},
		{"rate above 1 is nonsense", 1.5, 5, Options{}},
		{"negative rate is nonsense", -0.2, 5, Options{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := exploreDispatcher(tc.rate)
			in := exploreHeads(tc.heads)
			for i := 0; i < 200; i++ {
				got, p := d.pick(in, tc.opts)
				if p != 1 {
					t.Fatalf("act_prob = %v, want 1", p)
				}
				if got[0].ID != in[0].ID {
					t.Fatalf("explored past a guard: front is %s", got[0].ID)
				}
			}
		})
	}
}

// The recorded propensity must be the probability the policy actually assigns
// to the head it returned, or every downstream estimate is weighted by fiction.
func TestPickReportsItsOwnDistribution(t *testing.T) {
	for _, eps := range []float64{0.05, 0.25, 1} {
		for _, n := range []int{2, 3, 7} {
			d := exploreDispatcher(eps)
			in := exploreHeads(n)
			counts := make([]int, n)
			const trials = 20000
			for i := 0; i < trials; i++ {
				got, p := d.pick(in, Options{})
				idx := -1
				for j := range in {
					if in[j].ID == got[0].ID {
						idx = j
						break
					}
				}
				if idx < 0 {
					t.Fatalf("returned a head that was not a candidate")
				}
				counts[idx]++
				if want := SelectionProb(idx, n, eps); math.Abs(p-want) > 1e-12 {
					t.Fatalf("eps=%v n=%d idx=%d: act_prob %v, want %v", eps, n, idx, p, want)
				}
				// The non-chosen heads must survive as the fallback chain.
				if len(got) != n {
					t.Fatalf("candidate lost: %d of %d", len(got), n)
				}
			}
			var sum float64
			for i := 0; i < n; i++ {
				sum += SelectionProb(i, n, eps)
			}
			if math.Abs(sum-1) > 1e-12 {
				t.Fatalf("eps=%v n=%d: probabilities sum to %v, want 1", eps, n, sum)
			}
			for i := 0; i < n; i++ {
				want := SelectionProb(i, n, eps)
				got := float64(counts[i]) / trials
				if math.Abs(got-want) > 0.02 {
					t.Errorf("eps=%v n=%d idx=%d: empirical %.4f vs stated %.4f", eps, n, i, got, want)
				}
			}
		}
	}
}

// Exploration reorders; it must never drop, duplicate or invent a candidate,
// because the tail is the fallback chain the dispatch loop walks.
func TestPickPreservesCandidateSet(t *testing.T) {
	d := exploreDispatcher(1)
	in := exploreHeads(6)
	for i := 0; i < 2000; i++ {
		got, _ := d.pick(in, Options{})
		seen := map[string]int{}
		for _, h := range got {
			seen[h.ID]++
		}
		if len(seen) != len(in) {
			t.Fatalf("candidate set changed: %v", seen)
		}
		for _, h := range in {
			if seen[h.ID] != 1 {
				t.Fatalf("head %s appears %d times", h.ID, seen[h.ID])
			}
		}
	}
}

func TestSelectionProbEdgeCases(t *testing.T) {
	if got := SelectionProb(0, 0, 0.1); got != 0 {
		t.Errorf("n=0: %v, want 0", got)
	}
	if got := SelectionProb(3, 3, 0.1); got != 0 {
		t.Errorf("index out of range: %v, want 0", got)
	}
	if got := SelectionProb(0, 1, 0.5); got != 1 {
		t.Errorf("single candidate: %v, want 1", got)
	}
	if got := SelectionProb(0, 4, 0); got != 1 {
		t.Errorf("eps=0 greedy: %v, want 1", got)
	}
	if got := SelectionProb(2, 4, 0); got != 0 {
		t.Errorf("eps=0 non-greedy: %v, want 0", got)
	}
}

// The propensity is worthless unless it reaches the log. Both rows must carry
// it, and it must be present as a field rather than omitted: a reader cannot
// tell an absent propensity from a certain one, and 1/0 is not a weight.
func TestLogDispatchWritesPropensity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	const actProb = 0.0125
	d := newTestDispatcher()
	if err := d.logDispatch(logResult(), "p", Options{}, actProb); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"dispatch.jsonl", "cost.jsonl"} {
		rows := readLog(t, home, name)
		if len(rows) != 1 {
			t.Fatalf("%s: got %d rows, want 1", name, len(rows))
		}
		got, ok := rows[0]["act_prob"]
		if !ok {
			t.Fatalf("%s: act_prob missing — an absent propensity is unusable", name)
		}
		if f, _ := got.(float64); f != actProb {
			t.Errorf("%s: act_prob %v, want %v", name, got, actProb)
		}
		keep, ok := rows[0]["keep_prob"]
		if !ok {
			t.Fatalf("%s: keep_prob missing", name)
		}
		if f, _ := keep.(float64); f != 1 {
			t.Errorf("%s: keep_prob %v, want 1 (nothing samples these rows yet)", name, keep)
		}
	}
}
