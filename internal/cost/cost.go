// Package cost reads ~/.hydra/logs/cost.jsonl and produces spend summaries.
// It is the Go port of dispatch/cost.sh.
package cost

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ankit373/hydra/internal/config"
)

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
	Source         string  `json:"source"`
	TaskID         string  `json:"task_id"`
	RunID          string  `json:"run_id"`
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
	Today   Totals  `json:"today"`
	AllTime Totals  `json:"all_time"`
	Recent  []Row   `json:"recent"`
}

// LoadAll reads all rows from cost.jsonl.
func LoadAll() ([]Row, error) {
	path := costLogPath()
	return loadRows(path, "")
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

	return &SummaryResult{
		Today:   aggregate(todayRows),
		AllTime: aggregate(all),
		Recent:  recent,
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
	Totals  Totals     `json:"totals"`
	ByTier  []GroupRow `json:"by_tier"`
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

// Tail returns the last N rows.
func Tail(n int) ([]Row, error) {
	all, err := LoadAll()
	if err != nil {
		return nil, err
	}
	if n <= 0 || n > len(all) {
		n = len(all)
	}
	rows := all[len(all)-n:]
	// Reverse so newest first.
	out := make([]Row, len(rows))
	for i, r := range rows {
		out[len(rows)-1-i] = r
	}
	return out, nil
}

// JSON returns raw rows, optionally filtered by since (ISO timestamp prefix).
func JSON(since string) ([]Row, error) {
	if since == "" {
		return LoadAll()
	}
	all, err := LoadAll()
	if err != nil {
		return nil, err
	}
	var out []Row
	for _, r := range all {
		if r.TS >= since {
			out = append(out, r)
		}
	}
	return out, nil
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

func loadRows(path, since string) ([]Row, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no cost log at %s — has anything dispatched yet?", path)
		}
		return nil, err
	}
	defer f.Close()

	var rows []Row
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var r Row
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			continue // skip malformed rows
		}
		if since != "" && r.TS < since {
			continue
		}
		rows = append(rows, r)
	}
	return rows, scanner.Err()
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
