// SPDX-License-Identifier: MIT

package runlog

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeRun creates a run with n events and back-dates it so Seal considers it.
func writeRun(t *testing.T, runID string, n int, age time.Duration) {
	t.Helper()
	l := New(runID)
	for i := 0; i < n; i++ {
		if err := l.Append(Event{Kind: KindDispatchFinished, TaskID: "t", Head: "agy:sonnet",
			Model: "claude-sonnet-5", Tier: 2, Status: "ok", Detail: fmt.Sprintf("event %d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-age)
	if err := os.Chtimes(Path(runID), old, old); err != nil {
		t.Fatal(err)
	}
}

func runID(month string, i int) string {
	return fmt.Sprintf("%s01T12000%dZ-%016x", month, i%10, i)
}

// Sealing must be invisible to every reader: internal/tree, the cockpit and the
// desktop app all call Load, and none of them should learn about segments.
func TestSealIsReaderTransparent(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())
	id := runID("202601", 1)
	writeRun(t, id, 12, 30*24*time.Hour)

	before, err := Load(id)
	if err != nil || len(before) != 12 {
		t.Fatalf("before: %d events, err=%v", len(before), err)
	}
	if _, err := Seal(7 * 24 * time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(Path(id)); !os.IsNotExist(err) {
		t.Error("loose file survived sealing")
	}
	after, err := Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("after sealing: %d events, want %d", len(after), len(before))
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("event %d changed across sealing:\n before %+v\n after  %+v", i, before[i], after[i])
		}
	}
}

// The reason this exists: one file per run is charged a whole filesystem block.
func TestSealRecoversDiskSpace(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())
	const runs = 60
	for i := 0; i < runs; i++ {
		writeRun(t, runID("202601", i), 8, 30*24*time.Hour)
	}
	res, err := Seal(7 * 24 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if res.Runs != runs {
		t.Fatalf("sealed %d runs, want %d", res.Runs, runs)
	}
	if res.BytesAfter >= res.BytesBefore {
		t.Fatalf("sealing did not shrink anything: %d -> %d", res.BytesBefore, res.BytesAfter)
	}
	ratio := float64(res.BytesBefore) / float64(res.BytesAfter)
	t.Logf("%d runs, %d events: %d bytes on disk -> %d bytes sealed (%.1fx)",
		res.Runs, res.Events, res.BytesBefore, res.BytesAfter, ratio)
	if ratio < 4 {
		t.Errorf("only %.1fx reduction; the block-amplification win should be much larger", ratio)
	}
}

// A run still being appended to must never be sealed out from under its writer.
func TestSealLeavesRecentRunsAlone(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())
	oldID, newID := runID("202601", 1), runID("202601", 2)
	writeRun(t, oldID, 4, 30*24*time.Hour)
	writeRun(t, newID, 4, time.Minute)

	if _, err := Seal(7 * 24 * time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(Path(oldID)); !os.IsNotExist(err) {
		t.Error("old run was not sealed")
	}
	if _, err := os.Stat(Path(newID)); err != nil {
		t.Error("recent run was sealed while it may still be written")
	}
}

// Seal runs opportunistically at the end of a dispatch, so it will be called
// again and again over the same directory.
func TestSealIsIdempotent(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())
	id := runID("202601", 3)
	writeRun(t, id, 6, 30*24*time.Hour)

	first, err := Seal(7 * 24 * time.Hour)
	if err != nil || first.Runs != 1 {
		t.Fatalf("first seal: %+v err=%v", first, err)
	}
	second, err := Seal(7 * 24 * time.Hour)
	if err != nil || second.Runs != 0 {
		t.Fatalf("second seal wrote again: %+v err=%v", second, err)
	}
	ev, err := Load(id)
	if err != nil || len(ev) != 6 {
		t.Fatalf("after two seals: %d events, err=%v", len(ev), err)
	}
	idx, err := LoadIndex("2026-01")
	if err != nil || len(idx) != 1 {
		t.Fatalf("index has %d entries, want 1", len(idx))
	}
}

// A crash after the index is durable but before the loose file is removed must
// resolve to "already sealed", not a duplicate.
func TestSealSkipsAnAlreadyIndexedRun(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())
	id := runID("202601", 4)
	writeRun(t, id, 5, 30*24*time.Hour)
	if _, err := Seal(7 * 24 * time.Hour); err != nil {
		t.Fatal(err)
	}
	// Simulate the crash: the loose file is back, the index still has it.
	writeRun(t, id, 5, 30*24*time.Hour)
	res, err := Seal(7 * 24 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if res.Runs != 0 {
		t.Errorf("re-sealed an indexed run: %+v", res)
	}
	idx, _ := LoadIndex("2026-01")
	if len(idx) != 1 {
		t.Errorf("index has %d entries, want 1 — a duplicate was appended", len(idx))
	}
	if _, err := os.Stat(Path(id)); !os.IsNotExist(err) {
		t.Error("the leftover loose file was not cleaned up")
	}
}

// Runs must list sealed runs too, or history appears to shrink as retention
// advances.
func TestRunsIncludesSealed(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())
	sealed, live := runID("202601", 5), runID("202602", 6)
	writeRun(t, sealed, 3, 30*24*time.Hour)
	writeRun(t, live, 3, time.Minute)
	if _, err := Seal(7 * 24 * time.Hour); err != nil {
		t.Fatal(err)
	}
	ids, err := Runs()
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, id := range ids {
		found[id] = true
	}
	if !found[sealed] || !found[live] {
		t.Fatalf("Runs() = %v, want both %s and %s", ids, sealed, live)
	}
	// Newest first: IDs are timestamp-prefixed, so lexical descending is
	// chronological and the February run must lead.
	if ids[0] != live {
		t.Errorf("Runs()[0] = %s, want the newest (%s)", ids[0], live)
	}
}

// A run whose ID carries no parseable month must be left alone rather than
// filed somewhere arbitrary.
func TestSealSkipsUnparseableRunIDs(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())
	weird := "not-a-timestamp-id"
	writeRun(t, weird, 3, 30*24*time.Hour)
	res, err := Seal(7 * 24 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if res.Runs != 0 {
		t.Errorf("sealed a run with no month: %+v", res)
	}
	if _, err := os.Stat(Path(weird)); err != nil {
		t.Error("an unfileable run was deleted instead of left alone")
	}
}

func TestSealNoRunsIsNotAnError(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())
	res, err := Seal(time.Hour)
	if err != nil || res.Runs != 0 {
		t.Errorf("res=%+v err=%v", res, err)
	}
}

// Edit snapshots belong to their run and must not outlive it on disk.
func TestSealRemovesEditSnapshots(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())
	id := runID("202601", 7)
	writeRun(t, id, 2, 30*24*time.Hour)
	if err := SaveEdit(id, "000001", []byte("before"), []byte("after")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(EditsDir(id)); err != nil {
		t.Fatal(err)
	}
	if _, err := Seal(7 * 24 * time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(EditsDir(id)); !os.IsNotExist(err) {
		t.Error("edit snapshots survived their run being sealed")
	}
}

func TestMonthOf(t *testing.T) {
	cases := map[string]string{
		"20260801T104530Z-3f9c1a4b": "2026-08",
		"20261231T000000Z-aaaa":     "2026-12",
		"short":                     "",
		"notadate-xxxx":             "",
		"":                          "",
	}
	for in, want := range cases {
		if got := monthOf(in); got != want {
			t.Errorf("monthOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoadSealedMissingSegment(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())
	ev, ok, err := loadSealed(runID("202601", 9))
	if err != nil || ok || ev != nil {
		t.Errorf("ev=%v ok=%v err=%v; want nil,false,nil", ev, ok, err)
	}
}

func TestSegmentFilesLandInSegDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HYDRA_HOME", home)
	writeRun(t, runID("202601", 10), 3, 30*24*time.Hour)
	if _, err := Seal(7 * 24 * time.Hour); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"2026-01.zst", "2026-01.idx"} {
		if _, err := os.Stat(filepath.Join(SegDir(), name)); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}
}

// The dry-run listing and the seal itself must never disagree about what is old
// enough to archive — they are one function precisely so they cannot.
func TestSealCandidatesAreExactlyWhatSealActsOn(t *testing.T) {
	tempHome(t)
	old := []string{runID("202601", 1), runID("202602", 2), runID("202603", 3)}
	for _, id := range old {
		writeRun(t, id, 2, 48*time.Hour)
	}
	fresh := runID("202604", 4)
	writeRun(t, fresh, 2, 0)

	got, err := SealCandidates(24 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(old) {
		t.Fatalf("candidates = %v, want the %d back-dated runs", got, len(old))
	}
	for i, id := range old {
		if got[i] != id {
			t.Errorf("candidate %d = %q, want %q (sorted)", i, got[i], id)
		}
	}

	res, err := Seal(24 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if res.Runs != len(got) {
		t.Errorf("Seal folded %d runs but SealCandidates listed %d", res.Runs, len(got))
	}
	if res.Months != 3 {
		t.Errorf("Months = %d, want 3 — one segment per calendar month", res.Months)
	}
	if _, err := os.Stat(Path(fresh)); err != nil {
		t.Errorf("the fresh run was sealed: %v", err)
	}
}

func TestSealCandidatesSkipsRecentAndUnparseable(t *testing.T) {
	tempHome(t)
	writeRun(t, runID("202601", 1), 1, 48*time.Hour)
	writeRun(t, "no-month-prefix", 1, 48*time.Hour)
	writeRun(t, runID("202601", 2), 1, 0)

	got, err := SealCandidates(24 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != runID("202601", 1) {
		t.Fatalf("candidates = %v, want only the back-dated well-formed run", got)
	}
}

func TestSealCandidatesNoDirectoryIsEmpty(t *testing.T) {
	tempHome(t)
	got, err := SealCandidates(time.Hour)
	if err != nil {
		t.Fatalf("a machine that has never run anything is not an error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("candidates = %v, want none", got)
	}
}

// Months backs the segment listing; newest-first is what a reader renders.
func TestMonthsListsSegmentsNewestFirst(t *testing.T) {
	tempHome(t)
	for i, m := range []string{"202601", "202603", "202602"} {
		writeRun(t, runID(m, i), 1, 48*time.Hour)
	}
	if _, err := Seal(24 * time.Hour); err != nil {
		t.Fatal(err)
	}
	got, err := Months()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"2026-03", "2026-02", "2026-01"}
	if len(got) != len(want) {
		t.Fatalf("Months = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Months = %v, want %v", got, want)
		}
	}
}

func TestMonthsNoSegDirIsEmpty(t *testing.T) {
	tempHome(t)
	got, err := Months()
	if err != nil {
		t.Fatalf("no segments yet is not an error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Months = %v, want none", got)
	}
}

// A torn index line must cost only its own run, not the whole month. The index
// is appended to per run, so a crash mid-write leaves exactly this.
func TestLoadIndexSkipsATornLineAndKeepsTheRest(t *testing.T) {
	tempHome(t)
	id := runID("202601", 1)
	writeRun(t, id, 3, 48*time.Hour)
	if _, err := Seal(24 * time.Hour); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(SegDir(), "2026-01.idx"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{\"run_id\": tru\n\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	idx, err := LoadIndex("2026-01")
	if err != nil {
		t.Fatal(err)
	}
	if len(idx) != 1 || idx[0].RunID != id {
		t.Fatalf("index = %+v, want just the one intact entry for %q", idx, id)
	}
	// The intact run must still read back, which is the point of skipping.
	events, err := Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Errorf("got %d events, want 3", len(events))
	}
}

func TestLoadIndexMissingMonthIsNotAnError(t *testing.T) {
	tempHome(t)
	idx, err := LoadIndex("1999-12")
	if err != nil {
		t.Fatalf("a month never sealed is not an error: %v", err)
	}
	if idx != nil {
		t.Errorf("index = %+v, want nil", idx)
	}
}

// A corrupt segment must surface as an error, never as a run with no events —
// silently rendering a sealed run as empty is the failure mode that matters.
func TestLoadReportsACorruptSegmentRatherThanEmptyingIt(t *testing.T) {
	tempHome(t)
	id := runID("202601", 1)
	writeRun(t, id, 4, 48*time.Hour)
	if _, err := Seal(24 * time.Hour); err != nil {
		t.Fatal(err)
	}
	seg := filepath.Join(SegDir(), "2026-01.zst")
	raw, err := os.ReadFile(seg)
	if err != nil {
		t.Fatal(err)
	}
	for i := range raw {
		raw[i] ^= 0xFF
	}
	if err := os.WriteFile(seg, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(id); err == nil {
		t.Error("Load returned no error for a corrupt segment; a truncated history must be loud")
	}
}

// The index says a run is in the segment; the segment file is gone. Reporting
// that is the difference between "history is damaged" and "this run was empty".
func TestLoadReportsAMissingSegmentBehindAnIndex(t *testing.T) {
	tempHome(t)
	id := runID("202601", 1)
	writeRun(t, id, 2, 48*time.Hour)
	if _, err := Seal(24 * time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(SegDir(), "2026-01.zst")); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(id); err == nil {
		t.Error("Load returned no error when the segment behind the index was gone")
	}
}

// onDiskSize feeds the reclaimed-bytes figure the CLI prints; a vanished file
// must contribute nothing rather than a garbage number.
func TestOnDiskSizeOfAMissingFileIsZero(t *testing.T) {
	tempHome(t)
	if got := onDiskSize(filepath.Join(t.TempDir(), "absent")); got != 0 {
		t.Errorf("onDiskSize = %d, want 0", got)
	}
}
