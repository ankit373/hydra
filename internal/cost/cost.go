// SPDX-License-Identifier: MIT

// Package cost reads ~/.hydra/logs/cost.jsonl and produces spend summaries.
// It is the Go port of dispatch/cost.sh.
package cost

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ankit373/hydra/internal/config"
)

// ErrNoLog reports that cost.jsonl does not exist — nothing has dispatched yet.
// It is distinct from a read failure so a caller can tell "never ran" (render an
// empty state) from "cannot read" (surface the error). Match it with errors.Is.
var ErrNoLog = errors.New("no cost log")

// Row is one entry in cost.jsonl.
type Row struct {
	TS             string  `json:"ts"`
	Tier           int     `json:"tier"`
	Enum           string  `json:"enum"`
	Model          string  `json:"model"`
	Executor       string  `json:"executor"`
	Pool           string  `json:"pool"`
	PromptTokens   int     `json:"prompt_tokens"`
	ResponseTokens int     `json:"response_tokens"`
	EstCostUSD     float64 `json:"est_cost_usd"`
	WallMS         int64   `json:"wall_ms"`
	Source         string  `json:"source"`        // legacy, mirrors tokens_source
	TokensSource   string  `json:"tokens_source"` // "actual" (provider) or "estimated" (agy char/4)
	CostSource     string  `json:"cost_source"`   // always "estimated" — cost is derived, never billed
	TaskID         string  `json:"task_id"`
	RunID          string  `json:"run_id"`
	SwarmMode      string  `json:"swarm_mode"`
	SwarmWinner    bool    `json:"swarm_winner"`
	Config         string  `json:"config,omitempty"` // deployment-identity breadcrumb (config.Breadcrumb)
}

// Totals is an aggregate summary.
type Totals struct {
	Calls          int     `json:"calls"`
	PromptTokens   int     `json:"prompt_tokens"`
	ResponseTokens int     `json:"response_tokens"`
	EstCostUSD     float64 `json:"est_cost_usd"`
	WallSeconds    int64   `json:"wall_seconds"`
}

// GroupRow is one row in a by-X breakdown.
type GroupRow struct {
	Key            string  `json:"key"`
	Calls          int     `json:"calls"`
	PromptTokens   int     `json:"prompt_tokens"`
	ResponseTokens int     `json:"response_tokens"`
	EstCostUSD     float64 `json:"est_cost_usd"`
	WallMS         int64   `json:"wall_ms"`
}

// SummaryResult is the output of Summary().
type SummaryResult struct {
	Today   Totals `json:"today"`
	AllTime Totals `json:"all_time"`
	Recent  []Row  `json:"recent"`
	// Token-source share (all-time), counting prompt+response tokens.
	ActualTokens    int `json:"actual_tokens"`
	EstimatedTokens int `json:"estimated_tokens"`
}

// SourceLabels returns the cost.jsonl provenance labels for a token count that
// was either reported by the provider (estimated=false) or estimated by Hydra
// (estimated=true, e.g. agy's char/4). It is the single source of truth for
// these labels so the dispatch and swarm log paths cannot drift.
//   - tokensSource: "actual" | "estimated"
//   - costSource:   always "estimated" (est_cost_usd is pricing × tokens, never billed)
//   - legacySource: "real" | "estimate" — mirrors tokensSource for older readers
func SourceLabels(estimated bool) (tokensSource, costSource, legacySource string) {
	if estimated {
		return "estimated", "estimated", "estimate"
	}
	return "actual", "estimated", "real"
}

// tokensEstimated reports whether a row's tokens were estimated rather than
// reported by the provider. New rows carry tokens_source; legacy rows fall
// back to the old `source` field ("estimate" → estimated).
func tokensEstimated(r Row) bool {
	switch r.TokensSource {
	case "estimated":
		return true
	case "actual":
		return false
	}
	return r.Source == "estimate"
}

// TokenSourceShare returns total actual vs estimated tokens (prompt+response).
func TokenSourceShare(rows []Row) (actual, estimated int) {
	for _, r := range rows {
		n := r.PromptTokens + r.ResponseTokens
		if tokensEstimated(r) {
			estimated += n
		} else {
			actual += n
		}
	}
	return actual, estimated
}

// LoadAll reads all rows from cost.jsonl.
func LoadAll() ([]Row, error) {
	return loadRows(costLogPath())
}

// Summary returns today + all-time totals and last 5 recent rows.
func Summary() (*SummaryResult, error) {
	all, err := LoadAll()
	if err != nil {
		return nil, err
	}

	todayStr := time.Now().UTC().Format("2006-01-02")
	var todayRows []Row
	for _, r := range all {
		if strings.HasPrefix(r.TS, todayStr) {
			todayRows = append(todayRows, r)
		}
	}

	recent := all
	if len(recent) > 5 {
		recent = recent[len(recent)-5:]
	}
	// Reverse so newest first.
	for i, j := 0, len(recent)-1; i < j; i, j = i+1, j-1 {
		recent[i], recent[j] = recent[j], recent[i]
	}

	actualTok, estTok := TokenSourceShare(all)
	return &SummaryResult{
		Today:           aggregate(todayRows),
		AllTime:         aggregate(all),
		Recent:          recent,
		ActualTokens:    actualTok,
		EstimatedTokens: estTok,
	}, nil
}

// Today returns today's per-tier breakdown.
func Today() ([]GroupRow, error) {
	all, err := LoadAll()
	if err != nil {
		return nil, err
	}
	todayStr := time.Now().UTC().Format("2006-01-02")
	var today []Row
	for _, r := range all {
		if strings.HasPrefix(r.TS, todayStr) {
			today = append(today, r)
		}
	}
	return groupBy(today, func(r Row) string { return fmt.Sprintf("%d", r.Tier) }), nil
}

// All returns all-time per-tier breakdown.
func All() ([]GroupRow, error) {
	all, err := LoadAll()
	if err != nil {
		return nil, err
	}
	return groupBy(all, func(r Row) string { return fmt.Sprintf("%d", r.Tier) }), nil
}

// ByPool returns per-pool totals.
func ByPool() ([]GroupRow, error) {
	all, err := LoadAll()
	if err != nil {
		return nil, err
	}
	return groupBy(all, func(r Row) string {
		if r.Pool == "" {
			return "unknown"
		}
		return r.Pool
	}), nil
}

// ByTask returns totals for a specific task_id.
func ByTask(taskID string) (*Totals, error) {
	all, err := LoadAll()
	if err != nil {
		return nil, err
	}
	var rows []Row
	for _, r := range all {
		if r.TaskID == taskID {
			rows = append(rows, r)
		}
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("no calls for task_id=%s", taskID)
	}
	t := aggregate(rows)
	return &t, nil
}

// ByRunResult is the output of ByRun.
type ByRunResult struct {
	Totals Totals     `json:"totals"`
	ByTier []GroupRow `json:"by_tier"`
}

// ByRun returns totals and per-tier breakdown for a specific run_id.
func ByRun(runID string) (*ByRunResult, error) {
	all, err := LoadAll()
	if err != nil {
		return nil, err
	}
	var rows []Row
	for _, r := range all {
		if r.RunID == runID {
			rows = append(rows, r)
		}
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("no calls for run_id=%s", runID)
	}
	return &ByRunResult{
		Totals: aggregate(rows),
		ByTier: groupBy(rows, func(r Row) string { return fmt.Sprintf("%d", r.Tier) }),
	}, nil
}

// Tail returns the last N rows, newest first, via a backward tail-seek rather
// than loading the whole log.
func Tail(n int) ([]Row, error) {
	if n <= 0 {
		return LoadAll()
	}
	lines, err := tailLines(costLogPath(), n)
	if err != nil {
		return nil, err
	}
	rows := make([]Row, 0, len(lines))
	for _, line := range lines {
		var r Row
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			continue // skip malformed rows, same tolerance as loadRows
		}
		rows = append(rows, r)
	}
	// lines is oldest-first; reverse so the newest row is first.
	out := make([]Row, len(rows))
	for i, r := range rows {
		out[len(rows)-1-i] = r
	}
	return out, nil
}

// JSON returns raw rows, optionally filtered by since (RFC3339 timestamp).
// Uses time.Parse for comparison so timezone offsets are handled correctly.
func JSON(since string) ([]Row, error) {
	all, err := LoadAll()
	if err != nil {
		return nil, err
	}
	if since == "" {
		return all, nil
	}
	sinceT, err := time.Parse(time.RFC3339, since)
	if err != nil {
		return nil, fmt.Errorf("cost: invalid since timestamp %q: %w", since, err)
	}
	var out []Row
	for _, r := range all {
		t, err := time.Parse(time.RFC3339, r.TS)
		if err != nil {
			continue
		}
		if !t.Before(sinceT) {
			out = append(out, r)
		}
	}
	return out, nil
}

// FilterDays returns rows from the last n calendar days (UTC). n=0 returns all.
func FilterDays(rows []Row, n int) []Row {
	if n <= 0 {
		return rows
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -n).Format("2006-01-02")
	var out []Row
	for _, r := range rows {
		if len(r.TS) >= 10 && r.TS[:10] >= cutoff {
			out = append(out, r)
		}
	}
	return out
}

// ByModel returns per-model totals sorted by cost descending.
func ByModel(rows []Row) []GroupRow {
	return groupBy(rows, func(r Row) string {
		if r.Model == "" {
			return "unknown"
		}
		return r.Model
	})
}

// ByDay returns per-day totals sorted by date ascending.
func ByDay(rows []Row) []GroupRow {
	groups := groupBy(rows, func(r Row) string {
		if len(r.TS) >= 10 {
			return r.TS[:10]
		}
		return "unknown"
	})
	sort.Slice(groups, func(i, j int) bool { return groups[i].Key < groups[j].Key })
	return groups
}

// SwarmSummary holds swarm-specific aggregate stats.
type SwarmSummary struct {
	Runs       int            `json:"runs"`
	WinnerRate float64        `json:"winner_rate"` // fraction 0-1
	AvgWallMS  int64          `json:"avg_wall_ms"`
	TotalCost  float64        `json:"total_cost_usd"`
	ByMode     map[string]int `json:"by_mode"`
}

// SwarmStats returns aggregated stats for swarm-only rows.
func SwarmStats(rows []Row) SwarmSummary {
	var swarmRows []Row
	for _, r := range rows {
		if r.SwarmMode != "" {
			swarmRows = append(swarmRows, r)
		}
	}
	s := SwarmSummary{ByMode: map[string]int{}}
	s.Runs = len(swarmRows)
	if s.Runs == 0 {
		return s
	}
	winners := 0
	var totalWall int64
	for _, r := range swarmRows {
		if r.SwarmWinner {
			winners++
		}
		totalWall += r.WallMS
		s.TotalCost += r.EstCostUSD
		if r.SwarmMode != "" {
			s.ByMode[r.SwarmMode]++
		}
	}
	s.WinnerRate = float64(winners) / float64(s.Runs)
	s.AvgWallMS = totalWall / int64(s.Runs)
	s.TotalCost = math.Round(s.TotalCost*1_000_000) / 1_000_000
	return s
}

// ── Rendering ─────────────────────────────────────────────────────────────────

// RenderSummary prints a human-readable summary.
func RenderSummary(r *SummaryResult) {
	today := time.Now().UTC().Format("2006-01-02")
	fmt.Println()
	fmt.Println("  Hydra cost summary")
	fmt.Println("  ═════════════════════════════════════════════════════════════")
	fmt.Println()
	fmt.Printf("  Today (%s)\n", today)
	renderTotals(r.Today, "    ")
	fmt.Println()
	fmt.Println("  All time")
	renderTotals(r.AllTime, "    ")
	if tot := r.ActualTokens + r.EstimatedTokens; tot > 0 {
		estPct := float64(r.EstimatedTokens) * 100 / float64(tot)
		fmt.Printf("    token source   %.0f%% actual · %.0f%% estimated (agy char/4)\n",
			100-estPct, estPct)
	}
	fmt.Println("    (cost is estimated: pricing × tokens, not billed)")
	fmt.Println()
	fmt.Println("  Recent (last 5):")
	for _, row := range r.Recent {
		fmt.Printf("  %s  %s/%d  %s — %d+%d tok, $%.6f, %dms\n",
			row.TS, row.Enum, row.Tier, row.Model,
			row.PromptTokens, row.ResponseTokens, row.EstCostUSD, row.WallMS)
	}
	fmt.Println()
}

// RenderTable prints a human-readable breakdown table.
func RenderTable(title string, rows []GroupRow) {
	fmt.Println()
	fmt.Printf("  %s\n", title)
	fmt.Println("  ─────────────────────────────────────────────────────────────")
	fmt.Printf("  %-18s %6s %10s %10s %10s %8s\n", "key", "calls", "tok_in", "tok_out", "$", "wall_s")
	for _, r := range rows {
		fmt.Printf("  %-18s %6d %10d %10d %10.6f %8d\n",
			r.Key, r.Calls, r.PromptTokens, r.ResponseTokens,
			r.EstCostUSD, r.WallMS/1000)
	}
	fmt.Println()
}

// RenderStatsTable prints the polished stats-command table with comma-formatted numbers.
func RenderStatsTable(period string, rows []GroupRow) {
	sep := strings.Repeat("─", 71)
	fmt.Printf("\nPeriod: %s\n\n", period)
	fmt.Printf("  %-32s  %6s  %9s  %9s  %10s\n", "Model / Key", "Calls", "In Tok", "Out Tok", "Cost USD")
	fmt.Println("  " + sep)
	var totCalls, totIn, totOut int
	var totCost float64
	for _, r := range rows {
		fmt.Printf("  %-32s  %6s  %9s  %9s  %10s\n",
			truncLabel(r.Key, 32),
			commaInt(r.Calls),
			commaInt(r.PromptTokens),
			commaInt(r.ResponseTokens),
			fmt.Sprintf("$%.3f", r.EstCostUSD),
		)
		totCalls += r.Calls
		totIn += r.PromptTokens
		totOut += r.ResponseTokens
		totCost += r.EstCostUSD
	}
	fmt.Println("  " + sep)
	fmt.Printf("  %-32s  %6s  %9s  %9s  %10s\n",
		"Total",
		commaInt(totCalls),
		commaInt(totIn),
		commaInt(totOut),
		fmt.Sprintf("$%.3f", totCost),
	)
	fmt.Println()
}

// RenderSwarmStats prints the swarm-specific summary.
func RenderSwarmStats(s SwarmSummary) {
	fmt.Printf("\nSwarm runs: %d  Winner rate: %.0f%%  Avg wall time: %.1fs  Total: $%.4f\n",
		s.Runs, s.WinnerRate*100, float64(s.AvgWallMS)/1000, s.TotalCost)
	if len(s.ByMode) > 0 {
		// Sorted, because Go randomises map iteration: `hyctl stats` printed the
		// modes in a different order on every run, so two invocations on
		// identical data disagreed and the output could not be diffed or
		// scripted against. Found by the golden test, which is the one kind of
		// test that notices.
		modes := make([]string, 0, len(s.ByMode))
		for mode := range s.ByMode {
			modes = append(modes, mode)
		}
		sort.Strings(modes)

		fmt.Print("Modes:")
		for _, mode := range modes {
			fmt.Printf("  %s=%d", mode, s.ByMode[mode])
		}
		fmt.Println()
	}
	fmt.Println()
}

func commaInt(n int) string {
	s := fmt.Sprintf("%d", n)
	if n < 1000 {
		return s
	}
	result := ""
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			result += ","
		}
		result += string(c)
	}
	return result
}

func truncLabel(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

// RenderTail prints human-readable tail rows.
func RenderTail(rows []Row) {
	for _, r := range rows {
		enum := r.Enum
		if enum == "" {
			enum = "?"
		}
		fmt.Printf("  %s  %s/%d  %s — %d+%d tok, $%.6f, %dms\n",
			r.TS, enum, r.Tier, r.Model,
			r.PromptTokens, r.ResponseTokens, r.EstCostUSD, r.WallMS)
	}
}

// ── Internal helpers ──────────────────────────────────────────────────────────

func costLogPath() string {
	return filepath.Join(config.Dir(), "logs", "cost.jsonl")
}

func loadRows(path string) ([]Row, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w at %s — has anything dispatched yet?", ErrNoLog, path)
		}
		return nil, err
	}
	defer f.Close()

	var rows []Row
	scanner := bufio.NewScanner(f)
	// Match every other jsonl reader here (ledger, runlog, trust): a row with a
	// long prompt preview would otherwise exceed the 64 KiB default and abort
	// the whole report, since Err() is returned below.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var r Row
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			continue // skip malformed rows
		}
		rows = append(rows, r)
	}
	return rows, scanner.Err()
}

// tailLines returns the last n non-empty lines, oldest first, reading
// backward from EOF in chunks (the tail -n algorithm) instead of the whole file.
func tailLines(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w at %s — has anything dispatched yet?", ErrNoLog, path)
		}
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}

	const chunkSize = 64 * 1024
	var buf []byte
	pos := info.Size()
	for pos > 0 && bytes.Count(buf, []byte("\n")) <= n {
		readSize := int64(chunkSize)
		if readSize > pos {
			readSize = pos
		}
		pos -= readSize
		chunk := make([]byte, readSize)
		if _, err := f.ReadAt(chunk, pos); err != nil {
			return nil, err
		}
		buf = append(chunk, buf...)
	}

	var lines []string
	for _, l := range strings.Split(string(buf), "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}

func aggregate(rows []Row) Totals {
	var t Totals
	t.Calls = len(rows)
	for _, r := range rows {
		t.PromptTokens += r.PromptTokens
		t.ResponseTokens += r.ResponseTokens
		t.EstCostUSD += r.EstCostUSD
		t.WallSeconds += r.WallMS / 1000
	}
	// Round to 6 decimal places.
	t.EstCostUSD = math.Round(t.EstCostUSD*1_000_000) / 1_000_000
	return t
}

// GroupBy aggregates rows using an arbitrary key function, sorted by cost descending.
func GroupBy(rows []Row, key func(Row) string) []GroupRow { return groupBy(rows, key) }

func groupBy(rows []Row, key func(Row) string) []GroupRow {
	m := map[string]*GroupRow{}
	for _, r := range rows {
		k := key(r)
		if _, ok := m[k]; !ok {
			m[k] = &GroupRow{Key: k}
		}
		g := m[k]
		g.Calls++
		g.PromptTokens += r.PromptTokens
		g.ResponseTokens += r.ResponseTokens
		g.EstCostUSD += r.EstCostUSD
		g.WallMS += r.WallMS
	}
	result := make([]GroupRow, 0, len(m))
	for _, g := range m {
		g.EstCostUSD = math.Round(g.EstCostUSD*1_000_000) / 1_000_000
		result = append(result, *g)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].EstCostUSD > result[j].EstCostUSD
	})
	return result
}

func renderTotals(t Totals, indent string) {
	fmt.Printf("%scalls          %d\n", indent, t.Calls)
	fmt.Printf("%stokens in/out  %d / %d\n", indent, t.PromptTokens, t.ResponseTokens)
	fmt.Printf("%sest cost       $%.6f\n", indent, t.EstCostUSD)
	fmt.Printf("%swall time      %ds\n", indent, t.WallSeconds)
}
