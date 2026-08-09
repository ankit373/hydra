// SPDX-License-Identifier: MIT

package trust

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

// Crossing snapshotThreshold must write a checkpoint that replays to the same state.
func TestSnapshot_WrittenAfterThresholdAndReplaysToTheSameState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "calibration.jsonl")

	c1, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	feed(t, c1, "model:good", "go", 150, 50, 0, 0) // c1 only appends; it never replays its own writes
	want := c1.D("model:good", "go")

	// c2's construction is the load() that actually replays the file and crosses the threshold.
	c2, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(snapshotPath(path)); err != nil {
		t.Fatalf("expected a snapshot file after c2's load crossed the threshold, stat failed: %v", err)
	}
	got := c2.D("model:good", "go")
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("D after snapshot-assisted replay = %.6f, want %.6f", got, want)
	}
	if n := c2.Report()[0].N; n != 200 {
		t.Errorf("observations after snapshot-assisted replay = %v, want 200", n)
	}
}

// Records appended after a snapshot must still be picked up by the next load.
func TestSnapshot_RecordsAppendedAfterSnapshotAreStillReplayed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "calibration.jsonl")

	c1, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	feed(t, c1, "model:good", "go", 150, 50, 0, 0)

	c2, err := New(path) // this load crosses the threshold and writes the snapshot
	if err != nil {
		t.Fatal(err)
	}
	feed(t, c2, "model:good", "go", 10, 0, 0, 0) // appended after the snapshot existed

	c3, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := c3.Report()[0].N; n != 210 {
		t.Errorf("observations = %v, want 210 (200 pre-snapshot + 10 post-snapshot)", n)
	}
}

// A corrupt snapshot must never lose data or error out of New.
func TestSnapshot_CorruptFileFallsBackToFullReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "calibration.jsonl")

	c1, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	feed(t, c1, "model:good", "go", 150, 50, 0, 0)
	want := c1.D("model:good", "go")

	if err := os.WriteFile(snapshotPath(path), []byte("{not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}

	c2, err := New(path)
	if err != nil {
		t.Fatalf("New must tolerate a corrupt snapshot, got error: %v", err)
	}
	got := c2.D("model:good", "go")
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("D after corrupt-snapshot fallback = %.6f, want %.6f (data was lost)", got, want)
	}
	if n := c2.Report()[0].N; n != 200 {
		t.Errorf("observations after corrupt-snapshot fallback = %v, want 200", n)
	}
}

// An offset past the file's actual size (truncated/rotated) must be rejected, not seeked-to.
func TestSnapshot_OffsetPastFileSizeIsRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "calibration.jsonl")

	c1, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	feed(t, c1, "model:good", "go", 150, 50, 0, 0)
	want := c1.D("model:good", "go")

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := saveSnapshot(snapshotPath(path), map[calibKey]*confusion{}, info.Size()+1_000_000); err != nil {
		t.Fatal(err)
	}

	c2, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	got := c2.D("model:good", "go")
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("D after rejecting a too-far-ahead snapshot = %.6f, want %.6f (real history was skipped)", got, want)
	}
}

// Below snapshotThreshold, load() must not write a snapshot file at all.
func TestSnapshot_NotWrittenBelowThreshold(t *testing.T) {
	path := filepath.Join(t.TempDir(), "calibration.jsonl")

	c1, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	feed(t, c1, "model:good", "go", 2, 1, 0, 0)

	if _, err := New(path); err != nil { // replays 3 records — well under snapshotThreshold
		t.Fatal(err)
	}
	if _, err := os.Stat(snapshotPath(path)); !os.IsNotExist(err) {
		t.Errorf("expected no snapshot file below the threshold, stat err = %v", err)
	}
}
