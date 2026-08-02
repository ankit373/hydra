// SPDX-License-Identifier: MIT

package runlog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MaxSnapshotBytes caps one side of one edit snapshot.
//
// A snapshot store must not be able to fill a disk because a model was pointed
// at a 200 MB file. Above the cap the edit is still logged — losing the *event*
// would hide that a change happened at all — but its content is not stored and
// the ref resolves to "too large", which a reader can render honestly.
const MaxSnapshotBytes = 4 << 20 // 4 MiB

// ErrSnapshotTooLarge reports content above MaxSnapshotBytes.
var ErrSnapshotTooLarge = errors.New("runlog: edit snapshot exceeds the size cap")

// ErrNoSnapshot reports a ref with no stored content — pruned, never written,
// or refused for size. Distinct from a read failure so a caller can say "diff
// unavailable" rather than "cannot read disk".
var ErrNoSnapshot = errors.New("runlog: no snapshot for this ref")

// EditsDir is where a run's edit snapshots live. Per-run, like the event log
// itself, so retention is deleting a directory rather than compacting a store.
func EditsDir(runID string) string { return filepath.Join(Dir(), runID+".edits") }

// SaveEdit stores one edit's before/after content and returns the ref to put on
// the event. Bulk content never goes in the event itself: the run log's
// atomicity guarantee is per write() call, so entries must stay small.
//
// ref is caller-chosen and must be filesystem-safe; SaveEdit rejects anything
// containing a separator so a ref can never escape the run's directory.
func SaveEdit(runID, ref string, before, after []byte) error {
	if err := validRef(ref); err != nil {
		return err
	}
	if len(before) > MaxSnapshotBytes || len(after) > MaxSnapshotBytes {
		return ErrSnapshotTooLarge
	}
	dir := EditsDir(runID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// 0600: an edit snapshot is a verbatim copy of the user's source.
	if err := os.WriteFile(filepath.Join(dir, ref+".before"), before, 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ref+".after"), after, 0o600)
}

// LoadEdit returns a stored edit's before/after content.
func LoadEdit(runID, ref string) (before, after []byte, err error) {
	if err := validRef(ref); err != nil {
		return nil, nil, err
	}
	dir := EditsDir(runID)
	before, err = os.ReadFile(filepath.Join(dir, ref+".before"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, ErrNoSnapshot
		}
		return nil, nil, err
	}
	after, err = os.ReadFile(filepath.Join(dir, ref+".after"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, ErrNoSnapshot
		}
		return nil, nil, err
	}
	return before, after, nil
}

// validRef rejects anything that could address a file outside the run's own
// edits directory. Refs come from event data, which a reader must treat as
// untrusted — a run log can be written by anything sharing the run id.
func validRef(ref string) error {
	if ref == "" {
		return fmt.Errorf("runlog: empty edit ref")
	}
	if strings.ContainsAny(ref, `/\`) || strings.Contains(ref, "..") {
		return fmt.Errorf("runlog: unsafe edit ref %q", ref)
	}
	return nil
}
