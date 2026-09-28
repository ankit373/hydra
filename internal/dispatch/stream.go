// SPDX-License-Identifier: MIT

package dispatch

import "github.com/ankit373/hydra/internal/provider"

// StreamKind names what happened during a dispatch.
//
// Three, and no success kind: Dispatch returning a Result *is* the success
// signal, and Result.Attempts already records the chain afterwards.
type StreamKind int

const (
	// StreamAttemptStarted fires before a candidate runs, so a surface can
	// open a block for it and show something during prefill, which on a local
	// 7B is several seconds before any token exists.
	StreamAttemptStarted StreamKind = iota
	// StreamDelta carries output as it arrives.
	StreamDelta
	// StreamAttemptFailed fires when a candidate did not answer and the chain
	// moved on. It is the reason this is an event and not a bare
	// func(string): a head can stream 200 tokens and then fail, and text
	// alone gives a surface no way to tell that partial from the answer.
	StreamAttemptFailed
)

// StreamEvent is one thing that happened while a dispatch ran.
type StreamEvent struct {
	Kind StreamKind
	Head provider.Head
	Tier int

	// Text is set on StreamDelta only.
	Text string
	// Reason is set on StreamAttemptFailed only.
	Reason string

	// SpanID identifies the attempt, so an abandoned partial stays reachable
	// as `hyctl trace view <run-id> --span <id>` after a surface has collapsed
	// it away. Only dispatch knows it.
	SpanID string
}

// OnStream receives events in order, from the goroutine driving the executor,
// never concurrently with itself. It must not block for long: a slow consumer
// stalls the read loop and inflates the measured duration.
//
// A surface that renders deltas must not also print Result.Output, or the
// answer appears twice. executor.Stream guarantees the deltas carry the whole
// output even for a head that cannot stream, which is what makes that safe.
type OnStream func(StreamEvent)
