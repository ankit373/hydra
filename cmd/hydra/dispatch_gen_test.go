// SPDX-License-Identifier: MIT

package main

import (
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/evalset"
	"github.com/ankit373/hydra/internal/testutil"
	"github.com/ankit373/hydra/internal/util"
)

const genPrompt = "Is this a refund, billing or shipping question? I want my money back."

// genHeads answers with cody (sourcegraph) and lets claude (anthropic), another family, write the checks.
func genHeads(t *testing.T, answer string) {
	t.Helper()
	s, _ := dispatchable(t, answer)
	s.FakeBinary(t, "claude", testutil.EchoScript(`["refund", "billing", "shipping"]`))
}

func TestDispatch_CheckGen_RunsGeneratorForKind(t *testing.T) {
	genHeads(t, "refund")
	out, cobraOut, err := run(t, "dispatch", "--no-stream", "--tier", "6", "--kind", "classification", "--check", "gen", genPrompt)
	if err != nil {
		t.Fatalf("dispatch failed: %v (%s)", err, cobraOut)
	}
	if !strings.Contains(out, "PASS  gen:labels") || !strings.Contains(out, "generated checks: all passed") ||
		!strings.Contains(out, "by claude") {
		t.Fatalf("no label check verdict printed:\n%s", out)
	}
	answers, err := evalset.Joint(evalset.VerdictsPath())
	if err != nil || len(answers) != 1 || len(answers[0].Checks) != 1 {
		t.Fatalf("verdict log = %+v, %v", answers, err)
	}
	if v := answers[0].Checks[0]; v.Check != "gen:labels" || !v.Passed || v.GenFamily != "anthropic" || v.Head != "cody" {
		t.Errorf("verdict = %+v, want a passing gen:labels written by anthropic about cody's answer", v)
	}
}

func TestDispatch_CheckGen_NoKindNotChecked(t *testing.T) {
	genHeads(t, "refund")
	out, cobraOut, err := run(t, "dispatch", "--no-stream", "--tier", "6", "--check", "gen", genPrompt)
	if err != nil {
		t.Fatalf("dispatch failed: %v (%s)", err, cobraOut)
	}
	if !strings.Contains(out, "NOT CHECKED  gen  no kind of work") || strings.Contains(out, "PASS") {
		t.Fatalf("a dispatch with no kind did not say so:\n%s", out)
	}
}

func TestDispatch_CheckGen_BudgetStopsChecks(t *testing.T) {
	genHeads(t, "refund")
	out, cobraOut, err := run(t, "dispatch", "--no-stream", "--tier", "6", "--kind", "classification",
		"--check", "gen", "--max-usd", "0.000000001", genPrompt)
	if err != nil {
		t.Fatalf("dispatch failed: %v (%s)", err, cobraOut)
	}
	if !strings.Contains(out, "NOT CHECKED  gen:labels  budget") || !strings.Contains(out, "generated checks: not checked") {
		t.Fatalf("a spent budget did not stop the checks:\n%s", out)
	}
}

func TestDispatch_CheckGen_UnknownRefusedBeforeRouting(t *testing.T) {
	genHeads(t, "refund")
	_, _, err := run(t, "dispatch", "--no-stream", "--tier", "6", "--check", "gen:vibes", genPrompt)
	if err == nil || !strings.Contains(err.Error(), `no generated check "vibes"`) {
		t.Fatalf("err = %v, want gen:vibes refused", err)
	}
}

func TestDispatch_CheckGenFields_PrintsEveryBrokenField(t *testing.T) {
	s, _ := dispatchable(t, `{"vendor": "Apex", "total": "9", "date": "30 June"}`)
	s.FakeBinary(t, "claude", testutil.EchoScript(`[{"name": "vendor", "type": "string", "required": true, "from_source": true},
{"name": "total", "type": "number", "required": true}, {"name": "date", "type": "string", "from_source": true},
{"name": "ref", "type": "string", "required": true}]`))
	prompt := "Pull vendor, total, date and ref as JSON.\n" + util.WrapUntrusted("DOCUMENT", "Acme Ltd, total 1,200.50, due 30 June")
	out, cobraOut, err := run(t, "dispatch", "--no-stream", "--tier", "6", "--kind", "extraction", "--check", "gen:fields", prompt)
	if err != nil {
		t.Fatalf("dispatch failed: %v (%s)", err, cobraOut)
	}
	for _, want := range []string{"FAIL  gen:fields", "not_in_source  vendor", "wrong_type     total", "missing        ref", "1 of 4 fields ok"} {
		if !strings.Contains(out, want) {
			t.Errorf("output has no %q:\n%s", want, out)
		}
	}
}
