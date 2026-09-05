// SPDX-License-Identifier: MIT

package tui

// cockpit_metrics.go, the real numbers behind the views, computed once at
// startup so the render path stays pure and does no I/O. The rule (#189):
// never invent a figure, but a figure that exists must be shown, not dropped.

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/cost"
	"github.com/ankit373/hydra/internal/graph"
	"github.com/ankit373/hydra/internal/pricing"
	"github.com/ankit373/hydra/internal/trust"
)

// ckMetrics is everything the views need from cost.jsonl, trust, and the code
// graph. Every field degrades to empty rather than to a placeholder value, so
// a renderer can tell "no data" from "zero".
type ckMetrics struct {
	stats map[string]*ckModelStat // cost-row model name → per-model aggregates

	spendUSD  float64 // today
	monthUSD  float64
	monthReqs int
	todayReqs int

	todayActualTok, todayEstTok int
	savedTodayUSD, baseTodayUSD float64

	byModelToday []cost.GroupRow
	byTierToday  []cost.GroupRow
	byDay        []cost.GroupRow // last 14 days
	localModels  map[string]bool // model → only ever routed to a local provider

	runCost map[string]ckRunCost // run_id → what cost.jsonl knows about the run

	graph      *graph.Graph // nil when graph.json is absent
	calibrator *trust.Calibrator
	trustStats *trust.Stats // nil when no consensus run was ever recorded
}

// ckModelStat aggregates one model's cost rows for the detail panel.
type ckModelStat struct {
	wall      []int64 // all wall_ms samples, for p50
	reqsToday int
	costToday float64
	lastRunID string
	lastTS    string
}

// ckRunCost is the cost-side of a run, joined by run_id into activity traces.
type ckRunCost struct {
	enum     string
	strategy string // swarm mode when the run fanned out
	prompt   int
	resp     int
	actual   int // tokens the provider reported
	est      int // tokens Hydra estimated
	costUSD  float64
}

// ckFixedSwarmN is the fixed-N swarm baseline trust.Aggregate compares the
// real consensus sample counts against.
const ckFixedSwarmN = 5

// ckPricer is the per-tier cost lookup the savings math needs. An interface
// rather than *pricing.DB keeps the arithmetic testable without a resolvable
// registry/pricing.yaml on disk.
type ckPricer interface {
	EstimateCost(tier, inputTokens, outputTokens int) float64
}

// ckLoadMetrics reads the real logs once, one cost.jsonl pass feeds spend,
// per-model stats, usage groupings, and the run join.
func ckLoadMetrics(pr *pricing.DB) ckMetrics {
	m := ckMetrics{
		stats:       map[string]*ckModelStat{},
		localModels: map[string]bool{},
		runCost:     map[string]ckRunCost{},
	}
	if rows, err := cost.LoadAll(); err == nil {
		m.fold(rows, pr, time.Now().UTC())
	}
	if cal, err := trust.New(trust.DefaultPath()); err == nil {
		m.calibrator = cal
	}
	if runs, err := trust.LoadRuns(trust.DefaultLogPath()); err == nil && len(runs) > 0 {
		st := trust.Aggregate(runs, ckFixedSwarmN)
		m.trustStats = &st
	}
	// graph.json is optional; a missing one simply means no change impact.
	if g, err := graph.Load(ckGraphPath()); err == nil {
		m.graph = g
	}
	return m
}

// fold aggregates the cost rows relative to now (UTC, matching cost.Summary's
// day boundary).
func (m *ckMetrics) fold(rows []cost.Row, pr ckPricer, now time.Time) {
	day := now.Format("2006-01-02")
	month := now.Format("2006-01")
	var today []cost.Row
	nonLocal := map[string]bool{}

	for _, r := range rows {
		if r.Model != "" {
			st := m.stats[r.Model]
			if st == nil {
				st = &ckModelStat{}
				m.stats[r.Model] = st
			}
			if r.WallMS > 0 {
				st.wall = append(st.wall, r.WallMS)
			}
			if r.RunID != "" {
				st.lastRunID = r.RunID
			}
			st.lastTS = r.TS
			if strings.HasPrefix(r.TS, day) {
				st.reqsToday++
				st.costToday += r.EstCostUSD
			}
			if ckLocalExecutor(r.Executor) {
				m.localModels[r.Model] = true
			} else {
				nonLocal[r.Model] = true
			}
		}
		if r.RunID != "" {
			rc := m.runCost[r.RunID]
			if r.Enum != "" {
				rc.enum = r.Enum
			}
			if r.SwarmMode != "" {
				rc.strategy = r.SwarmMode
			}
			rc.prompt += r.PromptTokens
			rc.resp += r.ResponseTokens
			a, e := cost.TokenSourceShare([]cost.Row{r})
			rc.actual += a
			rc.est += e
			rc.costUSD += r.EstCostUSD
			m.runCost[r.RunID] = rc
		}
		if strings.HasPrefix(r.TS, month) {
			m.monthUSD += r.EstCostUSD
			m.monthReqs++
		}
		if strings.HasPrefix(r.TS, day) {
			today = append(today, r)
		}
	}
	// A model with any non-local row must not claim "local · free".
	for model := range nonLocal {
		delete(m.localModels, model)
	}

	m.todayReqs = len(today)
	m.spendUSD = cost.SummaryFromRows(rows).Today.EstCostUSD
	m.todayActualTok, m.todayEstTok = cost.TokenSourceShare(today)
	m.savedTodayUSD, m.baseTodayUSD = ckSavings(today, pr)
	m.byModelToday = cost.ByModel(today)
	m.byTierToday = cost.GroupBy(today, func(r cost.Row) string { return "T" + strconv.Itoa(r.Tier) })
	m.byDay = cost.ByDay(cost.FilterDays(rows, 14))
}

// ckLocalExecutor reports whether a cost row's executor field names a local
// provider, dispatch stamps Head.Provider there ("local" for port heads,
// "ollama" in older rows).
func ckLocalExecutor(e string) bool { return e == "local" || e == "ollama" }

// ckGraphPath locates graph.json the same way the CLI does.
func ckGraphPath() string { return filepath.Join(config.ScriptHome(), "graph.json") }

// ckSavings compares what was actually spent against what the same tokens
// would have cost routed entirely to tier 1, the "one frontier model for
// everything" baseline Hydra exists to beat. Both sides come from real rows
// priced through the same pricing DB, so the comparison is like-for-like.
// saved may be ≤ 0; the renderer decides how to say that honestly.
func ckSavings(rows []cost.Row, pr ckPricer) (saved, baseline float64) {
	if pr == nil {
		return 0, 0
	}
	var actual float64
	for _, r := range rows {
		actual += r.EstCostUSD
		baseline += pr.EstimateCost(1, r.PromptTokens, r.ResponseTokens)
	}
	return baseline - actual, baseline
}

// ckBlastFor computes the real change impact of a file: the dependent count and
// percolation factor from the code graph. Returns ok=false when there is no
// graph or the file is not in it, the caller then says nothing (#193).
func (m ckMetrics) ckBlastFor(file string) (radius float64, dependents int, kappa float64, ok bool) {
	if m.graph == nil || file == "" {
		return 0, 0, 0, false
	}
	impact := m.graph.Impact(file)
	radius, kappa, dependents = impact.Radius, impact.Percolation, impact.Dependents
	// A file absent from the graph yields the neutral radius; saying nothing is
	// more honest than reporting a floor value as a measurement.
	if radius <= 1.0 && dependents == 0 {
		return 0, 0, 0, false
	}
	return radius, dependents, kappa, true
}

// ckStatFor finds a model's cost aggregates. cost.jsonl records the model as
// the executor reported it ("Qwen2.5-Coder:7b (Ollama)") while the scan names
// it differently, so an exact match silently misses, matching is therefore
// tolerant: exact, then either string containing the other, case-insensitively.
func (m ckMetrics) ckStatFor(name, id string) ckModelStat {
	if v, ok := m.stats[name]; ok {
		return *v
	}
	if v, ok := m.stats[id]; ok {
		return *v
	}
	for _, key := range []string{name, id} {
		if key == "" {
			continue
		}
		lk := strings.ToLower(key)
		for model, v := range m.stats {
			lm := strings.ToLower(model)
			if strings.Contains(lm, lk) || strings.Contains(lk, lm) {
				return *v
			}
		}
	}
	return ckModelStat{}
}

// ckScorecardFor returns the calibration rows recorded against this model,
// most-observed first. Trust keys sources by id, so the id is matched first
// and the display name tolerantly after it.
func (m ckMetrics) ckScorecardFor(id, name string) []trust.Stat {
	if m.calibrator == nil {
		return nil
	}
	var out []trust.Stat
	for _, s := range m.calibrator.Report() {
		if ckSourceMatches(s.Source, id) || ckSourceMatches(s.Source, name) {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].N != out[j].N {
			return out[i].N > out[j].N
		}
		return out[i].Domain < out[j].Domain
	})
	return out
}

// ckSourceMatches is the tolerant source↔model comparison.
func ckSourceMatches(source, key string) bool {
	if key == "" || source == "" {
		return false
	}
	ls, lk := strings.ToLower(source), strings.ToLower(key)
	return ls == lk || strings.Contains(ls, lk) || strings.Contains(lk, ls)
}
