// SPDX-License-Identifier: MIT

package dispatch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/pricing"
	"github.com/ankit373/hydra/internal/provider"
)

// readLog returns the parsed rows of one jsonl file under the temp HOME.
func readLog(t *testing.T, home, name string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(home, ".hydra", "logs", name))
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
			t.Fatalf("unparseable %s row %q: %v", name, line, err)
		}
		rows = append(rows, m)
	}
	return rows
}

// logResult is a minimal successful dispatch Result with token data, so both
// dispatch.jsonl and cost.jsonl get written.
func logResult() *Result {
	return &Result{
		Output: "ok",
		Head:   provider.Head{ID: "h1", Name: "H1", Provider: "agy", Source: "registry", CapScore: 90},
		Response: &executor.Response{
			Output:       "ok",
			Model:        "m",
			InputTokens:  100,
			OutputTokens: 50,
			Duration:     time.Second,
		},
	}
}

func newTestDispatcher() *Dispatcher {
	return &Dispatcher{pricing: pricing.Load()}
}

// The bug: every row in the real cost.jsonl carried run_id:"" because these
// read env vars nothing ever set. They must now always be populated.
func TestLogDispatch_IdentityIsNeverEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HYDRA_RUN_ID", "")
	t.Setenv("HYDRA_TASK_ID", "")

	d := newTestDispatcher()
	if err := d.logDispatch(logResult(), "a prompt", Options{}, 1); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"dispatch.jsonl", "cost.jsonl"} {
		rows := readLog(t, home, name)
		if len(rows) != 1 {
			t.Fatalf("%s: got %d rows, want 1", name, len(rows))
		}
		if id, _ := rows[0]["run_id"].(string); id == "" {
			t.Errorf("%s: run_id is empty — this is exactly the #181 bug", name)
		}
		if id, _ := rows[0]["task_id"].(string); id == "" {
			t.Errorf("%s: task_id is empty", name)
		}
	}
}

// dispatch.jsonl and cost.jsonl describe the same call, so they must agree —
// otherwise a reader joining the two logs sees two different runs.
func TestLogDispatch_BothLogsShareOneIdentity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	d := newTestDispatcher()
	if err := d.logDispatch(logResult(), "p", Options{RunID: "run-9", TaskID: "task-9"}, 1); err != nil {
		t.Fatal(err)
	}

	disp := readLog(t, home, "dispatch.jsonl")
	cost := readLog(t, home, "cost.jsonl")
	if len(disp) != 1 || len(cost) != 1 {
		t.Fatalf("dispatch=%d cost=%d rows, want 1 each", len(disp), len(cost))
	}
	if disp[0]["run_id"] != "run-9" || cost[0]["run_id"] != "run-9" {
		t.Errorf("run_id mismatch: dispatch=%v cost=%v, want run-9 in both",
			disp[0]["run_id"], cost[0]["run_id"])
	}
	if disp[0]["task_id"] != "task-9" || cost[0]["task_id"] != "task-9" {
		t.Errorf("task_id mismatch: dispatch=%v cost=%v, want task-9 in both",
			disp[0]["task_id"], cost[0]["task_id"])
	}
}

func TestLogDispatch_ExplicitBeatsEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HYDRA_RUN_ID", "env-run")

	d := newTestDispatcher()
	if err := d.logDispatch(logResult(), "p", Options{RunID: "explicit-run"}, 1); err != nil {
		t.Fatal(err)
	}
	rows := readLog(t, home, "cost.jsonl")
	if len(rows) != 1 {
		t.Fatalf("cost.jsonl: got %d rows, want 1", len(rows))
	}
	if rows[0]["run_id"] != "explicit-run" {
		t.Errorf("run_id = %v, want the explicit Options value to win over env", rows[0]["run_id"])
	}
}

// An external orchestrator that exports HYDRA_RUN_ID to group several hyctl
// invocations must still be honoured when no explicit value is passed.
func TestLogDispatch_EnvUsedWhenNoExplicit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HYDRA_RUN_ID", "orchestrator-run")

	d := newTestDispatcher()
	if err := d.logDispatch(logResult(), "p", Options{}, 1); err != nil {
		t.Fatal(err)
	}
	rows := readLog(t, home, "cost.jsonl")
	if len(rows) != 1 {
		t.Fatalf("cost.jsonl: got %d rows, want 1", len(rows))
	}
	if rows[0]["run_id"] != "orchestrator-run" {
		t.Errorf("run_id = %v, want the env value", rows[0]["run_id"])
	}
}
