// SPDX-License-Identifier: MIT

package runid

import (
	"strings"
	"sync"
	"testing"
)

func TestNew_NonEmptyAndUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		id := New()
		if id == "" {
			t.Fatal("New() returned empty")
		}
		if seen[id] {
			t.Fatalf("New() collided on %q after %d calls", id, i)
		}
		seen[id] = true
	}
}

// IDs are timestamp-prefixed so a jsonl log sorts chronologically by run.
func TestNew_IsTimestampPrefixed(t *testing.T) {
	id := New()
	if len(id) < 16 {
		t.Fatalf("id %q is shorter than a timestamp prefix", id)
	}
	if !strings.Contains(id, "T") || !strings.Contains(id, "Z") {
		t.Errorf("id %q does not look like a UTC timestamp prefix", id)
	}
}

func TestNew_ConcurrentCallsDoNotCollide(t *testing.T) {
	const n = 200
	var mu sync.Mutex
	seen := map[string]bool{}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := New()
			mu.Lock()
			defer mu.Unlock()
			if seen[id] {
				t.Errorf("concurrent collision on %q", id)
			}
			seen[id] = true
		}()
	}
	wg.Wait()
	if len(seen) != n {
		t.Errorf("got %d distinct ids from %d goroutines", len(seen), n)
	}
}

// Resolution order: explicit wins over env, env wins over generated. Explicit
// must win because env is process-global and cannot distinguish concurrent runs
// inside one long-lived host process.
func TestResolve_ExplicitBeatsEnv(t *testing.T) {
	t.Setenv(EnvRunID, "from-env")
	if got := ResolveRun("explicit"); got != "explicit" {
		t.Errorf("ResolveRun(explicit) = %q, want the explicit value", got)
	}
	t.Setenv(EnvTaskID, "task-env")
	if got := ResolveTask("explicit-task"); got != "explicit-task" {
		t.Errorf("ResolveTask(explicit) = %q, want the explicit value", got)
	}
}

func TestResolve_EnvUsedWhenNoExplicit(t *testing.T) {
	t.Setenv(EnvRunID, "orchestrator-run")
	if got := ResolveRun(""); got != "orchestrator-run" {
		t.Errorf("ResolveRun(\"\") = %q, want the env value", got)
	}
	t.Setenv(EnvTaskID, "orchestrator-task")
	if got := ResolveTask(""); got != "orchestrator-task" {
		t.Errorf("ResolveTask(\"\") = %q, want the env value", got)
	}
}

// The bug this package exists to fix: identity must never be empty. Every log
// row used to carry run_id:"" because nothing set the env var.
func TestResolve_NeverEmpty(t *testing.T) {
	t.Setenv(EnvRunID, "")
	t.Setenv(EnvTaskID, "")
	if got := ResolveRun(""); got == "" {
		t.Error("ResolveRun with no explicit value and no env returned empty")
	}
	if got := ResolveTask(""); got == "" {
		t.Error("ResolveTask with no explicit value and no env returned empty")
	}
}
