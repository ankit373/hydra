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
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ankit373/hydra/internal/a2a"
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
	// ErrAlreadyRunning refuses a second runner over a live workflow: both
	// would dispatch every remaining step (#1162).
	ErrAlreadyRunning = errors.New("workflow is already running")
)

// Status is where a step or a whole workflow has got to.
type Status string

const (
	Pending Status = "pending"
	Running Status = "running"
	Done    Status = "done"
	Failed  Status = "failed"
	// Interrupted is a reader's verdict and is never stored: the run was
	// recorded as running and the process that recorded it is gone (#898).
	Interrupted Status = "interrupted"
)

// Step is one unit of a workflow: a prompt, its own routing hint, and what
// happened when it ran.
type Step struct {
	N      int      `json:"n"` // 1-based, the order the user sees
	Title  string   `json:"title"`
	Prompt string   `json:"prompt"`
	Enum   string   `json:"enum,omitempty"` // routing hint for THIS step
	Tools  []string `json:"tools,omitempty"`
	Kind   string   `json:"kind,omitempty"`  // kind of work, the record a failed check's rerun climbs on
	Check  []string `json:"check,omitempty"` // dispatch --check specs this step's answer must pass
	// Needs are the step numbers this step waits for and reads. nil, as in a
	// record from before #1591, means every earlier step; empty means none.
	Needs []int `json:"needs"`
	// Writes are what the step writes, as a2a keys: file:<path>, doc:<name>#<section>,
	// record:<table>/<id>. Two steps that write the same one never run together.
	Writes []string `json:"writes,omitempty"`
	// Reads are the hashes of the blocks this step is given, beyond its needs.
	Reads []string `json:"reads,omitempty"`

	Status Status `json:"status"`
	Head   string `json:"head,omitempty"`
	Model  string `json:"model,omitempty"`
	Tier   int    `json:"tier,omitempty"`
	Output string `json:"output,omitempty"`
	Err    string `json:"err,omitempty"`
	// Escalated says a failed check reran the step on a stronger head than FirstHead.
	Escalated bool   `json:"escalated,omitempty"`
	FirstHead string `json:"first_head,omitempty"`
	// Checked says the step had checks and every one ran on the answer it kept;
	// false for a step with none, or one whose check could not run.
	Checked bool `json:"checked"`

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

	// What the run was started under, persisted so a resume continues it
	// rather than inventing new settings: the record carried none, so a
	// resumed workflow ran with no system prompt and no --local at all
	// (#1168, #1161). A nil MaxCostUSD is "no explicit ceiling", which
	// policy.yaml then decides, and is why it is a pointer.
	System     string   `json:"system,omitempty"`
	LocalOnly  bool     `json:"local_only,omitempty"`
	MaxCostUSD *float64 `json:"max_cost_usd,omitempty"`

	// Coupled says two files are linked in the code graph, so writing both at
	// once is a clash too. Set by the caller when a graph exists; never stored.
	Coupled func(fileA, fileB string) bool `json:"-"`
	// Blocks is the request's context store, keyed by hash. Kept with the
	// record so a resume sees the same blocks, and deleted with it.
	Blocks map[string]Block `json:"blocks,omitempty"`
}

// Observed is the status to show a reader. The stored one, except that a run
// recorded as running whose heartbeat has stopped is interrupted: nothing is
// running, and `hyctl workflow resume` is what picks it up.
//
// Read-only by design. A reader that repaired the stored status would race the
// writer it is trying to describe.
func (w Workflow) Observed() Status {
	if w.Status == Running && !alive(w.ID) {
		return Interrupted
	}
	return w.Status
}

// ObservedStep is the same verdict for the step that was in flight: it is not
// running either, and labelling it so is what sends someone looking for a
// process that exited.
func ObservedStep(step, run Status) Status {
	if step == Running && run == Interrupted {
		return Interrupted
	}
	return step
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

// Needs is what step i waits for, with nil read as every earlier step.
func (w Workflow) Needs(i int) []int {
	if n := w.Steps[i].Needs; n != nil {
		return n
	}
	all := make([]int, i)
	for j := range all {
		all[j] = j + 1
	}
	return all
}

// Chain reports whether every step waits for every earlier one, the order
// steps ran in before they could name their needs.
func (w Workflow) Chain() bool {
	for i := range w.Steps {
		if len(w.Needs(i)) != i {
			return false
		}
	}
	return true
}

// ready reports whether every step that step i needs is done.
func (w Workflow) ready(i int) bool {
	for _, n := range w.Needs(i) {
		if w.Steps[n-1].Status != Done {
			return false
		}
	}
	return true
}

// clashes reports whether steps i and j write the same thing, or two files the
// code graph couples.
func (w Workflow) clashes(i, j int) bool {
	a, b := w.Steps[i].Writes, w.Steps[j].Writes
	if a2a.Overlap(a, b) {
		return true
	}
	if w.Coupled == nil {
		return false
	}
	for _, x := range a {
		fx, ok := strings.CutPrefix(a2a.Key(x), "file:")
		if !ok {
			continue
		}
		for _, y := range b {
			if fy, ok := strings.CutPrefix(a2a.Key(y), "file:"); ok && w.Coupled(fx, fy) {
				return true
			}
		}
	}
	return false
}

// checkNeeds refuses a need no run could meet. Needs point backwards only, so
// the typed order is always a valid run order and there is no cycle to find.
func checkNeeds(steps []Step) error {
	for i, s := range steps {
		for _, n := range s.Needs {
			switch {
			case n == i+1:
				return fmt.Errorf("step %d needs itself", i+1)
			case n > i+1 && n <= len(steps):
				return fmt.Errorf("step %d needs step %d, which comes after it", i+1, n)
			case n < 1 || n > len(steps):
				return fmt.Errorf("step %d needs step %d, which does not exist", i+1, n)
			}
		}
	}
	return nil
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

// nowFunc is a var so a test can pin the clock. The guard below exists for
// clocks too coarse to tick between two calls, which a real one will not do
// on demand.
var nowFunc = time.Now

var created struct {
	sync.Mutex
	last time.Time
}

// List orders on Created, so two workflows must never share one. Formatting to
// nanoseconds does not achieve that: Windows' clock can hand consecutive calls
// the same instant, and the comparison then falls back to ids, which is not an
// order in time at all. Step past the last stamp issued instead (#856).
func createdStamp() string {
	created.Lock()
	defer created.Unlock()
	now := nowFunc().UTC()
	if !now.After(created.last) {
		now = created.last.Add(time.Nanosecond)
	}
	created.last = now
	return now.Format(time.RFC3339Nano)
}

// New builds an unsaved workflow from step prompts.
func New(id, task string, steps []Step) (Workflow, error) {
	if len(steps) == 0 {
		return Workflow{}, ErrNoSteps
	}
	now := createdStamp()
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
	if err := checkNeeds(w.Steps); err != nil {
		return Workflow{}, err
	}
	// Written out, so the stored record and `workflow show` say what each step waits for.
	for i := range w.Steps {
		n := slices.Clone(w.Needs(i))
		slices.Sort(n)
		w.Steps[i].Needs = slices.Compact(n)
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

// Exists reports whether an id already has a stored record. `run --id` of one
// overwrote it and corrupted the trace of whatever was using it; resume
// continues a stored workflow and rm replaces it (#1168).
func Exists(id string) (bool, error) {
	path, err := Path(id)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
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
		// Parsed, not compared as strings: RFC3339Nano trims trailing zeros, so
		// a whole-second stamp ends in "Z" where a sub-second one has "." at the
		// same offset, and 'Z' > '.' sorts the older of the two as newer (#856).
		ti, erri := time.Parse(time.RFC3339Nano, out[i].Created)
		tj, errj := time.Parse(time.RFC3339Nano, out[j].Created)
		if erri == nil && errj == nil && !ti.Equal(tj) {
			return ti.After(tj)
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
