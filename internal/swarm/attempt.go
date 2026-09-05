// SPDX-License-Identifier: MIT

// Package swarm implements fan-out dispatch: one prompt fired at multiple
// Heads simultaneously, with race / best / all response strategies.
package swarm

import (
	"context"
	"errors"
	"time"

	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/provider"
)

// HeadStatus describes how a single head's execution ended.
type HeadStatus string

const (
	StatusPending      HeadStatus = "pending"
	StatusRunning      HeadStatus = "running"
	StatusOK           HeadStatus = "ok"
	StatusFailed       HeadStatus = "failed"
	StatusCanceled     HeadStatus = "canceled"
	StatusTimeout      HeadStatus = "timeout"
	StatusAuthRequired HeadStatus = "auth_required"
)

// Attempt is the immutable per-head execution record.
// All fields are set before the Attempt is returned from a runner; callers
// must treat the value as read-only.
type Attempt struct {
	Head            provider.Head
	Status          HeadStatus
	Output          string
	Truncated       bool // true when output was capped by the accumulator
	InputTokens     int
	OutputTokens    int
	TokensEstimated bool // true when tokens were estimated (agy char/4), not provider-reported
	Duration        time.Duration
	EstCostUSD      float64
	Err             error // original typed error, never stringified before display
	StartedAt       time.Time
	FinishedAt      time.Time
	Rank            int // 1 = winner; 0 = unranked
}

// Succeeded reports whether the attempt produced usable output.
func (a Attempt) Succeeded() bool { return a.Status == StatusOK }

// TotalTokens returns input + output token count.
func (a Attempt) TotalTokens() int { return a.InputTokens + a.OutputTokens }

// classifyError converts a raw executor error into the appropriate HeadStatus.
// Keeps error classification in one place so runners never branch on error types.
func classifyError(err error) HeadStatus {
	if err == nil {
		return StatusOK
	}
	var authErr *executor.AuthRequiredError
	if errors.As(err, &authErr) {
		return StatusAuthRequired
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return StatusTimeout
	}
	if errors.Is(err, context.Canceled) {
		return StatusCanceled
	}
	return StatusFailed
}
