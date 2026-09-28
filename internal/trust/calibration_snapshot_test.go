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

// A schema-valid, in-bounds offset that lands mid-line (not on a JSONL line
// boundary) must be rejected, not seeked-to, otherwise the split first line
// silently drops a real record instead of triggering a full replay.
func TestSnapshot_MisalignedOffsetFallsBackToFullReplay(t *testing.T) {
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
	// A few bytes short of EOF lands inside the last line's JSON, not on a
	// newline, schema-valid and in-bounds, but not a real line boundary.
	if err := saveSnapshot(snapshotPath(path), map[calibKey]*confusion{}, info.Size()-5); err != nil {
		t.Fatal(err)
	}

	c2, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	got := c2.D("model:good", "go")
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("D after rejecting a misaligned snapshot = %.6f, want %.6f (a record was silently dropped)", got, want)
	}
	if n := c2.Report()[0].N; n != 200 {
		t.Errorf("observations after rejecting a misaligned snapshot = %v, want 200 (a record was silently dropped)", n)
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

	if _, err := New(path); err != nil { // replays 3 records, well under snapshotThreshold
		t.Fatal(err)
	}
	if _, err := os.Stat(snapshotPath(path)); !os.IsNotExist(err) {
		t.Errorf("expected no snapshot file below the threshold, stat err = %v", err)
	}
}

// A checkpoint written before the key was normalized holds its rows under "".
// loadSnapshot rebuilds the store without going through apply, so restoring one
// verbatim puts the old history in a cell no reader asks for while every later
// observation lands in "default", and saveSnapshot then re-persists the dead
// cell forever.
func TestSnapshot_APreNormalizationCheckpointFoldsIntoTheCellReadersAsk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "calibration.jsonl")

	c1, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	feed(t, c1, "verifier:go test", "", 50, 0, 20, 0)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// What load() would have checkpointed before the fix: "" keys, offset at
	// the end of the file so nothing is left to replay on top of it.
	if err := saveSnapshot(snapshotPath(path), map[calibKey]*confusion{
		{"verifier:go test", ""}: {TP: 51, FP: 1, TN: 21, FN: 1},
	}, info.Size()); err != nil {
		t.Fatal(err)
	}

	c2, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	rows := c2.Report()
	if len(rows) != 1 {
		t.Fatalf("got %d cells after restore, want 1: %+v", len(rows), rows)
	}
	if rows[0].Domain != DefaultDomain {
		t.Errorf("restored cell domain = %q, want %q", rows[0].Domain, DefaultDomain)
	}
	if rows[0].N != 70 {
		t.Errorf("restored observations = %v, want 70", rows[0].N)
	}
	if d := c2.D("verifier:go test", DefaultDomain); d == 0 {
		t.Error("D = 0 after restoring 70 observations: the checkpoint landed where nothing reads")
	}

	// The next observations must join that history rather than open a second cell.
	feed(t, c2, "verifier:go test", "", 10, 0, 0, 0)
	if rows = c2.Report(); len(rows) != 1 || rows[0].N != 80 {
		t.Errorf("after ten more: %d cells, first N=%v; want 1 cell of 80", len(rows), rows[0].N)
	}
}

// Folding two entries must add what they observed, not what they store: every
// entry carries the Laplace prior, so summing the raw counts inflates the cell
// by a whole prior's worth and reports observations that never happened.
func TestSnapshot_FoldingTwoEntriesDoesNotCountThePriorTwice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "calibration.jsonl")

	c1, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	feed(t, c1, "claude", DefaultDomain, 1, 0, 0, 0) // one line, so the offset has somewhere to land

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Both spellings of the same cell, as a store straddling the fix holds them.
	if err := saveSnapshot(snapshotPath(path), map[calibKey]*confusion{
		{"claude", ""}:            {TP: 41, FP: 1, TN: 31, FN: 1},
		{"claude", DefaultDomain}: {TP: 21, FP: 1, TN: 11, FN: 1},
	}, info.Size()); err != nil {
		t.Fatal(err)
	}

	c2, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	rows := c2.Report()
	if len(rows) != 1 {
		t.Fatalf("got %d cells, want the two spellings folded into 1: %+v", len(rows), rows)
	}
	// 40+30 real from one entry, 20+10 from the other. 104 means the prior was
	// added twice; 30 or 70 means one entry overwrote the other.
	if rows[0].N != 100 {
		t.Errorf("observations = %v, want 100", rows[0].N)
	}
}
