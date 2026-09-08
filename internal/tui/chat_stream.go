// SPDX-License-Identifier: MIT

package tui

import (
	"fmt"
	"strings"
	"sync"

	"github.com/ankit373/hydra/internal/dispatch"
)

// ckLiveCapBytes bounds the retained stream. The tail is kept rather than the
// head: while a task runs, the newest text is what is being watched, and a
// live view that froze once a long answer passed the cap would be worse than
// one that admits it clipped.
const ckLiveCapBytes = 16 << 10

// ckAbandoned is one attempt that produced output and then failed.
//
// The text is kept rather than discarded, which is what makes expanding a
// collapsed partial free: it is already in hand and nothing is re-fetched.
type ckAbandoned struct {
	head   string
	reason string
	span   string
	text   string
	chars  int
	clip   bool
	open   bool // expanded in the log
}

// ckStream is what a running task has produced so far. The worker goroutine
// writes it and the render reads it, so every access takes the lock. Its zero
// value is usable, which is why it is a value field on ckExecState rather than
// a pointer, tests build bare &ckExecState{} literals.
type ckStream struct {
	mu   sync.Mutex
	head string
	live strings.Builder
	clip bool
	gone []ckAbandoned
}

// handler adapts the stream to dispatch.Options.OnStream. Nil-safe on the
// receiver so a task with no stream state simply does not stream.
func (s *ckStream) handler() dispatch.OnStream {
	if s == nil {
		return nil
	}
	return func(ev dispatch.StreamEvent) {
		switch ev.Kind {
		case dispatch.StreamAttemptStarted:
			s.start(ev.Head.Name)
		case dispatch.StreamDelta:
			s.delta(ev.Text)
		case dispatch.StreamAttemptFailed:
			s.fail(ev.Reason, ev.SpanID)
		}
	}
}

// start opens a new attempt. The previous attempt's text is dropped only if it
// was never abandoned, a succeeded attempt's output arrives as the answer.
func (s *ckStream) start(head string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.head = head
	s.live.Reset()
	s.clip = false
}

func (s *ckStream) delta(text string) {
	if text == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.live.WriteString(text)
	if s.live.Len() > ckLiveCapBytes {
		kept := s.live.String()
		kept = kept[len(kept)-ckLiveCapBytes:]
		s.live.Reset()
		s.live.WriteString(kept)
		s.clip = true
	}
}

// fail moves the attempt's partial into the collapsed list and clears the live
// buffer, so the next head's tokens never mix with text that is not the answer.
func (s *ckStream) fail(reason, span string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	text := s.live.String()
	s.gone = append(s.gone, ckAbandoned{
		head: s.head, reason: ckStreamReason(reason), span: span,
		text: text, chars: len(text), clip: s.clip,
	})
	s.live.Reset()
	s.clip = false
}

// snapshot is the attempt in flight: its head and the text so far.
func (s *ckStream) snapshot() (head, text string, clipped bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.head, s.live.String(), s.clip
}

// abandoned copies the collapsed list out for the UI to own. Copied rather
// than shared because the UI toggles `open` on its own copy, and mutating the
// worker's slice from the render loop is how a race gets written.
func (s *ckStream) abandoned() []ckAbandoned {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.gone) == 0 {
		return nil
	}
	out := make([]ckAbandoned, len(s.gone))
	copy(out, s.gone)
	return out
}

// ckStreamEntries renders the live attempt and every collapsed partial as log
// entries, so they wrap, clip and scroll exactly like the rest of the log
// instead of needing their own geometry.
//
// An abandoned partial is one dim line unless it is open. It is deliberately
// not styled as an answer: the whole point of collapsing it is that it is not
// one.
func ckStreamEntries(gone []ckAbandoned, head, live string, clipped bool) []string {
	var out []string
	for i, a := range gone {
		hint := ""
		if i == len(gone)-1 && !a.open {
			hint = ckFaintS.Render(" · e expands")
		}
		out = append(out, ckDimS.Render(fmt.Sprintf("⤺ %s abandoned after ~%d chars: %s",
			a.head, a.chars, a.reason))+hint)
		if a.open {
			body := a.text
			if a.clip {
				body = ckFaintS.Render("…(clipped)\n") + body
			}
			out = append(out, ckFaintS.Render(body))
		}
	}
	if live != "" {
		out = append(out, ckAquaS.Render("▍ ")+ckDimS.Render(head))
		body := live
		if clipped {
			body = ckFaintS.Render("…(clipped)\n") + body
		}
		// The caret says the stream is still open, which is the difference
		// between a short answer and one still arriving.
		out = append(out, ckInkS.Render(body)+ckAquaS.Render("▏"))
	}
	return out
}

// ckMergeAbandoned takes the newly abandoned attempts from fresh without
// discarding the expand state the user has already set on the older ones. The
// worker's copy never carries `open`, so replacing the slice wholesale would
// silently re-collapse a partial someone is reading.
func ckMergeAbandoned(have, fresh []ckAbandoned) []ckAbandoned {
	out := make([]ckAbandoned, len(fresh))
	copy(out, fresh)
	for i := range have {
		if i < len(out) {
			out[i].open = have[i].open
		}
	}
	return out
}

// displayLog is the scrollback plus whatever is happening right now: the
// collapsed partials and the attempt in flight.
//
// Composed at render time rather than appended into t.log, because both change
// under the render's feet, a delta arrives every ~20ms, and a log entry once
// written is permanent.
func (t *ckThread) displayLog() []string {
	var head, live string
	var clipped bool
	if t.exec != nil {
		head, live, clipped = t.exec.stream.snapshot()
	}
	extra := ckStreamEntries(t.gone, head, live, clipped)
	if len(extra) == 0 {
		return t.log
	}
	out := make([]string, 0, len(t.log)+len(extra))
	return append(append(out, t.log...), extra...)
}

// ckStreamReason is a failure reduced to one line for a collapsed row. A
// stack or a multi-line provider error would push the answer off the screen,
// which is the opposite of collapsing.
func ckStreamReason(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return truncate(strings.TrimSpace(s), 120)
}

// toggleNewestAbandoned opens or closes the most recent collapsed partial,
// which is the one the user just watched disappear.
func (t *ckThread) toggleNewestAbandoned() bool {
	if n := len(t.gone); n > 0 {
		t.gone[n-1].open = !t.gone[n-1].open
		return true
	}
	return false
}
