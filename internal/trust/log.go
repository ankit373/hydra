// SPDX-License-Identifier: MIT

package trust

import (
	"bufio"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RunLog is one persisted SPRT run — the data the "By The Numbers" page
// graduates from [MODEL] to [MEASURED], and what `hydra trust stats/explain` read.
type RunLog struct {
	TS         string     `json:"ts"`
	TaskHash   string     `json:"task_hash"`
	Domain     string     `json:"domain"`
	TargetConf float64    `json:"target_conf"`
	FinalConf  float64    `json:"final_conf"`
	Samples    int        `json:"samples"`
	Models     []string   `json:"models"`
	CostUSD    float64    `json:"cost_usd"`
	CostSource string     `json:"cost_source"` // from cost.SourceLabels
	Decision   string     `json:"decision"`    // accept | stopped_on_budget
	Ledger     []Evidence `json:"ledger,omitempty"`
}

// DefaultLogPath is where SPRT runs are persisted (~/.hydra/trust.jsonl).
func DefaultLogPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".hydra", "trust.jsonl")
}

// TaskHash is a short stable identifier for a prompt, used to correlate a run
// with `hydra trust explain <task_hash>`.
func TaskHash(prompt string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(prompt))
	return fmt.Sprintf("%08x", h.Sum32())
}

// LogRun appends one run to the trust log, stamping TS if the caller left it blank.
func LogRun(path string, r RunLog) error {
	if r.TS == "" {
		r.TS = time.Now().UTC().Format(time.RFC3339)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(f, string(raw))
	return err
}

// LoadRuns reads all runs from the trust log. A missing file yields no runs.
func LoadRuns(path string) ([]RunLog, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var runs []RunLog
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var r RunLog
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			continue
		}
		runs = append(runs, r)
	}
	return runs, scanner.Err()
}

// Stats is the aggregate view produced by `hydra trust stats`.
type Stats struct {
	Runs            int     `json:"runs"`
	MeanSamples     float64 `json:"mean_samples"`
	FixedSwarmN     int     `json:"fixed_swarm_n"` // baseline for comparison
	SamplesSavedPct float64 `json:"samples_saved_pct"`
	AutoClearedPct  float64 `json:"auto_cleared_pct"` // reached accept without a human
	MeanTargetConf  float64 `json:"mean_target_conf"`
	MeanFinalConf   float64 `json:"mean_final_conf"`
	TotalCostUSD    float64 `json:"total_cost_usd"`
}

// Aggregate summarizes a set of runs against a fixed-N swarm baseline.
func Aggregate(runs []RunLog, fixedN int) Stats {
	s := Stats{FixedSwarmN: fixedN}
	s.Runs = len(runs)
	if s.Runs == 0 {
		return s
	}
	var totSamples, accepted int
	var totTarget, totFinal, totCost float64
	for _, r := range runs {
		totSamples += r.Samples
		totTarget += r.TargetConf
		totFinal += r.FinalConf
		totCost += r.CostUSD
		if r.Decision == DecisionAccept.String() {
			accepted++
		}
	}
	n := float64(s.Runs)
	s.MeanSamples = float64(totSamples) / n
	s.MeanTargetConf = totTarget / n
	s.MeanFinalConf = totFinal / n
	s.TotalCostUSD = totCost
	s.AutoClearedPct = 100 * float64(accepted) / n
	if fixedN > 0 {
		s.SamplesSavedPct = 100 * (1 - s.MeanSamples/float64(fixedN))
	}
	return s
}
