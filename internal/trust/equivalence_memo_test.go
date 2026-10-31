// SPDX-License-Identifier: MIT

package trust

import (
	"context"
	"testing"
)

// flaky answers a different way each time it is asked, which is what a
// judge-backed comparator is: an LLM deciding whether two answers say the same
// thing. It also counts, since asking the same pair repeatedly is spend.
type flaky struct {
	calls int
	next  bool
}

func (f *flaky) eq(_, _ string) bool {
	f.calls++
	f.next = !f.next
	return f.next
}

// The run asks the same pair several times: scoreHypotheses groups with it and
// then scores every hypothesis with it, and the ledger asks again for the entry
// it writes. With a comparator that is not a pure function, one run counted a
// head's vote toward the accepted answer and recorded that same head as having
// dissented from it, and hyctl trust outcome trains on the second (#997).
func TestRun_OneEquivalenceVerdictPerPair(t *testing.T) {
	c, _ := New("")
	calibrateSymmetric(c, "sim", "d", 0.9, 1000)

	f := &flaky{}
	res, err := Run(context.Background(), Task{Domain: "d"}, nSources("sim", 3, 1),
		&scriptExec{seq: []string{"one", "two", "three"}}, c,
		Target{Confidence: 0.95}, WithEquivalence(f.eq))
	if err != nil {
		t.Fatal(err)
	}

	// Three distinct answers make three unordered pairs. Anything above that is
	// the same question asked twice, which is both spend and a chance to get a
	// different answer.
	if f.calls > 3 {
		t.Errorf("comparator called %d times for 3 answers, want at most one verdict per pair", f.calls)
	}

	// Every vote a hypothesis counted must be recorded as agreement by the
	// ledger entry for that source, or the stopping rule and the training
	// signal describe different runs.
	assertLedgerMatchesHypotheses(t, res)
}

// A pure comparator must behave exactly as before, so memoizing cannot change
// what an ordinary run decides.
func TestRun_MemoizingAPureComparatorChangesNothing(t *testing.T) {
	c, _ := New("")
	calibrateSymmetric(c, "sim", "d", 0.9, 1000)

	seq := []string{"A", "A", "A", "A", "A"}
	res, err := Run(context.Background(), Task{Domain: "d"}, nSources("sim", 5, 1),
		&scriptExec{seq: seq}, c, Target{Confidence: 0.95})
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != DecisionAccept {
		t.Errorf("decision = %v, want accept on five identical answers", res.Decision)
	}
	assertLedgerMatchesHypotheses(t, res)
}

// Votes are attributed to hypotheses by the same comparator the ledger uses, so
// they cannot add up to more sources than the run actually sampled. Four votes
// across two hypotheses from three samples is what the flaky comparator
// produced.
func assertLedgerMatchesHypotheses(t *testing.T, res *Result) {
	t.Helper()
	total := 0
	for _, h := range res.Hypotheses {
		total += h.Votes
	}
	if total > res.Samples {
		t.Errorf("hypotheses hold %d votes from %d samples; a source voted twice", total, res.Samples)
	}

	final := res.Candidate
	for _, e := range res.Ledger {
		if e.Candidate != final {
			continue // weighed against an answer that was later superseded
		}
		var leader *Hypothesis
		for i := range res.Hypotheses {
			if res.Hypotheses[i].Answer == final {
				leader = &res.Hypotheses[i]
				break
			}
		}
		if leader == nil {
			t.Fatalf("the accepted answer is not among the hypotheses")
		}
		// A source whose LLR was positive contributed agreement; the ledger
		// must say so too.
		if e.LLR > 0 && !e.Agreed {
			t.Errorf("%s: LLR %+.3f raised the accepted answer but the ledger records a dissent, which is what trust outcome replays",
				e.Source, e.LLR)
		}
		if e.LLR < 0 && e.Agreed {
			t.Errorf("%s: LLR %+.3f lowered the accepted answer but the ledger records agreement", e.Source, e.LLR)
		}
	}
}

// Equivalence is symmetric and reflexive, so the memo must answer the same
// whichever way a pair is handed to it. No caller swaps the order today, which
// is exactly why this is asserted here rather than left to a run to reveal.
func TestMemoizeEquivalence_IsReflexiveAndSymmetric(t *testing.T) {
	f := &flaky{}
	eq := memoizeEquivalence(f.eq)

	if !eq("same", "same") {
		t.Error("a text is not equivalent to itself")
	}
	if f.calls != 0 {
		t.Errorf("comparing a text with itself cost %d call(s); reflexivity needs no judge", f.calls)
	}

	first := eq("a", "b")
	if got := eq("b", "a"); got != first {
		t.Errorf("swapped pair answered %v after %v; the same two answers are equivalent or they are not", got, first)
	}
	if f.calls != 1 {
		t.Errorf("comparator called %d times for one pair, want 1", f.calls)
	}
}
