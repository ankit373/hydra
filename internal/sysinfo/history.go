// SPDX-License-Identifier: MIT

package sysinfo

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const (
	historyFile    = "memory_history.jsonl"
	historyDays    = 7
	minSamples     = 3         // need at least this many samples before trusting the average
	sampleInterval = time.Hour // don't write more than once per hour
)

type memorySample struct {
	TS      time.Time `json:"ts"`
	TotalGB float64   `json:"total_gb"`
	FreeGB  float64   `json:"free_gb"`
	WiredGB float64   `json:"wired_gb"`
}

// historyPath returns ~/.hydra/memory_history.jsonl, or "" if the home dir is unavailable.
func historyPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".hydra", historyFile)
}

// recordSample appends the current reading to the history file if enough time
// has passed since the last sample. Silently ignores write errors.
func recordSample(s *Specs) {
	path := historyPath()
	if path == "" {
		return
	}

	// Don't spam: skip if we wrote recently.
	if info, err := os.Stat(path); err == nil {
		if time.Since(info.ModTime()) < sampleInterval {
			return
		}
	}

	sample := memorySample{
		TS:      time.Now().UTC(),
		TotalGB: s.TotalRAMGB,
		FreeGB:  s.FreeRAMGB,
		WiredGB: s.WiredRAMGB,
	}
	raw, err := json.Marshal(sample)
	if err != nil {
		return
	}

	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, string(raw))
}

// loadRecentSamples reads the last historyDays worth of samples.
func loadRecentSamples() []memorySample {
	path := historyPath()
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	cutoff := time.Now().UTC().AddDate(0, 0, -historyDays)
	var samples []memorySample
	for _, line := range splitLines(data) {
		var s memorySample
		if err := json.Unmarshal([]byte(line), &s); err != nil {
			continue
		}
		if s.TS.After(cutoff) {
			samples = append(samples, s)
		}
	}
	return samples
}

// HistoricalStats summarises recent memory readings.
type HistoricalStats struct {
	Samples   int
	Days      int
	AvgFreeGB float64
	P75FreeGB float64 // 75th percentile — "typically this much is free"
	MinFreeGB float64
	MaxFreeGB float64
	Reliable  bool // true when we have enough samples to trust the average
}

// LoadHistory reads recent samples and computes summary statistics.
// Returns nil if no history exists yet.
func LoadHistory() *HistoricalStats {
	samples := loadRecentSamples()
	if len(samples) == 0 {
		return nil
	}

	frees := make([]float64, len(samples))
	for i, s := range samples {
		frees[i] = s.FreeGB
	}
	sort.Float64s(frees)

	n := float64(len(frees))
	sum := 0.0
	for _, v := range frees {
		sum += v
	}

	// Oldest and newest sample span
	days := int(time.Since(samples[0].TS).Hours()/24) + 1

	return &HistoricalStats{
		Samples:   len(samples),
		Days:      days,
		AvgFreeGB: sum / n,
		P75FreeGB: percentile(frees, 75),
		MinFreeGB: frees[0],
		MaxFreeGB: frees[len(frees)-1],
		Reliable:  len(samples) >= minSamples,
	}
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := (p / 100) * float64(len(sorted)-1)
	lo := int(idx)
	if lo >= len(sorted)-1 {
		return sorted[len(sorted)-1]
	}
	frac := idx - float64(lo)
	return sorted[lo]*(1-frac) + sorted[lo+1]*frac
}

func splitLines(data []byte) []string {
	var lines []string
	start := 0
	for i, b := range data {
		if b == '\n' {
			line := string(data[start:i])
			if line != "" {
				lines = append(lines, line)
			}
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, string(data[start:]))
	}
	return lines
}
