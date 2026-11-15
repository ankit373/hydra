// SPDX-License-Identifier: MIT

package main

import (
	"runtime"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/evalset"
	"github.com/ankit373/hydra/internal/testutil"
	"github.com/ankit373/hydra/internal/util"
)

var invoiceSummary = "Summarise this invoice.\n\n" +
	util.WrapUntrusted("CONTEXT", "The invoice total is 120 dollars, due on 3 March.")

// escalating has two heads measured at kind: "a" answers first, cheaper, and
// "b" is the stronger one a failed check climbs to.
func escalating(t *testing.T, kind, first, second string) (*testutil.Sandbox, *cascadeFixture) {
	t.Helper()
	s := testutil.NewSandbox(t)
	return s, litellmHeads(t, map[string]string{"a": first, "b": second}, kind+".general")
}

// containsChecker exits 0 when the file in $2 holds $1, a stand-in for a schema check.
func containsChecker(t *testing.T, s *testutil.Sandbox) {
	t.Helper()
	checker := "#!/bin/sh\ncase \"$(/bin/cat \"$2\")\" in *\"$1\"*) exit 0;; esac\nexit 1\n"
	if runtime.GOOS == "windows" {
		checker = "@echo off\r\n\"%SystemRoot%\\System32\\findstr.exe\" /c:%1 %2 >nul\r\nexit /b %errorlevel%\r\n"
	}
	s.FakeBinary(t, "contains", checker)
}

func TestDispatchCheck_UngroundedNumberRerunsOnce(t *testing.T) {
	_, f := escalating(t, "summary", "The total is 450 dollars.", "The total is 120 dollars.")

	out, cobraOut, err := run(t, "dispatch", "--no-stream", "--kind", "summary", "--check", "ground", invoiceSummary)
	if err != nil {
		t.Fatalf("dispatch failed: %v (%s)", err, cobraOut)
	}
	if f.called("a") != 1 || f.called("b") != 1 {
		t.Fatalf("calls a=%d b=%d, want one each", f.called("a"), f.called("b"))
	}
	if p := f.prompt("b"); !strings.Contains(p, "REJECTED ANSWER") || !strings.Contains(p, "450") {
		t.Errorf("the rerun was not sent the rejected answer:\n%s", p)
	}
	if !strings.Contains(out, "REJECTED") || !strings.Contains(out, "GROUNDED") || !strings.Contains(out, "served litellm/b") {
		t.Errorf("the report does not show the rejected answer and the one served:\n%s", out)
	}
}

func TestDispatchCheck_SchemaFailureRerunsOnce(t *testing.T) {
	s, f := escalating(t, "extraction", `{"invoice": "A-17"}`, `{"invoice": "A-17", "total": 120}`)
	containsChecker(t, s)

	out, cobraOut, err := run(t, "dispatch", "--no-stream", "--kind", "extraction",
		"--check", "cmd:contains total {file}", "pull the invoice number and total from this email as JSON")
	if err != nil {
		t.Fatalf("dispatch failed: %v (%s)", err, cobraOut)
	}
	if f.called("a") != 1 || f.called("b") != 1 {
		t.Fatalf("calls a=%d b=%d, want one each", f.called("a"), f.called("b"))
	}
	if !strings.Contains(out, "PASS") || !strings.Contains(out, "REJECTED") {
		t.Errorf("want the first answer rejected and the rerun passing:\n%s", out)
	}
}

// No fenced source means grounding cannot run: that is not a fail, so nothing reruns.
func TestDispatchCheck_NotCheckedNeverReruns(t *testing.T) {
	_, f := escalating(t, "summary", "The total is 450 dollars.", "The total is 120 dollars.")

	out, cobraOut, err := run(t, "dispatch", "--no-stream", "--kind", "summary", "--check", "ground", "summarise the invoice")
	if err != nil {
		t.Fatalf("dispatch failed: %v (%s)", err, cobraOut)
	}
	if f.called("b") != 0 || !strings.Contains(out, "NOT CHECKED") {
		t.Errorf("an unchecked answer reran (b called %d times):\n%s", f.called("b"), out)
	}
}

// The ceiling covers the rerun: the first answer fits, the longer retry does not.
func TestDispatchCheck_CeilingStopsTheRerun(t *testing.T) {
	_, f := escalating(t, "summary", "The total is 450 dollars. "+strings.Repeat("Paid in full. ", 300), "The total is 120 dollars.")

	out, cobraOut, err := run(t, "dispatch", "--no-stream", "--kind", "summary", "--max-cost", "0.0002",
		"--check", "ground", invoiceSummary)
	if err != nil {
		t.Fatalf("dispatch failed: %v (%s)", err, cobraOut)
	}
	if f.called("a") != 1 || f.called("b") != 0 {
		t.Fatalf("calls a=%d b=%d, want only a: the rerun does not fit the ceiling", f.called("a"), f.called("b"))
	}
	if !strings.Contains(out, "no rerun") || !strings.Contains(out, "ceiling") {
		t.Errorf("the report does not say the ceiling stopped the rerun:\n%s", out)
	}
}

// Each judged answer is filed under the head that wrote it, so the failure counts against "a".
func TestDispatchCheck_FilesBothJudgedAnswers(t *testing.T) {
	escalating(t, "summary", "The total is 450 dollars.", "The total is 120 dollars.")

	if _, cobraOut, err := run(t, "dispatch", "--no-stream", "--kind", "summary", "--check", "ground", invoiceSummary); err != nil {
		t.Fatalf("dispatch failed: %v (%s)", err, cobraOut)
	}
	examples, err := evalset.Load(evalset.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range examples {
		got[e.Head] = e.Passed
	}
	if passed, ok := got["litellm/a"]; len(examples) != 2 || !ok || passed || !got["litellm/b"] {
		t.Errorf("examples = %+v, want a failed one under litellm/a and a passed one under litellm/b", examples)
	}
}

func TestDispatchCheck_MaxEscalationsZeroTurnsItOff(t *testing.T) {
	_, f := escalating(t, "summary", "The total is 450 dollars.", "The total is 120 dollars.")

	out, cobraOut, err := run(t, "dispatch", "--no-stream", "--kind", "summary", "--max-escalations", "0",
		"--check", "ground", invoiceSummary)
	if err != nil {
		t.Fatalf("dispatch failed: %v (%s)", err, cobraOut)
	}
	if f.called("b") != 0 || !strings.Contains(out, "UNSUPPORTED") {
		t.Errorf("with --max-escalations 0 the failure should stand (b called %d times):\n%s", f.called("b"), out)
	}
}

func TestDispatchCheck_NegativeMaxEscalationsRefused(t *testing.T) {
	cliSandbox(t)
	if _, _, err := run(t, "dispatch", "--max-escalations", "-1", "--check", "ground", "q"); err == nil ||
		!strings.Contains(err.Error(), "--max-escalations") {
		t.Fatalf("err = %v, want a negative count refused", err)
	}
}
