// SPDX-License-Identifier: MIT

package swarm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// readCostRows redirects config.Dir() at a temp HOME, runs fn, and returns every
// cost.jsonl row written during it.
func readCostRows(t *testing.T, fn func()) []map[string]any {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // config.Dir() uses os.UserHomeDir()

	fn()

	raw, err := os.ReadFile(filepath.Join(home, ".hydra", "logs", "cost.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var rows []map[string]any
	for _, line := range splitLines(string(raw)) {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("unparseable cost row %q: %v", line, err)
		}
		rows = append(rows, m)
	}
	return rows
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func costAttempt(id string, status HeadStatus, rank int) Attempt {
	return Attempt{
		Head:         registryHead(id, id, 80),
		Status:       status,
		Rank:         rank,
		InputTokens:  100,
		OutputTokens: 50,
		EstCostUSD:   0.02,
		FinishedAt:   time.Now(),
	}
}

// An SPRT run's sampled heads must reach cost.jsonl. Before #175, RunSPRT never
// called logAttempts, so the whole --confidence path was invisible to
// `hyctl cost` / `hyctl stats` — only the aggregate trust.jsonl row survived.
func TestLogAttempts_SPRTModeIsRecorded(t *testing.T) {
	attempts := []Attempt{
		costAttempt("a", StatusOK, 1),
		costAttempt("b", StatusOK, 0),
	}

	rows := readCostRows(t, func() {
		logAttempts(attempts, ModeSPRT, "explain this migration")
	})

	if len(rows) != 2 {
		t.Fatalf("wrote %d cost rows, want one per sampled head (2)", len(rows))
	}
	for _, r := range rows {
		if r["swarm_mode"] != string(ModeSPRT) {
			t.Errorf("swarm_mode = %v, want %q so SPRT spend is distinguishable from race/best/all",
				r["swarm_mode"], ModeSPRT)
		}
		// Provenance labels must match the dispatch path — see cost.SourceLabels.
		if r["cost_source"] != "estimated" {
			t.Errorf("cost_source = %v, want \"estimated\"", r["cost_source"])
		}
		if r["tokens_source"] == nil || r["tokens_source"] == "" {
			t.Error("tokens_source must be set")
		}
	}

	// Exactly the Rank==1 attempt is the winner.
	var winners int
	for _, r := range rows {
		if w, _ := r["swarm_winner"].(bool); w {
			winners++
		}
	}
	if winners != 1 {
		t.Errorf("swarm_winner true on %d rows, want exactly 1", winners)
	}
}

// Pending/Canceled heads never executed, so they must not be billed.
func TestLogAttempts_SkipsUnexecutedAttempts(t *testing.T) {
	attempts := []Attempt{
		costAttempt("ok", StatusOK, 1),
		costAttempt("pending", StatusPending, 0),
		costAttempt("canceled", StatusCanceled, 0),
		costAttempt("failed", StatusFailed, 0),
	}

	rows := readCostRows(t, func() {
		logAttempts(attempts, ModeRace, "p")
	})

	// OK + Failed both really ran; Pending/Canceled did not.
	if len(rows) != 2 {
		t.Fatalf("wrote %d rows, want 2 (OK + Failed only)", len(rows))
	}
	for _, r := range rows {
		if r["model"] == "pending" || r["model"] == "canceled" {
			t.Errorf("unexecuted attempt %v was billed", r["model"])
		}
	}
}

// The mode label must be whatever the caller passed — the signature change from
// *SwarmResult to (attempts, mode) is what lets SPRT share this writer at all.
func TestLogAttempts_ModeLabelIsCallerSupplied(t *testing.T) {
	for _, mode := range []SwarmMode{ModeRace, ModeBest, ModeAll, ModeSPRT} {
		rows := readCostRows(t, func() {
			logAttempts([]Attempt{costAttempt("h", StatusOK, 1)}, mode, "p")
		})
		if len(rows) != 1 {
			t.Fatalf("mode %s: wrote %d rows, want 1", mode, len(rows))
		}
		if rows[0]["swarm_mode"] != string(mode) {
			t.Errorf("swarm_mode = %v, want %q", rows[0]["swarm_mode"], mode)
		}
	}
}
