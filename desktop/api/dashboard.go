// SPDX-License-Identifier: MIT

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ankit373/hydra/internal/budget"
	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/cost"
	"github.com/ankit373/hydra/internal/trust"
)

// TrustFixedSwarmBaseline is the fixed-N swarm the SPRT ensemble is scored
// against. It must stay equal to the literal `hyctl trust stats` passes to
// trust.Aggregate, or the two surfaces report different savings for the same
// runs.
const TrustFixedSwarmBaseline = 5

// Dashboard is everything the Dashboard view renders, assembled server-side.
// The frontend does no aggregation: if a number is wrong it is wrong in Go,
// where a test can catch it.
type Dashboard struct {
	// HasData is false when cost.jsonl does not exist or holds no rows. The
	// view must render "no data yet" rather than a wall of zeros — zeros look
	// like a measurement, and an unrun tool reporting $0.00 spend is a lie.
	HasData bool `json:"hasData"`

	Spend    SpendPanel    `json:"spend"`
	Governor GovernorPanel `json:"governor"`
	Trust    TrustPanel    `json:"trust"`

	ByModel []Breakdown `json:"byModel"`
	ByTier  []Breakdown `json:"byTier"`
	ByDay   []Breakdown `json:"byDay"`

	Recent []RecentCall `json:"recent"`

	// Calibration is per-(source,domain) calibration quality from
	// internal/trust — which models/oracles actually earn their stated
	// confidence. Independent of cost.jsonl (a machine can have calibration
	// history from `hyctl trust record` without ever having dispatched), so
	// unlike ByModel/ByTier/ByDay it is always a list, never nil — an empty
	// leaderboard is a real, renderable state, not "never ran".
	Calibration []CalibrationRow `json:"calibration"`
}

// SpendPanel is the headline cost figure.
type SpendPanel struct {
	TodayUSD   float64 `json:"todayUsd"`
	AllTimeUSD float64 `json:"allTimeUsd"`
	TodayCalls int     `json:"todayCalls"`
	TotalCalls int     `json:"totalCalls"`

	// TokensActualPct is the share of all-time tokens the provider actually
	// reported, as opposed to Hydra's char/4 estimate. Surfacing it is the
	// point: it tells the reader how much to trust the dollar figure above.
	TokensActualPct float64 `json:"tokensActualPct"`
}

// GovernorPanel is the orchestrator's context-window pressure.
type GovernorPanel struct {
	// Known is false when logs/state.json is absent or carries no percentage.
	// Nothing has measured the orchestrator's usage, so Pct is meaningless.
	Known bool   `json:"known"`
	Pct   int    `json:"pct"`
	Mode  string `json:"mode"`

	// EffectiveMode is what the router actually acts on: the level band raised
	// by rate-driven risk when a fast burn would cross 80% before a static
	// threshold catches it. Equals Mode when there is no rate signal.
	EffectiveMode string `json:"effectiveMode"`

	// BurnRatePct is the mean change in percentage points per observation, and
	// Risk the probability of reaching the 80% emergency line within
	// HorizonObs observations. Both are zero when Observations < 2 — that is
	// "no rate signal", not "no risk", which is why Observations ships too: a
	// bare 0 would read as a measurement.
	BurnRatePct float64 `json:"burnRatePct"`
	Risk        float64 `json:"risk"`

	// Observations is how many points the trajectory estimate rests on, and
	// HorizonObs the look-ahead Risk is computed over. Both are counts of
	// claude_pct *updates*, not wall-clock time and not chat turns.
	Observations int `json:"observations"`
	HorizonObs   int `json:"horizonObs"`
}

// TrustPanel summarises the SPRT ensemble's record.
type TrustPanel struct {
	Runs            int     `json:"runs"`
	MeanSamples     float64 `json:"meanSamples"`
	FixedSwarmN     int     `json:"fixedSwarmN"`
	SamplesSavedPct float64 `json:"samplesSavedPct"`
	AutoClearedPct  float64 `json:"autoClearedPct"`
	MeanTargetConf  float64 `json:"meanTargetConf"`
	MeanFinalConf   float64 `json:"meanFinalConf"`
	TotalCostUSD    float64 `json:"totalCostUsd"`
}

// Breakdown is one row of a by-X grouping.
type Breakdown struct {
	Key            string  `json:"key"`
	Calls          int     `json:"calls"`
	PromptTokens   int     `json:"promptTokens"`
	ResponseTokens int     `json:"responseTokens"`
	CostUSD        float64 `json:"costUsd"`
	WallMS         int64   `json:"wallMs"`
}

// CalibrationRow is one (source, domain) row of the calibration leaderboard —
// a direct mapping of trust.Stat, the same shape `hyctl trust calibration`
// prints.
type CalibrationRow struct {
	Source string  `json:"source"`
	Domain string  `json:"domain"`
	D      float64 `json:"d"`  // diagnostic power (nats) — sort key, most first
	Se     float64 `json:"se"` // sensitivity
	Sp     float64 `json:"sp"` // specificity
	N      int     `json:"n"`  // real observations (excludes the Laplace prior)
}

// RecentCall is one row of the activity list.
type RecentCall struct {
	TS      string  `json:"ts"`
	Model   string  `json:"model"`
	Tier    int     `json:"tier"`
	CostUSD float64 `json:"costUsd"`
	WallMS  int64   `json:"wallMs"`
	RunID   string  `json:"runId"`
	TaskID  string  `json:"taskId"`
}

// GetDashboard assembles the Dashboard view.
//
// Every figure routes through the same cost/trust logic the CLI uses —
// cost.SummaryFromRows for the headline, cost.ByModel/ByDay/GroupBy for the
// breakdowns, trust.Aggregate for the ensemble record. Reimplementing any of
// them here would let `hyctl cost` and this view disagree about one file.
func (a *API) GetDashboard() (*Dashboard, error) {
	// A machine that has never dispatched has no cost.jsonl, and cost.LoadAll
	// reports that as an error because the CLI wants to say "has anything
	// dispatched yet?". For a GUI that is the ordinary first-launch state, not a
	// failure — the window must open on an empty dashboard, not an error dialog.
	rows, err := cost.LoadAll()
	if err != nil && !errors.Is(err, cost.ErrNoLog) {
		return nil, err
	}

	d := &Dashboard{
		HasData:     len(rows) > 0,
		Governor:    governorPanel(),
		Trust:       trustPanel(),
		Calibration: calibrationPanel(),
	}
	if !d.HasData {
		// Breakdowns stay nil, not empty slices: the frontend distinguishes
		// "nothing has run" from "ran, produced nothing".
		return d, nil
	}

	// rows is already loaded above — cost.Summary would reload cost.jsonl a
	// second time for the same data, doubling this call's I/O (#524).
	sum := cost.SummaryFromRows(rows)
	d.Spend = spendPanel(sum)

	d.ByModel = toBreakdowns(cost.ByModel(rows))
	d.ByTier = toBreakdowns(cost.GroupBy(rows, tierKey))
	d.ByDay = toBreakdowns(cost.ByDay(rows))
	d.Recent = toRecent(sum.Recent)

	return d, nil
}

// tierKey mirrors `hyctl stats --tier` exactly, including its "unknown" label
// for tier 0, so the two groupings cannot diverge.
func tierKey(r cost.Row) string {
	if r.Tier == 0 {
		return "unknown"
	}
	return fmt.Sprintf("tier-%d", r.Tier)
}

func spendPanel(s *cost.SummaryResult) SpendPanel {
	p := SpendPanel{
		TodayUSD:   s.Today.EstCostUSD,
		AllTimeUSD: s.AllTime.EstCostUSD,
		TodayCalls: s.Today.Calls,
		TotalCalls: s.AllTime.Calls,
	}
	if total := s.ActualTokens + s.EstimatedTokens; total > 0 {
		p.TokensActualPct = 100 * float64(s.ActualTokens) / float64(total)
	}
	return p
}

// governorPanel reads the orchestrator's context usage from state.json. An
// absent or zero-valued file means nothing has reported usage, which renders as
// unknown rather than as a confident 0%.
func governorPanel() GovernorPanel {
	raw, err := os.ReadFile(filepath.Join(config.Dir(), "logs", "state.json"))
	if err != nil {
		return GovernorPanel{}
	}
	// One read for both: the level and the trajectory live in the same file,
	// and dispatch already pairs them the same way (dispatch.go:375).
	var s struct {
		ClaudePct *int  `json:"claude_pct"`
		History   []int `json:"claude_pct_history"`
	}
	if err := json.Unmarshal(raw, &s); err != nil || s.ClaudePct == nil {
		return GovernorPanel{}
	}
	pct := *s.ClaudePct

	burn, risk := budget.RiskFromHistory(s.History)
	return GovernorPanel{
		Known:         true,
		Pct:           pct,
		Mode:          budget.ModeFor(pct).String(),
		EffectiveMode: budget.EffectiveMode(pct, risk).String(),
		BurnRatePct:   burn,
		Risk:          risk,
		Observations:  len(s.History),
		HorizonObs:    int(budget.RiskHorizon),
	}
}

func trustPanel() TrustPanel {
	runs, err := trust.LoadRuns(trust.DefaultLogPath())
	if err != nil {
		return TrustPanel{FixedSwarmN: TrustFixedSwarmBaseline}
	}
	s := trust.Aggregate(runs, TrustFixedSwarmBaseline)
	return TrustPanel{
		Runs:            s.Runs,
		MeanSamples:     s.MeanSamples,
		FixedSwarmN:     s.FixedSwarmN,
		SamplesSavedPct: s.SamplesSavedPct,
		AutoClearedPct:  s.AutoClearedPct,
		MeanTargetConf:  s.MeanTargetConf,
		MeanFinalConf:   s.MeanFinalConf,
		TotalCostUSD:    s.TotalCostUSD,
	}
}

// calibrationPanel loads the same on-disk calibration store `hyctl trust
// calibration` reads (trust.New(trust.DefaultPath())) and reports it in the
// same most-diagnostic-first order (Calibrator.Report). A brand-new machine
// has no ~/.hydra/calibration.jsonl, which trust.New treats as an empty store
// rather than an error, so this always returns a non-nil, possibly-empty
// slice — never an error, never nil.
func calibrationPanel() []CalibrationRow {
	cal, err := trust.New(trust.DefaultPath())
	if err != nil {
		return []CalibrationRow{}
	}
	stats := cal.Report()
	out := make([]CalibrationRow, 0, len(stats))
	for _, s := range stats {
		out = append(out, CalibrationRow{
			Source: s.Source,
			Domain: s.Domain,
			D:      s.D,
			Se:     s.Se,
			Sp:     s.Sp,
			N:      int(s.N),
		})
	}
	return out
}

func toBreakdowns(gs []cost.GroupRow) []Breakdown {
	out := make([]Breakdown, 0, len(gs))
	for _, g := range gs {
		out = append(out, Breakdown{
			Key:            g.Key,
			Calls:          g.Calls,
			PromptTokens:   g.PromptTokens,
			ResponseTokens: g.ResponseTokens,
			CostUSD:        g.EstCostUSD,
			WallMS:         g.WallMS,
		})
	}
	return out
}

func toRecent(rows []cost.Row) []RecentCall {
	out := make([]RecentCall, 0, len(rows))
	for _, r := range rows {
		out = append(out, RecentCall{
			TS: r.TS, Model: r.Model, Tier: r.Tier,
			CostUSD: r.EstCostUSD, WallMS: r.WallMS,
			RunID: r.RunID, TaskID: r.TaskID,
		})
	}
	return out
}
