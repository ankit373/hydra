// SPDX-License-Identifier: MIT

package runlog

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Heartbeat defaults. StaleAfter is a multiple of Interval so a reader tolerates
// a missed tick or two, a briefly busy process must not flicker to "dead".
const (
	HeartbeatInterval = 2 * time.Second
	StaleAfter        = 10 * time.Second
)

// HeartbeatPath is the liveness marker for a run.
func HeartbeatPath(runID string) string {
	return filepath.Join(Dir(), runID+".alive")
}

// Heartbeat keeps a run's liveness marker fresh.
//
// Liveness is inferred from the marker's *freshness*, never from its existence.
// The obvious design, create on start, delete on exit, is wrong: a crashed or
// killed process never runs its cleanup, leaving a permanent "still running"
// ghost a reader can never clear. Touching a marker periodically makes the
// failure self-healing: a dead process simply stops touching it and the marker
// ages out.
//
// Stop is best-effort tidiness for the graceful case. Correctness does not
// depend on it ever running.
type Heartbeat struct {
	path     string
	interval time.Duration
	stop     chan struct{}
	done     chan struct{}
	once     sync.Once
}

// StartHeartbeat begins touching runID's marker until Stop is called or ctx is
// cancelled. interval <= 0 uses HeartbeatInterval.
func StartHeartbeat(ctx context.Context, runID string, interval time.Duration) *Heartbeat {
	if interval <= 0 {
		interval = HeartbeatInterval
	}
	h := &Heartbeat{
		path:     HeartbeatPath(runID),
		interval: interval,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	h.touch() // mark alive immediately; don't wait a full interval

	go func() {
		defer close(h.done)
		t := time.NewTicker(h.interval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				h.touch()
			case <-h.stop:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return h
}

// Stop ends the heartbeat and removes the marker. It is idempotent and waits
// for the goroutine to exit, so a caller that returns immediately afterwards
// leaves nothing running.
func (h *Heartbeat) Stop() {
	h.once.Do(func() { close(h.stop) })
	<-h.done
	_ = os.Remove(h.path)
}

// touch creates or updates the marker's mtime. Errors are ignored: a missing
// heartbeat degrades a run to "not shown as live", which must never be allowed
// to fail the run itself.
func (h *Heartbeat) touch() {
	if err := os.MkdirAll(filepath.Dir(h.path), 0o700); err != nil {
		return
	}
	now := time.Now()
	if err := os.Chtimes(h.path, now, now); err == nil {
		return
	}
	// Not there yet, create it.
	f, err := os.OpenFile(h.path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_ = f.Close()
}

// IsAlive reports whether a run looks live: its marker exists and was touched
// within StaleAfter. A marker left behind by a crash ages out on its own.
func IsAlive(runID string) bool { return aliveAt(HeartbeatPath(runID), time.Now(), StaleAfter) }

// aliveAt is IsAlive with the clock and window injected, so staleness is
// testable without sleeping.
func aliveAt(path string, now time.Time, staleAfter time.Duration) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false // no marker: never started, or cleanly stopped
	}
	return now.Sub(fi.ModTime()) <= staleAfter
}

// LiveRuns returns the runs currently considered alive, newest first.
func LiveRuns() ([]string, error) {
	ids, err := Runs()
	if err != nil {
		return nil, err
	}
	var live []string
	for _, id := range ids {
		if IsAlive(id) {
			live = append(live, id)
		}
	}
	return live, nil
}
