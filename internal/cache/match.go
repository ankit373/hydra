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

// content is the set of tokens that carry the question, from the same
// tokenizer the lexical index uses so the cache and recall cannot disagree
// about what a word is.
func content(normalized string) map[string]bool {
	out := map[string]bool{}
	for _, tok := range retrieve.Tokenize(normalized) {
		if !stopwords[tok] {
			out[tok] = true
		}
	}
	return out
}

// sameQuestion reports whether two prompts ask about exactly the same things.
//
// This is the gate that exists because of a measurement, not a hunch. Embedded
// with nomic-embed-text, `--max-cost` and `--max-heads` sit at 0.8557 cosine
// and `internal/awsconf` and `internal/executor` at 0.8598, both *above* the
// 0.85 a cosine-only cache would serve at (#911). The pairs differ by one
// identifier, which is exactly what a content-token set catches and what
// distance in embedding space does not.
//
// Set equality rather than an overlap ratio, because a ratio needs a threshold
// and there is no honest number to put there: on a six-word question a single
// changed noun still scores 0.71.
func sameQuestion(a, b map[string]bool) bool {
	if len(a) != len(b) || len(a) == 0 {
		return false
	}
	for tok := range a {
		if !b[tok] {
			return false
		}
	}
	return true
}
