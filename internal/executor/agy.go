package executor

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ankit373/hydra/internal/config"
)

// AuthRequiredError is returned when agy.sh exits with code 3 (Google auth needed).
type AuthRequiredError struct {
	ModelFlag string
	Pool      string
}

func (e *AuthRequiredError) Error() string {
	return fmt.Sprintf("auth required for agy model %q (pool %q) — run agy interactively to authenticate", e.ModelFlag, e.Pool)
}

// AgyExecutor delegates to dispatch/agy.sh, reusing its model-swapping,
// auth detection, and token estimation logic rather than reimplementing them.
type AgyExecutor struct{}

func (e *AgyExecutor) Execute(ctx context.Context, req Request) (*Response, error) {
	modelFlag := req.Head.Meta["model_flag"]
	if modelFlag == "" {
		return nil, fmt.Errorf("agy executor: head %q has no model_flag in Meta", req.Head.ID)
	}

	script := filepath.Join(config.ScriptHome(), "dispatch", "agy.sh")

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, script, modelFlag, req.Prompt)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	err := cmd.Run()
	duration := time.Since(start)

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 3 {
			return nil, &AuthRequiredError{
				ModelFlag: modelFlag,
				Pool:      req.Head.Meta["token_pool"],
			}
		}
		return nil, fmt.Errorf("agy exec %s: %w — %s", req.Head.ID, err, strings.TrimSpace(stderr.String()))
	}

	return &Response{
		Output:   strings.TrimSpace(stdout.String()),
		Duration: duration,
		Model:    req.Head.ID,
	}, nil
}
