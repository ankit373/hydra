// SPDX-License-Identifier: MIT

package decide

import (
	"context"
	"fmt"
	"strings"

	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/rank"
	"github.com/ankit373/hydra/internal/trust"
)

// OnFail is the plain escalation rule for the CLI paths: a check that ran and
// failed asks one stronger head, a check that could not run never does.
// One per request; not shared.
type OnFail struct {
	Checks []Verifier
	// Max is how many stronger heads failed checks may ask; see policy.Escalations.
	Max int

	judged  []Judged
	answers []*dispatch.Result
	asked   int
	// open is the answer last passed over, until a later answer is judged.
	open *dispatch.Result
}

// Judged is one check's verdict on one answer. Checked false means the check
// could not run, so Passed says nothing and Detail holds why.
type Judged struct {
	Head    string
	SpanID  string
	Check   string
	Checked bool
	Passed  bool
	Detail  string
}

// Judge runs every check on r and matches dispatch.Options.Escalate.
func (o *OnFail) Judge(ctx context.Context, r *dispatch.Result) (next bool, reason string) {
	o.open = nil
	o.answers = append(o.answers, r)
	for _, c := range o.Checks {
		v, err := c.Oracle.Verify(ctx, r.Output, trust.Task{Domain: r.Domain})
		j := Judged{Head: r.Head.ID, SpanID: r.SpanID, Check: c.Source,
			Checked: err == nil, Passed: err == nil && v.Passed, Detail: v.Detail}
		if err != nil {
			j.Detail = err.Error()
		}
		o.judged = append(o.judged, j)
		if j.Checked && !j.Passed && reason == "" {
			reason = c.Source + " failed"
			if line, _, _ := strings.Cut(strings.TrimSpace(v.Detail), "\n"); line != "" {
				reason += ": " + line
			}
		}
	}
	if reason == "" || o.asked >= o.Max {
		return false, ""
	}
	o.asked++
	o.open = r
	return true, reason
}

// Verdicts is every check's verdict on every answer judged, in order.
func (o *OnFail) Verdicts() []Judged { return o.judged }

// Answers is every answer judged, in order; the last is the one dispatch serves.
func (o *OnFail) Answers() []*dispatch.Result { return o.answers }

// Unasked says why a failed answer was served with nothing stronger after it,
// "" when nothing is waiting on a stronger head.
func (o *OnFail) Unasked() string {
	r := o.open
	if r == nil {
		return ""
	}
	// The same climb dispatch takes, so this names the reason it stopped.
	heads := make([]provider.Head, 0, len(r.Scores))
	for id := range r.Scores {
		heads = append(heads, provider.Head{ID: id})
	}
	if _, ok := rank.Escalation(heads, r.Scores, r.Head, nil); ok {
		return "no stronger head answered within the budget and policy"
	}
	work := "this kind of work"
	if r.Domain != "" {
		work = r.Domain
	}
	return fmt.Sprintf("no head measured at %s beats %s, so no stronger head was asked", work, r.Head.ID)
}
