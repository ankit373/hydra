// SPDX-License-Identifier: MIT

package decide

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/rank"
)

func answer(head, span string) *dispatch.Result {
	return &dispatch.Result{Output: "x", Head: provider.Head{ID: head}, SpanID: span, Domain: "extraction.json"}
}

func onFail(maxEsc int, checks ...verdicts) *OnFail {
	o := &OnFail{Max: maxEsc}
	for i, v := range checks {
		o.Checks = append(o.Checks, Verifier{Source: "verifier:cmd:" + string(rune('a'+i)), Oracle: v})
	}
	return o
}

func TestOnFail_FailedCheckEscalatesOnce(t *testing.T) {
	o := onFail(1, verdicts{detail: "missing field: total\nmore output"})
	next, reason := o.Judge(context.Background(), answer("cheap", "s1"))
	if !next || reason != "verifier:cmd:a failed: missing field: total" {
		t.Fatalf("first fail: next=%v reason=%q, want an escalation naming the check and its first line", next, reason)
	}
	if next, _ := o.Judge(context.Background(), answer("strong", "s2")); next {
		t.Fatal("a second failure escalated past Max 1")
	}
}

func TestOnFail_OracleErrorIsNotCheckedAndStops(t *testing.T) {
	o := onFail(1, verdicts{err: errors.New("no claims to ground")})
	if next, reason := o.Judge(context.Background(), answer("cheap", "s1")); next {
		t.Fatalf("a check that could not run escalated: %q", reason)
	}
	v := o.Verdicts()
	if len(v) != 1 || v[0].Checked || v[0].Passed || v[0].Detail != "no claims to ground" {
		t.Fatalf("verdicts = %+v, want one not-checked entry carrying the error", v)
	}
	if o.Unasked() != "" {
		t.Fatalf("Unasked = %q for an answer nothing failed", o.Unasked())
	}
}

func TestOnFail_ErrorBesideARealFailStillEscalates(t *testing.T) {
	o := onFail(1, verdicts{err: errors.New("cannot run")}, verdicts{detail: "3 tests failed"})
	if next, reason := o.Judge(context.Background(), answer("cheap", "s1")); !next || !strings.HasPrefix(reason, "verifier:cmd:b failed") {
		t.Fatalf("next=%v reason=%q, want the real fail to escalate", next, reason)
	}
}

func TestOnFail_AllPassStops(t *testing.T) {
	o := onFail(1, verdicts{pass: true}, verdicts{pass: true})
	if next, _ := o.Judge(context.Background(), answer("cheap", "s1")); next {
		t.Fatal("an answer every check passed escalated")
	}
}

func TestOnFail_NoChecksNeverEscalates(t *testing.T) {
	if next, _ := onFail(1).Judge(context.Background(), answer("cheap", "s1")); next {
		t.Fatal("an answer with no check escalated on a guess")
	}
}

func TestOnFail_StopsAfterMax(t *testing.T) {
	for _, maxEsc := range []int{0, 1, 2} {
		o := onFail(maxEsc, verdicts{})
		asked := 0
		for range 5 {
			if next, _ := o.Judge(context.Background(), answer("h", "s")); next {
				asked++
			}
		}
		if asked != maxEsc {
			t.Errorf("Max %d: escalated %d times", maxEsc, asked)
		}
	}
}

func TestOnFail_RecordsEveryJudgedAnswer(t *testing.T) {
	o := onFail(1, verdicts{detail: "bad"}, verdicts{pass: true})
	o.Judge(context.Background(), answer("cheap", "s1"))
	o.Judge(context.Background(), answer("strong", "s2"))
	want := []Judged{
		{Head: "cheap", SpanID: "s1", Check: "verifier:cmd:a", Checked: true, Detail: "bad"},
		{Head: "cheap", SpanID: "s1", Check: "verifier:cmd:b", Checked: true, Passed: true},
		{Head: "strong", SpanID: "s2", Check: "verifier:cmd:a", Checked: true, Detail: "bad"},
		{Head: "strong", SpanID: "s2", Check: "verifier:cmd:b", Checked: true, Passed: true},
	}
	got := o.Verdicts()
	if len(got) != len(want) {
		t.Fatalf("got %d verdicts, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("verdict %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestOnFail_UnaskedSaysNoMeasuredHeadBeatsIt(t *testing.T) {
	measured := rank.Score{Effective: 60, InDomain: rank.MinCommitments}
	r := answer("cheap", "s1")
	r.Scores = map[string]rank.Score{"cheap": measured}
	o := onFail(1, verdicts{})
	o.Judge(context.Background(), r)
	if got := o.Unasked(); got != "no head measured at extraction.json beats cheap, so no stronger head was asked" {
		t.Fatalf("Unasked = %q", got)
	}

	stronger := measured
	stronger.Effective = 80
	r.Scores["strong"] = stronger
	if got := o.Unasked(); !strings.HasPrefix(got, "no stronger head answered") {
		t.Fatalf("with a stronger measured head, Unasked = %q", got)
	}
	o.Judge(context.Background(), answer("strong", "s2"))
	if got := o.Unasked(); got != "" {
		t.Fatalf("Unasked = %q after the stronger head was judged", got)
	}
}
