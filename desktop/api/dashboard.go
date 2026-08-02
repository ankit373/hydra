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
// Every figure routes through the same cost/trust calls the CLI makes —
// cost.Summary for the headline, cost.ByModel/ByDay/GroupBy for the
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
		HasData:  len(rows) > 0,
		Governor: governorPanel(),
		Trust:    trustPanel(),
	}
	if !d.HasData {
		// Breakdowns stay nil, not empty slices: the frontend distinguishes
		// "nothing has run" from "ran, produced nothing".
		return d, nil
	}

	sum, err := cost.Summary()
	if err != nil {
		return nil, err
	}
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
	var s struct {
		ClaudePct *int `json:"claude_pct"`
	}
	if err := json.Unmarshal(raw, &s); err != nil || s.ClaudePct == nil {
		return GovernorPanel{}
	}
	pct := *s.ClaudePct
	return GovernorPanel{Known: true, Pct: pct, Mode: budget.ModeFor(pct).String()}
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
