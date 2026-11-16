// SPDX-License-Identifier: MIT

package drift

import (
	"math/rand/v2"
	"slices"
	"testing"
)

const (
	// simWarmup is the record a cell has before the drop: long enough that the
	// first segment is settled, as on any head in daily use.
	simWarmup = 1000
	// simHorizon is how long after the drop a detection still counts.
	simHorizon = 200
)

// simDelay is how many requests after a drop from base to base-drop the change
// is found, simHorizon+1 when it is not found within simHorizon.
func simDelay(seed uint64, base, drop float64) int {
	r, d := rand.New(rand.NewPCG(seed, 1878)), New(1, 1)
	for range simWarmup {
		d.Observe(r.Float64() < base)
	}
	for i := 1; i <= simHorizon; i++ {
		if d.Observe(r.Float64() < base-drop); d.Changed() {
			return i
		}
	}
	return simHorizon + 1
}

// simAlarms counts changes found on a stable rate over months at perMonth requests.
func simAlarms(seed uint64, base float64, perMonth, months int) int {
	r, d := rand.New(rand.NewPCG(seed, 1878)), New(1, 1)
	alarms := 0
	for range perMonth * months {
		if d.Observe(r.Float64() < base); d.Changed() {
			alarms++
		}
	}
	return alarms
}

// delays is simDelay over seeds [0, runs), sorted, so a quantile is an index.
func delays(runs int, base, drop float64) []int {
	out := make([]int, runs)
	for seed := range runs {
		out[seed] = simDelay(uint64(seed), base, drop)
	}
	slices.Sort(out)
	return out
}

// quantile reads q of sorted delays; simHorizon+1 means not found by then.
func quantile(sorted []int, q float64) int { return sorted[int(q*float64(len(sorted)-1))] }

// missed counts the runs not found within simHorizon, the tail of sorted.
func missed(sorted []int) int {
	i := slices.Index(sorted, simHorizon+1)
	if i < 0 {
		return 0
	}
	return len(sorted) - i
}

// A 16-point drop at a 0.9 base, the easiest measured case. Within 50 is not
// met, nor reachable at one false alarm a month at 1,000 requests (#1878).
func TestSim_DetectsSixteenPointDrop(t *testing.T) {
	ds := delays(40, 0.9, 0.16)
	if med := quantile(ds, 0.5); med > 110 {
		t.Errorf("median delay %d requests, measured 89 over 1,000 runs", med)
	}
	if n := missed(ds); n > len(ds)/5 {
		t.Errorf("%d of %d runs missed the drop within %d, measured 6%%", n, len(ds), simHorizon)
	}
}

// The bar: under one false alarm a month, at the busiest measured traffic.
func TestSim_StableRateFalseAlarmBound(t *testing.T) {
	const runs, perMonth, months = 4, 1000, 12
	alarms := 0
	for seed := range uint64(runs) {
		alarms += simAlarms(seed, 0.6, perMonth, months)
	}
	if perMonthRate := float64(alarms) / (runs * months); perMonthRate >= 1 {
		t.Errorf("%.2f false alarms a month at %d requests a month, the bar is under 1", perMonthRate, perMonth)
	}
}
