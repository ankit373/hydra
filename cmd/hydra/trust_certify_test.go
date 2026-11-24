// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/aver"
	"github.com/ankit373/hydra/internal/evalset"
)

// seedCertifyFixture writes one kind, one check, two heads. weak: tests passed
// 0 of 40 wrong answers and 36 of 40 right. strong: 16 of 40 wrong, 18 of 40 right.
func seedCertifyFixture(t *testing.T) {
	t.Helper()
	p := evalset.VerdictsPath()
	add := func(head string, i int, correct, passed bool) {
		span := fmt.Sprintf("%s-%v-%d", head, correct, i)
		v := evalset.Verdict{TS: "2026-11-01T00:00:00Z", SpanID: span, Head: head, Domain: "code", Check: "tests", Passed: passed}
		if err := evalset.AddVerdict(p, v); err != nil {
			t.Fatal(err)
		}
		out := evalset.OutcomeIncorrect
		if correct {
			out = evalset.OutcomeCorrect
		}
		if err := evalset.AddTruth(p, evalset.Truth{TS: v.TS, SpanID: span, Outcome: out}); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 40 {
		add("weak", i, false, false)
		add("weak", i, true, i < 36)
		add("strong", i, false, i < 16)
		add("strong", i, true, i < 18)
	}
}

func TestTrustCertifyTable_FromFixtureVerdictLog(t *testing.T) {
	cliSandbox(t)
	seedCertifyFixture(t)

	out, _, err := run(t, "trust", "certify-table", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var tab aver.Table
	if err := json.Unmarshal([]byte(out), &tab); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if len(tab.Cells) != 2 {
		t.Fatalf("want one cell per head, got %+v", tab.Cells)
	}
	strong, weak := tab.Cells[0], tab.Cells[1]
	// One check, so delta is not split: q̄ = 1 - 0.05^(1/40) for 0 of 40 (Clopper-Pearson, k = 0).
	if want := 1 - math.Pow(0.05, 1.0/40); weak.Head != "weak" || math.Abs(weak.QBar-want) > 1e-9 || weak.Skip {
		t.Fatalf("weak cell %+v, want q̄ %.6f", weak, want)
	}
	if strong.Head != "strong" || strong.FalsePass != 16 || !strong.Skip {
		t.Fatalf("strong cell %+v, want 16 false passes and skipped", strong)
	}

	out, _, err = run(t, "trust", "certify-table", "--kind", "code")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 7 && f[1] == "strong" && f[6] != "skip" {
			t.Errorf("strong row is not shown as skip: %q", line)
		}
		if len(f) == 7 && f[1] == "weak" && f[6] != fmt.Sprintf("%.2f", weak.EPass()) {
			t.Errorf("weak row has the wrong e on pass: %q", line)
		}
	}
	if !strings.Contains(out, "skip") || !strings.Contains(out, "weak") {
		t.Fatalf("table misses a row:\n%s", out)
	}
}

func TestTrustCertifyTable_EmptyLogAndOtherKind(t *testing.T) {
	cliSandbox(t)
	out, _, err := run(t, "trust", "certify-table")
	if err != nil || !strings.Contains(out, "No answers with ground truth") {
		t.Fatalf("empty log: err %v\n%s", err, out)
	}
	seedCertifyFixture(t)
	out, _, err = run(t, "trust", "certify-table", "--kind", "writing")
	if err != nil || !strings.Contains(out, "No answers with ground truth") {
		t.Fatalf("other kind: err %v\n%s", err, out)
	}
	if _, _, err := run(t, "trust", "certify-table", "--delta", "2"); err == nil {
		t.Fatal("delta 2 accepted")
	}
}

func TestAverAnswers_KeepsLabelledAndFoldsRepeats(t *testing.T) {
	in := []evalset.Answer{
		{Key: "a", Head: "m", Domain: "go", Truth: &evalset.Truth{Outcome: evalset.OutcomeIncorrect},
			Checks: []evalset.Verdict{{Check: "tests", Passed: true}, {Check: "tests", Passed: false}}},
		{Key: "b", Truth: &evalset.Truth{Outcome: evalset.OutcomeCorrect},
			Checks: []evalset.Verdict{{Check: "lint", Passed: true}}},
		{Key: "c", Head: "m", Domain: "go", Checks: []evalset.Verdict{{Check: "tests", Passed: true}}},
	}
	got := averAnswers(in)
	if len(got) != 2 {
		t.Fatalf("want the two labelled answers, got %+v", got)
	}
	if got[0].Correct || got[0].Verdicts["tests"] || got[0].Kind != "go" || got[0].Head != "m" {
		t.Fatalf("first answer: %+v", got[0])
	}
	if !got[1].Correct || got[1].Kind != "unknown" || got[1].Head != "unknown" || !got[1].Verdicts["lint"] {
		t.Fatalf("second answer: %+v", got[1])
	}
}
