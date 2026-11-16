// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/serve"
	"github.com/ankit373/hydra/internal/util"
)

var invoiceTurn = "Summarise this invoice.\n\n" +
	util.WrapUntrusted("CONTEXT", "The invoice total is 120 dollars, due on 3 March.")

// chatChecked asks r with X-Hydra-Check values and returns the answer.
func chatChecked(t *testing.T, r serveRouter, prompt string, checks ...string) serve.Answer {
	t.Helper()
	ans, err := r.Chat(context.Background(), serve.Request{
		RequestID: "run-check", TaskClass: cascadeTestClass, Checks: checks,
		Messages: []executor.Message{{Role: "user", Content: prompt}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ans.Evidence == nil {
		t.Fatal("no evidence, so no verdict")
	}
	return ans
}

func TestServe_CheckHeaderPasses(t *testing.T) {
	_, r := newCascadeFixture(t, map[string]string{"first": "The total is 120 dollars."}, false)
	r.verifiers = nil
	ev := chatChecked(t, r, invoiceTurn, "ground").Evidence
	if ev.CheckResult != serve.CheckPassed || ev.CheckDetail != "verifier:grounding" {
		t.Errorf("verdict %q %q, want passed by grounding", ev.CheckResult, ev.CheckDetail)
	}
}

// A measured check joins the cascade as a config verifier does: the first answer
// invents a figure, the stronger head's is served, and the verdict is on that one.
func TestServe_CheckHeaderFailsAndEscalates(t *testing.T) {
	f, r := newCascadeFixture(t, map[string]string{"first": "The total is 450 dollars.", "second": "The total is 120 dollars."}, false)
	r.verifiers = nil
	for i := range 20 {
		mustUpdate(t, r.cal, "verifier:grounding", true, i < 19)
		mustUpdate(t, r.cal, "verifier:grounding", false, i == 19)
	}
	ans := chatChecked(t, r, invoiceTurn, "ground")
	if ans.Head != "litellm/second" || f.called("first") != 1 {
		t.Errorf("served %s after %d calls to first, want second after one", ans.Head, f.called("first"))
	}
	if ans.Evidence.CheckResult != serve.CheckPassed {
		t.Errorf("verdict %q, want passed on the served answer", ans.Evidence.CheckResult)
	}
}

// Unmeasured, the check cannot escalate, so the failed answer is served and says so.
func TestServe_CheckHeaderReportsAFailure(t *testing.T) {
	_, r := newCascadeFixture(t, map[string]string{"first": "The total is 450 dollars.", "second": "The total is 120 dollars."}, false)
	r.verifiers = nil
	ev := chatChecked(t, r, invoiceTurn, "ground", "match:pattern=.*dollars\\.").Evidence
	if ev.CheckResult != serve.CheckFailed || !strings.Contains(ev.CheckDetail, "450") {
		t.Errorf("verdict %q %q, want failed naming 450", ev.CheckResult, ev.CheckDetail)
	}
}

// Nothing fenced means nothing to check against: not_checked, never passed.
func TestServe_CheckHeaderNotChecked(t *testing.T) {
	_, r := newCascadeFixture(t, map[string]string{"first": "The total is 450 dollars."}, false)
	r.verifiers = nil
	if ev := chatChecked(t, r, "summarise the invoice", "ground").Evidence; ev.CheckResult != serve.CheckNotChecked {
		t.Errorf("verdict %q, want not_checked", ev.CheckResult)
	}
	// The stub's writer answers prose where a label list is due, so gen ran and could not check.
	if ev := chatChecked(t, r, invoiceTurn, "gen").Evidence; ev.CheckResult != serve.CheckNotChecked || !strings.HasPrefix(ev.CheckDetail, "gen:labels") {
		t.Errorf("gen verdict %q %q, want not_checked by gen:labels", ev.CheckResult, ev.CheckDetail)
	}
}

func TestServe_BadCheckPatternIs400(t *testing.T) {
	_, r := newCascadeFixture(t, map[string]string{"first": "ok"}, false)
	_, err := r.Chat(context.Background(), serve.Request{Checks: []string{"match:pattern=("},
		Messages: []executor.Message{{Role: "user", Content: "q"}}})
	if !errors.Is(err, serve.ErrBadRequest) {
		t.Errorf("err = %v, want a bad request", err)
	}
}

// A named check the class already verifies is counted once, so its evidence is not doubled.
func TestServe_NamedCheckAlreadyConfiguredCountsOnce(t *testing.T) {
	_, r := newCascadeFixture(t, map[string]string{"first": "ok"}, false)
	named, err := parseServedChecks([]string{"ground", "ground"}, invoiceTurn)
	if err != nil {
		t.Fatal(err)
	}
	req := serve.Request{TaskClass: cascadeTestClass}
	opts, ev, err := r.options(req)
	if err != nil {
		t.Fatal(err)
	}
	esc := r.escalationFor(ev, req, &opts, named.verifiers())
	if n := len(esc.cascade.Verifiers); n != 2 {
		t.Errorf("%d verifiers, want the class's match and one ground", n)
	}
}
