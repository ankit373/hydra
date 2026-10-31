// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/swarm"
	"github.com/ankit373/hydra/internal/trust"
)

func panelHeads(ids ...string) []provider.Head {
	var out []provider.Head
	for _, id := range ids {
		out = append(out, provider.Head{ID: id, Name: id})
	}
	return out
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

// plainLines strips the styling and the cursor moves, leaving what a reader
// actually sees.
func plainLines(s string) []string {
	return strings.Split(strings.TrimRight(ansi.ReplaceAllString(s, ""), "\n"), "\n")
}

// lastFrame returns the panel as it stands after the writes so far: every
// repaint erases the one before it, so only the final block is on screen.
func lastFrame(buf *bytes.Buffer) []string {
	parts := strings.Split(buf.String(), "\x1b[J")
	return plainLines(parts[len(parts)-1])
}

func newTestPanel(buf *bytes.Buffer, title string) *ensemblePanel {
	// No animate(): a ticker repainting mid-assertion would make every test
	// here a race against a 100ms timer.
	return newEnsemblePanel(buf, 80, title)
}

// The roster is drawn before anything runs, so the wait has a shape from the
// first moment rather than after the first head answers.
func TestEnsemblePanel_QueuedThenRunningThenDone(t *testing.T) {
	var buf bytes.Buffer
	p := newTestPanel(&buf, "swarm · best")

	p.Handle(swarm.Progress{Kind: swarm.ProgressSelected, Heads: panelHeads("alpha", "beta")})
	frame := lastFrame(&buf)
	if !strings.Contains(strings.Join(frame, "\n"), "queued") {
		t.Errorf("roster frame = %q, want both heads shown as queued", frame)
	}

	p.Handle(swarm.Progress{Kind: swarm.ProgressStarted, Head: provider.Head{ID: "alpha"}})
	buf.Reset()
	p.Handle(swarm.Progress{Kind: swarm.ProgressFinished,
		Head:    provider.Head{ID: "alpha"},
		Attempt: swarm.Attempt{Status: swarm.StatusOK, Duration: 1500 * time.Millisecond, InputTokens: 300, OutputTokens: 112}})

	joined := strings.Join(lastFrame(&buf), "\n")
	for _, want := range []string{"alpha", "1.5s", "412 tok", "beta", "queued"} {
		if !strings.Contains(joined, want) {
			t.Errorf("frame missing %q:\n%s", want, joined)
		}
	}
}

// A head that failed must read as failed. Leaving it on the spinner is the
// failure mode this panel would otherwise introduce: a run that is over still
// looking like it is working.
func TestEnsemblePanel_FailedHeadIsNotLeftRunning(t *testing.T) {
	var buf bytes.Buffer
	p := newTestPanel(&buf, "swarm · all")

	p.Handle(swarm.Progress{Kind: swarm.ProgressSelected, Heads: panelHeads("dead")})
	p.Handle(swarm.Progress{Kind: swarm.ProgressStarted, Head: provider.Head{ID: "dead"}})
	buf.Reset()
	p.Handle(swarm.Progress{Kind: swarm.ProgressFinished,
		Head:    provider.Head{ID: "dead"},
		Attempt: swarm.Attempt{Status: swarm.StatusTimeout, Duration: 300 * time.Millisecond}})

	joined := strings.Join(lastFrame(&buf), "\n")
	if !strings.Contains(joined, string(swarm.StatusTimeout)) {
		t.Errorf("frame does not say how the head ended:\n%s", joined)
	}
	if !strings.Contains(joined, "✗") {
		t.Errorf("frame does not mark the failure:\n%s", joined)
	}
	for _, f := range panelSpinner {
		if strings.Contains(joined, f) {
			t.Errorf("frame still shows the spinner frame %q for a finished head:\n%s", f, joined)
		}
	}
}

// The SPRT footer is the reason this beats a spinner: it says how far the
// evidence has moved and how far it still has to go.
func TestEnsemblePanel_SPRTFooterShowsLambdaAgainstTheThreshold(t *testing.T) {
	var buf bytes.Buffer
	p := newTestPanel(&buf, "ensemble · target 95.0% confidence")

	p.Handle(swarm.Progress{Kind: swarm.ProgressSelected, Heads: panelHeads("qwen")})
	// The order the run reports in: a head answers, and only then is its
	// answer weighed against the others.
	p.Handle(swarm.Progress{Kind: swarm.ProgressFinished, Head: provider.Head{ID: "qwen"},
		Attempt: swarm.Attempt{Status: swarm.StatusOK, Duration: time.Second}})
	buf.Reset()
	p.Handle(swarm.Progress{Kind: swarm.ProgressEvidence, Threshold: 2.944, Evidence: trust.Evidence{
		Source: "qwen", Agreed: true, LambdaAfter: 2.31, ConfidenceAfter: 0.91,
	}})

	joined := strings.Join(lastFrame(&buf), "\n")
	for _, want := range []string{"+2.31", "+2.94", "91.0%", "agrees"} {
		if !strings.Contains(joined, want) {
			t.Errorf("footer missing %q:\n%s", want, joined)
		}
	}
}

// Before anything is weighed there is no Λ, and a 0.00 there would read as a
// measurement rather than as nothing yet.
func TestEnsemblePanel_NoLambdaUntilSomethingIsWeighed(t *testing.T) {
	var buf bytes.Buffer
	p := newTestPanel(&buf, "ensemble · target 95.0% confidence")
	p.Handle(swarm.Progress{Kind: swarm.ProgressSelected, Heads: panelHeads("qwen")})

	if joined := strings.Join(lastFrame(&buf), "\n"); strings.Contains(joined, "need") {
		t.Errorf("frame claims a threshold before any evidence:\n%s", joined)
	}
}

// Every repaint erases the one before it by moving up the lines it drew. If
// that count is wrong the panel eats the scrollback above it, or leaves a
// trail of stale frames behind.
func TestEnsemblePanel_ErasesExactlyWhatItDrew(t *testing.T) {
	var buf bytes.Buffer
	p := newTestPanel(&buf, "swarm · best")
	p.Handle(swarm.Progress{Kind: swarm.ProgressSelected, Heads: panelHeads("a", "b", "c")})

	drawn := strings.Count(buf.String(), "\n")
	buf.Reset()
	p.Handle(swarm.Progress{Kind: swarm.ProgressStarted, Head: provider.Head{ID: "a"}})

	up := regexp.MustCompile(`\x1b\[(\d+)A`).FindStringSubmatch(buf.String())
	if up == nil {
		t.Fatal("repaint did not move the cursor back over the frame it replaces")
	}
	if up[1] != strconv.Itoa(drawn) {
		t.Errorf("moved up %s lines, drew %d", up[1], drawn)
	}

	p.Stop()
	if !strings.Contains(buf.String(), "\x1b[J") {
		t.Error("Stop left the panel on screen; the result block would print every head twice")
	}
}

// A wrapped row makes the line count wrong, and every erase after it wrong
// with it. Long names and a narrow terminal are exactly when that happens.
func TestEnsemblePanel_RowsNeverExceedTheTerminal(t *testing.T) {
	for _, width := range []int{20, 40, 80} {
		var buf bytes.Buffer
		p := newEnsemblePanel(&buf, width, "ensemble · target 99.0% confidence")
		p.Handle(swarm.Progress{Kind: swarm.ProgressSelected,
			Heads: panelHeads("a-really-long-head-identifier-that-will-not-fit", "b")})
		p.Handle(swarm.Progress{Kind: swarm.ProgressFinished,
			Head:    provider.Head{ID: "a-really-long-head-identifier-that-will-not-fit"},
			Attempt: swarm.Attempt{Status: swarm.StatusOK, Duration: time.Second, InputTokens: 999999}})
		p.Handle(swarm.Progress{Kind: swarm.ProgressEvidence, Threshold: 4.59, Evidence: trust.Evidence{
			Source: "b", LambdaAfter: -1.2, ConfidenceAfter: 0.23,
		}})

		for _, line := range lastFrame(&buf) {
			if n := utf8.RuneCountInString(line); n >= width {
				t.Errorf("width %d: line of %d runes would wrap: %q", width, n, line)
			}
		}
	}
}

// A pipe must hand swarm a nil callback, not a method value on a nil panel,
// which is itself non-nil and would switch the run onto the progress path.
func TestEnsemblePanel_NilPanelHandsBackNoCallback(t *testing.T) {
	var p *ensemblePanel
	if p.handler() != nil {
		t.Error("nil panel produced a callback; a piped run would report progress to nobody")
	}
	p.Handle(swarm.Progress{Kind: swarm.ProgressSelected})
	p.Stop()
}
