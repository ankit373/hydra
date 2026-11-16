//go:build sim

// SPDX-License-Identifier: MIT

// The full table behind the drift alarm's published numbers. Too slow for CI:
//
//	go test -tags sim -run TestSim -v ./internal/drift
package drift

import (
	"fmt"
	"testing"
)

func TestSim_DelayTable(t *testing.T) {
	const runs = 1000
	for _, base := range []float64{0.6, 0.8, 0.9} {
		for _, drop := range []float64{0.05, 0.10, 0.16} {
			t.Run(fmt.Sprintf("%.2f-%.2f", base, drop), func(t *testing.T) {
				t.Parallel()
				ds := delays(runs, base, drop)
				t.Logf("| %.2f | %2.0f | %s | %s | %.1f%% |", base, drop*100,
					delayCell(quantile(ds, 0.5)), delayCell(quantile(ds, 0.9)),
					100*float64(missed(ds))/runs)
			})
		}
	}
}

func TestSim_FalseAlarmTable(t *testing.T) {
	const runs, months = 1000, 12
	for _, base := range []float64{0.6, 0.8, 0.9} {
		for _, perMonth := range []int{50, 200, 1000} {
			t.Run(fmt.Sprintf("%.2f-%d", base, perMonth), func(t *testing.T) {
				t.Parallel()
				alarms := 0
				for seed := range uint64(runs) {
					alarms += simAlarms(seed, base, perMonth, months)
				}
				t.Logf("| %.2f | %d | %.4f |", base, perMonth, float64(alarms)/(runs*months))
			})
		}
	}
}

// delayCell prints a delay, or that it was past the horizon.
func delayCell(d int) string {
	if d > simHorizon {
		return fmt.Sprintf(">%d", simHorizon)
	}
	return fmt.Sprint(d)
}
