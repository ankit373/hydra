// SPDX-License-Identifier: MIT

package cache

import "testing"

// One derivation, used for hashing, tokenizing and embedding alike. Two that
// disagree is worse than none: every lookup misses and the cache reports
// itself working (#888).
func TestNormalize_IsOneDerivation(t *testing.T) {
	same := []string{
		"rotate the signing key",
		"  rotate the signing key  ",
		"rotate  the\tsigning\nkey",
		"\n rotate the signing key\n",
	}
	want := Key(Normalize(same[0]))
	for _, s := range same[1:] {
		if got := Key(Normalize(s)); got != want {
			t.Errorf("%q keyed as %s, want %s: whitespace changed the question", s, got[:8], want[:8])
		}
	}
	if Key(Normalize("rotate the signing keys")) == want {
		t.Error("a different question got the same key")
	}
}

// Redaction runs inside Normalize, so a secret cannot reach the store even
// through a caller that forgot to check. It also has to run on both sides, or
// the write and the read disagree about the key.
func TestNormalize_RedactsBeforeHashing(t *testing.T) {
	withKey := "deploy with AKIAIOSFODNN7EXAMPLE now"
	norm := Normalize(withKey)
	if norm == withKey {
		t.Fatalf("Normalize left the credential in place: %q", norm)
	}
	if Key(Normalize(withKey)) != Key(norm) {
		t.Error("normalizing twice gave two keys, so a write and a read cannot agree")
	}
}

// The gate that exists because of a measurement. Both pairs sit above the 0.85
// a cosine-only cache would serve at, and differ by exactly one identifier.
func TestSameQuestion_RefusesTheMeasuredNearMisses(t *testing.T) {
	pairs := [][2]string{
		{"what does --max-cost do", "what does --max-heads do"},
		{"explain internal/awsconf", "explain internal/executor"},
		{"how do I delete this branch", "how do I delete this repo"},
	}
	for _, p := range pairs {
		if sameQuestion(content(Normalize(p[0])), content(Normalize(p[1]))) {
			t.Errorf("served %q from %q: they ask about different things", p[1], p[0])
		}
	}
}

// And the other half: a question asked again in words that change nothing
// about what it asks is the same question.
func TestSameQuestion_AcceptsFunctionWordDifferences(t *testing.T) {
	pairs := [][2]string{
		{"how do I rotate the signing key", "how to rotate the signing key"},
		{"rotate the signing key", "please can you rotate the signing key for me"},
		{"what is the blast radius of token.go", "the blast radius of token.go"},
	}
	for _, p := range pairs {
		if !sameQuestion(content(Normalize(p[0])), content(Normalize(p[1]))) {
			t.Errorf("refused %q against %q: only function words differ", p[1], p[0])
		}
	}
}

// An empty content set matches nothing, or a prompt of pure function words
// would be the same question as every other such prompt.
func TestSameQuestion_EmptyMatchesNothing(t *testing.T) {
	if sameQuestion(content("how do i"), content("what is it")) {
		t.Error("two prompts with no content words were treated as the same question")
	}
	if sameQuestion(map[string]bool{}, map[string]bool{}) {
		t.Error("two empty content sets matched")
	}
}
