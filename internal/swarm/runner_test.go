// SPDX-License-Identifier: MIT

package swarm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/ledger"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/testutil"
)

// countingExecutor always succeeds and counts how many times it actually ran —
// so a test can prove a denied head's executor was never invoked, not merely
// that its output was discarded.
type countingExecutor struct{ calls *int }

func (e countingExecutor) Execute(context.Context, executor.Request) (*executor.Response, error) {
	*e.calls++
	return &executor.Response{Output: "ran"}, nil
}

func writeLedgerPolicy(t *testing.T, p ledger.Policy) {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	path := ledger.DefaultPolicyPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A ledger deny rule must stop executeHead before it ever calls the executor —
// gating, not just logging after the fact.
func TestExecuteHead_LedgerDenyBlocksExecution(t *testing.T) {
	testutil.NewSandbox(t)
	writeLedgerPolicy(t, ledger.Policy{Rules: []ledger.Rule{{Tool: "denied", Decision: ledger.Deny}}})
	calls := 0
	withStubExecutor(t, countingExecutor{&calls})

	a := executeHead(context.Background(), registryHead("denied", "Denied", 90), "p", Options{})
	if a.Status != StatusFailed {
		t.Errorf("Status = %q, want %q for a denied head", a.Status, StatusFailed)
	}
	if calls != 0 {
		t.Errorf("executor was called %d times for a denied head, want 0", calls)
	}
}

// With no policy configured, default-allow must let the head run normally.
func TestExecuteHead_DefaultLedgerPolicyAllowsExecution(t *testing.T) {
	testutil.NewSandbox(t)
	calls := 0
	withStubExecutor(t, countingExecutor{&calls})

	a := executeHead(context.Background(), registryHead("h1", "H1", 90), "p", Options{})
	if a.Status != StatusOK {
		t.Errorf("Status = %q, want %q", a.Status, StatusOK)
	}
	if calls != 1 {
		t.Errorf("executor was called %d times, want 1", calls)
	}
}

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
