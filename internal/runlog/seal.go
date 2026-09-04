// SPDX-License-Identifier: MIT

package runlog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

// One file per run is right while a run is live and wrong once it is not.
// Measured on a real machine: 65 runs holding 27,835 bytes of events occupied
// 245,760 bytes, because a filesystem charges a whole 4 KiB block for a 344-byte
// file. Sealing recovers that before a single byte is compressed.
//
// A sealed segment is one zstd frame per run, concatenated, plus a sidecar
// index. Frames concatenate legally in zstd, so one run can be read back by
// seeking to its offset and decoding a single frame — the alternative, one
// frame for the whole month, would mean decompressing a month to read a minute.

// SegDir is where sealed segments live.
func SegDir() string { return filepath.Join(Dir(), "seg") }

func segPath(month string) string { return filepath.Join(SegDir(), month+".zst") }
func idxPath(month string) string { return filepath.Join(SegDir(), month+".idx") }

// IndexEntry locates one run inside a segment.
type IndexEntry struct {
	RunID  string `json:"run_id"`
	Off    int64  `json:"off"`
	Len    int64  `json:"len"`
	Events int    `json:"events"`
}

// SealResult reports what one Seal call did.
type SealResult struct {
	Runs        int   `json:"runs"`
	Events      int   `json:"events"`
	BytesBefore int64 `json:"bytes_before"` // on-disk, including block overhead
	BytesAfter  int64 `json:"bytes_after"`
	Months      int   `json:"months"`
}

// monthOf derives the segment a run belongs to from its ID, which is
// timestamp-prefixed (see internal/runid). Falling back to file mtime would be
// wrong: a run copied between machines keeps its ID but not its mtime.
func monthOf(runID string) string {
	if len(runID) < 6 {
		return ""
	}
	y, m := runID[:4], runID[4:6]
	if !isDigits(y) || !isDigits(m) {
		return ""
	}
	return y + "-" + m
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(s) > 0
}

// Seal folds every run older than age into its month's segment and removes the
// loose file.
//
// Idempotent, and deliberately conservative: a run already present in an index
// is skipped rather than appended twice, and a run whose ID carries no parseable
// month is left alone rather than filed somewhere arbitrary.
func Seal(age time.Duration) (SealResult, error) {
	var res SealResult
	candidates, err := SealCandidates(age)
	if err != nil {
		return res, err
	}

	byMonth := map[string][]string{}
	for _, runID := range candidates {
		m := monthOf(runID)
		byMonth[m] = append(byMonth[m], runID)
	}
	if len(byMonth) == 0 {
		return res, nil
	}
	if err := os.MkdirAll(SegDir(), 0o700); err != nil {
		return res, err
	}

	months := make([]string, 0, len(byMonth))
	for m := range byMonth {
		months = append(months, m)
	}
	sort.Strings(months)

	for _, m := range months {
		n, err := sealMonth(m, byMonth[m], &res)
		if err != nil {
			return res, err
		}
		if n > 0 {
			res.Months++
		}
	}
	return res, nil
}

func sealMonth(month string, runIDs []string, res *SealResult) (int, error) {
	existing, err := LoadIndex(month)
	if err != nil {
		return 0, err
	}
	already := map[string]bool{}
	for _, e := range existing {
		already[e.RunID] = true
	}

	seg, err := os.OpenFile(segPath(month), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	defer seg.Close()
	off, err := seg.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, err
	}

	idx, err := os.OpenFile(idxPath(month), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	defer idx.Close()

	sort.Strings(runIDs)
	sealed := 0
	for _, runID := range runIDs {
		if already[runID] {
			// Already sealed but the loose file survived a previous crash.
			// Removing it is the completion of that earlier Seal, not a new one.
			_ = os.Remove(Path(runID))
			continue
		}
		raw, err := os.ReadFile(Path(runID))
		if err != nil {
			continue // vanished under us; nothing to seal
		}
		before := onDiskSize(Path(runID))

		var buf strings.Builder
		enc, err := zstd.NewWriter(&stringWriter{&buf}, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
		if err != nil {
			return sealed, err
		}
		if _, err := enc.Write(raw); err != nil {
			enc.Close()
			return sealed, err
		}
		if err := enc.Close(); err != nil {
			return sealed, err
		}
		frame := []byte(buf.String())
		if _, err := seg.Write(frame); err != nil {
			return sealed, err
		}
		entry := IndexEntry{RunID: runID, Off: off, Len: int64(len(frame)), Events: countLines(raw)}
		line, err := json.Marshal(entry)
		if err != nil {
			return sealed, err
		}
		if _, err := fmt.Fprintln(idx, string(line)); err != nil {
			return sealed, err
		}
		off += int64(len(frame))

		// Index and segment are durable before the loose file goes. The order
		// matters: a crash between them leaves a duplicate to skip, while the
		// reverse would lose the run outright.
		if err := seg.Sync(); err != nil {
			return sealed, err
		}
		if err := idx.Sync(); err != nil {
			return sealed, err
		}
		_ = os.Remove(Path(runID))
		_ = os.RemoveAll(EditsDir(runID))

		res.Runs++
		res.Events += entry.Events
		res.BytesBefore += before
		res.BytesAfter += int64(len(frame))
		sealed++
	}
	return sealed, nil
}

// stringWriter adapts strings.Builder to io.Writer for the encoder.
type stringWriter struct{ b *strings.Builder }

func (w *stringWriter) Write(p []byte) (int, error) { return w.b.Write(p) }

func countLines(raw []byte) int {
	n := 0
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

// onDiskSize reports blocks actually charged, not the logical length — the
// whole point of sealing is the gap between them.
func onDiskSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return blocksFor(info)
}

// LoadIndex reads one month's index. A missing index is not an error.
func LoadIndex(month string) ([]IndexEntry, error) {
	f, err := os.Open(idxPath(month))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []IndexEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e IndexEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

// Months lists sealed segments, newest first.
func Months() ([]string, error) {
	entries, err := os.ReadDir(SegDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".idx") {
			out = append(out, strings.TrimSuffix(e.Name(), ".idx"))
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out, nil
}

// loadSealed returns a sealed run's events, or false if no segment holds it.
func loadSealed(runID string) ([]Event, bool, error) {
	m := monthOf(runID)
	if m == "" {
		return nil, false, nil
	}
	idx, err := LoadIndex(m)
	if err != nil {
		return nil, false, err
	}
	for _, e := range idx {
		if e.RunID != runID {
			continue
		}
		f, err := os.Open(segPath(m))
		if err != nil {
			return nil, false, err
		}
		defer f.Close()
		sec := io.NewSectionReader(f, e.Off, e.Len)
		dec, err := zstd.NewReader(sec)
		if err != nil {
			return nil, false, err
		}
		defer dec.Close()
		raw, err := io.ReadAll(dec)
		if err != nil {
			return nil, false, err
		}
		events, _, err := parseEvents(raw)
		return events, true, err
	}
	return nil, false, nil
}

// SealCandidates lists loose runs older than age. Seal calls it to choose what
// to fold in, so `--dry-run` reporting the same list is the same code, not a
// mirror of it that could drift.
//
// Not to be confused with LiveRuns in heartbeat.go, which reports runs that are
// currently *active*; these are the opposite, the ones old enough to archive.
func SealCandidates(age time.Duration) ([]string, error) {
	entries, err := os.ReadDir(Dir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	cutoff := time.Now().Add(-age)
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".jsonl")
		if monthOf(id) == "" {
			continue
		}
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}
