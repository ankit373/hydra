// SPDX-License-Identifier: MIT

package swarm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// costRows redirects config.Dir() at a temp HOME, runs fn, and returns the
// cost.jsonl rows written during it.
func costRows(t *testing.T, fn func()) []map[string]any {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	// Identity must come from Options, not ambient env.
	t.Setenv("HYDRA_RUN_ID", "")
	t.Setenv("HYDRA_TASK_ID", "")

	fn()

	raw, err := os.ReadFile(filepath.Join(home, ".hydra", "logs", "cost.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var rows []map[string]any
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
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

func idAttempt(id string) Attempt {
	return Attempt{
		Head:         registryHead(id, id, 80),
		Status:       StatusOK,
		InputTokens:  10,
		OutputTokens: 5,
		FinishedAt:   time.Now(),
	}
}

// Every head racing or voting on one prompt is working the same logical task,
// so all its rows must carry the same run_id AND task_id. Before #181 both were
// always "" and nothing could be grouped.
func TestLogAttempts_AllHeadsShareRunAndTaskID(t *testing.T) {
	result := &SwarmResult{
		Mode:     ModeBest,
		Attempts: []Attempt{idAttempt("a"), idAttempt("b"), idAttempt("c")},
	}
	opts := Options{RunID: "run-1", TaskID: "task-1"}

	rows := costRows(t, func() { logAttempts(result.Attempts, result.Mode, opts, "p") })

	if len(rows) != 3 {
		t.Fatalf("wrote %d rows, want 3", len(rows))
	}
	for i, r := range rows {
		if r["run_id"] != "run-1" {
			t.Errorf("row %d run_id = %v, want run-1", i, r["run_id"])
		}
		if r["task_id"] != "task-1" {
			t.Errorf("row %d task_id = %v, want task-1 (one task, many heads)", i, r["task_id"])
		}
	}
}

// With no explicit identity the rows must still be correlatable — the whole
// point is that run_id/task_id are never empty again.
func TestLogAttempts_DerivesIdentityWhenAbsent(t *testing.T) {
	result := &SwarmResult{
		Mode:     ModeRace,
		Attempts: []Attempt{idAttempt("a"), idAttempt("b")},
	}

	rows := costRows(t, func() { logAttempts(result.Attempts, result.Mode, Options{}, "p") })

	if len(rows) != 2 {
		t.Fatalf("wrote %d rows, want 2", len(rows))
	}
	runID, _ := rows[0]["run_id"].(string)
	taskID, _ := rows[0]["task_id"].(string)
	if runID == "" || taskID == "" {
		t.Fatalf("derived identity is empty: run_id=%q task_id=%q", runID, taskID)
	}
	// Derived once for the whole call, not per row.
	for i, r := range rows {
		if r["run_id"] != runID {
			t.Errorf("row %d run_id = %v, want all rows to share %q", i, r["run_id"], runID)
		}
		if r["task_id"] != taskID {
			t.Errorf("row %d task_id = %v, want all rows to share %q", i, r["task_id"], taskID)
		}
	}
}

// An external orchestrator can group Hydra invocations via env, but an explicit
// Options value must win — env is process-global and can't distinguish
// concurrent runs inside one host process.
func TestLogAttempts_ExplicitOptionsBeatEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HYDRA_RUN_ID", "env-run")

	result := &SwarmResult{Mode: ModeAll, Attempts: []Attempt{idAttempt("a")}}
	logAttempts(result.Attempts, result.Mode, Options{RunID: "explicit-run"}, "p")

	raw, err := os.ReadFile(filepath.Join(home, ".hydra", "logs", "cost.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &m); err != nil {
		t.Fatal(err)
	}
	if m["run_id"] != "explicit-run" {
		t.Errorf("run_id = %v, want the explicit Options value to beat env", m["run_id"])
	}
}
