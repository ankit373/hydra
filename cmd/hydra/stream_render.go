// SPDX-License-Identifier: MIT

package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/term"

	"github.com/ankit373/hydra/internal/dispatch"
)

// streamRenderer draws a dispatch as it arrives.
//
// Only used when stdout is a terminal. A pipe keeps the buffered rendering it
// has always had, byte for byte, so a script that parses hyctl's output cannot
// be broken by an interactive nicety.
type streamRenderer struct {
	w      io.Writer
	runID  string
	width  int
	height int
	// recoverable says the partial can actually be read back. Without payload
	// capture the span still opens but reports the text is not stored, so
	// offering the command would send someone to an empty page.
	recoverable bool

	mu sync.Mutex
	// row is where the cursor is, counted from the first row this attempt
	// drew. Collapsing an abandoned partial means moving up exactly this many
	// and erasing down, so it is a cursor position rather than a line tally.
	row int
	col int
	// rowsTrusted goes false once something was printed whose width this
	// cannot know: a rune that may render wider than one cell, or an escape
	// sequence. Erasing on a guessed row count corrupts the scrollback of a
	// terminal someone was reading, so the guess disables collapsing instead.
	rowsTrusted bool
	head        string
	spin        *spinner
	open        bool // the body is open, so a delta has arrived
	chars       int
}

func newStreamRenderer(w io.Writer, runID string, width, height int, recoverable bool) *streamRenderer {
	return &streamRenderer{
		w: w, runID: runID, width: width, height: height, recoverable: recoverable,
	}
}

// Handle turns one dispatch event into terminal output. Safe to pass as
// dispatch.Options.OnStream.
func (r *streamRenderer) Handle(ev dispatch.StreamEvent) {
	switch ev.Kind {
	case dispatch.StreamAttemptStarted:
		r.attemptStarted(ev)
	case dispatch.StreamDelta:
		r.delta(ev.Text)
	case dispatch.StreamAttemptFailed:
		r.attemptFailed(ev)
	}
}

func (r *streamRenderer) attemptStarted(ev dispatch.StreamEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// A terminal narrower than the 58-column rule wraps the header rows this
	// counts as one each, so the row model is wrong before any delta arrives.
	r.col, r.chars, r.open, r.rowsTrusted = 0, 0, false, r.width >= 60
	r.head = ev.Head.Name
	fmt.Fprintln(r.w)
	r.row = 1
	// The spinner animates the wait before the first token, which is the part
	// that looked like nothing happening: 4.2s of prefill, measured. It draws
	// on the row the cursor is already on, so the row count does not move.
	r.spin = startSpinner(r.w, "  "+cortexStyle.Render("▶")+" "+dimStyle.Render(r.head)+"  ")
}

func (r *streamRenderer) delta(text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.open {
		// Reprints the header over the spinner's own row, then opens the body.
		// Deferred to here rather than printed up front because the numbers
		// that used to be on that line do not exist until the call returns.
		r.stopSpinnerLocked()
		fmt.Fprintf(r.w, "  %s %s\n", cortexStyle.Render("▶"), dimStyle.Render(r.head))
		fmt.Fprintln(r.w, dimStyle.Render("  "+strings.Repeat("─", 56)))
		fmt.Fprintln(r.w)
		r.row += 3
		r.open = true
	}
	fmt.Fprint(r.w, text)
	r.advance(text)
	r.chars += len(text)
}

// advance moves the row/column model over text. Exact for single-width runes,
// which is what a model's prose overwhelmingly is.
func (r *streamRenderer) advance(text string) {
	for _, ru := range text {
		switch {
		case ru == '\n':
			r.row++
			r.col = 0
		case ru == '\r':
			r.col = 0
		default:
			// 0x1100 is the conventional start of the wide ranges, and an
			// escape means the rest of the sequence is not printable width.
			if ru >= 0x1100 || ru == 0x1b {
				r.rowsTrusted = false
			}
			r.col++
			if r.width > 0 && r.col >= r.width {
				r.row++
				r.col = 0
			}
		}
	}
}

func (r *streamRenderer) attemptFailed(ev dispatch.StreamEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopSpinnerLocked()
	// Collapse in place only when the partial is still on screen and the row
	// count is trustworthy. Otherwise the marker goes below it, which says the
	// same thing without touching what has already scrolled.
	if r.rowsTrusted && r.row > 0 && r.row < r.height-2 {
		fmt.Fprintf(r.w, "\r\033[%dA\033[J", r.row)
	} else if r.col > 0 {
		fmt.Fprintln(r.w)
	}
	fmt.Fprintf(r.w, "\n  %s %s\n",
		warnStyle.Render("⤺ abandoned"),
		dimStyle.Render(fmt.Sprintf("%s after ~%d chars: %s", r.head, r.chars, firstLine(ev.Reason))))
	// Only worth offering when there is a partial and it was actually stored.
	if r.chars > 0 && r.recoverable {
		fmt.Fprintf(r.w, "    %s\n", dimStyle.Render(
			"hyctl trace view "+r.runID+" --span "+ev.SpanID))
	}
	r.row, r.col, r.chars, r.open = 0, 0, 0, false
}

// Finish closes the body and prints the numbers that only exist once the call
// has returned, which is why they are a footer here and a header when the
// output is not streamed.
func (r *streamRenderer) Finish(in, out int, dur, ttft time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopSpinnerLocked()
	if r.col > 0 {
		fmt.Fprintln(r.w)
	}
	stats := fmt.Sprintf("%d→%d tokens  %s", in, out, dur.Round(time.Millisecond))
	// Zero means unknown, never instant, so it is left out rather than shown
	// as 0ms.
	if ttft > 0 {
		stats += fmt.Sprintf("  ttft %s", ttft.Round(time.Millisecond))
	}
	fmt.Fprintf(r.w, "\n  %s\n\n", dimStyle.Render(stats))
}

// Streamed reports whether any output was drawn, so a caller knows not to
// print Result.Output as well and show the answer twice.
func (r *streamRenderer) Streamed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.open
}

// Stop clears the spinner and gives the line back. Needed because a head can
// answer with no deltas at all, an empty response from a CLI head is the real
// case, and the buffered rendering would then print over a spinner still
// ticking on its own goroutine.
func (r *streamRenderer) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopSpinnerLocked()
}

func (r *streamRenderer) stopSpinnerLocked() {
	if r.spin == nil {
		return
	}
	r.spin.stop()
	r.spin = nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return truncateTo(strings.TrimSpace(s), 100)
}

func truncateTo(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// spinner animates one line in place until stopped. It gets its own goroutine
// because the thing it reports on is a blocking read.
type spinner struct {
	w      io.Writer
	stopCh chan struct{}
	done   chan struct{}
}

var spinFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func startSpinner(w io.Writer, prefix string) *spinner {
	s := &spinner{w: w, stopCh: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(s.done)
		t := time.NewTicker(80 * time.Millisecond)
		defer t.Stop()
		for i := 0; ; i++ {
			select {
			case <-s.stopCh:
				return
			case <-t.C:
				fmt.Fprintf(w, "\r%s%s", prefix, dimStyle.Render(spinFrames[i%len(spinFrames)]))
			}
		}
	}()
	return s
}

// stop halts the animation, waits for the goroutine, and clears the line it
// was using. Without the wait a final frame can land after the caller has
// started printing over that row.
func (s *spinner) stop() {
	close(s.stopCh)
	<-s.done
	fmt.Fprint(s.w, "\r\033[K")
}

// terminalSize reports the usable stdout geometry, with a conservative
// fallback: an 80x24 assumption makes the collapse guard refuse rather than
// erase rows it cannot see, which is the safe direction to be wrong in.
func terminalSize() (width, height int) {
	w, h, err := term.GetSize(os.Stdout.Fd())
	if err != nil || w <= 0 || h <= 0 {
		return 80, 24
	}
	return w, h
}
