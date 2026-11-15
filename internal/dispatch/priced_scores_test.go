// SPDX-License-Identifier: MIT

package dispatch

import (
	"math"
	"testing"

	"github.com/ankit373/hydra/internal/provider"
)

// PickGenerator reads a missing CostUSD as free, so every head must come back priced.
func TestPricedScores_UnpricedHeadIsNeverFree(t *testing.T) {
	heads := []provider.Head{{ID: "cheap"}, {ID: "unknown"}}
	d := &Dispatcher{price: func(h provider.Head) (float64, bool) { return 0.002, h.ID == "cheap" }}
	got := d.PricedScores(heads, "")
	if got["cheap"].CostUSD != 0.002 || !math.IsInf(got["unknown"].CostUSD, 1) {
		t.Fatalf("scores = %+v, want cheap at 0.002 and unknown at +Inf", got)
	}
	if got := (&Dispatcher{}).PricedScores(heads, ""); !math.IsInf(got["cheap"].CostUSD, 1) {
		t.Errorf("with no pricing at all, cheap = %v, want +Inf", got["cheap"].CostUSD)
	}
}
