// SPDX-License-Identifier: MIT

package trust

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/ankit373/hydra/internal/drift"
)

// snapshotSchema is the calibration snapshot file's schema version. 2 added
// each cell's change detector; an older file is ignored and the log replayed.
const snapshotSchema = 2

// snapshotThreshold: replay at least this many records before checkpointing.
const snapshotThreshold = 200

// snapshotEntry is one (source, domain) confusion-matrix row.
type snapshotEntry struct {
	Source string  `json:"source"`
	Domain string  `json:"domain"`
	TP     float64 `json:"tp"`
	FP     float64 `json:"fp"`
	TN     float64 `json:"tn"`
	FN     float64 `json:"fn"`

	Drift   *drift.Detector `json:"drift"`
	Seen    int64           `json:"seen"`
	Changed time.Time       `json:"changed,omitzero"`
}

// snapshotFile is a checkpoint: the confusion-matrix state as of a byte offset
// into the jsonl log, so load() only has to replay what's after it.
type snapshotFile struct {
	Schema  int              `json:"schema"`
	Offset  int64            `json:"offset"`
	Steps   map[string]int64 `json:"steps"`
	Entries []snapshotEntry  `json:"entries"`
}

func snapshotPath(jsonlPath string) string {
	return jsonlPath + ".snapshot"
}

// loadSnapshot never errors, missing/corrupt/out-of-bounds/misaligned offset
// just means "no usable checkpoint," so load() falls back to a full replay.
func loadSnapshot(path string, jsonl *os.File, jsonlSize int64) (store map[calibKey]*confusion, steps map[string]int64, offset int64, ok bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, 0, false
	}
	var snap snapshotFile
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, nil, 0, false
	}
	if snap.Schema != snapshotSchema || snap.Offset < 0 || snap.Offset > jsonlSize {
		return nil, nil, 0, false
	}
	// A valid checkpoint must land exactly on a line boundary; otherwise
	// load() would seek mid-line and silently drop the split record.
	if snap.Offset > 0 {
		var b [1]byte
		if _, err := jsonl.ReadAt(b[:], snap.Offset-1); err != nil || b[0] != '\n' {
			return nil, nil, 0, false
		}
	}
	store = make(map[calibKey]*confusion, len(snap.Entries))
	for _, e := range snap.Entries {
		// Written through keyFor, so a key appears once. A cell with no usable
		// detector means the file is not one this build wrote: replay instead.
		if e.Drift == nil || len(e.Drift.Segments) == 0 {
			return nil, nil, 0, false
		}
		store[keyFor(e.Source, e.Domain)] = &confusion{TP: e.TP, FP: e.FP, TN: e.TN, FN: e.FN,
			drift: e.Drift, seen: e.Seen, changed: e.Changed}
	}
	return store, snap.Steps, snap.Offset, true
}

// saveSnapshot writes temp-then-renames for atomicity; a write failure is
// non-fatal, it just costs the next load a full replay.
func saveSnapshot(path string, store map[calibKey]*confusion, steps map[string]int64, offset int64) error {
	entries := make([]snapshotEntry, 0, len(store))
	for k, c := range store {
		entries = append(entries, snapshotEntry{Source: k.source, Domain: k.domain, TP: c.TP, FP: c.FP, TN: c.TN, FN: c.FN,
			Drift: c.drift, Seen: c.seen, Changed: c.changed})
	}
	data, err := json.Marshal(snapshotFile{Schema: snapshotSchema, Offset: offset, Steps: steps, Entries: entries})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
