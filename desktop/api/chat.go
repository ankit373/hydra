// SPDX-License-Identifier: MIT

package api

import (
	"context"
	"errors"
	"time"

	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/rank"
	"github.com/ankit373/hydra/internal/runid"
	"github.com/ankit373/hydra/internal/runlog"
)

// ChatTimeout bounds one dispatch from the dock. A GUI cannot leave a request
// outstanding forever, the window has no way to say "still working" that a
// user will keep believing.
const ChatTimeout = 5 * time.Minute

// ChatReply is one answer, with the routing that produced it.
//
// The routing is not decoration. Which head answered, at which tier, for how
// much is the difference between an answer you can weigh and one you can only
// believe, and this is a router, so it is the part the user is actually
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

	// Question is set when the task parked waiting on a human decision, and
	// TaskID is what answers it. A parked task is not a failure: routed through
	// Error it rendered as a red dispatch error, which reads as "this broke"
	// rather than "this needs you" (#583).
	Question string `json:"question,omitempty"`
	TaskID   string `json:"taskId,omitempty"`

	// NeedsProbe is true when there is nothing to route this chat to, zero
	// heads discovered at all, or heads discovered but none dispatchable for
	// this request (dispatch.ErrNoHeads, e.g. the dock's "auto-route" default
	// left every candidate filtered out). dispatch.New probes fresh on every
	// call, so there is no separate "go discover models" step to point at;
	// the dock offers to retry (which re-probes) instead of surfacing a CLI
	// instruction a GUI user has no terminal for (#434, #452).
	NeedsProbe bool `json:"needsProbe,omitempty"`
}

// NewRunID mints a run id for the caller to hold before a dispatch starts.
//
// The frontend uses this to poll GetSession(runId) for an in-flight chat turn
// instead of waiting for Chat to return, the run's log exists and is being
// appended to from the moment Chat begins, not just once it finishes. A thin
// wrapper rather than the frontend minting its own: the timestamp-prefix +
// hex format is a contract Fleet's sort depends on (see fleet.go), so there is
// exactly one place allowed to generate it.
func (a *API) NewRunID() string { return runid.New() }

// Chat dispatches one prompt and returns the reply.
//
// enum is a routing key ("SIMPLE", "STANDARD", …); empty lets dispatch pick.
// runID lets the caller supply an id minted via NewRunID so it can watch the
// run live; empty mints a fresh one, same as before NewRunID existed.
//
// tier pins the starting tier ("1".."10"), which is how the model picker
// expresses "answer this with Opus Thinking": every registry model declares a
// tier, so choosing a model chooses its tier. It is a *starting point*, not a
// guarantee, the governor can still downgrade it and fallback can still move
// off it, exactly as for any other dispatch. It outranks enum, since an
// explicit choice should beat an inferred one. Invalid values are rejected by
// dispatch's own resolveTierHint rather than silently coerced.
//
// Errors come back inside the reply rather than as a Go error: a failed
// dispatch is a normal outcome the view renders as a message, not an exception
// that should blank the window.
func (a *API) Chat(prompt, enum, runID, tier string) (*ChatReply, error) {
	if prompt == "" {
		return &ChatReply{Error: "empty prompt"}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), ChatTimeout)
	defer cancel()

	runID, taskID := runid.ResolveRun(runID), runid.New()
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
	if len(d.Heads()) == 0 {
		r.Error = "No models found on this machine yet."
		r.NeedsProbe = true
		return r, nil
	}

	// An explicit tier outranks an inferred one: picking a model is a stronger
	// statement than the complexity band a routing key implies. Left unvalidated
	// here on purpose, dispatch's resolveTierHint already rejects anything
	// outside 1-10, and duplicating that bound is how the two drift apart.
	tierHint := tier
	if tierHint == "" && enum != "" {
		// A garbage enum must not fall through to unrestricted auto-routing,
		// EnumToTier's "" is ambiguous with "no enum given" (#501's fix,
		// applied here too since the dock is a second caller of the same map).
		if !dispatch.IsKnownEnum(enum) {
			r.Error = "unknown routing key: " + enum
			return r, nil
		}
		tierHint = dispatch.EnumToTier(enum)
	}

	res, err := d.Dispatch(ctx, prompt, dispatch.Options{
		TierHint: tierHint,
		Enum:     enum,
		RunID:    runID,
		TaskID:   taskID,
	})
	if err != nil {
		if errors.Is(err, dispatch.ErrNoHeads) {
			// Same dead end as zero heads at all, same friendly retry reply
			// instead of dispatch's raw, CLI-flavored error text (#452).
			r.Error = "No model is available to answer this yet."
			r.NeedsProbe = true
			return r, nil
		}
		var parked *dispatch.ParkedError
		if errors.As(err, &parked) {
			r.Question, r.TaskID, r.Head = parked.Question, parked.TaskID, parked.Head
			return r, nil
		}
		r.Error = err.Error()
		return r, nil
	}

	fill(r, res)
	return r, nil
}

// fill copies a dispatch result onto a reply. Shared by Chat and
// AnswerQuestion so a resumed task reports its head, tier and cost the same
// way a first-time one does.
func fill(r *ChatReply, res *dispatch.Result) {
	r.Output = res.Output
	r.Head = res.Head.ID
	r.Model = res.Head.Name
	r.Tier = rank.UITier(res.Head)
	if res.Response != nil {
		r.DurationMS = res.Response.Duration.Milliseconds()
	}
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
