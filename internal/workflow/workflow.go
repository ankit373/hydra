// SPDX-License-Identifier: MIT

// Package workflow runs an ordered list of steps, each routed on its own.
//
// The point is per-step routing: triage on a cheap head and the fix on a strong
// one, rather than one tier for the whole task (#736). State is written before
// each step runs, so a process killed mid-workflow leaves something resumable
// instead of a lost run.
package workflow

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

// Max is the ceiling on stored workflows. Refusing new work is better than
// pruning: a discarded workflow is work somebody is waiting on, the same
// reasoning as pending.MaxPending.
const Max = 200

var (
	// ErrNotFound is returned for an id with no stored workflow.
	ErrNotFound = errors.New("no such workflow")
	// ErrFull is returned when Max workflows are already stored.
	ErrFull = errors.New("workflow store is full")
	// ErrNoSteps refuses a workflow with nothing to run, which would otherwise
	// persist and report success having done nothing.
	ErrNoSteps = errors.New("a workflow needs at least one step")
)

// Status is where a step or a whole workflow has got to.
type Status string

const (
	Pending Status = "pending"
	Running Status = "running"
	Done    Status = "done"
	Failed  Status = "failed"
)

// Step is one unit of a workflow: a prompt, its own routing hint, and what
// happened when it ran.
type Step struct {
	N      int      `json:"n"` // 1-based, the order the user sees
	Title  string   `json:"title"`
	Prompt string   `json:"prompt"`
	Enum   string   `json:"enum,omitempty"` // routing hint for THIS step
	Tools  []string `json:"tools,omitempty"`

	Status Status `json:"status"`
	Head   string `json:"head,omitempty"`
	Model  string `json:"model,omitempty"`
	Tier   int    `json:"tier,omitempty"`
	Output string `json:"output,omitempty"`
	Err    string `json:"err,omitempty"`

	CostUSD    float64 `json:"cost_usd"`
	DurationMS int64   `json:"duration_ms"`
	StartedAt  string  `json:"started_at,omitempty"`
	EndedAt    string  `json:"ended_at,omitempty"`
}

// Workflow is the persisted record of a multi-step task.
type Workflow struct {
	ID      string `json:"id"`
	Task    string `json:"task"`
	Created string `json:"created"`
	Updated string `json:"updated"`
	Status  Status `json:"status"`
	RunID   string `json:"run_id,omitempty"`
	Steps   []Step `json:"steps"`
}

// CostUSD totals what the workflow has spent so far.
func (w Workflow) CostUSD() float64 {
	var t float64
	for _, s := range w.Steps {
		t += s.CostUSD
	}
	return t
}

// Progress counts completed steps.
func (w Workflow) Progress() (done, total int) {
	for _, s := range w.Steps {
		if s.Status == Done {
			done++
		}
	}
	return done, len(w.Steps)
}

// Next returns the index of the first step still to run, and false when none is
// left. A step that already produced output is never returned, which is what
// makes resuming idempotent rather than a second charge for work already paid
// for.
func (w Workflow) Next() (int, bool) {
	for i, s := range w.Steps {
		if s.Status == Done {
			continue
		}
		return i, true
	}
	return 0, false
}

// Dir is where workflows are stored.
func Dir() string { return filepath.Join(config.Dir(), "logs", "workflows") }

var idOK = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// Path is the file backing one workflow. The id is validated rather than
// trusted: it lands in a filesystem path, so "../../x" must not resolve.
func Path(id string) (string, error) {
	if !idOK.MatchString(id) {
		return "", fmt.Errorf("invalid workflow id %q: allowed characters are letters, digits, dot, dash, underscore", id)
	}
	return filepath.Join(Dir(), id+".json"), nil
}

// New builds an unsaved workflow from step prompts.
func New(id, task string, steps []Step) (Workflow, error) {
	if len(steps) == 0 {
		return Workflow{}, ErrNoSteps
	}
	// Nano, not second, precision: List orders on Created, and two workflows
	// started in the same second would otherwise fall back to an id comparison,
	// so "newest first" stopped being true exactly when a fleet was busy.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	w := Workflow{ID: id, Task: task, Created: now, Updated: now, Status: Pending}
	for i, s := range steps {
		s.N = i + 1
		if s.Status == "" {
			s.Status = Pending
		}
		if strings.TrimSpace(s.Title) == "" {
			s.Title = firstLine(s.Prompt)
		}
		w.Steps = append(w.Steps, s)
	}
	return w, nil
}

// Save writes the workflow atomically: temp file then rename, so a reader never
// sees a half-written record and a crash mid-write leaves the previous state
// rather than a truncated one.
func Save(w Workflow) error {
	path, err := Path(w.ID)
	if err != nil {
		return err
	}
	if len(w.Steps) == 0 {
		return ErrNoSteps
	}
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return err
	}
	// Counted only for an id not already stored, so saving progress on a running
	// workflow can never be refused by its own entry.
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		n, cErr := count()
		if cErr != nil {
			return cErr
		}
		if n >= Max {
			return fmt.Errorf("%w (%d stored): remove one before starting another", ErrFull, n)
		}
	}
	w.Updated = time.Now().UTC().Format(time.RFC3339Nano)

	raw, err := json.MarshalIndent(w, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(Dir(), "."+w.ID+".*.tmp")
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

// Load reads one workflow.
//
// It fails loudly on a malformed or stepless file and never returns a partial
// Workflow: resuming a zero value would report a finished workflow that ran
// nothing, which is worse than refusing to resume at all.
func Load(id string) (Workflow, error) {
	path, err := Path(id)
	if err != nil {
		return Workflow{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Workflow{}, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return Workflow{}, err
	}
	var w Workflow
	if err := json.Unmarshal(raw, &w); err != nil {
		return Workflow{}, fmt.Errorf("workflow %s is unreadable (%v); it is not resumable, inspect %s", id, err, path)
	}
	if w.ID == "" || len(w.Steps) == 0 {
		return Workflow{}, fmt.Errorf("workflow %s is incomplete (no id or no steps); it is not resumable, inspect %s", id, path)
	}
	return w, nil
}

// List returns every stored workflow, newest first.
func List() ([]Workflow, error) {
	entries, err := os.ReadDir(Dir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Workflow
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		w, err := Load(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			// One unreadable file must not hide every other workflow. Load
			// refuses it; List keeps going and the reader still sees the rest.
			continue
		}
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Created != out[j].Created {
			return out[i].Created > out[j].Created
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}

// Delete removes a workflow.
func Delete(id string) error {
	path, err := Path(id)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return err
	}
	return nil
}

func count() (int, error) {
	entries, err := os.ReadDir(Dir())
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	var n int
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			n++
		}
	}
	return n, nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	const max = 60
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
