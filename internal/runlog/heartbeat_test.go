// SPDX-License-Identifier: MIT

package runlog

import (
	"context"
	"os"
	"runtime"
	"testing"
	"time"
)

func TestHeartbeat_LiveRunIsAlive(t *testing.T) {
	tempHome(t)
	h := StartHeartbeat(context.Background(), "run-live", 10*time.Millisecond)
	defer h.Stop()

	if !IsAlive("run-live") {
		t.Fatal("a just-started heartbeat is not alive — the marker should be touched immediately")
	}
}

func TestIsAlive_MissingMarkerIsDead(t *testing.T) {
	tempHome(t)
	if IsAlive("never-ran") {
		t.Error("a run with no marker reported alive")
	}
}

// The design's whole point: a crashed process never runs cleanup, so its marker
// lingers. Liveness must come from freshness, not existence, or the UI shows a
// permanent ghost it can never clear.
func TestIsAlive_StaleMarkerFromCrashAgesOut(t *testing.T) {
	tempHome(t)
	// Interval long enough that it never ticks again — the marker is written
	// once at start and then, as in a crash, never touched.
	StartHeartbeat(context.Background(), "run-crashed", time.Hour)
	path := HeartbeatPath("run-crashed")

	if !IsAlive("run-crashed") {
		t.Fatal("fresh marker should be alive")
	}

	// Simulate a crash: the marker is left behind and stops being touched.
	// Backdate it well past the staleness window.
	old := time.Now().Add(-StaleAfter - time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	if IsAlive("run-crashed") {
		t.Error("a marker left behind by a crash still reports alive — it must age out")
	}
	// The file is still there; deletion is not what makes it dead.
	if _, err := os.Stat(path); err != nil {
		t.Errorf("marker unexpectedly gone: %v", err)
	}
}

// Boundary: exactly at the window is alive, one tick past is not.
func TestAliveAt_Boundary(t *testing.T) {
	tempHome(t)
	path := HeartbeatPath("run-boundary")
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	base := time.Now()
	if err := os.Chtimes(path, base, base); err != nil {
		t.Fatal(err)
	}

	if !aliveAt(path, base.Add(StaleAfter), StaleAfter) {
		t.Error("exactly at the staleness window should still be alive")
	}
	if aliveAt(path, base.Add(StaleAfter+time.Millisecond), StaleAfter) {
		t.Error("past the staleness window should be dead")
	}
}

// A busy process that misses a tick must not flicker to dead — hence
// StaleAfter being a multiple of the interval.
func TestStaleAfter_ToleratesMissedTicks(t *testing.T) {
	if StaleAfter <= HeartbeatInterval {
		t.Fatalf("StaleAfter (%v) must exceed HeartbeatInterval (%v) or a single missed tick reads as dead",
			StaleAfter, HeartbeatInterval)
	}
	if StaleAfter < 3*HeartbeatInterval {
		t.Errorf("StaleAfter (%v) tolerates fewer than 3 missed ticks at interval %v — too twitchy",
			StaleAfter, HeartbeatInterval)
	}
}

// The whole liveness design rests on mtime advancing: IsAlive is
// now-ModTime <= StaleAfter, so a marker that never moves ages out and every
// running agent reports dead.
//
// Polled rather than asserted after one fixed sleep (#274). Windows' system
// clock and timer granularity are both ~15.6ms, so a single 60ms window is
// within noise there — and a flaky assertion on a real invariant is worse than
// no assertion, because it gets deleted. Polling keeps the invariant exact (it
// still fails if the heartbeat genuinely stops) while removing the dependency on
// how coarse the platform's clock is. On failure it prints the observed
// timestamps, so CI reports what actually happened instead of a bare verdict.
func TestHeartbeat_KeepsMarkerFresh(t *testing.T) {
	tempHome(t)
	const interval = 10 * time.Millisecond
	h := StartHeartbeat(context.Background(), "run-fresh", interval)
	defer h.Stop()

	first, err := os.Stat(HeartbeatPath("run-fresh"))
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	var last time.Time
	for time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		cur, err := os.Stat(HeartbeatPath("run-fresh"))
		if err != nil {
			t.Fatal(err)
		}
		last = cur.ModTime()
		if last.After(first.ModTime()) {
			return
		}
	}
	t.Errorf("marker mtime did not advance in 3s at a %v interval — the heartbeat is not ticking.\n"+
		"  first = %s\n  last  = %s\n  delta = %v",
		interval, first.ModTime().Format(time.RFC3339Nano), last.Format(time.RFC3339Nano),
		last.Sub(first.ModTime()))
}

func TestHeartbeat_StopIsIdempotentAndRemovesMarker(t *testing.T) {
	tempHome(t)
	h := StartHeartbeat(context.Background(), "run-stop", 10*time.Millisecond)
	h.Stop()
	h.Stop() // must not panic or block

	if _, err := os.Stat(HeartbeatPath("run-stop")); !os.IsNotExist(err) {
		t.Error("Stop did not remove the marker in the graceful case")
	}
	if IsAlive("run-stop") {
		t.Error("run still reports alive after Stop")
	}
}

func TestHeartbeat_ContextCancellationStops(t *testing.T) {
	tempHome(t)
	ctx, cancel := context.WithCancel(context.Background())
	h := StartHeartbeat(ctx, "run-ctx", 10*time.Millisecond)

	cancel()
	// Stop must still return promptly even though the goroutine exited via ctx.
	done := make(chan struct{})
	go func() { h.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop blocked after context cancellation")
	}
}

// Stop waits for the goroutine, so nothing is left running afterwards.
func TestHeartbeat_NoGoroutineLeak(t *testing.T) {
	tempHome(t)
	before := runtime.NumGoroutine()

	for i := 0; i < 20; i++ {
		h := StartHeartbeat(context.Background(), "run-leak", 5*time.Millisecond)
		h.Stop()
	}

	// Give any stragglers a chance to be scheduled out.
	time.Sleep(50 * time.Millisecond)
	after := runtime.NumGoroutine()
	if after > before+2 { // small slack for the test runtime itself
		t.Errorf("goroutines grew from %d to %d — heartbeat goroutines leaked", before, after)
	}
}

func TestLiveRuns_ListsOnlyFresh(t *testing.T) {
	tempHome(t)

	// Two runs with events; only one is beating.
	for _, id := range []string{"20260801T100000Z-old", "20260801T110000Z-new"} {
		if err := New(id).Append(Event{Kind: KindRunStarted}); err != nil {
			t.Fatal(err)
		}
	}
	h := StartHeartbeat(context.Background(), "20260801T110000Z-new", time.Hour)
	defer h.Stop()

	// The other run crashed: marker exists but is stale.
	stalePath := HeartbeatPath("20260801T100000Z-old")
	if err := os.WriteFile(stalePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-StaleAfter - time.Minute)
	if err := os.Chtimes(stalePath, old, old); err != nil {
		t.Fatal(err)
	}

	live, err := LiveRuns()
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 || live[0] != "20260801T110000Z-new" {
		t.Errorf("LiveRuns() = %v, want only the beating run", live)
	}
}

func TestLiveRuns_NoRunsIsEmpty(t *testing.T) {
	tempHome(t)
	live, err := LiveRuns()
	if err != nil {
		t.Fatalf("no runs should not error: %v", err)
	}
	if len(live) != 0 {
		t.Errorf("got %v, want none", live)
	}
}
