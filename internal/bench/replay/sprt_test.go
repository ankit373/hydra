// SPDX-License-Identifier: MIT

package replay

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ankit373/hydra/internal/trust"
)

// sprtRows: 120 math tasks, four heads. a and b are one family and give the same
// wrong answer when both are wrong on a multiple of 3; c and d are wrong alone.
func sprtRows() []Row {
	var rows []Row
	for i := range 120 {
		task := fmt.Sprintf("q%03d", i)
		key := func(right bool, wrong string) string {
			if right {
				return "r"
			}
			return wrong
		}
		shared := fmt.Sprintf("w%d", i)
		bWrong := shared
		if i%3 != 0 {
			bWrong = shared + "b"
		}
		aRight, bRight, cRight, dRight := i%10 != 0, i%7 != 0, i%6 != 0, i%9 != 0
		rows = append(rows,
			Row{Task: task, Kind: "math", Head: "a", Family: "f", CostUSD: 1, Correct: aRight, AnswerKey: key(aRight, shared)},
			Row{Task: task, Kind: "math", Head: "b", Family: "f", CostUSD: 2, Correct: bRight, AnswerKey: key(bRight, bWrong)},
			Row{Task: task, Kind: "math", Head: "c", Family: "g", CostUSD: 3, Correct: cRight, AnswerKey: key(cRight, shared+"c")},
			Row{Task: task, Kind: "math", Head: "d", CostUSD: 4, Correct: dRight, AnswerKey: key(dRight, shared+"d")})
	}
	return rows
}

// The replay policy must decide exactly as the live path does: the same fit
// written to disk and read back by trust.New, and the co-agreement log read by
// an unmodified trust.Run, on every test task.
func TestSPRT_AgreesWithTrustRunOnItsLivePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HYDRA_HOME", home)
	rows := sprtRows()
	byTask := map[string][]Row{}
	for _, r := range rows {
		byTask[r.Task] = append(byTask[r.Task], r)
	}
	sp := SplitTasks(rows, 0)
	var cal []Row
	for _, task := range sp.Calibration {
		cal = append(cal, byTask[task]...)
	}
	order := CheapestFirst(cal)

	pol, err := New("sprt", Options{Alpha: 0.05})
	if err != nil {
		t.Fatal(err)
	}
	if err := pol.Calibrate(cal); err != nil {
		t.Fatal(err)
	}
	sp2 := pol.(*sprtPolicy)
	if d := sp2.co.Discount("f"); d == 0.5 {
		t.Fatal("the family discount fell back to its default; the fixture must measure it")
	}

	calPath := filepath.Join(home, "calibration.jsonl")
	written, err := trust.New(calPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := fitSPRT(cal, written, func(d string, ids, fams, texts []string) {
		trust.RecordCoAgreement(trust.DefaultCoAgreementPath(), d, ids, fams, texts, sameKey)
	}); err != nil {
		t.Fatal(err)
	}
	live, err := trust.New(calPath)
	if err != nil {
		t.Fatal(err)
	}
	coLog, err := os.ReadFile(trust.DefaultCoAgreementPath())
	if err != nil {
		t.Fatal(err)
	}

	accepted, refused := 0, 0
	for _, task := range sp.Test {
		got := pol.Decide(byTask[task], order)
		// trust.Run appends to the log after each run; put it back so every task
		// sees the calibrate half only, as the replay does.
		if err := os.WriteFile(trust.DefaultCoAgreementPath(), coLog, 0o600); err != nil {
			t.Fatal(err)
		}
		rowsBy := byHead(byTask[task])
		res, err := trust.Run(context.Background(), trust.Task{Domain: "math"}, sp2.sources(rowsBy, order),
			sprtExec(rowsBy), live, trust.Target{Confidence: 0.95}, trust.WithEquivalence(sameKey))
		if err != nil {
			t.Fatalf("%s: live run: %v", task, err)
		}
		var ledger []string
		for _, e := range res.Ledger {
			ledger = append(ledger, e.Source)
		}
		if !slices.Equal(got.Bought, ledger) || got.Spent != res.SpentUSD {
			t.Fatalf("%s: replay bought %v for %v, live sampled %v for %v", task, got.Bought, got.Spent, ledger, res.SpentUSD)
		}
		if (got.Accepted != nil) != (res.Decision == trust.DecisionAccept) {
			t.Fatalf("%s: replay accepted %v, live decided %v", task, got.Accepted != nil, res.Decision)
		}
		if got.Accepted != nil {
			accepted++
			if keyOf(*got.Accepted) != res.Candidate {
				t.Fatalf("%s: replay accepted %q, live %q", task, keyOf(*got.Accepted), res.Candidate)
			}
		} else {
			refused++
		}
	}
	if accepted == 0 || refused == 0 {
		t.Fatalf("the fixture should both accept and refuse: %d accepted, %d refused", accepted, refused)
	}
}

func TestSPRT_RunsInTheReplayAndLeavesDiskAlone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HYDRA_HOME", home)
	rep, err := Run(sprtRows(), Config{Policies: []string{"sprt"}, Alphas: []float64{0.05}, Seeds: []uint64{0, 1}})
	if err != nil {
		t.Fatal(err)
	}
	if s := rep.Summaries[0]; s.Policy != "sprt" || s.Refusal == 1 {
		t.Fatalf("sprt accepted nothing: %+v", s)
	}
	if _, err := os.Stat(trust.DefaultCoAgreementPath()); !os.IsNotExist(err) {
		t.Fatalf("the replay wrote %s", trust.DefaultCoAgreementPath())
	}
}

func TestSPRT_NoCalibratedHeadOrEvidenceRefuses(t *testing.T) {
	pol, _ := New("sprt", Options{Alpha: 0.05})
	// One task, one head, always right: it never said "incorrect", so it carries no evidence.
	cal := []Row{{Task: "t", Kind: "k", Head: "a", Correct: true, AnswerKey: "r"}}
	if err := pol.Calibrate(cal); err != nil {
		t.Fatal(err)
	}
	if o := pol.Decide([]Row{{Task: "u", Kind: "k", Head: "a", AnswerKey: "r"}}, []string{"a"}); o.Accepted != nil || o.Spent != 0 {
		t.Fatalf("an uninformative head was accepted or paid for: %+v", o)
	}
	if o := pol.Decide([]Row{{Task: "u", Kind: "k", Head: "z", AnswerKey: "r"}}, []string{"a"}); o.Reason != "no calibrated head answered" {
		t.Fatalf("got %+v", o)
	}
	if keyOf(Row{Task: "t", Head: "h"}) == keyOf(Row{Task: "t", Head: "g"}) {
		t.Fatal("two rows with no answer key must not agree")
	}
}
