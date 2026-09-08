// SPDX-License-Identifier: MIT

package executor

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/ankit373/hydra/internal/sandbox"
	"github.com/ankit373/hydra/internal/util"
)

// ExecuteStream forwards the child's stdout as it arrives.
//
// The only difference from Execute is where cmd.Stdout points: exec already
// copies a non-*os.File Stdout through a pipe while the child runs, so the
// output was always arriving incrementally and simply had nowhere to go.
func (e *CLIExecutor) ExecuteStream(ctx context.Context, req Request, onDelta OnDelta) (*Response, error) {
	tmpl, ok := cliTemplates[req.Head.Provider]
	if !ok {
		tmpl, ok = cliTemplates[req.Head.ID]
	}
	if !ok {
		// Same refusal as Execute, and worded identically on purpose: a head
		// with no template is unrunnable either way, and two paths reporting
		// it differently is how a surface starts guessing.
		return nil, fmt.Errorf("no CLI template for head %q (provider %q)", req.Head.ID, req.Head.Provider)
	}

	args := tmpl.buildArgs(req.Prompt)
	cmd := sandbox.Harden(exec.CommandContext(ctx, req.Head.Executable, args...))
	cmd.Env = headEnv(req.Head)
	if tmpl.stdinPrompt {
		cmd.Stdin = strings.NewReader(req.Prompt)
	}

	start := time.Now()
	sink := newDeltaSink(onDelta, start)
	stderr := util.NewAccumulator(64 << 10)
	cmd.Stdout = sink
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("cli exec %s: %w, stderr: %s", req.Head.ID, err, stderr.String())
	}

	// Trimmed for the Response the same way Execute trims it, while the deltas
	// already forwarded are untrimmed: a surface renders what arrived, and
	// trailing whitespace it already printed is not worth retracting.
	output := strings.TrimSpace(sink.output())

	promptTokens := EstimateTokens(req.Prompt)
	responseTokens := EstimateTokens(output)
	writeTokenSidecar(req.Head.ID, "cli", "estimate", promptTokens, responseTokens)

	return &Response{
		Output:       output,
		Duration:     time.Since(start),
		Model:        req.Head.ID,
		InputTokens:  promptTokens,
		OutputTokens: responseTokens,
		Truncated:    sink.truncated(),
		// Measured here, unlike Execute which has no first-token moment to
		// observe. A tool that block-buffers its stdout reports its exit as
		// the first token, which is honest: that is when output appeared.
		TTFT:            sink.firstTokenAt(),
		TokensEstimated: true,
	}, nil
}

var _ StreamingExecutor = (*CLIExecutor)(nil)
