// SPDX-License-Identifier: MIT

package dispatch

import (
	"time"

	"github.com/ankit373/hydra/internal/embed"
)

// FlushTimeout bounds how long Close waits for queued embeddings. Short
// because it is paid after the answer has been handed back: what is still in
// flight past it is dropped and counted, never waited on indefinitely.
const FlushTimeout = 3 * time.Second

// recorder lazily builds the embedding recorder, at most once per Dispatcher.
//
// Lazy because resolving a model and opening a store costs a machine that never
// dispatches nothing, and because d.heads is already discovered by the time any
// dispatch runs.
func (d *Dispatcher) recorder() *embed.Recorder {
	d.embedOnce.Do(func() {
		emb, st, err := embed.Open(d.cfg, d.heads)
		if err != nil || st == nil {
			// No model, or a store that will not open. Both mean record
			// nothing, and neither is worth failing a dispatch over.
			d.embed = embed.NewRecorder(embed.Unavailable{}, nil)
			return
		}
		d.embed = embed.NewRecorder(emb, st)
	})
	return d.embed
}

// captureEmbedding queues the task prompt for embedding under its span.
//
// The prompt alone, not the response: this vector is a query key, and what a
// cache or a classifier is handed at lookup time is a prompt. Returns at once
// whatever the model is doing.
func (d *Dispatcher) captureEmbedding(span, prompt string) {
	if !embed.Enabled(d.cfg) {
		return
	}
	d.recorder().Record(span, prompt)
}

// Close drains anything queued for embedding, bounded by FlushTimeout.
//
// A CLI process does one dispatch and exits, so without this the work is queued
// and the process dies before the worker reaches it: the store would stay empty
// forever while every surface reported capture as on.
func (d *Dispatcher) Close() {
	if d == nil || !embed.Enabled(d.cfg) {
		return
	}
	d.recorder().Close(FlushTimeout)
}
