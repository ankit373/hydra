// SPDX-License-Identifier: MIT

package api

import "github.com/ankit373/hydra/internal/dispatch"

// ChatStreamEventName is the event every chat delta arrives on.
const ChatStreamEventName = "chat:stream"

// Emit sends an event to the frontend. main.go supplies it, being the only
// file that knows Wails exists, and it is nil in tests: a dropped delta must
// not fail a dispatch that is otherwise working.
type Emit func(name string, payload any)

// ChatStreamEvent is one thing that happened while a chat dispatch ran.
//
// Its own wire type rather than dispatch.StreamEvent, which carries a
// provider.Head and an integer kind. Neither should become a UI contract.
type ChatStreamEvent struct {
	RunID string `json:"runID"`
	// Kind is "started", "delta" or "failed".
	Kind string `json:"kind"`
	Head string `json:"head"`
	Tier int    `json:"tier"`

	Text   string `json:"text,omitempty"`
	Reason string `json:"reason,omitempty"`
	SpanID string `json:"spanID,omitempty"`

	// Offset is how many bytes of this attempt arrived before Text, so a view
	// that missed an event can tell a duplicate (offset below what it holds)
	// from a gap (offset above it) rather than rendering either as answer.
	Offset int `json:"offset"`

	// Recoverable says an abandoned partial can be read back. Without payload
	// capture the span still opens but stores no text, so offering it would
	// send someone to an empty page. Same guard the CLI renderer uses.
	Recoverable bool `json:"recoverable"`
}

// chatStream adapts dispatch's events to the wire, counting the bytes of the
// attempt in flight so the frontend can place each delta.
type chatStream struct {
	emit        Emit
	runID       string
	recoverable bool
	offset      int
}

func (c *chatStream) on(ev dispatch.StreamEvent) {
	out := ChatStreamEvent{
		RunID:       c.runID,
		Head:        ev.Head.ID,
		Tier:        ev.Tier,
		SpanID:      ev.SpanID,
		Recoverable: c.recoverable,
	}
	switch ev.Kind {
	case dispatch.StreamAttemptStarted:
		// A new attempt replaces an abandoned one's text rather than
		// continuing it, so the byte count starts again with it.
		c.offset = 0
		out.Kind = "started"
	case dispatch.StreamDelta:
		out.Kind, out.Text, out.Offset = "delta", ev.Text, c.offset
		c.offset += len(ev.Text)
	case dispatch.StreamAttemptFailed:
		out.Kind, out.Reason, out.Offset = "failed", ev.Reason, c.offset
	default:
		// An unknown kind is a newer dispatch than this build. Dropping it
		// beats emitting an event the frontend will read as a delta.
		return
	}
	if c.emit != nil {
		c.emit(ChatStreamEventName, out)
	}
}
