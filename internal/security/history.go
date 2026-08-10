// SPDX-License-Identifier: MIT

package security

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// scoreEntry is one line of ~/.hydra/security_score.jsonl — one hyctl
// security run's coverage snapshot.
type scoreEntry struct {
	TS             string   `json:"ts"`
	PercentCovered float64  `json:"percentCovered"`
	Applicable     int      `json:"applicable"`
	Covered        int      `json:"covered"`
	Gaps           []string `json:"gaps"`
}

// Trend compares the current run's coverage against the very first recorded
// run — "since you started tracking" rather than an arbitrary lookback
// window, so it needs no calendar-boundary logic and is never misleading
// about how far apart the two points actually are.
type Trend struct {
	Available bool    `json:"available"`
	DeltaPct  float64 `json:"deltaPct"`
	FirstPct  float64 `json:"firstPct"`
	FirstTS   string  `json:"firstTs"`
}

// DefaultScoreHistoryPath is where the coverage trend is persisted.
func DefaultScoreHistoryPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".hydra", "security_score.jsonl")
}

// loadScoreHistory reads all entries; a missing file yields none.
// Unparseable lines are skipped — a corrupt trend log must never fail
// `hyctl security` itself.
func loadScoreHistory(path string) []scoreEntry {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var out []scoreEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e scoreEntry
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		out = append(out, e)
	}
	return out
}

// appendScoreHistory records this run's coverage, best-effort — a failure to
// persist the trend must never fail the report it would have fed.
func appendScoreHistory(path string, cov Coverage) {
	gaps := make([]string, 0)
	for _, c := range cov.Categories {
		if c.Status == Gap {
			gaps = append(gaps, c.ID)
		}
	}
	entry := scoreEntry{
		TS:             time.Now().UTC().Format(time.RFC3339),
		PercentCovered: cov.PercentCovered,
		Applicable:     cov.Applicable,
		Covered:        cov.Covered,
		Gaps:           gaps,
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintln(f, string(raw))
}

// buildTrend compares cov against the earliest entry read *before* this run
// was recorded — prior must be captured before appendScoreHistory runs, or
// the "first" run would compare against itself.
func buildTrend(prior []scoreEntry, cov Coverage) Trend {
	if len(prior) == 0 {
		return Trend{}
	}
	first := prior[0]
	return Trend{
		Available: true,
		DeltaPct:  cov.PercentCovered - first.PercentCovered,
		FirstPct:  first.PercentCovered,
		FirstTS:   first.TS,
	}
}
