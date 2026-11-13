// SPDX-License-Identifier: MIT

package dispatch

import (
	"errors"
	"log"

	"github.com/ankit373/hydra/internal/cost"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/signals"
	"github.com/ankit373/hydra/internal/sketch"
)

// latencyIndex groups the cost log's wall_ms into one sketch per head, keyed by
// cost.CanonicalKey (#729). Not rollups.jsonl: only `hyctl stats --latency`
// rewrites that, so it can be weeks behind the log.
func latencyIndex(rows []cost.Row) map[string]*sketch.Sketch {
	idx := map[string]*sketch.Sketch{}
	for _, r := range rows {
		id := cost.CanonicalKey(r)
		sk := idx[id]
		if sk == nil {
			sk = sketch.New(sketch.DefaultAlpha)
			idx[id] = sk
		}
		sk.Add(float64(r.WallMS))
	}
	return idx
}

// loadLatency builds the index only when a rule reads the signal, so an empty
// signals.yaml pays nothing. An unreadable log degrades to absent.
func (d *Dispatcher) loadLatency() {
	if d.latency != nil || !d.rules.ReadsAny(signals.SigLatencyP95MS) {
		return
	}
	rows, err := cost.LoadRows(cost.DefaultLogPath())
	if err != nil && !errors.Is(err, cost.ErrNoLog) {
		log.Printf("warning: cost log unreadable (%v), latency.p95_ms will read absent", err)
		return
	}
	d.latency = latencyIndex(rows)
}

// latencyP95 is a head's p95 wall-clock latency in milliseconds over the cost
// log, and whether the log says anything about it at all.
func (d *Dispatcher) latencyP95(h provider.Head) (float64, bool) {
	d.latencyOnce.Do(d.loadLatency)
	sk, ok := d.latency[h.ID]
	if !ok || sk.Count() == 0 {
		return 0, false
	}
	return sk.Quantile(0.95), true
}
