// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
)

func report(rs ...Result) Report { return Report{Schema: schema, Suite: "s", Results: rs} }

func routed(id, head string, cost float64) Result {
	return Result{ID: id, Head: head, Tier: 7, CostUSD: cost}
}

func ensembled(id string, samples int, correct bool, cost float64) Result {
	r := routed(id, "claude", cost)
	r.SPRT = &SPRT{Decision: "accept", Samples: samples, Confidence: 0.96, SpentUSD: cost, Correct: correct}
	return r
}

func TestCompare_IdenticalReportsChangeNothing(t *testing.T) {
	r := report(routed("a", "flash-high", 0.01), ensembled("b", 2, true, 0.02))
	d, err := compare(r, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Changes) != 0 || len(d.Gate) != 0 {
		t.Fatalf("changes %v, gate %v, want none", d.Changes, d.Gate)
	}
	md := render(d, "s", "")
	for _, want := range []string{marker, "0 of 2 prompts changed", "Gate (advisory): nothing crossed."} {
		if !strings.Contains(md, want) {
			t.Errorf("comment lacks %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "<details") {
		t.Error("an unchanged run rendered a change table")
	}
}

func TestCompare_ReportsEachDecisionChangeOnce(t *testing.T) {
	base := report(routed("a", "flash-high", 0.01), routed("b", "flash-high", 0.01), routed("gone", "x", 0))
	head := report(routed("a", "ollama/qwen", 0), routed("b", "flash-high", 0.01), routed("new", "x", 0))
	d, err := compare(base, head)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, c := range d.Changes {
		ids = append(ids, c.ID)
	}
	if want := []string{"a", "new", "gone"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("changed %v, want %v", ids, want)
	}
	md := render(d, "s", "")
	for _, want := range []string{
		"| `a` | flash-high T7, 0 fallbacks, $0.010000 | ollama/qwen T7, 0 fallbacks, $0.000000 |",
		"| `new` | (not in suite) |",
		"| `gone` | x T7, 0 fallbacks, $0.000000 | (not in suite) |",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("comment lacks %q:\n%s", want, md)
		}
	}
}

func TestCompare_GatesOnCostEscalationVerifierAndRefusal(t *testing.T) {
	base := report(routed("r", "flash-high", 0.01), ensembled("v", 1, true, 0.01), ensembled("e", 1, true, 0.01))
	refused := Result{ID: "r", Refused: "no heads"}
	head := report(refused, ensembled("v", 1, false, 0.01), ensembled("e", 3, true, 0.05))
	d, err := compare(base, head)
	if err != nil {
		t.Fatal(err)
	}
	gate := strings.Join(d.Gate, "\n")
	for _, want := range []string{
		"`r` routed on base and is refused on head (no heads)",
		"`v` verified correct on base, not on head",
		"estimated cost rose +100.0%",
		"escalation rate rose +50.0 pp",
	} {
		if !strings.Contains(gate, want) {
			t.Errorf("gate lacks %q:\n%s", want, gate)
		}
	}
	md := render(d, "s", "")
	for _, want := range []string{
		"| Estimated cost (USD) | 0.030000 | 0.060000 | +100.0% |",
		"| Escalation rate (SPRT > 1 head) | 0/2 (0%) | 1/2 (50%) | +50.0 pp |",
		"| Verifier correct | 2/2 (100%) | 1/2 (50%) | -1 |",
		"| Refused | 0 | 1 | +1 |",
		"this PR would fail on",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("comment lacks %q:\n%s", want, md)
		}
	}
}

// Under the bars by a hair, so the thresholds are where the constants say.
func TestCompare_SmallMovesDoNotGate(t *testing.T) {
	base := report(routed("a", "x", 1.0))
	head := report(routed("a", "y", 1.09))
	d, err := compare(base, head)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Gate) != 0 {
		t.Fatalf("a 9%% rise gated: %v", d.Gate)
	}
}

func TestCompare_RefusesMismatchedSchemas(t *testing.T) {
	if _, err := compare(Report{Schema: 1}, Report{Schema: 2}); err == nil {
		t.Fatal("compared two report shapes")
	}
}

// A missing base has nothing to rise from, so it must not gate, or
// -fail-on-gate fails every PR whose base cannot build the harness.
func TestRender_NamesTheMissingBase(t *testing.T) {
	d, err := compare(report(), report(routed("a", "x", 0.01), ensembled("b", 3, true, 0.02)))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Gate) != 0 {
		t.Fatalf("a missing base gated: %v", d.Gate)
	}
	md := render(d, "s", "Base did not produce a report.")
	if !strings.Contains(md, "Base did not produce a report.") || !strings.Contains(md, "2 of 2 prompts changed") {
		t.Fatalf("missing base not reported:\n%s", md)
	}
	if !strings.Contains(md, "| n/a | 1/1 (100%) | n/a |") {
		t.Errorf("an empty ensemble set should read n/a, not 0/0 or a rise:\n%s", md)
	}
}

func TestCompare_SpendFromAFreeBaseGates(t *testing.T) {
	d, err := compare(report(routed("a", "ollama/qwen", 0)), report(routed("a", "claude", 0.01)))
	if err != nil {
		t.Fatal(err)
	}
	if gate := strings.Join(d.Gate, "\n"); !strings.Contains(gate, "estimated cost rose from $0 to $0.010000") {
		t.Fatalf("a free base moving to paid heads did not gate: %v", d.Gate)
	}
}

// The suite must route the same way twice and reach every SPRT outcome it was
// written for, or a diff of two runs reports noise as a regression.
func TestRunSuite_IsDeterministicAndExercisesEveryPath(t *testing.T) {
	before := os.Environ()
	first, err := runSuite(context.Background(), suiteJSON)
	if err != nil {
		t.Fatal(err)
	}
	second, err := runSuite(context.Background(), suiteJSON)
	if err != nil {
		t.Fatal(err)
	}
	if after := os.Environ(); !reflect.DeepEqual(before, after) {
		t.Error("runSuite left the environment changed, so the next run starts from a broken one")
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("two runs disagree:\n%+v\n%+v", first, second)
	}
	seen := map[string]bool{}
	for _, r := range first.Results {
		if r.Refused != "" {
			t.Errorf("%s refused (%s): the suite's machine should route every prompt", r.ID, r.Refused)
		}
		if r.SPRT != nil {
			seen[r.SPRT.Decision] = true
		}
	}
	for _, want := range []string{"accept", "stopped_on_budget", noEvidence} {
		if !seen[want] {
			t.Errorf("no prompt reached SPRT %q", want)
		}
	}
}
