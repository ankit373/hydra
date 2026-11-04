// SPDX-License-Identifier: MIT

package dispatch

import (
	"github.com/ankit373/hydra/internal/cost"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/rollup"
	"github.com/ankit373/hydra/internal/sketch"
)

// latencyIndex merges rollup rows into one sketch per head. A rollup row only
// carries the raw model string it was logged under (rollup.Build keys on it,
// not on the canonical head id), so cost.ResolveHeadName, the tool #729
// already built for this exact ambiguity, is what turns it back into one.
// A name nothing declares is dropped rather than guessed: merging it into the
// wrong head's figure is worse than leaving that figure alone.
func latencyIndex(rows []rollup.Row) map[string]*sketch.Sketch {
	idx := map[string]*sketch.Sketch{}
	for _, r := range rows {
		if r.Latency == nil || r.Latency.Count() == 0 {
			continue
		}
		id := cost.ResolveHeadName(r.Model)
		if id == "" {
			continue
		}
		if cur := idx[id]; cur != nil {
			_ = cur.Merge(r.Latency) // rollup.Build always uses sketch.DefaultAlpha
			continue
		}
		idx[id] = r.Latency.Clone()
	}
	return idx
}

// latencyP95 is a head's recent p95 wall-clock latency in milliseconds, and
// whether the rollups say anything about it at all.
func (d *Dispatcher) latencyP95(h provider.Head) (float64, bool) {
	sk, ok := d.latency[h.ID]
	if !ok || sk.Count() == 0 {
		return 0, false
	}
	return sk.Quantile(0.95), true
}
