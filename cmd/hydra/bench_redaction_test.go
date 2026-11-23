// SPDX-License-Identifier: MIT

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/provider"
)

// A local model runs the committed tasks with no key and no cap, prints the worst
// case before the table, and writes every task's answers when asked.
func TestBenchRedaction_LocalModelRunsTheCommittedTasks(t *testing.T) {
	exec := &benchAnswer{answer: "no idea"}
	swap(t, &benchExec, func(provider.Head) executor.Executor { return exec })
	out := filepath.Join(t.TempDir(), "rows.jsonl")
	got, _, err := run(t, "bench", "redaction", "--model", "ollama:qwen3:8b",
		"--tasks", "../../bench/redaction/tasks.jsonl", "--out", out)
	if err != nil {
		t.Fatalf("%v\n%s", err, got)
	}
	for _, want := range []string{"30 tasks × 2 on ollama:qwen3:8b: worst case $0.000000", "| all | 30 | 0 (0–11) | 0 (0–11) | 0 | 0 | 1 |",
		"qwen3:8b (local)"} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
	if b, _ := os.ReadFile(out); strings.Count(string(b), "\n") != 30 || exec.calls.Load() != 60 {
		t.Errorf("%d rows written, %d calls", strings.Count(string(b), "\n"), exec.calls.Load())
	}
}

// A hosted model with no cap is refused before its first call.
func TestBenchRedaction_HostedModelStopsAtTheCap(t *testing.T) {
	_, exec, _ := benchFixture(t, "1.0")
	_, _, err := run(t, "bench", "redaction", "--model", "openrouter:vendor/front", "--tasks", "../../bench/redaction/tasks.jsonl")
	var code exitStatus
	if !errors.As(err, &code) || code != 2 || exec.calls.Load() != 0 {
		t.Fatalf("err %v, %d calls; want exit 2 before any call", err, exec.calls.Load())
	}
}
