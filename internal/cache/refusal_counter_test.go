// SPDX-License-Identifier: MIT

package cache

import "testing"

// The reversal gate is the only gate a machine with no embedder has, so its
// refusals are the only evidence that gate earns its place. Before #1025 the
// counter could not move at all without a vector: the refusal branch asked
// nearestLocked, which answers nothing for an empty one, so the reversal #1010
// exists to catch was reported as a plain miss.
func TestLookup_WithoutVectorsCountsAReversalAsRefused(t *testing.T) {
	s := open(t)
	put(t, s, "rotate signing key auth service", "x")

	out := s.Lookup("rotate auth service signing key", nil, DefaultThreshold)

	if out.Found {
		t.Fatal("a reversed question must not be served")
	}
	if !out.Refused {
		t.Error("a reversal is the gate refusing, not the store missing; got a plain miss")
	}
}

// Honest in the other direction too: a question nothing resembles is a miss,
// or every unrelated prompt inflates the refusal count and it stops meaning
// anything.
func TestLookup_WithoutVectorsAnUnrelatedPromptIsAMiss(t *testing.T) {
	s := open(t)
	put(t, s, "rotate signing key auth service", "x")

	out := s.Lookup("what is the retention window for cost rows", nil, DefaultThreshold)

	if out.Found || out.Refused {
		t.Errorf("want a plain miss, got found=%v refused=%v", out.Found, out.Refused)
	}
}

// Repeats are part of the question, the same reason sameQuestion keeps them.
// Collapsing them would report a refusal against a question never asked.
func TestSameWordsLocked_RepeatsAreNotCollapsed(t *testing.T) {
	s := open(t)
	put(t, s, "test the test runner", "x")

	if s.sameWordsLocked(content(Normalize("test runner runner"))) {
		t.Error("[test test runner] and [test runner runner] are different questions")
	}
	if !s.sameWordsLocked(content(Normalize("runner test the test"))) {
		t.Error("the same words reordered must read as the gate refusing")
	}
}

// Two prompts of pure function words have the same (empty) content sequence,
// and sameQuestion already refuses that rather than calling them one question.
// The lexical refusal has to agree, or the two gates disagree about what an
// empty sequence means and the same prompt is a miss or a refusal depending on
// which one looked at it.
func TestLookup_FunctionWordsAloneAreAMissNotARefusal(t *testing.T) {
	s := open(t)
	put(t, s, "what is it", "x")

	out := s.Lookup("why is it", nil, DefaultThreshold)

	if out.Found {
		t.Fatal("two empty content sequences are not the same question")
	}
	if out.Refused {
		t.Error("an empty content sequence must match nothing, so this is a miss")
	}
}

// Neither question containing the other is the same question reordered, and
// the two directions fail differently: a stored entry with a word the query
// lacks overruns its count, and one missing a word the query has leaves want
// unsatisfied. Both are needed, so both are asked.
func TestLookup_ContainmentEitherWayIsAMiss(t *testing.T) {
	for _, tc := range []struct{ name, stored, asked string }{
		{"query is narrower", "rotate signing key auth service", "rotate signing key"},
		{"query is broader", "rotate signing key", "rotate signing key auth service"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := open(t)
			put(t, s, tc.stored, "x")

			out := s.Lookup(tc.asked, nil, DefaultThreshold)

			if out.Found || out.Refused {
				t.Errorf("a different question is a miss: found=%v refused=%v", out.Found, out.Refused)
			}
		})
	}
}

// A restatement is still served, not refused: the new test runs only where the
// ordered scan already found nothing, so it must not shadow a real hit.
func TestLookup_TheLexicalRefusalDoesNotShadowAHit(t *testing.T) {
	s := open(t)
	put(t, s, "rotate signing key auth service", "x")

	out := s.Lookup("Please rotate the signing key for the auth service.", nil, DefaultThreshold)

	if !out.Found || out.Refused {
		t.Errorf("a restatement must still be served: found=%v refused=%v", out.Found, out.Refused)
	}
}
