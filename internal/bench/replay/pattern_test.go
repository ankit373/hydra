// SPDX-License-Identifier: MIT

package replay

import (
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/aver"
)

func TestReplay_PatternRefusesWithNeedOnSmallFixture(t *testing.T) {
	rep, err := Run(averFixture(20), Config{Policies: []string{"aver_pattern"}, Alphas: []float64{0.1}, Seeds: []uint64{0}})
	if err != nil {
		t.Fatal(err)
	}
	s := rep.Summaries[0]
	if s.Refusal != 1 || s.CostPerTask != 0 {
		t.Fatalf("a small fixture should refuse everything and buy nothing: %+v", s)
	}
	// 10 calibration tasks, 5 wrong per head. Slot 0: bar 1/(0.1 * 1/2) = 20, 3 patterns,
	// delta 0.05 over 2 slots: need = ceil(ln(3 / 0.025) * 20) = 96.
	p, err := New("aver_pattern", Options{Alpha: 0.1, Delta: aver.DefaultDelta})
	if err != nil {
		t.Fatal(err)
	}
	cal := averFixture(10)
	if err := p.Calibrate(cal); err != nil {
		t.Fatal(err)
	}
	o := p.Decide(cal[:2], nil)
	if o.Reason != "not verified: too few labelled wrong answers, have 5, need 96" {
		t.Fatalf("reason %q", o.Reason)
	}
}

func TestReplay_PatternAcceptsWithEnoughWrongAnswers(t *testing.T) {
	cal := averFixture(220) // 110 wrong answers per head, cheap's checks never pass them
	p, err := New("aver_pattern", Options{Alpha: 0.1, Delta: aver.DefaultDelta})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Calibrate(cal); err != nil {
		t.Fatal(err)
	}
	o := p.Decide([]Row{averRow("x", "cheap", 1, true, true, true), averRow("x", "dear", 10, true, true, true)}, nil)
	if o.Accepted == nil || o.Accepted.Head != "cheap" || !strings.Contains(o.Reason, "bar 20") {
		t.Fatalf("want cheap accepted on its pattern, got %+v", o)
	}
	// The pattern was fixed on calibration: a test answer failing it is not
	// rescued by refitting, and dear's leaky checks give it no pattern at all.
	o = p.Decide([]Row{averRow("y", "cheap", 1, true, false, false), averRow("y", "dear", 10, true, true, true)}, nil)
	if o.Accepted != nil || o.Reason != "no answer matched its pattern" {
		t.Fatalf("want a refusal, got %+v", o)
	}
	rep, err := Run(averFixture(600), Config{Policies: []string{"aver_pattern"}, Alphas: []float64{0.1}, Seeds: []uint64{0, 1}})
	if err != nil {
		t.Fatal(err)
	}
	if s := rep.Summaries[0]; s.MeanFalseAccept != 0 || s.Refusal >= 0.6 || len(s.Bars) != 2 {
		t.Fatalf("pattern mode should accept the cheap right answers and no wrong one: %+v", s)
	}
}

func TestReplay_PatternUsesAgreement(t *testing.T) {
	// Two heads with no checks that separate anything, but the second agrees
	// with the first only when both are right.
	var rows []Row
	for i := range 600 {
		task := string(rune('a'+i%26)) + strings.Repeat("x", i/26)
		right := i%2 == 0
		key := "w1"
		if right {
			key = "ok"
		}
		a := Row{Task: task, Kind: "math", Head: "a", CostUSD: 1, Correct: right, AnswerKey: key, Verdicts: map[string]bool{"format": true}}
		b := a
		b.Head, b.CostUSD = "b", 2
		if !right {
			b.AnswerKey = "w2"
		}
		rows = append(rows, a, b)
	}
	rep, err := Run(rows, Config{Policies: []string{"aver_pattern"}, Alphas: []float64{0.1}, Seeds: []uint64{0}})
	if err != nil {
		t.Fatal(err)
	}
	if s := rep.Summaries[0]; s.MeanFalseAccept != 0 || s.Refusal > 0.55 {
		t.Fatalf("agreement should certify the right answers: %+v", s)
	}
	if _, err := New("aver_pattern", Options{}); err != nil {
		t.Fatal(err)
	}
	empty, _ := New("aver_pattern", Options{Alpha: 0.1, Delta: 0.05})
	if err := empty.Calibrate([]Row{{Task: "t", Kind: "k", Head: "h"}}); err == nil {
		t.Fatal("calibrated with no verdicts")
	}
}
