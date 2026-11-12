// SPDX-License-Identifier: MIT

package decide

import (
	"context"
	"errors"
	"testing"

	"github.com/ankit373/hydra/internal/decision"
	"github.com/ankit373/hydra/internal/oracle"
	"github.com/ankit373/hydra/internal/testutil"
	"github.com/ankit373/hydra/internal/trust"
)

const cascadeClass = "classification.test"

// verdicts answers Verify from a fixed verdict, or fails to run at all.
type verdicts struct {
	pass bool
	err  error
}

func (v verdicts) Verify(context.Context, string, trust.Task) (oracle.Verdict, error) {
	return oracle.Verdict{Passed: v.pass}, v.err
}

// measuredVerifier is calibrated in cascadeClass with both verdict directions,
// which is what oracle.Measured needs before its LLR counts.
func measuredVerifier(t *testing.T, cal *trust.Calibrator, source string) {
	t.Helper()
	for range 20 {
		if err := cal.Update(source, cascadeClass, true, trust.OutcomeCorrect); err != nil {
			t.Fatal(err)
		}
		if err := cal.Update(source, cascadeClass, false, trust.OutcomeIncorrect); err != nil {
			t.Fatal(err)
		}
	}
}

func cascade(cal *trust.Calibrator, v verdicts, maxEsc int, ceiling float64) *Cascade {
	return &Cascade{
		Target:         trust.Target{Confidence: 0.95, MaxCostUSD: ceiling},
		MaxEscalations: maxEsc,
		Cal:            cal,
		Domain:         cascadeClass,
		Verifiers:      []Verifier{{Source: "verifier:match:t", Oracle: v}},
	}
}

func TestCascade_Judge(t *testing.T) {
	testutil.NewSandbox(t)
	measured := newCal(t)
	measuredVerifier(t, measured, "verifier:match:t")
	for _, tc := range []struct {
		name    string
		cal     *trust.Calibrator
		pPass   float64
		v       verdicts
		spent   float64 // this answer's cost
		ceiling float64
		maxEsc  int
		next    bool
		want    Decision // empty while the cascade goes on
	}{
		{"accept on the first", measured, 0.9, verdicts{pass: true}, 0, 0, 2, false, Accepted},
		{"escalate on a fail", measured, 0.9, verdicts{pass: false}, 0, 0, 2, true, ""},
		{"no_evidence when unmeasured", newCal(t), 0.9, verdicts{pass: false}, 0, 0, 2, false, NoEvidence},
		{"budget_exhausted at the ceiling", measured, 0.9, verdicts{pass: false}, 0.02, 0.02, 2, false, BudgetExhausted},
		{"budget_exhausted at the max count", measured, 0.9, verdicts{pass: false}, 0, 0, 0, false, BudgetExhausted},
		{"an oracle error contributes 0", measured, 0.9, verdicts{err: errors.New("boom")}, 0, 0, 2, true, ""},
		{"a sure prior needs no verdict", measured, 0.99, verdicts{err: errors.New("boom")}, 0, 0, 2, false, Accepted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := cascade(tc.cal, tc.v, tc.maxEsc, tc.ceiling)
			next, reason := c.Judge(context.Background(), Answer{Text: "x", PPass: tc.pPass, CostUSD: tc.spent})
			if next != tc.next || c.decision != tc.want {
				t.Errorf("next=%v decision=%q (reason %q), want next=%v decision=%q", next, c.decision, reason, tc.next, tc.want)
			}
			if next && reason == "" {
				t.Error("an escalation gave no reason, so its Attempt says nothing")
			}
		})
	}
}

// A prior past the target accepts only once a verifier's verdict counted, when the caller needs one.
func TestCascade_NeedsVerdict(t *testing.T) {
	testutil.NewSandbox(t)
	measured := newCal(t)
	measuredVerifier(t, measured, "verifier:match:t")
	for _, tc := range []struct {
		name string
		cal  *trust.Calibrator
		v    verdicts
		want Decision
	}{
		{"unmeasured verifier", newCal(t), verdicts{pass: true}, NoEvidence},
		{"the verifier could not run", measured, verdicts{err: errors.New("boom")}, ""},
		{"a passing verdict", measured, verdicts{pass: true}, Accepted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := cascade(tc.cal, tc.v, 2, 0)
			c.NeedsVerdict = true
			c.Judge(context.Background(), Answer{Text: "x", PPass: 0.99})
			if c.decision != tc.want {
				t.Errorf("a 0.99 prior decided %q, want %q", c.decision, tc.want)
			}
		})
	}
}

// A verifier that cannot tell right from wrong is measured, yet its failed verdict
// moves a 0.97 prior nowhere: that verdict must not be the one that accepts.
func TestCascade_AFailedVerdictNeverAccepts(t *testing.T) {
	testutil.NewSandbox(t)
	cal := newCal(t)
	for range 10 {
		for _, said := range []bool{true, false} {
			for _, out := range []trust.Outcome{trust.OutcomeCorrect, trust.OutcomeIncorrect} {
				if err := cal.Update("verifier:match:t", cascadeClass, said, out); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	c := cascade(cal, verdicts{pass: false}, 2, 0)
	c.NeedsVerdict = true
	if next, _ := c.Judge(context.Background(), Answer{Text: "x", PPass: 0.97}); !next || c.decision != "" {
		t.Errorf("a failed verdict on a 0.97 prior decided %q (next=%v), want an escalation", c.decision, next)
	}
}

func TestCascade_OutcomeServesTheRightStep(t *testing.T) {
	for _, tc := range []struct {
		name     string
		confs    []float64
		decision Decision
		index    int
		want     Decision
	}{
		{"accepted serves the last", []float64{0.6, 0.97}, Accepted, 1, Accepted},
		{"accepted beats a surer unaccepted one", []float64{0.98, 0.96}, Accepted, 1, Accepted},
		{"no_evidence serves the first", []float64{0.6}, NoEvidence, 0, NoEvidence},
		{"exhausted serves the most confident", []float64{0.6, 0.8, 0.7}, BudgetExhausted, 1, BudgetExhausted},
		{"a tie serves the earliest", []float64{0.8, 0.6, 0.8}, BudgetExhausted, 0, BudgetExhausted},
		{"candidates ran out", []float64{0.6, 0.7}, "", 1, BudgetExhausted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &Cascade{confidences: tc.confs, decision: tc.decision}
			if tc.decision == Accepted {
				c.accepted = tc.index
			}
			if got := c.Outcome(); got.Index != tc.index || got.Decision != tc.want {
				t.Errorf("Outcome() = %+v, want index %d decision %q", got, tc.index, tc.want)
			}
		})
	}
}

// G17. decide.Router's rule: an uncalibrated verifier cannot move the posterior,
// so the cascade must not pay further heads to hear the same nothing.
func TestCascade_UncalibratedVerifierCannotEscalate(t *testing.T) {
	testutil.NewSandbox(t)
	c := cascade(newCal(t), verdicts{pass: false}, 5, 0)
	asked := 0
	for range 5 {
		asked++
		if next, _ := c.Judge(context.Background(), Answer{Text: "x", PPass: 0.5}); !next {
			break
		}
	}
	if asked != 1 || c.Outcome().Decision != NoEvidence {
		t.Errorf("no measured verifier, yet the cascade asked %d sources (decision %q)", asked, c.Outcome().Decision)
	}
}

// G18. One table fed to both: the chat cascade and the decisions router stop on
// the same rules, so the two endpoints report the same Decision for the same evidence.
func TestCascade_SameRuleAsDecideRouter(t *testing.T) {
	testutil.NewSandbox(t)
	for _, tc := range []struct {
		name       string
		first      float64 // the first source's own confidence, and the cascade's prior
		calibrated bool
		sources    int
	}{
		{"the first clears the target", 0.97, false, 2},
		{"nothing calibrated", 0.6, false, 2},
		{"no source left", 0.6, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cal := newCal(t)
			ids := []string{"a", "b"}[:tc.sources]
			if tc.calibrated {
				for _, id := range ids {
					calibrate(t, cal, id, 8)
				}
				measuredVerifier(t, cal, "verifier:match:t")
			}
			ask := &fakeAsker{answers: map[string]decision.Result{
				"a": answered(1-tc.first, tc.first),
				"b": answered(1-tc.first, tc.first),
			}}
			out, err := router(ask, cal, ids...).Decide(context.Background(), Call{Request: tone, RunID: "run-g18"})
			if err != nil {
				t.Fatal(err)
			}

			c := cascade(cal, verdicts{pass: false}, tc.sources-1, 0)
			c.Domain = trust.DefaultDomain
			if tc.calibrated {
				c.Domain = cascadeClass
			}
			for range tc.sources {
				if next, _ := c.Judge(context.Background(), Answer{Text: "x", PPass: tc.first}); !next {
					break
				}
			}
			if got := c.Outcome().Decision; got != out.Decision {
				t.Errorf("Router decided %s, Cascade decided %s for the same evidence", out.Decision, got)
			}
		})
	}
}

// The SPRT's own end is labelled too, so /v1/decisions never reports a blank verdict.
func TestDecide_OutcomeDecisionWhenTheSPRTAccepts(t *testing.T) {
	testutil.NewSandbox(t)
	cal := newCal(t)
	calibrate(t, cal, "a", 8)
	calibrate(t, cal, "b", 8)
	ask := &fakeAsker{answers: map[string]decision.Result{
		"a": answered(0.4, 0.6),
		"b": answered(0.1, 0.9),
	}}
	out, err := router(ask, cal, "a", "b").Decide(context.Background(), Call{Request: tone, RunID: "run-sprt"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision != Accepted {
		t.Errorf("an SPRT that cleared %.2f at %.3f decided %q, want %q", 0.95, out.Confidence, out.Decision, Accepted)
	}
}
