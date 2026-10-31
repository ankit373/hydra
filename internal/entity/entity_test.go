// SPDX-License-Identifier: MIT

package entity

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func askConst(reply string) Ask {
	return func(context.Context, string, string) (string, error) { return reply, nil }
}

// A model that explains itself is still answering. One that buries the word in
// a sentence is not, and "no" is a prefix of a great many words.
func TestParse_ReadsAWordNotASubstring(t *testing.T) {
	for _, in := range []string{"YES", "yes", " Yes ", "Yes.", "**YES**", "yes, a person is named", "`yes`"} {
		if found, ok := parse(in); !ok || !found {
			t.Errorf("%q read as found=%v ok=%v, want a yes", in, found, ok)
		}
	}
	for _, in := range []string{"NO", "no", "No.", "no - technical prose", "**No**"} {
		if found, ok := parse(in); !ok || found {
			t.Errorf("%q read as found=%v ok=%v, want a no", in, found, ok)
		}
	}
	// The ones that are not an answer. "nobody" and "nothing" begin with "no",
	// and reading either as a clean verdict is the defect this guards.
	for _, in := range []string{"nobody is named here", "nothing found", "yesterday's run",
		"I think the text may contain a name", "", "maybe", "1"} {
		if _, ok := parse(in); ok {
			t.Errorf("%q was read as an answer", in)
		}
	}
}

// An unreadable answer is an error, never a no. A detector that reports clean
// because the head malfunctioned is worse than no detector at all.
func TestCheck_UnreadableIsNotANo(t *testing.T) {
	v, err := Check(context.Background(), "some text", askConst("I cannot help with that"))
	if !errors.Is(err, ErrUnreadable) {
		t.Fatalf("err = %v, want ErrUnreadable", err)
	}
	if v.Found {
		t.Error("an unreadable answer reported a detection")
	}
	if !strings.Contains(v.Answer, "cannot help") {
		t.Errorf("the head's actual answer was discarded: %q", v.Answer)
	}
}

func TestCheck_NoAskerIsNoHeadNotAClean(t *testing.T) {
	if _, err := Check(context.Background(), "text", nil); !errors.Is(err, ErrNoHead) {
		t.Fatalf("err = %v, want ErrNoHead", err)
	}
}

func TestCheck_PassesTheOneSystemPrompt(t *testing.T) {
	var got string
	_, err := Check(context.Background(), "x", func(_ context.Context, sys, _ string) (string, error) {
		got = sys
		return "no", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != System {
		t.Error("Check asked something other than System, so a measurement " +
			"and a live check could disagree while comparing answers")
	}
}

func TestProbes_AreLabelledBothWays(t *testing.T) {
	ps, err := Probes()
	if err != nil {
		t.Fatal(err)
	}
	var pos, neg int
	for _, p := range ps {
		if p.PII {
			pos++
		} else {
			neg++
		}
		if strings.TrimSpace(p.Text) == "" {
			t.Error("an empty probe measures nothing")
		}
	}
	// A one-sided set cannot separate a working head from one that always says
	// the same thing, which is exactly what Eligible exists to catch.
	if pos < 20 || neg < 20 {
		t.Fatalf("probe set is %d positive / %d negative, too one-sided to measure with", pos, neg)
	}
}

// The measurement that decides the feature. A head that always says no has a
// flawless false-positive rate, so a bar on that alone rates a broken detector
// as the best on the machine; a head that always says yes is the mirror.
func TestMeasure_RejectsBothDegenerateHeads(t *testing.T) {
	for name, reply := range map[string]string{"always no": "NO", "always yes": "YES"} {
		t.Run(name, func(t *testing.T) {
			r, err := Measure(context.Background(), askConst(reply))
			if err != nil {
				t.Fatal(err)
			}
			if r.Eligible() {
				t.Errorf("a head that answers %q every time was rated eligible: %+v", reply, r)
			}
		})
	}
}

// And the head that actually works is accepted, or the bar is unreachable.
func TestMeasure_AcceptsAHeadThatReadsTheLabels(t *testing.T) {
	ps, _ := Probes()
	truth := make(map[string]bool, len(ps))
	for _, p := range ps {
		truth[p.Text] = p.PII
	}
	oracle := func(_ context.Context, _, text string) (string, error) {
		if truth[text] {
			return "YES", nil
		}
		return "NO", nil
	}
	r, err := Measure(context.Background(), oracle)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Eligible() {
		t.Fatalf("a perfect head was refused: %+v recall=%.2f fp=%.2f", r, r.Recall(), r.FalsePositiveRate())
	}
	if r.Recall() != 1 || r.FalsePositiveRate() != 0 {
		t.Errorf("a perfect head measured recall=%.2f fp=%.2f", r.Recall(), r.FalsePositiveRate())
	}
}

// Unreadable answers are counted on their own and drag recall down, because a
// head that cannot answer has not found anything.
func TestMeasure_UnreadableCountsAgainstRecallNotAgainstNothing(t *testing.T) {
	r, err := Measure(context.Background(), askConst("hmm, hard to say"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Unreadable != r.Positives+r.Negatives {
		t.Errorf("unreadable=%d of %d probes", r.Unreadable, r.Positives+r.Negatives)
	}
	if r.Eligible() {
		t.Error("a head that never answers was rated eligible")
	}
}

func TestMeasure_AHeadThatCannotBeReachedIsNotEligible(t *testing.T) {
	r, err := Measure(context.Background(), func(context.Context, string, string) (string, error) {
		return "", errors.New("connection refused")
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Failed == 0 {
		t.Error("failures were not counted")
	}
	if r.Eligible() {
		t.Error("an unreachable head was rated eligible")
	}
}

// Wilson, not the normal approximation: at 0 of 220 the normal interval is a
// point at zero, which claims certainty from a finite sample, and at the other
// end it puts a bound above 1.
func TestWilson_StaysInsideZeroToOneAndWidensOnLittleData(t *testing.T) {
	lo, hi := wilson(0, 220)
	if lo != 0 {
		t.Errorf("0 of 220 has a lower bound of %v, want 0", lo)
	}
	if hi <= 0 || hi > 0.05 {
		t.Errorf("0 of 220 has an upper bound of %.4f, want a small positive number", hi)
	}
	if _, hi := wilson(0, 5); hi < 0.4 {
		t.Errorf("0 of 5 claims an upper bound of %.2f, too confident for five samples", hi)
	}
	lo, hi = wilson(250, 250)
	if hi < 0.999 || lo <= 0.95 {
		t.Errorf("250 of 250 gave [%.3f, %.3f]", lo, hi)
	}
	// n=0 is no evidence, so the interval is the whole range rather than a
	// division by zero or a confident zero.
	if lo, hi := wilson(0, 0); lo != 0 || hi != 1 {
		t.Errorf("no observations gave [%v, %v], want the whole range", lo, hi)
	}
}

// The #1041 defect in one case: a head whose point estimate clears the bar but
// whose interval does not has not been shown to clear it.
func TestEligible_JudgesTheIntervalNotThePoint(t *testing.T) {
	// 33 of 40 is 0.825, over the 0.80 bar, on a sample far too small to say so.
	small := Report{Positives: 40, Recalled: 33, Negatives: 60}
	if small.Recall() < MinRecall {
		t.Fatalf("fixture is wrong: point estimate %.3f is already under the bar", small.Recall())
	}
	if small.Eligible() {
		t.Errorf("a point estimate over the bar on 40 samples was rated eligible "+
			"(low %.3f), which is how the verdict became a coin flip", small.RecallLow())
	}

	// The same rate measured on enough samples does clear it.
	big := Report{Positives: 250, Recalled: 217, Negatives: 220}
	if !big.Eligible() {
		t.Errorf("a measured head was refused: recall %.3f low %.3f, fp high %.3f",
			big.Recall(), big.RecallLow(), big.FalsePositiveHigh())
	}
}

// And the false-positive half is judged from its upper bound for the same
// reason: a clean run on a handful of negatives has not shown it is clean.
func TestEligible_FalsePositivesJudgedFromTheUpperBound(t *testing.T) {
	thin := Report{Positives: 250, Recalled: 250, Negatives: 8}
	if thin.Eligible() {
		t.Errorf("eight negatives were enough to certify a false-positive rate (high %.3f)",
			thin.FalsePositiveHigh())
	}
}
