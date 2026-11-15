// SPDX-License-Identifier: MIT

package workflow

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ankit373/hydra/internal/util"
)

// StepResult is what running one step produced. Deliberately not
// dispatch.Result: the runner is tested without a model, and depending on the
// router's concrete type here would make that impossible.
type StepResult struct {
	Output  string
	Head    string
	Model   string
	Tier    int
	CostUSD float64
}

// Router runs one step's prompt. internal/dispatch satisfies this via the
// adapter in cmd/hydra; a test satisfies it with a stub.
type Router interface {
	Route(ctx context.Context, prompt, enum string) (StepResult, error)
}

// Saver persists progress. Injected so a test can assert that state was written
// *before* each step ran, which is the property that makes a killed workflow
// resumable rather than lost.
type Saver func(Workflow) error

// maxParallel caps steps in flight at once: enough for side-by-side steps,
// few enough that one workflow does not trip a provider's rate limit.
const maxParallel = 4

// finished is one step's outcome, handed back to the goroutine that owns w.
type finished struct {
	i     int
	res   StepResult
	err   error
	took  time.Duration
	ended time.Time
}

// Run executes every step that is not done, each once all its needs are.
//
// State is saved before each step and again after it, so a process killed at
// any point leaves a record that says which steps were in flight. A failed step
// stops new launches: steps already running finish and are recorded, and its
// dependents stay pending for a resume. Only this goroutine touches w or calls
// save, so saves never interleave.
//
// The returned Workflow is the final state, saved. An error means a step failed
// or could not be persisted; the workflow is still resumable in both cases.
func Run(ctx context.Context, w Workflow, r Router, save Saver) (Workflow, error) {
	// The heartbeat is what tells a later reader this run is alive: without
	// it a killed run reads as running forever and nothing says it needs
	// resuming (#898). Claiming it is also what refuses a second runner,
	// which dispatched every remaining step a second time (#1162).
	stopBeat, err := heartbeat(w.ID)
	if err != nil {
		return w, fmt.Errorf("%w: %s is being run by another process; continue it once that one stops, or at most %s after its last heartbeat",
			err, w.ID, beatTimeout)
	}
	defer stopBeat()

	if len(w.Steps) == 0 {
		return w, ErrNoSteps
	}
	if r == nil {
		return w, fmt.Errorf("workflow %s: no router", w.ID)
	}
	// A stored record is a file anyone can edit, and a need nothing meets would never launch.
	if err := checkNeeds(w.Steps); err != nil {
		return w, fmt.Errorf("workflow %s: %w", w.ID, err)
	}
	if save == nil {
		save = Save
	}

	results := make(chan finished)
	launched := make([]bool, len(w.Steps))
	inflight := 0
	var stopped error
	for {
		if stopped == nil && ctx.Err() == nil {
			if err := launchReady(ctx, &w, r, save, launched, &inflight, results); err != nil {
				stopped = err
			}
		}
		if inflight == 0 {
			break
		}
		f := <-results
		inflight--
		if err := record(&w, f, save); err != nil && stopped == nil {
			stopped = err
		}
	}

	if stopped != nil {
		return w, stopped
	}
	if _, left := w.Next(); left {
		// Cancelled between steps: the record says these are pending, so a
		// resume picks them up rather than losing their place.
		w.Status = Pending
		_ = save(w)
		return w, ctx.Err()
	}
	w.Status = Done
	if err := save(w); err != nil {
		return w, err
	}
	return w, nil
}

// launchReady starts every ready step, up to maxParallel in flight, after
// saving them as running.
func launchReady(ctx context.Context, w *Workflow, r Router, save Saver, launched []bool, inflight *int, results chan<- finished) error {
	var batch []int
	for i := range w.Steps {
		if *inflight+len(batch) == maxParallel {
			break
		}
		if !launched[i] && w.Steps[i].Status != Done && w.ready(i) {
			batch = append(batch, i)
		}
	}
	if len(batch) == 0 {
		return nil
	}
	w.Status = Running
	for _, i := range batch {
		launched[i] = true
		w.Steps[i].Status = Running
		w.Steps[i].StartedAt = time.Now().UTC().Format(time.RFC3339Nano)
		w.Steps[i].Err = ""
	}
	// Written BEFORE the steps run. Saving only afterwards would lose the fact
	// that they were in flight, which is exactly what a resume needs (#736).
	if err := save(*w); err != nil {
		return fmt.Errorf("workflow %s: cannot record step %d before running it: %w", w.ID, batch[0]+1, err)
	}
	for _, i := range batch {
		prompt, enum := w.stepPrompt(i), w.Steps[i].Enum
		*inflight++
		go func() {
			start := time.Now()
			res, err := r.Route(ctx, prompt, enum)
			results <- finished{i: i, res: res, err: err, took: time.Since(start), ended: time.Now()}
		}()
	}
	return nil
}

// record writes one step's outcome into w and saves it.
func record(w *Workflow, f finished, save Saver) error {
	s := &w.Steps[f.i]
	s.DurationMS = f.took.Milliseconds()
	s.EndedAt = f.ended.UTC().Format(time.RFC3339Nano)
	if f.err != nil {
		s.Status = Failed
		s.Err = f.err.Error()
		w.Status = Failed
		_ = save(*w)
		return fmt.Errorf("workflow %s stopped at step %d (%s): %w", w.ID, f.i+1, s.Title, f.err)
	}
	s.Status = Done
	s.Output = f.res.Output
	s.Head = f.res.Head
	s.Model = f.res.Model
	s.Tier = f.res.Tier
	s.CostUSD = f.res.CostUSD
	if err := save(*w); err != nil {
		return fmt.Errorf("workflow %s: step %d ran but its result could not be saved: %w", w.ID, f.i+1, err)
	}
	return nil
}

// stepPrompt is step i's prompt with the output of every step it needs as
// context. Without this a workflow is a list of unrelated prompts, and with
// only the immediate predecessor a step could not read one two back (#1168).
//
// Each output is fenced, because a workflow exists to route a cheap head's step
// into a stronger one's, and unfenced it is a model writing the next model's
// instructions. a2a already fences exactly this (#740).
func (w Workflow) stepPrompt(i int) string {
	var prior []Step
	for _, n := range w.Needs(i) {
		if s := w.Steps[n-1]; strings.TrimSpace(s.Output) != "" {
			prior = append(prior, s)
		}
	}
	if len(prior) == 0 {
		return w.Steps[i].Prompt
	}
	var b strings.Builder
	fmt.Fprintf(&b, "This is step %d of %d in the task: %s\n\n", i+1, len(w.Steps), w.Task)
	for _, p := range prior {
		label := fmt.Sprintf("STEP %d OUTPUT (%s)", p.N, util.SafeTerminal(p.Title))
		b.WriteString(util.WrapUntrusted(label, p.Output))
		b.WriteString("\n\n")
	}
	b.WriteString(w.Steps[i].Prompt)
	return b.String()
}
