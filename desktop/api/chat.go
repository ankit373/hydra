// SPDX-License-Identifier: MIT

package api

import (
	"context"
	"time"

	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/rank"
	"github.com/ankit373/hydra/internal/runid"
	"github.com/ankit373/hydra/internal/runlog"
)

// ChatTimeout bounds one dispatch from the dock. A GUI cannot leave a request
// outstanding forever — the window has no way to say "still working" that a
// user will keep believing.
const ChatTimeout = 5 * time.Minute

// ChatReply is one answer, with the routing that produced it.
//
// The routing is not decoration. Which head answered, at which tier, for how
// much is the difference between an answer you can weigh and one you can only
// believe — and this is a router, so it is the part the user is actually
// buying.
type ChatReply struct {
	Output string `json:"output"`

	Head  string `json:"head"`
	Model string `json:"model"`
	Tier  int    `json:"tier"`

	CostUSD    float64 `json:"costUsd"`
	DurationMS int64   `json:"durationMs"`

	// RunID lets the reply link into Session, so "why did it say that" is one
	// click rather than a hunt through the logs.
	RunID string `json:"runId"`

	Error string `json:"error,omitempty"`
}

// Chat dispatches one prompt and returns the reply.
//
// enum is a routing key ("SIMPLE", "STANDARD", …); empty lets dispatch pick.
// Errors come back inside the reply rather than as a Go error: a failed
// dispatch is a normal outcome the dock renders as a message, not an exception
// that should blank the window.
func (a *API) Chat(prompt, enum string) (*ChatReply, error) {
	if prompt == "" {
		return &ChatReply{Error: "empty prompt"}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), ChatTimeout)
	defer cancel()

	runID, taskID := runid.New(), runid.New()
	r := &ChatReply{RunID: runID}

	// The dock's dispatch is a run like any other: it appears in Fleet while it
	// works and in Session afterwards. A chat that stayed invisible to the rest
	// of the app would be a second, untraceable path through the router.
	hb := runlog.StartHeartbeat(ctx, runID, runlog.HeartbeatInterval)
	defer hb.Stop()

	rl := runlog.New(runID)
	_ = rl.Append(runlog.Event{Kind: runlog.KindRunStarted, TaskID: taskID, Detail: preview(prompt)})
	defer func() {
		_ = rl.Append(runlog.Event{Kind: runlog.KindRunFinished, TaskID: taskID})
	}()

	d, err := dispatch.New(ctx)
	if err != nil {
		r.Error = err.Error()
		return r, nil
	}

	tierHint := ""
	if enum != "" {
		tierHint = dispatch.EnumToTier(enum)
	}

	res, err := d.Dispatch(ctx, prompt, dispatch.Options{
		TierHint: tierHint,
		Enum:     enum,
		RunID:    runID,
		TaskID:   taskID,
	})
	if err != nil {
		r.Error = err.Error()
		return r, nil
	}

	r.Output = res.Output
	r.Head = res.Head.ID
	r.Model = res.Head.Name
	r.Tier = rank.UITier(res.Head)
	if res.Response != nil {
		r.DurationMS = res.Response.Duration.Milliseconds()
	}
	return r, nil
}

// preview shortens a prompt for a log Detail. Entries stay small because the
// run log's atomicity guarantee is per write() call.
func preview(s string) string {
	const max = 80
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// ChatEnums lists the routing keys the dock offers, weakest head first.
//
// Taken from dispatch.EnumToTier's own table rather than restated, so the
// picker cannot offer a key the router does not understand. The dock's "auto"
// choice is the absence of an enum, not a member of this list.
func (a *API) ChatEnums() []string {
	return []string{
		"GRUNT", "TRIVIAL", "SIMPLE", "STANDARD", "MODERATE",
		"COMPLEX", "HARD", "VERY_HARD", "EXPERT", "CORE",
	}
}
