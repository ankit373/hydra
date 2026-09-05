// SPDX-License-Identifier: MIT

package runid

import (
	"encoding/hex"
	"strings"
	"sync"
	"testing"
)

func TestNew_NonEmptyAndUnique(t *testing.T) {
	// 20k in a tight loop lands well inside one wall-clock second, so this
	// exercises the suffix alone rather than relying on the timestamp to
	// separate anything. At 64 bits that collides with probability ~1e-11; at
	// the 24 bits this package shipped with (#198) it collides essentially
	// always.
	const n = 20_000
	seen := make(map[string]bool, n)
	for i := range n {
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

// The uniqueness test above is probabilistic, it would still pass at, say, 5
// bytes. This pins the entropy width itself so narrowing the suffix fails
// loudly rather than turning New() flaky again.
func TestNew_SuffixCarriesFullEntropy(t *testing.T) {
	id := New()
	_, suffix, ok := strings.Cut(id, "-")
	if !ok {
		t.Fatalf("id %q has no %q separator", id, "-")
	}
	// Deliberately a literal, not randBytes*2: asserting the constant against
	// itself would pass at any width and guard nothing.
	const wantHexChars = 16 // 64 bits
	if len(suffix) != wantHexChars {
		t.Errorf("suffix %q is %d hex chars (%d bits), want %d (64 bits), see #198",
			suffix, len(suffix), len(suffix)*4, wantHexChars)
	}
	if _, err := hex.DecodeString(suffix); err != nil {
		t.Errorf("suffix %q is not hex: %v", suffix, err)
	}
}

// A suffix that is constant, or drawn from a tiny set, would pass both tests
// above if the timestamp happened to tick. Sampling the first byte across many
// draws catches a suffix that is not actually random.
func TestNew_SuffixIsNotConstant(t *testing.T) {
	firstBytes := map[string]bool{}
	for range 1000 {
		_, suffix, _ := strings.Cut(New(), "-")
		firstBytes[suffix[:2]] = true
	}
	// 1000 draws over 256 values: seeing fewer than 200 distinct would mean the
	// source is badly skewed. A uniform source yields ~254.
	if len(firstBytes) < 200 {
		t.Errorf("only %d distinct leading bytes in 1000 draws, suffix is not uniformly random", len(firstBytes))
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
