// SPDX-License-Identifier: MIT

package retrieve

import (
	"strings"
	"unicode"
)

// MaxTermLen caps one term. A base64 blob or a minified line is one enormous
// run that is never anyone's query, and indexing it whole costs bytes to store
// a term nothing can match.
const MaxTermLen = 64

// Tokenize splits text into the terms the index scores on.
//
// Every run of letters and digits yields the run itself **and** its camelCase
// and letter/digit parts, both lowercased. Both, because this corpus is code
// and they carry different signal: `ErrNoPropensity` kept whole is what makes
// an exact identifier match exact, while "err", "no", "propensity" are what let
// a description of it match at all. Keeping only the parts throws away the
// precision that is the reason for a lexical index beside a dense one.
func Tokenize(text string) []string {
	var out []string
	for _, run := range runs(text) {
		lower := strings.ToLower(run)
		if len(lower) > MaxTermLen {
			continue
		}
		out = append(out, lower)
		parts := splitCase(run)
		if len(parts) < 2 {
			continue
		}
		for _, p := range parts {
			if p = strings.ToLower(p); p != lower && len(p) <= MaxTermLen {
				out = append(out, p)
			}
		}
	}
	return out
}

// runs cuts text at anything that is not a letter or digit, preserving case so
// splitCase still has the boundary to work with.
func runs(text string) []string {
	var out []string
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			out = append(out, b.String())
			b.Reset()
		}
	}
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return out
}

// splitCase cuts a run at lower-to-upper, acronym-to-word and letter-to-digit
// boundaries, so HTTPServer2 becomes HTTP, Server, 2.
func splitCase(run string) []string {
	rs := []rune(run)
	var out []string
	start := 0
	for i := 1; i < len(rs); i++ {
		prev, cur := rs[i-1], rs[i]
		var boundary bool
		switch {
		case unicode.IsLower(prev) && unicode.IsUpper(cur):
			boundary = true
		case unicode.IsDigit(prev) != unicode.IsDigit(cur):
			boundary = true
		case unicode.IsUpper(prev) && unicode.IsUpper(cur) &&
			i+1 < len(rs) && unicode.IsLower(rs[i+1]):
			// The last capital of an acronym starts the next word: the S in
			// HTTPServer belongs to Server, not to HTTP.
			boundary = true
		}
		if boundary {
			out = append(out, string(rs[start:i]))
			start = i
		}
	}
	return append(out, string(rs[start:]))
}

// Bag counts terms, returning the frequencies and the total token count that
// BM25's length normalisation needs.
func Bag(text string) (map[string]int, int) {
	terms := Tokenize(text)
	bag := make(map[string]int, len(terms))
	for _, t := range terms {
		bag[t]++
	}
	return bag, len(terms)
}
