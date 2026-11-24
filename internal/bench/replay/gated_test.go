// SPDX-License-Identifier: MIT

package replay

import (
	"fmt"
	"strings"
	"testing"
)

// correlated: one head, three tasks in four wrong. On every tenth wrong answer
// tests and lint both pass, and never one without the other. Right answers pass both.
func correlated(tasks int) []Row {
	var rows []Row
	wrong := 0
	for i := range tasks {
		right := i%4 == 0
		pass := right
		if !right {
			pass = wrong%10 == 0
			wrong++
		}
		rows = append(rows, averRow(fmt.Sprintf("t%03d", i), "m", 1, right, pass, pass))
	}
	return rows
}

func TestReplay_GatedProductOnCorrelatedFixture(t *testing.T) {
	rep, err := Run(correlated(400), Config{Policies: []string{"aver_product_perhead", "aver_gated"},
		Alphas: []float64{0.05}, Seeds: []uint64{0, 1, 2, 3, 4}})
	if err != nil {
		t.Fatal(err)
	}
	plain, gated := rep.Summaries[0], rep.Summaries[1]
	if plain.Policy != "aver_product_perhead" || gated.Policy != "aver_gated" {
		t.Fatalf("summaries out of order: %+v", rep.Summaries)
	}
	// Plain multiplies two e of about 6 each past bar 20 and accepts the 7.5% of
	// tasks whose wrong answer passed both; gated counts one e and accepts nothing.
	if !(plain.MeanFalseAccept > 0.05) {
		t.Fatalf("plain product should break alpha on this fixture, got %+v", plain)
	}
	if gated.MeanFalseAccept != 0 || gated.Refusal != 1 {
		t.Fatalf("gated product counted the pair twice: %+v", gated)
	}
	out := Table(rep)
	if !strings.Contains(out, "aver_gated") || !strings.Contains(out, "aver_product_perhead") {
		t.Fatalf("table lacks a row:\n%s", out)
	}
}

func TestReplay_GatedPrintsUntestablePairs(t *testing.T) {
	rep, err := Run(correlated(40), Config{Policies: []string{"aver_gated"}, Alphas: []float64{0.05}, Seeds: []uint64{0}})
	if err != nil {
		t.Fatal(err)
	}
	if s := rep.Summaries[0]; s.Untestable != 1 {
		t.Fatalf("15 shared wrong answers cannot test the pair: %+v", s)
	}
	if !strings.Contains(Table(rep), "1 check pair(s) linked for lack of shared wrong answers") {
		t.Fatalf("table does not say so:\n%s", Table(rep))
	}
}
