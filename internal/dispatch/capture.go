// SPDX-License-Identifier: MIT

package dispatch

import (
	"math/rand"

	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/payload"
)

// capturePayloads stores a dispatch's prompt and response and returns the refs
// to put on the span. Off unless opted into at `hyctl init`.
//
// Best-effort in both directions: a capture failure must never fail the work,
// and a dispatch that answered must never be reported as having no output just
// because the store could not take it.
func (d *Dispatcher) capturePayloads(prompt string, opts Options, resp *executor.Response) (inRef, outRef string) {
	if d.cfg == nil || !d.cfg.CapturePayloads {
		return "", ""
	}
	keep := payload.KeepRate(d.cfg)
	// Sampled stores stay correctable only if the draw is recorded, which Put
	// does, but a payload that loses the draw is simply not stored (#605).
	if keep < 1 && rand.Float64() >= keep {
		return "", ""
	}
	store, err := payload.Open(payload.Dir())
	if err != nil {
		return "", ""
	}
	store.SetBudget(payload.Budget(d.cfg))

	// Split before hashing: the system prompt is byte-identical across every
	// dispatch in a session, so addressing it apart from the task is what lets
	// it be stored once instead of once per call.
	segs := []payload.Segment{
		{Label: "system", Content: opts.System},
		{Label: "prompt", Content: prompt},
	}
	if ref, err := store.PutSegments(segs, keep); err == nil {
		inRef = ref
	}
	if resp != nil && resp.Output != "" {
		if ref, err := store.PutSegments([]payload.Segment{{Label: "response", Content: resp.Output}}, keep); err == nil {
			outRef = ref
		}
	}
	return inRef, outRef
}
