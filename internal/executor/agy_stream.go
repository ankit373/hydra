// SPDX-License-Identifier: MIT

package executor

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ankit373/hydra/internal/util"
)

// agy could always stream; what stopped it is that the auth check reads stderr,
// which is only readable after the process exits. Forwarding stdout as it
// arrives would therefore render an interactive sign-in prompt to the user as
// the model's answer. So the leading output is held until auth is decided, then
// released (#791).

// agyHoldBackCap bounds the hold-back for output that never reaches
// agyAuthLines newlines. A single enormous line must not buffer the whole
// answer before anything is shown.
const agyHoldBackCap = 64 << 10

// agyAuthGate buffers the head of stdout until the auth question is settled,
// then either releases it downstream or keeps it forever.
type agyAuthGate struct {
	sink    *deltaSink
	started time.Time

	head     []byte // the prefix, kept for the exit-time check even after release
	released bool
	blocked  bool // the prefix already looks like auth; emit nothing, ever

	// firstAt is when output actually arrived, not when the gate let it
	// through. Reporting the flush moment as TTFT would charge the hold-back
	// to the model.
	firstAt  time.Duration
	firstSet bool
}

func newAgyAuthGate(sink *deltaSink, started time.Time) *agyAuthGate {
	return &agyAuthGate{sink: sink, started: started}
}

func (g *agyAuthGate) Write(p []byte) (int, error) {
	if !g.firstSet && len(bytes.TrimSpace(p)) > 0 {
		g.firstSet = true
		g.firstAt = measured(time.Since(g.started))
	}
	if g.blocked {
		// Read and dropped: the child must not block on a full pipe while we
		// refuse its output.
		return len(p), nil
	}
	if g.released {
		return g.sink.Write(p)
	}

	g.head = append(g.head, p...)
	if bytes.Count(g.head, []byte("\n")) < agyAuthLines && len(g.head) < agyHoldBackCap {
		return len(p), nil // not enough to decide on yet
	}
	// Enough of the prefix is in hand to run the stdout half of the check. The
	// stderr half cannot run until exit, which is why finish() runs it again.
	if authSignalRe.MatchString(agyAuthPrefix(string(g.head))) {
		g.blocked = true
		return len(p), nil
	}
	if _, err := g.sink.Write(g.head); err != nil {
		return 0, err
	}
	g.released = true
	return len(p), nil
}

// finish settles the question once stderr is readable, and releases anything
// still held. It returns the auth error when there is one, in which case
// nothing was ever forwarded.
func (g *agyAuthGate) finish(stderrStr, pool, modelFlag string) error {
	// The check here is a superset of the one Write ran: same regex, over
	// stderr *plus* the same prefix. So a gate that blocked always errors here,
	// and there is no un-block path to write.
	if err := agyAuthError(stderrStr, agyAuthPrefix(string(g.head)), pool, modelFlag); err != nil {
		g.blocked = true
		return err
	}
	if !g.released {
		if _, err := g.sink.Write(g.head); err != nil {
			return err
		}
		g.released = true
	}
	return nil
}

// ttft is the arrival time of the first output, or the sink's own measurement
// when nothing was ever held back.
func (g *agyAuthGate) ttft() time.Duration {
	if g.firstSet {
		return g.firstAt
	}
	return g.sink.firstTokenAt()
}

func (e *AgyExecutor) ExecuteStream(ctx context.Context, req Request, onDelta OnDelta) (*Response, error) {
	cmd, cancel, modelFlag, err := agyCommand(ctx, req)
	if err != nil {
		return nil, err
	}
	defer cancel()

	start := time.Now()
	sink := newDeltaSink(onDelta, start)
	gate := newAgyAuthGate(sink, start)
	stderr := util.NewAccumulator(64 << 10)
	cmd.Stdout = gate
	cmd.Stderr = stderr

	runErr := cmd.Run()
	duration := time.Since(start)
	stderrStr := stderr.String()

	// Before runErr on purpose: a run that needs a sign-in also exits non-zero,
	// and "agy exec failed" is the wrong thing to tell someone whose actual
	// problem is that they have never signed in.
	if err := gate.finish(stderrStr, req.Head.Meta["token_pool"], modelFlag); err != nil {
		return nil, err
	}
	if runErr != nil {
		return nil, fmt.Errorf("agy exec %s: %w, %s", req.Head.ID, runErr, strings.TrimSpace(stderrStr))
	}

	output := strings.TrimSpace(sink.output())
	promptTokens := EstimateTokens(req.Prompt)
	responseTokens := EstimateTokens(output)
	writeTokenSidecar(modelFlag, "agy", "estimate", promptTokens, responseTokens)

	return &Response{
		Output:          output,
		Duration:        measured(duration),
		Model:           req.Head.ID,
		InputTokens:     promptTokens,
		OutputTokens:    responseTokens,
		Truncated:       sink.truncated(),
		TTFT:            gate.ttft(),
		TokensEstimated: true,
	}, nil
}

var _ StreamingExecutor = (*AgyExecutor)(nil)
