// SPDX-License-Identifier: MIT

package runlog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunID_IsTheLogsOwnIdentity(t *testing.T) {
	tempHome(t)
	l := New("20260804T120000Z-abc")
	if got := l.RunID(); got != "20260804T120000Z-abc" {
		t.Errorf("RunID() = %q, want the id the log was created with", got)
	}
}

// A log under an unwritable directory must surface, not vanish. Appends are
// best-effort at the call sites, but the function itself has to report failure
// or nobody can ever discover the run log is not being written.
func TestAppend_UnwritablePathIsAnError(t *testing.T) {
	home := tempHome(t)
	// Put a regular file where the runs directory needs to be.
	logsDir := filepath.Join(home, ".hydra", "logs")
	if err := os.MkdirAll(logsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logsDir, "runs"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := New("blocked").Append(Event{Kind: KindRunStarted}); err == nil {
		t.Error("appending under a blocked path reported success")
	}
}

// A run with no log is "nothing happened yet", not a failure — the tree view
// asks for runs that may never have emitted.
func TestLoad_MissingRunIsEmptyNotAnError(t *testing.T) {
	tempHome(t)
	events, err := Load("never-ran")
	if err != nil {
		t.Fatalf("loading an absent run errored: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("got %d events for a run that never emitted", len(events))
	}
}

// A truncated final line is what a crash mid-append leaves. It must not
// discard the events before it.
func TestLoad_SkipsMalformedLinesAndKeepsTheRest(t *testing.T) {
	home := tempHome(t)
	l := New("partial")
	if err := l.Append(Event{Kind: KindRunStarted, Agent: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := l.Append(Event{Kind: KindTaskStarted, Agent: "b"}); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash mid-write.
	path := filepath.Join(home, ".hydra", "logs", "runs", "partial.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"kind":"trunc`); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	events, err := Load(l.RunID())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want the 2 complete ones", len(events))
	}
	if events[0].Agent != "a" || events[1].Agent != "b" {
		t.Errorf("events out of order: %+v", events)
	}
}

// Sequence numbers are the total order — wall-clock ties and out-of-order
// timestamps are expected from concurrent goroutines, so position is what
// orders a run.
func TestAppend_AssignsMonotonicSequenceNumbers(t *testing.T) {
	tempHome(t)
	l := New("seq")
	for i := 0; i < 5; i++ {
		if err := l.Append(Event{Kind: KindTaskStarted}); err != nil {
			t.Fatal(err)
		}
	}
	events, err := Load(l.RunID())
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(events); i++ {
		if events[i].Seq <= events[i-1].Seq {
			t.Errorf("event %d has seq %d, not above the previous %d",
				i, events[i].Seq, events[i-1].Seq)
		}
	}
}

// SaveEdit refuses a ref that could address a file outside the run directory.
// The ref comes from event data, which anything sharing the run id can write.
func TestSaveEdit_RejectsEveryTraversalShape(t *testing.T) {
	tempHome(t)
	for _, ref := range []string{
		"../escape", "a/b", `a\b`, "..", "", "sub/../../x", "./x", "/abs",
	} {
		if err := SaveEdit("r", ref, []byte("x"), []byte("y")); err == nil {
			t.Errorf("SaveEdit accepted ref %q", ref)
		}
	}
}

func TestLoadEdit_UnreadableSnapshotIsAnError(t *testing.T) {
	home := tempHome(t)
	if err := SaveEdit("r", "001", []byte("before"), []byte("after")); err != nil {
		t.Fatal(err)
	}
	// Replace the .before file with a directory so the read fails.
	p := filepath.Join(home, ".hydra", "logs", "runs", "r.edits", "001.before")
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadEdit("r", "001"); err == nil {
		t.Error("an unreadable snapshot loaded without error")
	}
}

// LiveRuns lists runs with a fresh heartbeat. A directory that is not a run
// log, or a marker for a run with no events, must not crash it.
func TestLiveRuns_IgnoresUnrelatedEntries(t *testing.T) {
	home := tempHome(t)
	dir := filepath.Join(home, ".hydra", "logs", "runs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Junk that is not a run log.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "a-directory"), 0o700); err != nil {
		t.Fatal(err)
	}

	live, err := LiveRuns()
	if err != nil {
		t.Fatalf("LiveRuns errored on unrelated entries: %v", err)
	}
	for _, id := range live {
		if strings.Contains(id, "notes") || strings.Contains(id, "a-directory") {
			t.Errorf("LiveRuns returned a non-run entry: %q", id)
		}
	}
}

// touch must create the marker when it does not exist and refresh it when it
// does — both halves, since the create path only runs once per run.
func TestHeartbeatTouch_CreatesThenRefreshes(t *testing.T) {
	tempHome(t)
	h := &Heartbeat{path: HeartbeatPath("touch-test"), interval: time.Millisecond}

	if _, err := os.Stat(h.path); !os.IsNotExist(err) {
		t.Fatal("marker existed before the first touch")
	}
	h.touch()
	if _, err := os.Stat(h.path); err != nil {
		t.Fatalf("first touch did not create the marker: %v", err)
	}
	// Second touch takes the Chtimes path rather than the create path.
	h.touch()
	if _, err := os.Stat(h.path); err != nil {
		t.Fatalf("second touch removed the marker: %v", err)
	}
}
