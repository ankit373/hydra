// SPDX-License-Identifier: MIT

package ground

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/trust"
	"github.com/ankit373/hydra/internal/util"
)

// The one thing this must never report is that an unchecked answer checked
// out, so both "nothing to check against" and "nothing to check" come back as
// errors rather than as verdicts with a Passed field beside them.
func TestOracle_RefusesRatherThanPassingWhenItCheckedNothing(t *testing.T) {
	cases := []struct {
		name    string
		prompt  string
		answer  string
		wantErr error
	}{
		{"no fenced context", "just a question", "tier 10 is the floor", ErrNoContext},
		{"no checkable claim", "q\n\n" + util.WrapUntrusted("CONTEXT", "func f() {}"),
			"It does what the file says.", ErrNoClaims},
	}
	for _, c := range cases {
		o := &Oracle{Prompt: c.prompt}
		v, err := o.Verify(context.Background(), c.answer, trust.Task{})
		if !errors.Is(err, c.wantErr) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.wantErr)
		}
		if v.Passed {
			t.Errorf("%s: reported a pass alongside its refusal", c.name)
		}
	}
}

func TestOracle_MapsTheVerdictOntoOraclesShape(t *testing.T) {
	p := "q\n\n" + util.WrapUntrusted("CONTEXT", "tier 10 is the free floor")
	o := &Oracle{Prompt: p}

	v, err := o.Verify(context.Background(), "The free floor is tier 10.", trust.Task{})
	if err != nil || !v.Passed {
		t.Fatalf("grounded answer: passed=%v err=%v", v.Passed, err)
	}
	if !strings.Contains(v.Detail, "claims appear") {
		t.Errorf("detail = %q, want it to say what was checked", v.Detail)
	}

	v, err = o.Verify(context.Background(), "The free floor is tier 12.", trust.Task{})
	if err != nil || v.Passed {
		t.Fatalf("ungrounded answer: passed=%v err=%v", v.Passed, err)
	}
	if !strings.Contains(v.Detail, "12") {
		t.Errorf("detail = %q, want it to name the span that failed", v.Detail)
	}
	// Last carries the spans a caller needs to show, not only pass or fail.
	if len(o.Last.Unsupported) != 1 || o.Last.Unsupported[0].Text != "12" {
		t.Errorf("Last.Unsupported = %+v", o.Last.Unsupported)
	}
}

// Its own calibration key, so its sensitivity and specificity are measured
// apart from a test runner's.
func TestOracle_KeyDefaultsToItsOwnSource(t *testing.T) {
	if got := (&Oracle{}).Key(); got != DefaultSource {
		t.Errorf("Key() = %q, want %q", got, DefaultSource)
	}
	if got := (&Oracle{Source: "verifier:mine"}).Key(); got != "verifier:mine" {
		t.Errorf("Key() = %q, want the configured source", got)
	}
}
