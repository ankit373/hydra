// SPDX-License-Identifier: MIT

package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/ankit373/hydra/internal/policy"
	"github.com/ankit373/hydra/internal/retrieve"
)

// Normalize is the single derivation of what a prompt is stored, hashed,
// tokenized and embedded as. One function because a key derived one way on
// write and another on read is worse than no key at all: the two halves agree
// about nothing and every lookup misses (#888).
//
// Redaction runs first and is belt and braces: a prompt carrying a secret is
// refused entry to the cache outright, so nothing here should ever have one.
func Normalize(prompt string) string {
	redacted, _ := policy.Redact(prompt)
	return strings.Join(strings.Fields(redacted), " ")
}

// Key is a normalized prompt's content address. An exact key match is the only
// hit this package can make with certainty, and is the reason the cache is
// worth having at all.
func Key(normalized string) string {
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}

// stopwords are the English function words a prompt can differ in without
// changing what it asks. The content-token gate below ignores them, so "how do
// I delete this branch" and "how to delete this branch" are the same question
// while "…this repo" is not.
//
// A list of function words is a linguistic artifact, not a tuned threshold:
// nothing here decides how similar is similar enough, only which words carry
// the question.
//
// Referents are in here too, and are compared separately: a possessive fails
// the "without changing what it asks" test outright, since the referent is the
// question (#1035).
var stopwords = map[string]bool{
	"a": true, "about": true, "all": true, "am": true, "an": true, "and": true,
	"any": true, "are": true, "as": true, "at": true, "be": true, "been": true,
	"but": true, "by": true, "can": true, "could": true, "did": true, "do": true,
	"does": true, "for": true, "from": true, "get": true, "had": true, "has": true,
	"have": true, "how": true, "i": true, "if": true, "in": true, "into": true,
	"is": true, "it": true, "its": true, "just": true, "me": true, "my": true,
	"of": true, "on": true, "or": true, "our": true, "please": true, "should": true,
	"so": true, "some": true, "than": true, "that": true, "the": true, "their": true,
	"them": true, "then": true, "there": true, "these": true, "they": true,
	"this": true, "to": true, "up": true, "us": true, "was": true, "we": true,
	"were": true, "what": true, "when": true, "where": true, "which": true,
	"who": true, "why": true, "will": true, "with": true, "would": true,
	"you": true, "your": true,
}

// referents are the words that say *whose* and *which one*. They are not
// content, since "review changes" and "review my changes" ask the same thing,
// and they are not noise either: "review my changes" and "review your changes"
// do not. Each maps to the identity it names and the class it belongs to, so a
// comparison reads "the same person" rather than "the same spelling".
//
// Measured before the fix: all 12 possessive and demonstrative swaps tried
// passed the content gate, and 7 of 12 were then served by a real dispatch,
// 12 of 12 on a machine with no embedder to veto them (#1035).
var referents = map[string]referent{
	// Determiners say which thing, so one prompt having none is a different
	// question, not a vaguer one: "rotate the signing key" is not "rotate
	// their signing key".
	"my": {"poss", "1s"}, "our": {"poss", "1p"}, "your": {"poss", "2"},
	"their": {"poss", "3p"}, "its": {"poss", "3n"},
	"his": {"poss", "3ms"}, "her": {"poss", "3fs"},
	"this": {"demo", "near"}, "these": {"demo", "near"},
	"that": {"demo", "far"}, "those": {"demo", "far"},

	// Pronouns say who is asking or being addressed, which a restatement drops
	// freely: "how to delete this branch" is "how do I delete this branch",
	// and the false-hit harness wraps every prompt in "please can you … for
	// me". Only a swap between them is a different question.
	"i": {"pron", "1s"}, "me": {"pron", "1s"}, "mine": {"pron", "1s"},
	"we": {"pron", "1p"}, "us": {"pron", "1p"}, "ours": {"pron", "1p"},
	"you": {"pron", "2"}, "yours": {"pron", "2"},
	"they": {"pron", "3p"}, "them": {"pron", "3p"}, "theirs": {"pron", "3p"},
	"he": {"pron", "3ms"}, "him": {"pron", "3ms"},
	"she": {"pron", "3fs"}, "hers": {"pron", "3fs"},
	"it": {"pron", "3n"},
}

type referent struct{ class, id string }

// sameReferents reports whether two prompts refer to the same things.
//
// Per class, because "my" is an alternative to "your" and not to "this". A
// determiner class must match exactly: dropping one changes which thing is
// meant. A pronoun class only has to contain the other, since a restatement
// adds and drops those freely, and a swap is containment in neither direction.
//
// The strict half costs "the blast radius of this file" against "…of the
// file", which is a real restatement. That is the conservative direction on
// purpose: which file "this" names is exactly the context a cache does not
// have (#1035).
func sameReferents(a, b string) bool {
	ra, rb := referentsIn(a), referentsIn(b)
	for class, want := range ra {
		got, ok := rb[class]
		if class == "pron" {
			if !ok {
				continue
			}
			if !contains(want, got) && !contains(got, want) {
				return false
			}
			continue
		}
		if !ok || !contains(want, got) || !contains(got, want) {
			return false
		}
	}
	// A determiner class only b uses is the same mismatch seen from the other
	// side, and the loop above cannot see it.
	for class, got := range rb {
		if class == "pron" {
			continue
		}
		if _, ok := ra[class]; !ok && len(got) > 0 {
			return false
		}
	}
	return true
}

// contains reports whether every identity in sub also appears in super.
func contains(super, sub map[string]bool) bool {
	for k := range sub {
		if !super[k] {
			return false
		}
	}
	return true
}

// referentsIn is the identities each class names, as a set: "send it to them"
// names 3n and 3p, and repeating one says nothing more than using it once.
func referentsIn(normalized string) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, tok := range retrieve.Tokenize(normalized) {
		r, ok := referents[tok]
		if !ok {
			continue
		}
		if out[r.class] == nil {
			out[r.class] = map[string]bool{}
		}
		out[r.class][r.id] = true
	}
	return out
}

// content is the tokens that carry the question, in the order they were
// written, from the same tokenizer the lexical index uses so the cache and
// recall cannot disagree about what a word is.
//
// A sequence rather than a set. Order is what carries the direction of an
// operation, and discarding it made "merge develop into main" and "merge main
// into develop" the same question (#1010). Repeats are kept for the same
// reason: "test the test" is not "test".
func content(normalized string) []string {
	var out []string
	for _, tok := range retrieve.Tokenize(normalized) {
		if !stopwords[tok] {
			out = append(out, tok)
		}
	}
	return out
}

// sameQuestion reports whether two prompts ask exactly the same thing.
//
// This is the gate that exists because of a measurement, not a hunch. Embedded
// with nomic-embed-text, `--max-cost` and `--max-heads` sit at 0.8557 cosine
// and `internal/awsconf` and `internal/executor` at 0.8598, both *above* the
// 0.85 a cosine-only cache would serve at (#911). The pairs differ by one
// identifier, which is exactly what content tokens catch and what distance in
// embedding space does not.
//
// Equality rather than an overlap ratio, because a ratio needs a threshold and
// there is no honest number to put there: on a six-word question a single
// changed noun still scores 0.71.
//
// Compared in order. As a set this returned true for a question and its
// reverse, and the dense half does not save it: six order-swapped pairs measure
// 0.9736 to 0.9913 cosine, above the 0.95 threshold, so raising the threshold
// makes it worse rather than better (#1010). Every perturbation the false-hit
// harness counts as a true hit leaves the order alone, since case, punctuation
// and whitespace are normalised before tokenizing and filler words are
// stopwords.
func sameQuestion(a, b []string) bool {
	if len(a) != len(b) || len(a) == 0 {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
