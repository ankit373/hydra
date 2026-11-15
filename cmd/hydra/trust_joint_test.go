// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/evalset"
	"github.com/ankit373/hydra/internal/stats"
)

// seedJointFixture writes a verdict log with known counts. In "go", 25 wrong
// answers: go-test passed 10, lint 15, both 8. In "support", 5 wrong answers
// checked by grounding and a human, under the floor. Right answers are noise.
func seedJointFixture(t *testing.T) {
	t.Helper()
	p := evalset.VerdictsPath()
	add := func(span, domain, check string, passed bool) {
		if err := evalset.AddVerdict(p, evalset.Verdict{SpanID: span, Domain: domain, Check: check, Passed: passed}); err != nil {
			t.Fatal(err)
		}
	}
	truth := func(span, outcome string) {
		if err := evalset.AddTruth(p, evalset.Truth{SpanID: span, Outcome: outcome, Source: "review"}); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 25 {
		span := fmt.Sprintf("go-%d", i)
		add(span, "go", "go-test", i < 10)
		add(span, "go", "lint", i < 8 || (i >= 10 && i < 17))
		truth(span, evalset.OutcomeIncorrect)
	}
	for i := range 5 {
		span := fmt.Sprintf("support-%d", i)
		add(span, "support", "verifier:grounding", true)
		add(span, "support", "human", i < 2)
		truth(span, evalset.OutcomeIncorrect)
	}
	for i := range 30 {
		span := fmt.Sprintf("right-%d", i)
		add(span, "go", "go-test", true)
		add(span, "go", "lint", true)
		truth(span, evalset.OutcomeCorrect)
	}
}

func jointJSON(t *testing.T, args ...string) jointReport {
	t.Helper()
	out, _, err := run(t, append([]string{"trust", "joint", "--json"}, args...)...)
	if err != nil {
		t.Fatal(err)
	}
	var rep jointReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	return rep
}

func findRow(t *testing.T, rows []falsePass, checks ...string) falsePass {
	t.Helper()
	for _, r := range rows {
		if strings.Join(r.Checks, "+") == strings.Join(checks, "+") {
			return r
		}
	}
	t.Fatalf("no row for %v in %+v", checks, rows)
	return falsePass{}
}

func TestTrustJoint_PairRatesFromFixture(t *testing.T) {
	cliSandbox(t)
	seedJointFixture(t)

	rep := jointJSON(t, "--domain", "go")
	if len(rep.Domains) != 1 || rep.Domains[0].Wrong != 25 {
		t.Fatalf("domains = %+v, want go with 25 wrong answers", rep.Domains)
	}
	d := rep.Domains[0]
	test, lint := findRow(t, d.Checks, "go-test"), findRow(t, d.Checks, "lint")
	if test.Passed != 10 || *test.Rate != 0.4 || lint.Passed != 15 || *lint.Rate != 0.6 {
		t.Errorf("single rates: go-test %+v, lint %+v", test, lint)
	}
	pair := findRow(t, d.Pairs, "go-test", "lint")
	if pair.Wrong != 25 || pair.Passed != 8 || *pair.Rate != 0.32 {
		t.Errorf("pair = %+v, want 8 of 25", pair)
	}
	if *pair.Independent != 0.4*0.6 {
		t.Errorf("independent = %v, want the product of the single rates", *pair.Independent)
	}
	want, err := stats.ClopperPearsonUpper(8, 25, jointAlpha)
	if err != nil || *pair.Upper != want {
		t.Errorf("upper = %v, want %v", *pair.Upper, want)
	}
}

func TestTrustJoint_RefusesBelowFloor(t *testing.T) {
	cliSandbox(t)
	seedJointFixture(t)

	out, _, err := run(t, "trust", "joint", "--domain", "support")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "too few wrong answers") || !strings.Contains(out, "wider than the rate") {
		t.Errorf("a 5-answer domain was not refused with its reason:\n%s", out)
	}
	for _, r := range jointJSON(t, "--domain", "support").Domains[0].Pairs {
		if r.Rate != nil || r.Upper != nil {
			t.Errorf("row %+v states a rate under the floor", r)
		}
	}
}

func TestTrustJoint_JSON(t *testing.T) {
	cliSandbox(t)
	seedJointFixture(t)

	rep := jointJSON(t)
	var domains []string
	for _, d := range rep.Domains {
		domains = append(domains, d.Domain)
	}
	if fmt.Sprint(domains) != "[go support]" {
		t.Fatalf("domains = %v, want go and support in order", domains)
	}
	human := findRow(t, rep.Domains[1].Checks, "human")
	if human.Wrong != 5 || human.Passed != 2 {
		t.Errorf("human = %+v, want 2 of 5", human)
	}
}

// One judge starts passing wrong answers six times as often, 0.05 to 0.30,
// while the test suite beside it holds at 0.05.
func TestTrustJoint_FlagsWeakenedCheck(t *testing.T) {
	cliSandbox(t)
	p := evalset.VerdictsPath()
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for i := range 600 {
		span, ts := fmt.Sprintf("w-%d", i), start.Add(time.Duration(i)*time.Minute).Format(time.RFC3339)
		judge := i%20 == 0
		if i >= 400 {
			judge = i%10 < 3
		}
		for _, v := range []evalset.Verdict{{Check: "go-test", Passed: i%20 == 0}, {Check: "judge", Passed: judge}} {
			v.TS, v.SpanID, v.Domain = ts, span, "go"
			if err := evalset.AddVerdict(p, v); err != nil {
				t.Fatal(err)
			}
		}
		if err := evalset.AddTruth(p, evalset.Truth{SpanID: span, Outcome: evalset.OutcomeIncorrect}); err != nil {
			t.Fatal(err)
		}
	}

	d := jointJSON(t, "--domain", "go").Domains[0]
	if steady := findRow(t, d.Checks, "go-test"); steady.Changed != "" || steady.Since != nil {
		t.Errorf("steady check flagged: %+v", steady)
	}
	weak := findRow(t, d.Checks, "judge")
	if weak.Since == nil || weak.Since.Rate == nil {
		t.Fatalf("weakened check not flagged with a rate since: %+v", weak)
	}
	if weak.Since.Wrong < 150 || weak.Since.Wrong > 250 || *weak.Since.Rate < 0.2 {
		t.Errorf("since %d/%d, want about the 200 answers at 0.30 after the change", weak.Since.Passed, weak.Since.Wrong)
	}
	want, _ := stats.ClopperPearsonUpper(weak.Since.Passed, weak.Since.Wrong, jointAlpha)
	if *weak.Since.Upper != want {
		t.Errorf("since upper %v, want %v", *weak.Since.Upper, want)
	}

	out, _, err := run(t, "trust", "joint", "--domain", "go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, "changed ") != 1 || !strings.Contains(out, "since then passed") {
		t.Errorf("want one changed line, under judge:\n%s", out)
	}
}
