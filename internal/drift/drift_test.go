// SPDX-License-Identifier: MIT

package drift

import (
	"math"
	"math/rand/v2"
	"testing"
)

const seeds = 20

func stream(seed uint64) *rand.Rand { return rand.New(rand.NewPCG(seed, 1544)) }

func weightSum(d *Detector) float64 {
	sum := 0.0
	for _, s := range d.Segments {
		sum += s.Weight
	}
	return sum
}

func TestDetector_FindsInjectedDrop(t *testing.T) {
	const drop, within = 2000, 100
	for seed := range uint64(seeds) {
		r, d := stream(seed), New(1, 1)
		found := -1
		for step := range drop + within {
			rate := 0.92
			if step >= drop {
				rate = 0.70
			}
			d.Observe(r.Float64() < rate)
			if step >= drop && found < 0 && d.Changed() {
				found = step - drop
			}
		}
		if found < 0 {
			t.Errorf("seed %d: no change found within %d observations of the drop", seed, within)
			continue
		}
		// The segment it settled on began near the drop, not long before it.
		if start := d.Best; start < drop-50 || start > drop+within {
			t.Errorf("seed %d: change placed at step %d, drop was at %d", seed, start, drop)
		}
	}
}

// A run of fails at a stable rate can look like a change. It may be called one
// rarely, and is taken back once the passes return.
func TestDetector_NoFalseAlarmOnStableRate(t *testing.T) {
	const steps = 5000
	alarms := 0
	for seed := range uint64(seeds) {
		r, d := stream(seed), New(1, 1)
		for step := range steps {
			d.Observe(r.Float64() < 0.92)
			if step > 50 && d.Changed() {
				alarms++
			}
		}
		if p, f := d.Since(); p+f != steps {
			t.Errorf("seed %d: since-counts %d, want the whole stable run of %d", seed, p+f, steps)
		}
	}
	// About one alarm per 100,000 stable observations was measured over 200 seeds.
	if alarms > 1 {
		t.Errorf("%d false alarms over %d stable observations", alarms, seeds*steps)
	}
}

func TestDetector_IdleMovesMassToFreshSegment(t *testing.T) {
	r, d := stream(1), New(1, 1)
	for range 300 {
		d.Observe(r.Float64() < 0.9)
	}
	for _, k := range []int{1, 10, 693, 5000} {
		before := d.Segments[len(d.Segments)-1].Weight
		d.Idle(k)
		keep := math.Pow(1-d.Hazard, float64(k))
		moved := d.Segments[len(d.Segments)-1].Weight - before*keep
		if math.Abs(moved-(1-keep)) > 1e-12 {
			t.Errorf("Idle(%d) moved %.15f to the fresh segment, want %.15f", k, moved, 1-keep)
		}
	}
}

func TestDetector_WeightsSumToOneAfterTruncation(t *testing.T) {
	r, d := stream(2), New(1, 1)
	for step := range 3000 {
		d.Observe(r.Float64() < 0.8)
		if step%7 == 0 {
			d.Idle(3)
		}
		if len(d.Segments) > MaxSegments {
			t.Fatalf("step %d: %d segments, cap is %d", step, len(d.Segments), MaxSegments)
		}
		if s := weightSum(d); math.Abs(s-1) > 1e-9 {
			t.Fatalf("step %d: weights sum to %.12f", step, s)
		}
	}
	if fresh := d.Segments[len(d.Segments)-1]; fresh.Passes+fresh.Fails != 0 {
		t.Errorf("the last segment has %d observations, want the fresh one", fresh.Passes+fresh.Fails)
	}
}

// A source that got worse and then stopped being called is believed changed
// again after long enough, so its record is near empty and it gets tried.
func TestDetector_RecoveredRateIsTriedAgain(t *testing.T) {
	r, d := stream(3), New(1, 1)
	for step := range 2300 {
		rate := 0.92
		if step >= 2000 {
			rate = 0.70
		}
		d.Observe(r.Float64() < rate)
	}
	if p, f := d.Since(); p+f == 0 || p+f > 400 {
		t.Fatalf("since-counts %d after the drop, want the post-drop stretch", p+f)
	}
	// 0.99 of the mass must move: (1-H)^k <= 0.01 needs about 4,600 idle steps.
	moved := false
	for range 60 {
		d.Idle(100)
		moved = moved || d.Changed()
	}
	if p, f := d.Since(); p+f > 5 {
		t.Errorf("since-counts %d after 6000 idle steps, want a near-empty segment", p+f)
	}
	if !moved {
		t.Error("idle steps made the fresh segment most probable without reporting a change")
	}
}

func TestDetector_RoseTellsDirection(t *testing.T) {
	for _, tc := range []struct {
		after float64
		rose  bool
	}{{0.70, false}, {0.99, true}} {
		r, d := stream(7), New(1, 1)
		for range 2000 {
			d.Observe(r.Float64() < 0.85)
		}
		for range 500 {
			from := d.Best
			if d.Observe(r.Float64() < tc.after); d.Changed() {
				if got := d.Rose(from); got != tc.rose {
					t.Errorf("rate 0.85 to %.2f: Rose %v, want %v", tc.after, got, tc.rose)
				}
				break
			}
		}
		if d.Best == 0 {
			t.Errorf("rate 0.85 to %.2f: no change found", tc.after)
		}
	}
}
