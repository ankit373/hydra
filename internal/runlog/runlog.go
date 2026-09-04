// SPDX-License-Identifier: MIT

// Package runlog is Hydra's append-only record of what happened during a run.
//
// The other logs answer "what did this cost" and "what was the verdict".
// This one answers "what happened, in what order, and under whom" — the
// lifecycle events a timeline or supervision-tree view is reconstructed from.
// Nothing else in Hydra carries that: a2a's handoff file keeps only the latest
// hop, the MCP ledger is never called from the dispatch pipeline, and
// dispatch/cost rows are per-call outcomes with no start/finish or parentage.
//
// # One file per run
//
// Events live in ~/.hydra/logs/runs/<run_id>.jsonl rather than one global file.
// That gives retention for free (delete whole files; never compact one that is
// being appended to), bounds reconstruction cost to a single run however long
// Hydra has been in use, and means a reader tailing one run never has to skip
// another's events.
//
// # Ordering
//
// File order is the authority, not timestamps. Concurrent goroutines can
// produce equal or inverted wall-clock values at typical resolution, so Load
// returns events in the order they were appended. Seq is a per-writer counter
// carried for gap detection and display; it is not the sort key, and two
// processes appending to one run (an external orchestrator sharing a run ID)
// will legitimately produce interleaved sequences.
//
// # Concurrency
//
// Append does one os.OpenFile(O_APPEND) + one Fprintln, the same pattern every
// other jsonl writer in this codebase uses without a mutex. POSIX makes an
// O_APPEND write atomic, within and across processes, so concurrent writers
// cannot tear each other's lines. Keep entries small — the guarantee is about
// a single write() call, so bulk payloads (diffs, full model output) must be
// referenced via Ref, never inlined.
package runlog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ankit373/hydra/internal/config"
)

// SchemaVersion is stamped on every event. It exists so the terminal cockpit
// and the desktop app — two independent readers of this format — can branch on
// a version rather than guess, and so a format change is a one-line reader
// change instead of a coordinated migration.
const SchemaVersion = 1

// Kind is what happened. The set is deliberately small: these are the events a
// timeline and a supervision tree are built from, not a general trace facility.
type Kind string

const (
	KindRunStarted   Kind = "run_started"
	KindRunFinished  Kind = "run_finished"
	KindTaskStarted  Kind = "task_started"
	KindTaskFinished Kind = "task_finished"

	// KindHeadSelected records the routing decision itself — which head won and
	// why — which no existing log captures.
	KindHeadSelected Kind = "head_selected"

	KindDispatchStarted  Kind = "dispatch_started"
	KindDispatchFinished Kind = "dispatch_finished"

	// KindAttempt is one head's execution inside a swarm or SPRT ensemble.
	KindAttempt Kind = "attempt"

	// KindSample is one source's contribution to an SPRT run, carrying the
	// per-source timestamp trust.Evidence lacks.
	KindSample Kind = "sample"

	// KindHandoff is an A2A context handoff. a2a keeps only the newest in
	// last_handoff.json; appending it here is what makes a chain reconstructable.
	KindHandoff Kind = "handoff"

	// KindQuestionAsked is a task parked waiting on a human. Detail carries the
	// question, which Session's Timeline already renders, so a parked task is
	// visible with no new view.
	KindQuestionAsked Kind = "question_asked"

	// KindEdit is a file edit. File is the path changed; Ref points at the
	// content, which is never inlined.
	KindEdit Kind = "edit"

	KindError Kind = "error"
)

// Event is one record. Fields are omitempty so a line stays small — the atomic
// append guarantee is per write() call, not per arbitrary size.
type Event struct {
	V   int    `json:"v"`
	Seq uint64 `json:"seq"`
	TS  string `json:"ts"` // display only; ordering comes from file position

	RunID  string `json:"run_id"`
	TaskID string `json:"task_id,omitempty"`
	Kind   Kind   `json:"kind"`

	// Agent is this node's id in the supervision tree; Parent is its ownership
	// edge. Together they reconstruct the tree — separate from any A2A
	// collaboration edge, which is a KindHandoff event.
	Agent  string `json:"agent,omitempty"`
	Parent string `json:"parent,omitempty"`

	Head   string `json:"head,omitempty"`
	Model  string `json:"model,omitempty"`
	Tier   int    `json:"tier,omitempty"`
	Status string `json:"status,omitempty"`

	CostUSD    float64 `json:"cost_usd,omitempty"`
	DurationMS int64   `json:"duration_ms,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`

	// Ref points at bulk content held elsewhere (a path, a hash). Detail is a
	// short human string. Neither may carry a diff or a full model response.
	Ref    string `json:"ref,omitempty"`
	Detail string `json:"detail,omitempty"`

	// File is the path a KindEdit event changed. Its own field, not Agent —
	// an edit has no identity of its own, only a task it happened within
	// (#434); conflating the two used to mint a phantom tree node per file.
	File string `json:"file,omitempty"`
}

// Dir is where per-run files live.
func Dir() string { return filepath.Join(config.Dir(), "logs", "runs") }

// Path is the log file for a run.
func Path(runID string) string { return filepath.Join(Dir(), runID+".jsonl") }

// Logger appends events for one run. The zero value is unusable; use New.
// A Logger is safe for concurrent use.
type Logger struct {
	runID string
	path  string
	seq   atomic.Uint64
}

// New returns a Logger for runID. It does not create the file — that happens on
// the first Append, so a run that never logs leaves nothing behind.
func New(runID string) *Logger {
	return &Logger{runID: runID, path: Path(runID)}
}

// RunID reports which run this Logger writes.
func (l *Logger) RunID() string { return l.runID }

// Append writes one event. It stamps V, Seq, TS, and RunID, so callers supply
// only what happened.
//
// Errors are returned but callers may reasonably ignore them: losing an
// observability event must never fail the work being observed.
func (l *Logger) Append(e Event) error {
	e.V = SchemaVersion
	e.Seq = l.seq.Add(1)
	if e.TS == "" {
		e.TS = time.Now().UTC().Format(time.RFC3339Nano)
	}
	e.RunID = l.runID

	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()

	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	// One Fprintln = one write() = atomic under O_APPEND. Do not split this.
	_, err = fmt.Fprintln(f, string(raw))
	return err
}

// Load reads a run's events in append order, from the live file if it is still
// loose and from its sealed segment otherwise. A missing run yields no events
// and no error — a run that logged nothing is not a failure.
//
// Sealing is invisible here on purpose: internal/tree, the cockpit and the
// desktop app all call this, and none of them should learn about segments.
func Load(runID string) ([]Event, error) {
	events, _, err := LoadCounted(Path(runID))
	if err != nil {
		return nil, err
	}
	if len(events) > 0 {
		return events, nil
	}
	sealed, ok, serr := loadSealed(runID)
	if serr != nil || !ok {
		return events, nil
	}
	return sealed, nil
}

// LoadCounted is Load for an explicit path, plus the number of unparseable
// lines skipped. An append-only record that silently drops entries is worse
// than useless for reconstruction — a crash mid-write leaves a truncated tail,
// and a reader that hides it will render an incomplete run as a complete one.
func LoadCounted(path string) ([]Event, int, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	defer f.Close()

	raw, err := io.ReadAll(f)
	if err != nil {
		return nil, 0, err
	}
	return parseEvents(raw)
}

// parseEvents decodes an events file. Shared by the live and sealed readers so
// a sealed run cannot parse differently from a loose one.
func parseEvents(raw []byte) ([]Event, int, error) {
	var (
		events  []Event
		skipped int
	)
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			skipped++
			continue
		}
		events = append(events, e)
	}
	return events, skipped, sc.Err()
}

// Runs lists known run IDs, newest first. Filenames are timestamp-prefixed
// (see internal/runid), so lexical descending order is chronological.
func Runs() ([]string, error) {
	entries, err := os.ReadDir(Dir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		ids = append(ids, strings.TrimSuffix(e.Name(), ".jsonl"))
	}
	// Sealed runs are still runs. A reader listing only loose files would show
	// history silently shrinking as retention advances.
	months, err := Months()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, id := range ids {
		seen[id] = true
	}
	for _, m := range months {
		idx, err := LoadIndex(m)
		if err != nil {
			return nil, err
		}
		for _, e := range idx {
			if !seen[e.RunID] {
				seen[e.RunID] = true
				ids = append(ids, e.RunID)
			}
		}
	}
	// IDs are timestamp-prefixed, so lexical descending is chronological.
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	return ids, nil
}
