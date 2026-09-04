// SPDX-License-Identifier: MIT

package api

import "github.com/ankit373/hydra/internal/review"

// ReviewOutcome is the result of accepting or undoing one edit.
//
// Errors ride on the result rather than a second return value, the way
// ChatReply reports them, so the diff pane can render a refusal in place
// instead of the caller needing a separate error path.
type ReviewOutcome struct {
	File   string `json:"file"`
	Status string `json:"status,omitempty"`
	// Method is how the rollback was done — git_checkout, rm_untracked or
	// backup_restore. Shown because the three are not equally recoverable.
	Method string `json:"method,omitempty"`
	Error  string `json:"error,omitempty"`
}

// ApproveEdit accepts a change, and for a non-git workspace drops the backup
// that made undoing it possible.
//
// The path is passed through exactly as the run log recorded it, and is never
// resolved against a guessed workspace root — that could accept a file outside
// the intended scope. It does not need to be: both edit paths (editor.Edit,
// parallel) refuse a relative path outright, so a stored path is absolute by
// construction, and review.Approve refuses anything else. Re-checking it here
// was a second copy of a rule that already has one home.
func (a *API) ApproveEdit(file string) ReviewOutcome {
	res, err := review.Approve(file)
	if err != nil {
		return ReviewOutcome{File: file, Error: err.Error()}
	}
	return ReviewOutcome{File: res.File, Status: res.Status}
}

// RejectEdit rolls a file back to its pre-edit state.
//
// review.Reject fails loudly when there is nothing to roll back rather than
// reporting success for a no-op, and that error is surfaced verbatim: a button
// that silently does nothing is worse than one that says why it cannot.
func (a *API) RejectEdit(file string) ReviewOutcome {
	res, err := review.Reject(file)
	if err != nil {
		return ReviewOutcome{File: file, Error: err.Error()}
	}
	return ReviewOutcome{File: res.File, Status: res.Status, Method: res.Method}
}
