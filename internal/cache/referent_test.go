// SPDX-License-Identifier: MIT

package cache

import "testing"

// A possessive is not a function word: the referent is the question. Before
// this, every pair below passed the content-token gate, and a real dispatch
// served 7 of 12 with an embedder and 12 of 12 without one, since with no
// vector there is no cosine veto to catch what the gate let through (#1035).
func TestSameReferents_RefusesASwap(t *testing.T) {
	for _, tc := range [][2]string{
		{"review my changes", "review your changes"},
		{"delete my branch", "delete your branch"},
		{"deploy their service", "deploy our service"},
		{"send it to them", "send it to us"},
		{"show me the logs", "show us the logs"},
		{"should I merge this", "should I merge that"},
		{"revert their commit", "revert my commit"},
		{"rebase your branch", "rebase our branch"},
		{"drop this table", "drop that table"},
		{"open my pull request", "open their pull request"},
		{"restart it for me", "restart it for you"},
		{"what did they change", "what did we change"},
	} {
		a, b := Normalize(tc[0]), Normalize(tc[1])
		// The content gate cannot tell these apart, which is why the referent
		// check has to: asserting only the final verdict would pass even if
		// the content tokens were what refused them.
		if !sameQuestion(content(a), content(b)) {
			t.Errorf("%q vs %q no longer share content tokens, so this pair "+
				"stopped testing what it was written for", tc[0], tc[1])
		}
		if sameReferents(a, b) {
			t.Errorf("%q and %q were treated as asking about the same thing", tc[0], tc[1])
		}
	}
}

// Containment, not equality. A referent one prompt omits or adds makes it more
// specific; only a swap is a different question.
func TestSameReferents_AllowsOneSideToNameMore(t *testing.T) {
	for _, tc := range [][2]string{
		{"how do I delete this branch", "how to delete this branch"},
		{"let the user answer them", "you let the user answer them"},
		{"please can you rotate the signing key for me", "rotate the signing key"},
		{"run the tests", "please run the tests"},
		{"merge develop into main", "merge develop into main"},
	} {
		a, b := Normalize(tc[0]), Normalize(tc[1])
		if !sameReferents(a, b) {
			t.Errorf("%q and %q were treated as asking about different things", tc[0], tc[1])
		}
		if !sameReferents(b, a) {
			t.Errorf("%q and %q disagreed when the order was swapped", tc[1], tc[0])
		}
	}
}

// A determiner says which thing, so one prompt having none is a different
// question rather than a vaguer one. This is the deliberate cost of that:
// "of this file" against "of the file" is a real restatement and is refused,
// because which file "this" names is exactly the context a cache lacks. The
// pair is here so the trade is recorded rather than discovered later (#1035).
func TestSameReferents_RefusesADeterminerOnlyOneSideUses(t *testing.T) {
	for _, tc := range [][2]string{
		{"what is the blast radius of this file", "what is the blast radius of the file"},
		{"rotate the signing key", "rotate their signing key"},
		{"delete the branch", "delete my branch"},
	} {
		a, b := Normalize(tc[0]), Normalize(tc[1])
		if sameReferents(a, b) || sameReferents(b, a) {
			t.Errorf("%q and %q were treated as asking about the same thing", tc[0], tc[1])
		}
	}
}

// A pronoun is the opposite: dropped and added freely by a restatement, which
// is what the false-hit harness's "please can you … for me" wrapper does.
func TestSameReferents_IgnoresAPronounOnlyOneSideUses(t *testing.T) {
	for _, tc := range [][2]string{
		{"rotate the signing key", "can you rotate the signing key"},
		{"rotate the signing key", "rotate the signing key for me"},
		{"how do I delete this branch", "how to delete this branch"},
	} {
		a, b := Normalize(tc[0]), Normalize(tc[1])
		if !sameReferents(a, b) || !sameReferents(b, a) {
			t.Errorf("%q and %q were treated as asking about different things", tc[0], tc[1])
		}
	}
}

// A swap inside one class must not be excused by another class agreeing, and a
// pronoun only one prompt uses must not refuse on its own.
func TestSameReferents_JudgesEachClassOnItsOwn(t *testing.T) {
	a := Normalize("should I merge this")
	b := Normalize("should I merge that")
	if sameReferents(a, b) {
		t.Error("the person class agreeing excused a demonstrative swap")
	}
	if !sameReferents(Normalize("merge this"), Normalize("I merge this")) {
		t.Error("a person named by only one prompt refused on its own")
	}
}

// Lookup has to consult the referents, not merely have a function that could.
// Without this the whole suite stayed green with the check deleted from the
// gate, because everything else tested sameReferents on its own (#1035).
func TestLookup_RefusesAPossessiveSwapWithNoEmbedderAtAll(t *testing.T) {
	s := open(t)
	put(t, s, "review my changes", "here is the review")

	out := s.Lookup("review your changes", nil, DefaultThreshold)
	if out.Found {
		t.Fatalf("served %q for a different question, the answer to someone else's", out.Hit.Response)
	}
	// Refused, not a miss: the store holds a candidate and a gate turned it
	// down, which is the distinction #1025 made countable without a vector.
	if !out.Refused {
		t.Error("counted as a miss, so the refusal is invisible in hyctl trace cache")
	}

	// The exact question still works, or the gate is refusing everything.
	if out := s.Lookup("review my changes", nil, DefaultThreshold); !out.Found {
		t.Error("the stored question itself stopped being served")
	}
}

// With a vector the veto could refuse it for the wrong reason, so the same
// claim is made where cosine says the two are close enough to serve.
func TestLookup_RefusesAPossessiveSwapTheVectorCallsIdentical(t *testing.T) {
	s := open(t)
	vec := unit(8, 0, 0)
	if err := s.PutVec(Entry{Prompt: "review my changes", Response: "here is the review", Head: "h1"}, vec); err != nil {
		t.Fatal(err)
	}
	if out := s.Lookup("review your changes", vec, DefaultThreshold); out.Found {
		t.Errorf("a cosine of 1 let a possessive swap through: %q", out.Hit.Response)
	}
}
