// SPDX-License-Identifier: MIT

package cost

import (
	"math"
	"strconv"
	"testing"
)

// answers appends n answers of one kind on one model, the first pass of them
// passed, each its own span costing usd.
func answers(rows []Row, passed map[string]bool, kind, model string, n, pass int, usd float64) []Row {
	for i := 0; i < n; i++ {
		span := kind + "/" + model + "/" + strconv.Itoa(i)
		rows = append(rows, Row{Model: model, Domain: kind, SpanID: span, EstCostUSD: usd})
		passed[span] = i < pass
	}
	return rows
}

// Below MinChecked checked answers there is no report, only the count, so a
// thin history shows nothing rather than a guess.
func TestPerPassedAnswer_ThinHistoryShowsNothing(t *testing.T) {
	passed := map[string]bool{}
	rows := answers(nil, passed, "summarise", "a", MinChecked-1, MinChecked-1, 0.01)
	// Unchecked rows do not count toward the minimum.
	rows = append(rows, Row{Model: "a", Domain: "summarise", SpanID: "unchecked", EstCostUSD: 1})
	rows = append(rows, Row{Model: "a", Domain: "summarise", EstCostUSD: 1})

	rep, checked, ok := PerPassedAnswer(rows, passed)
	if ok || checked != MinChecked-1 || rep.Routed.Answers != 0 || rep.Kinds != nil {
		t.Fatalf("thin history: ok=%v checked=%d rep=%+v", ok, checked, rep)
	}
	if _, _, ok := PerPassedAnswer(nil, nil); ok {
		t.Error("an empty log produced a report")
	}
}

// Cost per passed answer counts the spend on failed answers too, and the best
// single model is chosen on its pass rate's lower bound, not on price.
func TestPerPassedAnswer_CostPerPassAndBestSingle(t *testing.T) {
	passed := map[string]bool{}
	var rows []Row
	rows = answers(rows, passed, "code", "cheap", 40, 20, 0.001) // 50% passed
	rows = answers(rows, passed, "code", "strong", 40, 38, 0.01) // 95% passed
	rows = answers(rows, passed, "code", "rare", MinChecked-1, MinChecked-1, 0.0001)
	rows = answers(rows, passed, "", "cheap", 10, 10, 0.001) // no kind: routed total only

	rep, checked, ok := PerPassedAnswer(rows, passed)
	if !ok || checked != 119 {
		t.Fatalf("ok=%v checked=%d", ok, checked)
	}
	if rep.Routed.Answers != 119 || rep.Routed.Passed != 97 {
		t.Errorf("routed = %+v", rep.Routed)
	}
	want := (40*0.001 + 40*0.01 + 29*0.0001 + 10*0.001) / 97
	if got, _ := rep.Routed.PerPassed(); math.Abs(got-want) > 1e-12 {
		t.Errorf("routed per passed = %v, want %v", got, want)
	}
	if !(rep.Routed.Lo < rep.Routed.Rate() && rep.Routed.Rate() < rep.Routed.Hi) {
		t.Errorf("interval %v–%v does not hold %v", rep.Routed.Lo, rep.Routed.Hi, rep.Routed.Rate())
	}
	if len(rep.Kinds) != 1 || rep.Kinds[0].Kind != "code" || rep.Kinds[0].Routed.Answers != 109 {
		t.Fatalf("kinds = %+v", rep.Kinds)
	}
	// "rare" is cheapest and never failed, but has too few answers to rank.
	k := rep.Kinds[0]
	if k.Best != "strong" || k.BestRecord.Passed != 38 {
		t.Errorf("best = %q %+v", k.Best, k.BestRecord)
	}
	if got, _ := k.BestRecord.PerPassed(); math.Abs(got-0.4/38) > 1e-12 {
		t.Errorf("best per passed = %v", got)
	}
}

// An answer more than one model worked on belongs to the routing, not to any
// single model; a kind with no single model of enough answers has no best.
func TestPerPassedAnswer_MixedAnswerIsNotSingleModel(t *testing.T) {
	passed := map[string]bool{}
	var rows []Row
	for i := 0; i < MinChecked; i++ {
		span := "s" + strconv.Itoa(i)
		passed[span] = true
		rows = append(rows,
			Row{Model: "a", Domain: "draft", SpanID: span, EstCostUSD: 0.01},
			Row{Model: "b", Domain: "draft", SpanID: span, EstCostUSD: 0.02})
	}
	rep, _, ok := PerPassedAnswer(rows, passed)
	if !ok || len(rep.Kinds) != 1 {
		t.Fatalf("ok=%v kinds=%+v", ok, rep.Kinds)
	}
	if rep.Kinds[0].Best != "" {
		t.Errorf("a mixed answer was credited to %q", rep.Kinds[0].Best)
	}
	if got, _ := rep.Routed.PerPassed(); math.Abs(got-0.03) > 1e-12 {
		t.Errorf("both rows' spend not counted: %v", got)
	}
}

// Nothing passed means cost per pass is undefined, not zero or infinite.
func TestPassRecord_NoPassesHasNoCostPerPass(t *testing.T) {
	if _, ok := (PassRecord{Answers: 5, CostUSD: 1}).PerPassed(); ok {
		t.Error("cost per pass reported with no passes")
	}
	if r := (PassRecord{}).Rate(); r != 0 {
		t.Errorf("empty rate = %v", r)
	}
}

// Equal pass records go to the cheaper pass, then to the name, so the same log
// always names the same model.
func TestPerPassedAnswer_TiesAreDeterministic(t *testing.T) {
	passed := map[string]bool{}
	var rows []Row
	rows = answers(rows, passed, "translate", "dear", 40, 36, 0.02)
	rows = answers(rows, passed, "translate", "lean", 40, 36, 0.01)
	rows = answers(rows, passed, "translate", "twin", 40, 36, 0.01)
	rep, _, ok := PerPassedAnswer(rows, passed)
	if !ok || len(rep.Kinds) != 1 || rep.Kinds[0].Best != "lean" {
		t.Fatalf("ok=%v kinds=%+v", ok, rep.Kinds)
	}
}
