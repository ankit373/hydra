// SPDX-License-Identifier: MIT

package ground

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ankit373/hydra/internal/oracle"
	"github.com/ankit373/hydra/internal/trust"
)

// ErrNoContext reports a dispatch that fenced no context, so there was nothing
// to check the answer against.
//
// An error rather than a failing verdict, and rather than a passing one with a
// flag beside it. internal/oracle already means "the oracle could not run" by
// an error, and every caller handles it as no evidence. Returning a Verdict
// here would leave a Passed field for someone to read, and the one thing this
// must never report is that an unchecked answer was checked.
var ErrNoContext = errors.New("ground: the prompt fenced no context, nothing to check the answer against")

// ErrNoClaims reports an answer that named and measured nothing, so the check
// found no objection because it had nothing to object to.
//
// The same rule as ErrNoContext, applied one level in. A purely prose answer
// is exactly what this check cannot speak to, and reporting it as verified
// would turn the one thing it is bad at into its most confident output.
var ErrNoClaims = errors.New("ground: the answer states no checkable claim, so nothing was verified")

// DefaultSource is the calibration key this oracle records under, so its
// sensitivity and specificity are measured separately from a test runner's.
const DefaultSource = "verifier:grounding"

// Oracle checks an answer against the context its prompt carried, as an
// internal/oracle evidence source.
//
// Adapting rather than adding a path: a verdict has to reach internal/trust
// through oracle.LLR like any other, or its strength is a number nobody
// measured. That is #771's lesson, where an unreachable true-negative pinned
// specificity under 0.5 and the evidence was worth less than it claimed.
type Oracle struct {
	// Prompt is what the head was given, fences and all.
	Prompt string
	// Source is the calibration key; empty uses DefaultSource.
	Source string
	// Last is the verdict from the most recent Verify, for a caller that wants
	// the unsupported spans rather than only pass or fail.
	Last Verdict
}

// Verify runs the grounding check.
func (o *Oracle) Verify(_ context.Context, candidate string, _ trust.Task) (oracle.Verdict, error) {
	v := Check(candidate, o.Prompt)
	o.Last = v
	if !v.Checked {
		return oracle.Verdict{}, ErrNoContext
	}
	if v.Claims == 0 {
		return oracle.Verdict{}, ErrNoClaims
	}
	return oracle.Verdict{Passed: v.Grounded, Detail: v.Detail()}, nil
}

// Key is the calibration source this oracle records under.
func (o *Oracle) Key() string {
	if o.Source != "" {
		return o.Source
	}
	return DefaultSource
}

// Detail names what failed, which is the whole diagnostic value: "ungrounded"
// alone tells a reader nothing they can check.
func (v Verdict) Detail() string {
	if !v.Checked {
		return "no fenced context"
	}
	if v.Claims == 0 {
		return "no checkable claim in the answer"
	}
	if v.Grounded {
		return fmt.Sprintf("all %d claims appear in the %d bytes of context given", v.Claims, v.Context)
	}
	spans := make([]string, 0, len(v.Unsupported))
	for _, c := range v.Unsupported {
		spans = append(spans, string(c.Kind)+" "+c.Text)
	}
	return "not in the context given: " + strings.Join(spans, ", ")
}
