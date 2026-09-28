// SPDX-License-Identifier: MIT

package runlog

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// ErrNoSuchSpan reports a score naming a span the run does not contain.
//
// Refused rather than appended: a score whose span cannot be found is invisible
// to every reader, so accepting it would report success for a verdict nobody
// will ever see.
var ErrNoSuchSpan = errors.New("runlog: no such span in this run")

// ErrBadScore reports a score with no name or a value that is not a number.
var ErrBadScore = errors.New("runlog: a score needs a name and a finite value")

// Score is a verdict on a span: whether the work was right, alongside what it
// cost and how long it took.
//
// It arrives after the span closes, sometimes long after when a test suite is
// what produces it, so it is carried on its own event rather than as a field on
// the span's. The log is append-only, and a verdict must not be able to rewrite
// what the head actually reported.
type Score struct {
	Name    string  `json:"name"`
	Value   float64 `json:"value"`
	Comment string  `json:"comment,omitempty"`

	// Source is who judged: an oracle id, "human", a CI job. A verdict with no
	// provenance cannot be weighed against a disagreeing one.
	Source string `json:"source,omitempty"`
}

// AppendScore records a verdict on an existing span.
//
// The span is looked up first, so an id that does not exist in the run is an
// error the caller can act on rather than a line nothing will ever read.
func AppendScore(runID, spanID string, sc Score) error {
	if strings.TrimSpace(sc.Name) == "" || math.IsNaN(sc.Value) || math.IsInf(sc.Value, 0) {
		return ErrBadScore
	}
	events, err := Load(runID)
	if err != nil {
		return err
	}
	target, ok := findSpan(events, spanID)
	if !ok {
		return fmt.Errorf("%w: %s", ErrNoSuchSpan, spanID)
	}
	return New(runID).Append(Event{
		Kind: KindScore, TaskID: target.TaskID, SpanID: target.SpanID,
		Head: target.Head, Model: target.Model, Score: &sc,
	})
}

// findSpan resolves a span id, or a unique prefix of one, to an event that
// carries it. A prefix is accepted because that is what a waterfall row shows.
func findSpan(events []Event, spanID string) (Event, bool) {
	if spanID == "" {
		return Event{}, false
	}
	var hit Event
	var found bool
	for _, e := range events {
		if e.SpanID == "" || e.Kind == KindScore {
			continue
		}
		if e.SpanID == spanID {
			return e, true
		}
		if len(spanID) < len(e.SpanID) && strings.HasPrefix(e.SpanID, spanID) {
			// Ambiguity is refused rather than resolved arbitrarily: attaching
			// a verdict to the wrong span is worse than not attaching it.
			if found && hit.SpanID != e.SpanID {
				return Event{}, false
			}
			hit, found = e, true
		}
	}
	return hit, found
}

// Span returns the event that declares a span, resolving a unique id prefix
// exactly as AppendScore does. Exported so a caller can read what a span
// recorded without reimplementing that resolution.
func Span(events []Event, spanID string) (Event, bool) { return findSpan(events, spanID) }

// Scores returns the verdicts recorded against a span, in append order.
func Scores(events []Event, spanID string) []Score {
	var out []Score
	for _, e := range events {
		if e.Kind == KindScore && e.SpanID == spanID && e.Score != nil {
			out = append(out, *e.Score)
		}
	}
	return out
}

// Verdict summarises a span's scores. Any non-positive score fails the span:
// aggregating the other way would let one passing check hide a failing one.
// known is false when nothing has judged the span, which is not a pass and must
// never render as one.
func Verdict(scores []Score) (passed, known bool) {
	if len(scores) == 0 {
		return false, false
	}
	for _, sc := range scores {
		if sc.Value <= 0 {
			return false, true
		}
	}
	return true, true
}
