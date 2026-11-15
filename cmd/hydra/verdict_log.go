// SPDX-License-Identifier: MIT

package main

import (
	"errors"
	"fmt"

	"github.com/ankit373/hydra/internal/evalset"
	"github.com/ankit373/hydra/internal/runlog"
)

// loadSpan resolves a span id or unique prefix in runID, the newest run when
// runID is empty, to the event that recorded it.
func loadSpan(runID, span string) (runlog.Event, error) {
	if runID == "" {
		runs, err := runlog.Runs()
		if err != nil || len(runs) == 0 {
			return runlog.Event{}, errors.New("--span given but no run to attach it to; pass --run")
		}
		runID = runs[0]
	}
	events, err := runlog.Load(runID)
	if err != nil {
		return runlog.Event{}, fmt.Errorf("run %s: %w", runID, err)
	}
	ev, ok := runlog.Span(events, span)
	if !ok {
		return runlog.Event{}, fmt.Errorf("span %q not found in run %s, or ambiguous", span, runID)
	}
	return ev, nil
}

// fileVerdict appends v to the verdict log, filling the answer's identity from
// span when there is one. Never fatal: the check is the work, not its record.
func fileVerdict(span runlog.Event, v evalset.Verdict) {
	v.SpanID, v.RunID, v.Head = span.SpanID, span.RunID, span.Head
	if span.Domain != "" {
		v.Domain = span.Domain
	}
	if err := evalset.AddVerdict(evalset.VerdictsPath(), v); err != nil {
		fmt.Fprintf(stdout, "  %s\n", dimStyle.Render("verdict log: "+err.Error()))
	}
}

// candidateHash keys an answer by its text. No text means no key, not the hash
// of the empty string, which every textless verdict would otherwise share.
func candidateHash(candidate string) string {
	if candidate == "" {
		return ""
	}
	return evalset.Hash(candidate)
}
