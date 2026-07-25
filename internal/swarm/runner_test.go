// SPDX-License-Identifier: MIT

package swarm

import (
	"context"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/provider"
)

func TestEffectiveTimeout(t *testing.T) {
	if got := effectiveTimeout(Options{}); got != defaultPerHeadTimeout {
		t.Errorf("zero PerHeadTimeout = %v, want default %v", got, defaultPerHeadTimeout)
	}
	if got := effectiveTimeout(Options{PerHeadTimeout: 5 * time.Second}); got != 5*time.Second {
		t.Errorf("explicit PerHeadTimeout = %v, want 5s", got)
	}
	// Regression guard: swarm must never run a head unbounded.
	if effectiveTimeout(Options{}) <= 0 {
		t.Fatal("default per-head timeout must be positive — heads must never run unbounded")
	}
}

// hangingExecutor blocks until its context is canceled, simulating a head that
// never returns on its own (e.g. a reachable-but-TTY-blocked CLI).
type hangingExecutor struct{}

func (hangingExecutor) Execute(ctx context.Context, _ executor.Request) (*executor.Response, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// withStubExecutor swaps executorFor for the duration of a test.
func withStubExecutor(t *testing.T, e executor.Executor) {
	t.Helper()
	orig := executorFor
	executorFor = func(provider.Head) executor.Executor { return e }
	t.Cleanup(func() { executorFor = orig })
}

// A hung head must degrade to a clean StatusTimeout — not block wg.Wait forever.
func TestRunAll_BoundsHangingHead(t *testing.T) {
	withStubExecutor(t, hangingExecutor{})
	heads := []provider.Head{registryHead("a", "A", 90), registryHead("b", "B", 80)}

	done := make(chan []Attempt, 1)
	go func() {
		done <- runAll(context.Background(), heads, "p", Options{PerHeadTimeout: 50 * time.Millisecond})
	}()

	select {
	case attempts := <-done:
		if len(attempts) != 2 {
			t.Fatalf("got %d attempts, want 2", len(attempts))
		}
		for i, a := range attempts {
			if a.Status != StatusTimeout {
				t.Errorf("head %d status = %q, want %q", i, a.Status, StatusTimeout)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runAll did not return within 5s — hung head was not bounded by the per-head timeout")
	}
}

// runRace must also drain rather than hang when every head hangs and none wins.
func TestRunRace_BoundsHangingHeads(t *testing.T) {
	withStubExecutor(t, hangingExecutor{})
	heads := []provider.Head{registryHead("a", "A", 90), registryHead("b", "B", 80)}

	done := make(chan []Attempt, 1)
	go func() {
		done <- runRace(context.Background(), heads, "p", Options{PerHeadTimeout: 50 * time.Millisecond})
	}()

	select {
	case <-done:
		// Returned instead of hanging — that is the property under test.
	case <-time.After(5 * time.Second):
		t.Fatal("runRace did not return within 5s — hung heads were not bounded")
	}
}
