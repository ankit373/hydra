// SPDX-License-Identifier: MIT

// Package rollup turns raw dispatch rows into per-day aggregates.
//
// Reading a whole log to answer "what did today cost" is the mistake Canopy
// names: features belong at ingest, not at query. The cockpit currently
// rescans cost.jsonl on every render, which gets slower forever.
//
// A rollup is also what survives retention. Deleting raw rows without first
// folding them into an aggregate throws away the history the calibration and
// cost trends are built from, which is exactly what self-hosted Langfuse does.
// Roll up first, then delete.
package rollup

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/cost"
	"github.com/ankit373/hydra/internal/sketch"
)

// SchemaVersion is stamped on every row so a reader can branch rather than guess.
const SchemaVersion = 1

// Key identifies one aggregate. Deliberately coarse: a finer key produces more
// rows than the raw log it replaces.
type Key struct {
	Date     string `json:"date"` // YYYY-MM-DD, UTC
	Model    string `json:"model"`
	Executor string `json:"executor"`
	Enum     string `json:"enum"`
	Tier     int    `json:"tier"`
}

// Row is one day's aggregate for one Key.
type Row struct {
	V int `json:"v"`
	Key

	Calls          int64   `json:"calls"`
	PromptTokens   int64   `json:"prompt_tokens"`
	ResponseTokens int64   `json:"response_tokens"`
	EstCostUSD     float64 `json:"est_cost_usd"`

	// Latency is a mergeable sketch rather than a mean: an average latency
	// answers no question anyone asks, and keeping raw values to compute p99
	// is what this package exists to avoid.
	Latency *sketch.Sketch `json:"latency"`

	// ActProbSum lets a reader weight this aggregate by inverse propensity
	// later. Summed rather than averaged because the sum is what an estimator
	// needs and an average of probabilities is not recoverable from it.
	ActProbSum  float64 `json:"act_prob_sum"`
	KeepProbSum float64 `json:"keep_prob_sum"`
	Explored    int64   `json:"explored"` // rows where the router did not take its top choice
}

// DefaultPath is where rollups live.
func DefaultPath() string { return filepath.Join(config.Dir(), "logs", "rollups.jsonl") }

// Build aggregates raw cost rows into per-day rows, newest date last.
func Build(rows []cost.Row) []Row {
	acc := map[Key]*Row{}
	for _, r := range rows {
		day := dateOf(r.TS)
		if day == "" {
			continue // an unparseable timestamp cannot be attributed to a day
		}
		k := Key{Date: day, Model: r.Model, Executor: r.Executor, Enum: r.Enum, Tier: r.Tier}
		a := acc[k]
		if a == nil {
			a = &Row{V: SchemaVersion, Key: k, Latency: sketch.New(sketch.DefaultAlpha)}
			acc[k] = a
		}
		a.Calls++
		a.PromptTokens += int64(r.PromptTokens)
		a.ResponseTokens += int64(r.ResponseTokens)
		a.EstCostUSD += r.EstCostUSD
		a.Latency.Add(float64(r.WallMS))
		a.ActProbSum += r.ActProb
		a.KeepProbSum += r.KeepProb
		// A propensity below 1 means the router took something other than its
		// top choice. Rows written before propensity existed record 0 and are
		// not evidence of exploration, so they are excluded.
		if r.ActProb > 0 && r.ActProb < 1 {
			a.Explored++
		}
	}
	out := make([]Row, 0, len(acc))
	for _, v := range acc {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date < out[j].Date
		}
		return out[i].Model < out[j].Model
	})
	return out
}

// Merge folds b into a, combining sketches. Used to reconcile a freshly built
// day against one already on disk, and to combine rollups from two machines.
func Merge(a, b []Row) ([]Row, error) {
	acc := map[Key]*Row{}
	for _, src := range [][]Row{a, b} {
		for i := range src {
			r := src[i]
			cur := acc[r.Key]
			if cur == nil {
				c := r
				if c.Latency != nil {
					c.Latency = c.Latency.Clone()
				}
				acc[r.Key] = &c
				continue
			}
			cur.Calls += r.Calls
			cur.PromptTokens += r.PromptTokens
			cur.ResponseTokens += r.ResponseTokens
			cur.EstCostUSD += r.EstCostUSD
			cur.ActProbSum += r.ActProbSum
			cur.KeepProbSum += r.KeepProbSum
			cur.Explored += r.Explored
			if cur.Latency == nil {
				cur.Latency = r.Latency
			} else if err := cur.Latency.Merge(r.Latency); err != nil {
				return nil, fmt.Errorf("rollup %v: %w", r.Key, err)
			}
		}
	}
	out := make([]Row, 0, len(acc))
	for _, v := range acc {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date < out[j].Date
		}
		return out[i].Model < out[j].Model
	})
	return out, nil
}

// Load reads rollups. A missing file is not an error — nothing has rolled up yet.
func Load(path string) ([]Row, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []Row
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r Row
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			continue // a torn tail must not hide every good row before it
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

// Save writes rows atomically. A rollup file is rewritten whole rather than
// appended because a day's aggregate changes as that day accumulates; a
// half-written replacement would lose history the raw log may no longer hold.
func Save(path string, rows []Row) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, r := range rows {
		raw, err := json.Marshal(r)
		if err != nil {
			f.Close()
			return err
		}
		if _, err := fmt.Fprintln(w, string(raw)); err != nil {
			f.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Refresh rebuilds rollups from the raw cost log and writes them.
//
// Complete days are immutable once written, so this could be incremental; it
// is not, because correctness matters more than speed at these sizes and a
// full rebuild cannot drift from its source. Phase 3 makes it incremental when
// segments seal.
func Refresh(costPath, rollupPath string) ([]Row, error) {
	raw, err := cost.LoadRows(costPath)
	if err != nil {
		return nil, err
	}
	rows := Build(raw)
	if err := Save(rollupPath, rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// dateOf extracts the UTC day from a row timestamp.
func dateOf(ts string) string {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, ts); err == nil {
			return t.UTC().Format("2006-01-02")
		}
	}
	return ""
}
