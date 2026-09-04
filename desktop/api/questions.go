// SPDX-License-Identifier: MIT

package api

import (
	"context"
	"errors"
	"strings"

	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/pending"
)

// PendingQuestion is one task parked waiting on a human decision.
//
// AskedAt is sent as epoch milliseconds rather than a Go time, matching how
// every other DTO here crosses the bridge: the frontend formats ages itself and
// a marshalled time.Time forces it to parse a layout string first.
type PendingQuestion struct {
	TaskID    string `json:"taskId"`
	RunID     string `json:"runId"`
	Question  string `json:"question"`
	Head      string `json:"head"`
	Resource  string `json:"resource,omitempty"`
	Prompt    string `json:"prompt"`
	AskedAtMS int64  `json:"askedAtMs"`
}

// QuestionQueue is what is waiting, plus whether the queue could be read.
//
// Error is reported alongside the questions rather than instead of them: one
// unreadable file must not hide the answerable ones, or a parked task is
// forgotten because an unrelated one is corrupt.
type QuestionQueue struct {
	Questions []PendingQuestion `json:"questions"`
	Error     string            `json:"error,omitempty"`
}

// GetPendingQuestions returns the parked tasks, oldest first.
//
// Poll-friendly like GetFleet and GetSession: a question is durable state on
// disk, so a UI that missed the moment it was asked still finds it.
func (a *API) GetPendingQuestions() QuestionQueue {
	out := QuestionQueue{Questions: []PendingQuestion{}}
	qs, err := pending.List()
	if err != nil {
		out.Error = err.Error()
	}
	for _, q := range qs {
		out.Questions = append(out.Questions, PendingQuestion{
			TaskID: q.TaskID, RunID: q.RunID, Question: q.Question,
			Head: q.Head, Resource: q.Resource, Prompt: q.Prompt,
			AskedAtMS: q.AskedAt.UnixMilli(),
		})
	}
	return out
}

// AnswerQuestion answers a parked task and runs it.
//
// Errors come back on the reply rather than as a second return value, the same
// way Chat reports them, so the transcript can render a failed answer in place
// instead of the caller needing a separate error path.
func (a *API) AnswerQuestion(taskID, answer string) (*ChatReply, error) {
	r := &ChatReply{TaskID: taskID}

	// Everything that can be decided without a router is decided first. Built
	// the other way round, a double-clicked question on a machine with no
	// config reported "no hydra config" — a setup error for something that is
	// not a setup problem.
	q, lErr := pending.Load(taskID)
	switch {
	// Already answered, by a double click or in another window. Not alarming,
	// and the consumed file is the proof the work did not run twice.
	case errors.Is(lErr, pending.ErrNotFound):
		r.Error = "This question was already answered."
		return r, nil
	case lErr != nil:
		// Corrupt or incomplete: loud, never resumed on a zero value.
		r.Error = lErr.Error()
		return r, nil
	}
	r.RunID = q.RunID

	// Checked here as well as in Resume, so refusing a non-answer does not
	// depend on a router being constructible.
	if strings.TrimSpace(answer) == "" {
		r.Error = "Answer the question to run this task, or decline it."
		return r, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), ChatTimeout)
	defer cancel()

	d, err := dispatch.New(ctx)
	if err != nil {
		r.Error = err.Error()
		return r, nil
	}
	res, err := d.Resume(ctx, taskID, answer)
	if err != nil {
		// Answering can park again when the resumed dispatch lands on a head
		// the human was never asked about: approval is per head.
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

// DeclineQuestion refuses a parked task. Nothing runs.
//
// Free text cannot carry a refusal reliably — "no, don't" folded into a prompt
// still dispatches and leaves it to the head to notice — so declining is its
// own path, and dispatch.Decline needs no config to take it.
func (a *API) DeclineQuestion(taskID, reason string) error {
	return dispatch.Decline(taskID, reason)
}
