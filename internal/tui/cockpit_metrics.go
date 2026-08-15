// SPDX-License-Identifier: MIT

package tui

// cockpit_metrics.go — the real numbers behind the dashboard.
//
// These panels previously showed invented values (an LCG-hashed "sparkline", a
// confidence bucketed by tier, savings from a made-up price table, a literal
// "κ=3.1 ⚠ 12 dependents"). #189 deleted them for being fake; that was half a
// fix, because every one of them is computable from data Hydra already stores.
// This file computes them for real. The rule is unchanged — never invent a
// figure — but a figure that exists must be shown, not dropped.

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/cost"
	"github.com/ankit373/hydra/internal/graph"
	"github.com/ankit373/hydra/internal/pricing"
	"github.com/ankit373/hydra/internal/trust"
)

// ckMetrics is everything the dashboard needs, computed once at startup so the
// render path stays pure and does no I/O.
type ckMetrics struct {
	latency  map[string][]float64 // model → recent wall_ms, oldest first
	lastMS   map[string]int64     // model → most recent wall_ms
	savedUSD float64              // real spend vs a top-tier baseline
	baseUSD  float64              // what that baseline would have cost
	spendUSD float64              // today's real spend
	graph    *graph.Graph         // nil when graph.json is absent

	// calibrator is nil when no calibration ledger exists yet.
	calibrator *trust.Calibrator
}

// ckSparkWidth is how many samples a sparkline shows.
const ckSparkWidth = 14

// ckPricer is the per-tier cost lookup the savings panel needs. Taking an
// interface rather than *pricing.DB keeps the arithmetic testable without a
// resolvable registry/pricing.yaml on disk — the same reason internal/swarm
// defines PricingReader.
type ckPricer interface {
	EstimateCost(tier, inputTokens, outputTokens int) float64
}

// ckLoadMetrics reads the real logs once. Every field degrades to empty rather
// than to a placeholder value, so the renderer can tell "no data" from "zero".
func ckLoadMetrics(pr *pricing.DB) ckMetrics {
	m := ckMetrics{
		latency: map[string][]float64{},
		lastMS:  map[string]int64{},
	}

	// One read of cost.jsonl feeds latency, savings, and today's spend — this
	// used to be three independent full reads (LoadAll here, plus a separate
	// Summary call here and another in ckSpendToday), tripling every cockpit
	// startup's cost.jsonl I/O for identical data.
	if rows, err := cost.LoadAll(); err == nil {
		if len(rows) > 0 {
			m.latency, m.lastMS = ckLatencySeries(rows)
			m.savedUSD, m.baseUSD = ckSavings(rows, pr)
		}
		m.spendUSD = cost.SummaryFromRows(rows).Today.EstCostUSD
	}
	if cal, err := trust.New(trust.DefaultPath()); err == nil {
		m.calibrator = cal
	}
	// graph.json is optional; a missing one simply means no blast radius.
	if g, err := graph.Load(ckGraphPath()); err == nil {
		m.graph = g
	}
	return m
}

// ckGraphPath locates graph.json the same way the CLI does.
func ckGraphPath() string { return filepath.Join(config.ScriptHome(), "graph.json") }

// ckLatencySeries turns cost rows into a per-model wall_ms history, oldest
// first, capped at ckSparkWidth. cost.jsonl carries wall_ms on every row, so
// this is a genuine latency trace — the thing the old LCG "sparkline" was
// pretending to be.
func ckLatencySeries(rows []cost.Row) (map[string][]float64, map[string]int64) {
	byModel := map[string][]float64{}
	last := map[string]int64{}
	for _, r := range rows {
		if r.WallMS <= 0 || r.Model == "" {
			continue
		}
		byModel[r.Model] = append(byModel[r.Model], float64(r.WallMS))
		last[r.Model] = r.WallMS
	}
	for k, v := range byModel {
		if len(v) > ckSparkWidth {
			byModel[k] = v[len(v)-ckSparkWidth:]
		}
	}
	return byModel, last
}

// ckSavings compares what was actually spent against what the same work would
// have cost routed entirely to tier 1 — the "one expensive model for
// everything" baseline Hydra exists to beat. Both sides come from real rows;
// the baseline is priced with the same pricing DB used for the real cost, so
// the two are comparable.
func ckSavings(rows []cost.Row, pr ckPricer) (saved, baseline float64) {
	if pr == nil {
		return 0, 0
	}
	var actual float64
	for _, r := range rows {
		actual += r.EstCostUSD
		baseline += pr.EstimateCost(1, r.PromptTokens, r.ResponseTokens)
	}
	saved = baseline - actual
	if saved < 0 {
		saved = 0 // never claim a saving that did not happen
	}
	return saved, baseline
}

// ckSpark renders values as block glyphs, scaled to the series' own range so a
// flat-but-slow head still reads as flat. Fewer than two samples is not a
// trace, so it renders as "—" rather than a misleading single bar.
func ckSpark(vals []float64) string {
	if len(vals) < 2 {
		return "—"
	}
	lo, hi := vals[0], vals[0]
	for _, v := range vals {
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	blocks := []rune("▁▂▃▄▅▆▇█")
	var b strings.Builder
	for _, v := range vals {
		idx := 0
		if hi > lo {
			idx = int((v - lo) / (hi - lo) * float64(len(blocks)-1))
		}
		if idx < 0 {
			idx = 0
		}
		if idx >= len(blocks) {
			idx = len(blocks) - 1
		}
		b.WriteRune(blocks[idx])
	}
	return b.String()
}

// ckBlastFor computes the real blast radius of a file: the dependent count and
// percolation factor from the code graph. Returns ok=false when there is no
// graph or the file is not in it — the caller then says nothing, rather than
// printing the fixed "κ=3.1 ⚠ 12 dependents" this replaced.
func (m ckMetrics) ckBlastFor(file string) (radius float64, dependents int, kappa float64, ok bool) {
	if m.graph == nil || file == "" {
		return 0, 0, 0, false
	}
	radius = m.graph.BlastRadiusForFile(file)
	kappa = m.graph.PercolationFactor(file)
	dependents = m.graph.DependentCountForFile(file)
	// A file absent from the graph yields the neutral radius; saying nothing is
	// more honest than reporting a floor value as a measurement.
	if radius <= 1.0 && dependents == 0 {
		return 0, 0, 0, false
	}
	return radius, dependents, kappa, true
}

// ckDiagnosticity is a head's calibrated information content in nats, from the
// trust ledger. Zero means uncalibrated, which the renderer shows as "—" —
// this replaced a confidence hardcoded by tier band.
func (m ckMetrics) ckDiagnosticity(headName, domain string) float64 {
	if m.calibrator == nil {
		return 0
	}
	if domain == "" {
		domain = "default"
	}
	return m.calibrator.D(headName, domain)
}

// ckSeriesFor finds a head's latency history. cost.jsonl records the model as
// the executor reported it ("Qwen2.5-Coder:7b (Ollama)") while probe names the
// head after its provider ("Ollama"), so an exact match silently misses — the
// bug that showed a busy local head as having never run. Matching is therefore
// tolerant: exact, then either string containing the other, case-insensitively.
func (m ckMetrics) ckSeriesFor(name, id string) ([]float64, int64) {
	if v, ok := m.latency[name]; ok {
		return v, m.lastMS[name]
	}
	if v, ok := m.latency[id]; ok {
		return v, m.lastMS[id]
	}
	for _, key := range []string{name, id} {
		if key == "" {
			continue
		}
		lk := strings.ToLower(key)
		for model, v := range m.latency {
			lm := strings.ToLower(model)
			if strings.Contains(lm, lk) || strings.Contains(lk, lm) {
				return v, m.lastMS[model]
			}
		}
	}
	return nil, 0
}

// ckSortedModels lists models with latency history, most-sampled first, so the
// dashboard's own ordering is stable rather than map-random.
func (m ckMetrics) ckSortedModels() []string {
	out := make([]string, 0, len(m.latency))
	for k := range m.latency {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if len(m.latency[out[i]]) != len(m.latency[out[j]]) {
			return len(m.latency[out[i]]) > len(m.latency[out[j]])
		}
		return out[i] < out[j]
	})
	return out
}

// ckFmtMS renders a latency for the dashboard.
func ckFmtMS(ms int64) string {
	if ms <= 0 {
		return "—"
	}
	if ms >= 10000 {
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	}
	return fmt.Sprintf("%dms", ms)
}
