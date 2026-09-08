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

// Run executes the workflow from its first incomplete step.
//
// State is saved before each step and again after it, so a process killed at
// any point leaves a record that says which step was in flight. A step that
// fails stops the workflow: later steps consume earlier output, so continuing
// past a failure builds on a gap.
//
// The returned Workflow is the final state, saved. An error means a step failed
// or could not be persisted; the workflow is still resumable in both cases.
func Run(ctx context.Context, w Workflow, r Router, save Saver) (Workflow, error) {
	if len(w.Steps) == 0 {
		return w, ErrNoSteps
	}
	if r == nil {
		return w, fmt.Errorf("workflow %s: no router", w.ID)
	}
	if save == nil {
		save = Save
	}

	for {
		i, ok := w.Next()
		if !ok {
			break
		}
		if err := ctx.Err(); err != nil {
			// Cancelled between steps: the record already says this step is
			// pending, so it resumes here rather than losing its place.
			w.Status = Pending
			_ = save(w)
			return w, err
		}

		w.Status = Running
		w.Steps[i].Status = Running
		w.Steps[i].StartedAt = time.Now().UTC().Format(time.RFC3339Nano)
		w.Steps[i].Err = ""
		// Written BEFORE the step runs. Saving only afterwards would lose the
		// fact that this step was in flight, which is exactly what a resume
		// needs to know (#736).
		if err := save(w); err != nil {
			return w, fmt.Errorf("workflow %s: cannot record step %d before running it: %w", w.ID, i+1, err)
		}

		start := time.Now()
		res, err := r.Route(ctx, w.stepPrompt(i), w.Steps[i].Enum)
		w.Steps[i].DurationMS = time.Since(start).Milliseconds()
		w.Steps[i].EndedAt = time.Now().UTC().Format(time.RFC3339Nano)

		if err != nil {
			w.Steps[i].Status = Failed
			w.Steps[i].Err = err.Error()
			w.Status = Failed
			_ = save(w)
			return w, fmt.Errorf("workflow %s stopped at step %d (%s): %w",
				w.ID, i+1, w.Steps[i].Title, err)
		}

		w.Steps[i].Status = Done
		w.Steps[i].Output = res.Output
		w.Steps[i].Head = res.Head
		w.Steps[i].Model = res.Model
		w.Steps[i].Tier = res.Tier
		w.Steps[i].CostUSD = res.CostUSD
		if err := save(w); err != nil {
			return w, fmt.Errorf("workflow %s: step %d ran but its result could not be saved: %w", w.ID, i+1, err)
		}
	}

	w.Status = Done
	if err := save(w); err != nil {
		return w, err
	}
	return w, nil
}

// stepPrompt is step i's prompt with the previous step's output as context.
// Without this a workflow is a list of unrelated prompts rather than a chain.
//
// The previous output is fenced, because a workflow exists to route a cheap
// head's step into a stronger one's, and unfenced it is a model writing the
// next model's instructions. a2a already fences exactly this (#740).
func (w Workflow) stepPrompt(i int) string {
	if i == 0 {
		return w.Steps[i].Prompt
	}
	prev := w.Steps[i-1]
	if strings.TrimSpace(prev.Output) == "" {
		return w.Steps[i].Prompt
	}
	var b strings.Builder
	fmt.Fprintf(&b, "This is step %d of %d in the task: %s\n\n", i+1, len(w.Steps), w.Task)
	label := fmt.Sprintf("STEP %d OUTPUT (%s)", prev.N, util.SafeTerminal(prev.Title))
	b.WriteString(util.WrapUntrusted(label, prev.Output))
	b.WriteString("\n\n")
	b.WriteString(w.Steps[i].Prompt)
	return b.String()
}
