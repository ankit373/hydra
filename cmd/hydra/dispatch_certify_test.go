// SPDX-License-Identifier: MIT

package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/evalset"
	"github.com/ankit373/hydra/internal/runlog"
)

// labelArgs dispatches a labelling task whose match check passes "billing".
func labelArgs(t *testing.T, extra ...string) []string {
	args := []string{"dispatch", "--no-stream", "--tier", "6",
		"--check", "match:labels=" + labelsFile(t, "billing\nbug\nhow-to\n")}
	return append(append(args, extra...), "label this ticket: I was charged twice")
}

// seedLabels runs one plain dispatch to learn the head, kind and check names the
// verdict log uses, then adds `wrong` wrong answers the check never passed and
// 30 right ones it always did, all under those names, with ground truth.
func seedLabels(t *testing.T, wrong int) {
	t.Helper()
	if _, out, err := run(t, labelArgs(t)...); err != nil {
		t.Fatalf("dispatch failed: %v (%s)", err, out)
	}
	joint, err := evalset.Joint(evalset.VerdictsPath())
	if err != nil || len(joint) != 1 || len(joint[0].Checks) != 1 {
		t.Fatalf("want one judged answer, got %+v (%v)", joint, err)
	}
	ref := joint[0].Checks[0]
	p := evalset.VerdictsPath()
	for i := range wrong + 30 {
		right := i >= wrong
		span := fmt.Sprintf("seed-%d", i)
		v := evalset.Verdict{TS: "2026-11-01T00:00:00Z", SpanID: span, Head: ref.Head, Domain: ref.Domain, Check: ref.Check, Passed: right}
		out := evalset.OutcomeIncorrect
		if right {
			out = evalset.OutcomeCorrect
		}
		if err := evalset.AddVerdict(p, v); err != nil {
			t.Fatal(err)
		}
		if err := evalset.AddTruth(p, evalset.Truth{TS: v.TS, SpanID: span, Outcome: out}); err != nil {
			t.Fatal(err)
		}
	}
}

// certifiedScores is every certified score in every run: the seeding dispatch
// and the one under test can share a second, so "newest" is not reliable.
func certifiedScores(t *testing.T) []runlog.Score {
	t.Helper()
	runs, err := runlog.Runs()
	if err != nil {
		t.Fatal(err)
	}
	var out []runlog.Score
	for _, id := range runs {
		events, err := runlog.Load(id)
		if err != nil {
			t.Fatal(err)
		}
		for _, ss := range runlog.ScoresBySpan(events) {
			for _, s := range ss {
				if s.Name == runlog.ScoreCertified {
					out = append(out, s)
				}
			}
		}
	}
	return out
}

func TestDispatch_CertifyAcceptsWithCalibratedChecks(t *testing.T) {
	dispatchable(t, "billing")
	seedLabels(t, 60)

	out, cobraOut, err := run(t, labelArgs(t, "--certify", "0.05")...)
	if err != nil {
		t.Fatalf("dispatch failed: %v (%s)", err, cobraOut)
	}
	// One check in the kind, 0 false passes in 60: q̄ = 1 - 0.05^(1/60) = 0.0487
	// (Clopper-Pearson, k = 0), s = 1, so a pass is worth 20.5 >= bar 1/0.05 = 20.
	if !strings.Contains(out, "certified (under independence) at alpha 0.05: product 20.5 >= bar 20") {
		t.Fatalf("not certified:\n%s", out)
	}
	s := certifiedScores(t)
	if len(s) != 1 || s[0].Value != 1 || s[0].Outcome == nil || len(s[0].Outcome.Steps) != 1 {
		t.Fatalf("certified score = %+v", s)
	}
	runs, _ := runlog.Runs()
	routed := 0
	for _, id := range runs {
		events, _ := runlog.Load(id)
		for _, e := range events {
			if e.Kind == runlog.KindRoute && e.Route.CertifyAlpha == 0.05 {
				routed++
			}
		}
	}
	if routed != 1 {
		t.Fatalf("want one route event with certify_alpha 0.05, got %d", routed)
	}
}

func TestDispatch_CertifyNotVerifiedWithFewLabels(t *testing.T) {
	dispatchable(t, "billing")
	seedLabels(t, 20)

	out, cobraOut, err := run(t, labelArgs(t, "--certify", "0.05")...)
	if err != nil {
		t.Fatalf("not verified must not be an error: %v (%s)", err, cobraOut)
	}
	// 20 wrong answers cap a pass at 1/(1 - 0.05^(1/20)) = 7.2; reaching 20 takes
	// ceil(ln(1/0.05) x 20) = 60.
	if !strings.Contains(out, "not verified: 20 labelled wrong answers, need 60") {
		t.Fatalf("want the shortfall named:\n%s", out)
	}
	if s := certifiedScores(t); len(s) != 1 || s[0].Value != 0 {
		t.Fatalf("certified score = %+v", s)
	}
}

func TestDispatch_CertifyFailedCheckIsFutile(t *testing.T) {
	dispatchable(t, "refund")
	seedLabels(t, 60) // the head answers "refund", which the label check fails
	out, cobraOut, err := run(t, labelArgs(t, "--certify", "0.05")...)
	if err != nil {
		t.Fatalf("dispatch failed: %v (%s)", err, cobraOut)
	}
	if !strings.Contains(out, "not verified: futility, product 0 cannot reach bar 20") {
		t.Fatalf("a failed check should end in futility:\n%s", out)
	}
}

func TestDispatch_CertifyWithoutCheckSaysSo(t *testing.T) {
	dispatchable(t, "billing")
	out, _, err := run(t, "dispatch", "--no-stream", "--tier", "6", "--certify", "0.05", "label this ticket")
	if err != nil || !strings.Contains(out, "not verified: no check for this kind") {
		t.Fatalf("err %v\n%s", err, out)
	}
}

func TestDispatch_CertifyAndConfidenceRefused(t *testing.T) {
	dispatchable(t, "billing")
	for _, args := range [][]string{
		{"--certify", "0.05", "--confidence", "0.9"},
		{"--certify", "0.05", "--swarm"},
		{"--certify", "0.5"},
		{"--certify", "-1"},
	} {
		_, _, err := run(t, labelArgs(t, args...)...)
		if err == nil || !strings.Contains(err.Error(), "--certify") {
			t.Errorf("%v: want a refusal naming --certify, got %v", args, err)
		}
	}
}

func TestDispatch_CertifyOffChangesNothing(t *testing.T) {
	dispatchable(t, "billing")
	seedLabels(t, 60)
	out, cobraOut, err := run(t, labelArgs(t)...)
	if err != nil {
		t.Fatalf("dispatch failed: %v (%s)", err, cobraOut)
	}
	if strings.Contains(out, "certified") || strings.Contains(out, "not verified") || len(certifiedScores(t)) != 0 {
		t.Fatalf("a run without --certify printed or wrote an outcome:\n%s", out)
	}
	if !strings.Contains(out, "PASS") {
		t.Fatalf("the check no longer ran:\n%s", out)
	}
}
