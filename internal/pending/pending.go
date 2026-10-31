// SPDX-License-Identifier: MIT

// Package pending stores tasks parked waiting on a human answer.
//
// A parked task is not a suspended process. Every executor runs its tool in
// one-shot batch mode, so "pause" is two ordinary dispatches: one that stops
// before invoking the executor and records a question, and a later one that
// dispatches normally with the answer folded in. This package is the durable
// handoff between them.
package pending

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ankit373/hydra/internal/config"
)

// MaxPending bounds the queue. Abandoned questions are never auto-discarded,
// dropping one would silently lose work someone is waiting on, so the bound is
// enforced by refusing to park anything new, loudly, rather than by pruning.
const MaxPending = 100

// ErrQueueFull is returned by Save once MaxPending questions are outstanding.
var ErrQueueFull = errors.New("pending question queue is full")

// ErrNotFound is returned by Load and Delete for an unknown task.
var ErrNotFound = errors.New("no pending question for task")

// Question is everything needed to re-dispatch a parked task, plus what to ask.
//
// The original prompt and options are stored rather than re-derived: the head
// list, policy and budget can all shift between the question and the answer,
// and the task the user answered must be the task that runs.
type Question struct {
	TaskID   string    `json:"taskId"`
	RunID    string    `json:"runId"`
	Question string    `json:"question"`
	Prompt   string    `json:"prompt"`
	Head     string    `json:"head"`
	Resource string    `json:"resource,omitempty"`
	Enum     string    `json:"enum,omitempty"`
	TierHint string    `json:"tierHint,omitempty"`
	System   string    `json:"system,omitempty"`
	AskedAt  time.Time `json:"askedAt"`

	LocalOnly bool `json:"localOnly,omitempty"`
	MaxTokens int  `json:"maxTokens,omitempty"`
}

// Dir is where parked tasks live, alongside the run logs.
func Dir() string { return filepath.Join(config.Dir(), "logs", "pending") }

// idOK constrains a task ID to one path segment. Task IDs reach Path from an
// explicit option, an env var and (with #583) a UI field, so treating one as a
// filename without checking is a traversal waiting to happen.
var idOK = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

func validID(taskID string) error {
	if !idOK.MatchString(taskID) || strings.Trim(taskID, ".") == "" {
		return fmt.Errorf("invalid task id %q", taskID)
	}
	return nil
}

// Path is the file for one parked task.
func Path(taskID string) (string, error) {
	if err := validID(taskID); err != nil {
		return "", err
	}
	return filepath.Join(Dir(), taskID+".json"), nil
}

// Save parks a task. The write is temp-then-rename so a crash mid-write cannot
// leave a half-written question that later parses as a different one.
func Save(q Question) error {
	path, err := Path(q.TaskID)
	if err != nil {
		return err
	}
	if q.AskedAt.IsZero() {
		q.AskedAt = time.Now().UTC()
	}
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return err
	}
	// Counted before writing, and only for a task not already parked, so
	// re-parking an existing task cannot be refused by its own entry.
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		n, cErr := count()
		if cErr != nil {
			return cErr
		}
		if n >= MaxPending {
			return fmt.Errorf("%w (%d outstanding): answer or clear one before parking another", ErrQueueFull, n)
		}
	}

	raw, err := json.MarshalIndent(q, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(Dir(), "."+q.TaskID+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }() // no-op once renamed
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// Load reads one parked task.
//
// It fails loudly on a malformed file and never returns a partial Question: a
// caller that resumed on a zero value would dispatch the task with no answer
// folded in, turning an unreadable question into a silent approval.
func Load(taskID string) (Question, error) {
	path, err := Path(taskID)
	if err != nil {
		return Question{}, err
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Question{}, fmt.Errorf("%w %s", ErrNotFound, taskID)
	}
	if err != nil {
		return Question{}, err
	}
	var q Question
	if err := json.Unmarshal(raw, &q); err != nil {
		return Question{}, fmt.Errorf("parked task %s is unreadable (%s): %w", taskID, path, err)
	}
	if q.TaskID == "" || q.Prompt == "" {
		return Question{}, fmt.Errorf("parked task %s is incomplete (%s): missing taskId or prompt", taskID, path)
	}
	return q, nil
}

// List returns every parked task, oldest question first.
//
// A file that cannot be read is reported in the error but does not suppress the
// ones that can: hiding every outstanding question because one is corrupt is
// how a parked task gets forgotten. Callers should surface both.
func List() ([]Question, error) {
	entries, err := os.ReadDir(Dir())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Question
	var bad []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		q, lErr := Load(strings.TrimSuffix(e.Name(), ".json"))
		if lErr != nil {
			bad = append(bad, lErr.Error())
			continue
		}
		out = append(out, q)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AskedAt.Before(out[j].AskedAt) })
	if len(bad) > 0 {
		return out, fmt.Errorf("%d unreadable parked task(s): %s", len(bad), strings.Join(bad, "; "))
	}
	return out, nil
}

// Delete removes a parked task. Consuming the file is what makes answering
// idempotent: the second resume of the same task finds nothing to resume.
func Delete(taskID string) error {
	path, err := Path(taskID)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w %s", ErrNotFound, taskID)
		}
		return err
	}
	return nil
}

func count() (int, error) {
	entries, err := os.ReadDir(Dir())
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			n++
		}
	}
	return n, nil
}
