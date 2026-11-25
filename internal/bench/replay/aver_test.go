// SPDX-License-Identifier: MIT

package replay

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/aver"
)

// averRow is one candidate with two checks; lint is free and tests cost 0.01.
func averRow(task, head string, cost float64, correct, tests, lint bool) Row {
	return Row{Task: task, Kind: "code", Head: head, CostUSD: cost, Correct: correct,
		Verdicts:     map[string]bool{"tests": tests, "lint": lint},
		CheckCostUSD: map[string]float64{"tests": 0.01}}
}

// averFixture: the cheap head's checks never pass its wrong answers, the dear
// head's pass half of them. Every right answer passes both checks.
func averFixture(tasks int) []Row {
	var rows []Row
	for i := range tasks {
		task := fmt.Sprintf("t%03d", i)
		rows = append(rows,
			averRow(task, "cheap", 1, i%2 == 0, i%2 == 0, i%2 == 0),
			averRow(task, "dear", 10, i%2 == 0, i%2 == 0 || i%4 == 1, i%2 == 0 || i%4 == 1))
	}
	return rows
}

func calibrated(t *testing.T, name string, alpha float64, cal []Row) Policy {
	t.Helper()
	p, err := New(name, Options{Alpha: alpha, Delta: aver.DefaultDelta})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Calibrate(cal); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReplay_AverProductOnFixture(t *testing.T) {
	cal := averFixture(80) // 40 wrong answers per head
	order := []string{"cheap", "dear"}
	per := calibrated(t, "aver_product_perhead", 0.1, cal)

	// cheap: 0 false passes in 40 at 0.05/2, q̄ = 1 - 0.025^(1/40) = 0.0880 (Clopper-Pearson, k = 0),
	// s = 1, so a pass is worth 11.36 and two are worth 129 >= bar 2/0.1 = 20.
	o := per.Decide([]Row{averRow("x", "cheap", 1, true, true, true), averRow("x", "dear", 10, true, true, true)}, order)
	if o.Accepted == nil || o.Accepted.Head != "cheap" || math.Abs(o.Spent-1.01) > 1e-12 {
		t.Fatalf("want cheap accepted for 1 + 0.01, got %+v", o)
	}
	qbar := 1 - math.Pow(0.025, 1.0/40)
	if want := fmt.Sprintf("product %.3g >= bar 20", 1/(qbar*qbar)); o.Reason != want {
		t.Fatalf("reason %q, want %q", o.Reason, want)
	}

	// A failed free lint makes cheap's product 0 at once, so tests is never paid for.
	// dear's q̄ is near 0.7: even two passes cannot reach 20, so it is never bought.
	o = per.Decide([]Row{averRow("y", "cheap", 1, false, true, false), averRow("y", "dear", 10, true, true, true)}, order)
	if o.Accepted != nil || o.Spent != 1 || !slices.Equal(o.Bought, []string{"cheap"}) {
		t.Fatalf("want a refusal that paid only for cheap's answer, got %+v", o)
	}

	// Pooled, the dear head's false passes widen the one shared bound past reach.
	pooled := calibrated(t, "aver_product", 0.1, cal)
	o = pooled.Decide([]Row{averRow("x", "cheap", 1, true, true, true)}, order)
	if o.Accepted != nil || o.Spent != 0 || o.Reason != "no answer reached its bar" {
		t.Fatalf("pooled should refuse without buying, got %+v", o)
	}

	rep, err := Run(averFixture(200), Config{Policies: []string{"aver_product", "aver_product_perhead"},
		Alphas: []float64{0.1}, Seeds: []uint64{0, 1, 2}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Summaries) != 2 {
		t.Fatalf("want a summary per policy, got %+v", rep.Summaries)
	}
	for _, s := range rep.Summaries {
		if s.MeanFalseAccept != 0 {
			t.Errorf("%s accepted a wrong answer: %+v", s.Policy, s)
		}
	}
	if pooledS, perS := rep.Summaries[0], rep.Summaries[1]; !(pooledS.Refusal == 1 && perS.Refusal < 0.6) {
		t.Fatalf("refusal pooled %v per head %v; want per head to accept the cheap right answers", pooledS.Refusal, perS.Refusal)
	}
}

func TestReplay_AverMissingVerdictIsNotRun(t *testing.T) {
	// Two heads at alpha 0.2 by spend: bar 1/(0.2 * 1/2) = 10; lint alone is worth 11.36.
	per := calibrated(t, "aver_product_perhead", 0.2, averFixture(80))
	r := averRow("z", "cheap", 1, true, true, true)
	delete(r.Verdicts, "tests")
	o := per.Decide([]Row{r}, []string{"cheap"})
	if o.Accepted == nil || o.Spent != 1 {
		t.Fatalf("lint alone clears bar 10 and tests was never paid for: %+v", o)
	}
}

func TestReplay_AverRefusesCalibrationWithNoVerdicts(t *testing.T) {
	p, err := New("aver_product", Options{Alpha: 0.05, Delta: aver.DefaultDelta})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Calibrate([]Row{{Task: "t", Kind: "k", Head: "h"}}); err == nil {
		t.Fatal("calibrated on rows with no verdicts")
	}
}

func TestReplay_DeltaReachesTheFit(t *testing.T) {
	// With delta 0.5 the cheap head's q̄ is 1 - 0.25^(1/40) = 0.0341, so one free
	// lint pass is worth 29.3 and clears bar 20 alone; at 0.05 it is worth 11.4.
	cal := averFixture(80)
	task := []Row{averRow("x", "cheap", 1, true, true, true)}
	order := []string{"cheap", "dear"}
	loose, err := New("aver_product_perhead", Options{Alpha: 0.1, Delta: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	if err := loose.Calibrate(cal); err != nil {
		t.Fatal(err)
	}
	if o := loose.Decide(task, order); o.Accepted == nil || o.Spent != 1 {
		t.Fatalf("delta 0.5 should accept on lint alone, without paying for tests: %+v", o)
	}
	if o := calibrated(t, "aver_product_perhead", 0.1, cal).Decide(task, order); o.Spent != 1.01 {
		t.Fatalf("delta 0.05 needs both checks: %+v", o)
	}
	for _, d := range []float64{-0.1, 1, 2} {
		if _, err := Run(cal, Config{Policies: []string{"aver_product"}, Alphas: []float64{0.1}, Seeds: []uint64{0}, Delta: d}); err == nil {
			t.Errorf("delta %v accepted", d)
		}
	}
}

// threeHeads: cheap is right on even tasks, mid on the rest, dear never.
// Wrong answers from cheap and mid never pass a check; dear's always do.
func threeHeads(tasks int) []Row {
	var rows []Row
	for i := range tasks {
		task := fmt.Sprintf("t%03d", i)
		even := i%2 == 0
		rows = append(rows,
			averRow(task, "cheap", 1, even, even, even),
			averRow(task, "mid", 2, !even, !even, !even),
			averRow(task, "dear", 3, false, true, true))
	}
	return rows
}

func TestReplay_SpendVsBonferroniBothUnderAlphaOnFixture(t *testing.T) {
	rep, err := Run(threeHeads(200), Config{
		Policies: []string{"aver_product_perhead", "aver_product_perhead:bonferroni"},
		Alphas:   []float64{0.1}, Seeds: []uint64{0, 1, 2}})
	if err != nil {
		t.Fatal(err)
	}
	// alpha 0.1 over 3 candidates: spend 1/2, 1/4, 1/4 gives bars 20, 40, 40;
	// bonferroni 1/3 each gives 30, 30, 30.
	want := map[string][]float64{
		"aver_product_perhead":            {20, 40, 40},
		"aver_product_perhead:bonferroni": {30, 30, 30},
	}
	for _, s := range rep.Summaries {
		if s.MeanFalseAccept > s.Alpha || s.Refusal != 0 {
			t.Errorf("%s: false accept %v, refusal %v", s.Policy, s.MeanFalseAccept, s.Refusal)
		}
		for j, b := range want[s.Policy] {
			if math.Abs(s.Bars[j]-b) > 1e-9 {
				t.Fatalf("%s bars %v, want %v", s.Policy, s.Bars, want[s.Policy])
			}
		}
	}
	if out := Table(rep); !strings.Contains(out, "bars per candidate, code aver_product_perhead:bonferroni alpha 0.10: 30, 30, 30") {
		t.Fatalf("table does not show the bars:\n%s", out)
	}
}

func TestReplay_BonferroniRefusedAboveThreeHeads(t *testing.T) {
	rows := threeHeads(20)
	for i := range 20 {
		rows = append(rows, averRow(fmt.Sprintf("t%03d", i), "fourth", 4, false, false, false))
	}
	_, err := Run(rows, Config{Policies: []string{"aver_product:bonferroni"}, Alphas: []float64{0.1}, Seeds: []uint64{0}})
	if err == nil || !strings.Contains(err.Error(), "use spend") {
		t.Fatalf("want bonferroni refused for 4 heads, got %v", err)
	}
	if _, err := New("first_check_pass:spend", Options{Check: "tests"}); err == nil {
		t.Fatal("a rule on a non-AVER policy was accepted")
	}
}

func TestReplay_CandidateWithNoWeightIsNeverAccepted(t *testing.T) {
	// Calibrated on one head, so there is one bar; a second head in the order gets none.
	var cal []Row
	for _, r := range averFixture(80) {
		if r.Head == "cheap" {
			cal = append(cal, r)
		}
	}
	p := calibrated(t, "aver_product_perhead", 0.1, cal)
	o := p.Decide([]Row{averRow("x", "cheap", 1, false, true, false), averRow("x", "dear", 10, true, true, true)},
		[]string{"cheap", "dear"})
	if o.Accepted != nil || slices.Contains(o.Bought, "dear") {
		t.Fatalf("the second candidate had no weight but was bought: %+v", o)
	}
}
