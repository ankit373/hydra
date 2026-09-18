// SPDX-License-Identifier: MIT

package dispatch

import (
	"time"

	"github.com/ankit373/hydra/internal/retrieve"
)

// FlushTimeout bounds how long Close waits for queued capture. Short because it
// is paid after the answer has been handed back: what is still in flight past
// it is dropped and counted, never waited on indefinitely.
const FlushTimeout = 3 * time.Second

// recorder lazily builds the capture path, at most once per Dispatcher.
//
// Lazy because a machine that never dispatches should pay nothing, and because
// d.heads is already discovered by the time any dispatch runs.
func (d *Dispatcher) recorder() *retrieve.Recorder {
	d.recallOnce.Do(func() {
		s, err := retrieve.Open(d.cfg, d.heads)
		if err != nil {
			// A store that will not open means record nothing, which is not
			// worth failing a dispatch over.
			d.recall = retrieve.NewRecorder(nil)
			return
		}
		d.recall = retrieve.NewRecorder(s)
	})
	return d.recall
}

// captureRecall queues the task prompt for indexing and embedding.
//
// The prompt alone, not the response: what a cache or a classifier is handed at
// lookup time is a prompt, so that is what the stored document has to be.
// Returns at once whatever the model is doing.
func (d *Dispatcher) captureRecall(span, prompt string) {
	if !retrieve.Enabled(d.cfg) {
		return
	}
	d.recorder().Record(span, prompt)
}

// Close drains anything queued for capture, bounded by FlushTimeout.
//
// A CLI process does one dispatch and exits, so without this the work is queued
// and the process dies before the workers reach it: the stores would stay empty
// forever while every surface reported capture as on.
func (d *Dispatcher) Close() {
	if d == nil || !retrieve.Enabled(d.cfg) {
		return
	}
	d.recorder().Close(FlushTimeout)
}
