// SPDX-License-Identifier: MIT

package executor

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/ankit373/hydra/internal/util"
)

// OnDelta receives output as it arrives. Called from the goroutine driving the
// executor, in order, never concurrently with itself, so an implementation may
// append to unsynchronised state. It must not block for long: a slow consumer
// stalls the read loop and inflates the measured duration.
type OnDelta func(delta string)

// StreamingExecutor is an Executor that can deliver output incrementally.
//
// Optional on purpose. Making Execute itself streaming would change every
// executor and every caller to serve the heads that can stream, and some
// genuinely cannot: Replicate's prediction API is a polling interface, not a
// stream, and a CLI head that buffers its own stdout has nothing to hand over
// until it exits.
type StreamingExecutor interface {
	Executor
	ExecuteStream(ctx context.Context, req Request, onDelta OnDelta) (*Response, error)
}

// Stream runs req against ex, delivering output through onDelta as it arrives.
//
// An executor that does not implement StreamingExecutor still works: its whole
// output is delivered as one delta once Execute returns. That is what lets a
// surface be written once. The alternative is every surface type-asserting for
// itself and growing its own non-streaming branch, which is how two surfaces
// end up disagreeing about what a non-streaming head looks like.
//
// A nil onDelta is allowed and means "no incremental delivery wanted", which
// takes the plain Execute path rather than streaming into a discarded callback.
func Stream(ctx context.Context, ex Executor, req Request, onDelta OnDelta) (*Response, error) {
	se, ok := ex.(StreamingExecutor)
	if !ok || onDelta == nil {
		resp, err := ex.Execute(ctx, req)
		if err != nil {
			return resp, err
		}
		if onDelta != nil && resp != nil && resp.Output != "" {
			onDelta(resp.Output)
		}
		return resp, nil
	}
	return se.ExecuteStream(ctx, req, onDelta)
}

// CanStream reports whether ex delivers output incrementally, so a surface can
// tell a real token stream from one whole-output delta and render a cursor only
// where there is genuinely more coming.
func CanStream(ex Executor) bool {
	_, ok := ex.(StreamingExecutor)
	return ok
}

// deltaSink accumulates a streamed response while forwarding each delta on.
//
// It exists so every streaming executor gets the same three things right
// without repeating them: output goes through util.Accumulator so the 33 MB cap
// and Response.Truncated still hold (a hard invariant, and the reason this is
// not a strings.Builder), TTFT is the time to the first non-empty delta rather
// than a provider's own guess at it, and nothing is forwarded once the cap is
// hit, since a consumer must not render text the Response will not contain.
type deltaSink struct {
	mu      sync.Mutex
	acc     *util.Accumulator
	onDelta OnDelta
	started time.Time
	ttft    time.Duration
}

// newDeltaSink starts measuring from started, which must be when the request
// was issued, not when its response headers came back.
//
// Measured from headers, TTFT read as 1ms on a live call whose first token the
// user waited 4.171s for: the headers themselves arrive only once the provider
// begins producing, so the whole prefill had already happened and the number
// said "instant" for a four-second wait.
func newDeltaSink(onDelta OnDelta, started time.Time) *deltaSink {
	return &deltaSink{
		acc:     util.NewAccumulator(util.DefaultMaxBytes),
		onDelta: onDelta,
		started: started,
	}
}

// write records a delta and forwards it. Empty deltas are recorded but never
// forwarded: a provider emits them as keep-alives and role preambles, and a
// surface that renders a cursor per delta would flicker on them.
func (s *deltaSink) write(delta string) {
	if delta == "" {
		return
	}
	s.mu.Lock()
	if s.ttft == 0 && strings.TrimSpace(delta) != "" {
		s.ttft = time.Since(s.started)
	}
	truncated := s.acc.Truncated()
	if !truncated {
		_, _ = s.acc.Write([]byte(delta))
		truncated = s.acc.Truncated()
	}
	cb := s.onDelta
	s.mu.Unlock()

	if truncated || cb == nil {
		return
	}
	cb(delta)
}

// Write adapts the sink to io.Writer, for an executor whose output arrives as
// subprocess stdout rather than as parsed chunks. exec already copies a
// non-*os.File Stdout through a pipe as the child produces it, so a tee here is
// the whole of what subprocess streaming needs.
//
// Chunks arrive on pipe boundaries, not token boundaries, and a child that
// block-buffers its own stdout when it is not on a tty will still deliver
// everything at exit. That is the child's behaviour, not something this can fix
// without allocating a pty.
func (s *deltaSink) Write(p []byte) (int, error) {
	s.write(string(p))
	return len(p), nil
}

func (s *deltaSink) output() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.acc.String()
}

func (s *deltaSink) truncated() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.acc.Truncated()
}

// firstTokenAt is the measured time to the first non-empty delta, or 0 when
// nothing ever arrived. Zero means unknown, never instant, the same contract
// Response.TTFT already carries.
func (s *deltaSink) firstTokenAt() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ttft
}
