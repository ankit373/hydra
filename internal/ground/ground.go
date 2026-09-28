// SPDX-License-Identifier: MIT

// Package ground checks an answer against the context the dispatch was given.
//
// Not a hallucination detector. It answers one narrow question with high
// precision: does the answer state a number, path, flag or symbol that appears
// nowhere in what the model was handed? That is extrinsic hallucination, where
// a model contradicts ground truth sitting in its own context, and it is the
// only part of the problem a check with no trained model can speak to.
//
// Everything else it declines to judge. With no fenced context there is no
// verdict at all, because "nothing to check against" and "checked and fine"
// are different facts and only one of them is evidence.
package ground

import (
	"sort"
	"strings"
	"unicode"

	"github.com/ankit373/hydra/internal/util"
)

// Kind is why a span had to be supported. Reported so a failure names the sort
// of claim that failed rather than only the text.
type Kind string

const (
	// KindNumber is a bare quantity: a count, a version, a percentage.
	KindNumber Kind = "number"
	// KindSymbol is an identifier: a function, a package path, a file, a flag.
	KindSymbol Kind = "symbol"
)

// Claim is one span of the answer that had to appear in the context.
type Claim struct {
	Text string `json:"text"`
	Kind Kind   `json:"kind"`
}

// Verdict is what the check found.
type Verdict struct {
	// Checked is false when the dispatch carried no fenced context. The other
	// fields mean nothing then, and a caller must not read Grounded as a pass:
	// Oracle returns ErrNoContext rather than a verdict for exactly that
	// reason, so the mistake is not available to make.
	Checked bool `json:"checked"`
	// Grounded is true when every checkable claim in the answer appears in
	// what the model was given.
	Grounded bool `json:"grounded"`
	// Unsupported names the spans that did not, which is the whole diagnostic.
	Unsupported []Claim `json:"unsupported,omitempty"`
	// Overlap is the share of the answer's content words present in the
	// context. Reported and deliberately not part of the verdict: a good
	// summary in fresh words has low overlap, so gating on it would fail
	// exactly the answers worth having. It is here to be looked at, not
	// thresholded.
	Overlap float64 `json:"overlap"`
	// Context is how many characters of fenced context were checked against.
	Context int `json:"context_bytes"`
	// Claims is how many checkable spans the answer contained. Zero means the
	// answer named and measured nothing, so there was nothing to verify and
	// Grounded says only that no objection was found. Oracle refuses to report
	// that as a pass for the same reason it refuses an unfenced prompt: an
	// answer nobody could check is not an answer that checked out.
	Claims int `json:"claims"`
}

// MaxUnsupported bounds the reported spans. A wholly invented answer produces
// hundreds, and a list that long is not a diagnostic.
const MaxUnsupported = 12

// Check verifies answer against the untrusted spans fenced into prompt.
//
// The supporting set is the whole prompt, instructions included, not only the
// fenced part. A model repeating a flag the user named is grounded in what it
// was given, and calling that unsupported would file a finding on the most
// ordinary thing an answer does. The fence decides whether there is anything
// to check at all; what counts as support is broader.
func Check(answer, prompt string) Verdict {
	spans := util.Unwrap(prompt)
	if len(spans) == 0 {
		return Verdict{}
	}
	var ctxBytes int
	for _, s := range spans {
		ctxBytes += len(s.Content)
	}

	supported := tokens(prompt)
	v := Verdict{Checked: true, Grounded: true, Context: ctxBytes}

	cited := citations(answer)
	seen := map[string]bool{}
	var words, present int
	for _, tok := range tokenize(answer) {
		low := strings.ToLower(tok)
		words++
		if supported[low] {
			present++
		}
		kind, checkable := claimKind(tok)
		if !checkable || cited[low] || seen[low] {
			continue
		}
		seen[low] = true
		v.Claims++
		if supported[low] {
			continue
		}
		v.Grounded = false
		if len(v.Unsupported) < MaxUnsupported {
			v.Unsupported = append(v.Unsupported, Claim{Text: tok, Kind: kind})
		}
	}
	if words > 0 {
		v.Overlap = float64(present) / float64(words)
	}
	// Stable order: the same answer must report the same list twice, and the
	// spans arrive in map-independent order only because tokenize preserves
	// the answer's own.
	sort.SliceStable(v.Unsupported, func(i, j int) bool {
		return v.Unsupported[i].Text < v.Unsupported[j].Text
	})
	return v
}

// citations are the numbers the answer cited rather than measured: #843 is a
// reference to something outside the context by construction, so requiring it
// to appear inside the context asks the impossible. Measured on this
// repository's own doc comments, these were 50 of 89 numeric false alarms.
func citations(text string) map[string]bool {
	out := map[string]bool{}
	for i := 0; i+1 < len(text); i++ {
		if text[i] != '#' {
			continue
		}
		j := i + 1
		for j < len(text) && text[j] >= '0' && text[j] <= '9' {
			j++
		}
		if j > i+1 {
			out[text[i+1:j]] = true
		}
	}
	return out
}

// claimKind reports whether a token is the sort of thing that has to come from
// somewhere, and which sort.
//
// An ordinary English word is not: a model is asked to explain, so it writes
// words the context never used, and requiring those to appear would fail every
// answer. What must appear is anything that names or measures something.
//
// Three of the four rules here were narrowed by a measurement rather than
// chosen. Against 253 of this repository's own doc comments the first cut
// flagged 73% of grounded answers, and the false alarms were all one of:
//
//   - hyphenated prose read as an identifier (best-effort, highest-stakes).
//     A hyphen joins words in English and joins nothing in Go, so it only
//     makes a symbol alongside a slash, a dot, a digit or a leading dash.
//   - an acronym read as camelCase (PII, SPRT). camelCase is a lowercase
//     letter followed by an uppercase one, which an acronym never has. This
//     costs the bare form of a name like UITier, which is the trade: a
//     hallucinated symbol usually arrives qualified, and prose is full of
//     acronyms.
//   - a lone short integer read as a measurement. "three of the four" is not
//     a claim about the material; 40960 is.
func claimKind(tok string) (Kind, bool) {
	var hasDigit, hasLetter, hasPath, hasHyphen, camel bool
	var prevLower bool
	for _, r := range tok {
		switch {
		case unicode.IsDigit(r):
			hasDigit = true
			prevLower = false
		case unicode.IsLetter(r):
			hasLetter = true
			if prevLower && unicode.IsUpper(r) {
				camel = true
			}
			prevLower = unicode.IsLower(r)
		case r == '.' || r == '/' || r == '_':
			hasPath = true
			prevLower = false
		case r == '-':
			hasHyphen = true
			prevLower = false
		}
	}
	switch {
	case hasDigit && !hasLetter:
		if len(tok) < minDigits {
			return "", false
		}
		return KindNumber, true
	case strings.HasPrefix(tok, "-") && hasLetter:
		return KindSymbol, true
	case hasPath && hasLetter, camel, hasDigit && hasLetter && (hasPath || hasHyphen):
		return KindSymbol, true
	}
	return "", false
}

// minDigits is where a bare integer starts being a measurement rather than a
// count in a sentence. Two digits: "the 3 phases" is prose, "40960" is a claim.
const minDigits = 2

// tokenize splits text the way a reader would see it: runs of characters that
// can belong to an identifier, path, flag or number, kept whole.
//
// Deliberately not retrieve.Tokenize, which splits camelCase and drops the
// joins. That is right for a search index, where matching a part is a hit, and
// wrong here, where `internal/awsconf` matching `internal/executor` on the
// shared first half is the exact failure this is meant to catch.
func tokenize(text string) []string {
	var out []string
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			out = append(out, strings.Trim(b.String(), ".-_/"))
			b.Reset()
		}
	}
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) ||
			r == '.' || r == '/' || r == '_' || r == '-' {
			b.WriteRune(r)
			continue
		}
		flush()
	}
	flush()

	kept := out[:0]
	for _, t := range out {
		if t != "" {
			kept = append(kept, t)
		}
	}
	return kept
}

// tokens is the supporting set: every token in the text, lowercased, plus the
// parts of each dotted or slashed one.
//
// The parts matter in one direction only. An answer saying `UITier` is
// supported by a context that says `rank.UITier`, because the context does
// contain that symbol. The reverse is not true and is not offered: an answer
// saying `rank.UITier` is not supported by a context that only mentions
// `UITier` somewhere else.
func tokens(text string) map[string]bool {
	out := map[string]bool{}
	for _, t := range tokenize(text) {
		low := strings.ToLower(t)
		out[low] = true
		for _, part := range strings.FieldsFunc(low, func(r rune) bool {
			return r == '.' || r == '/' || r == '_'
		}) {
			if part != "" {
				out[part] = true
			}
		}
	}
	return out
}
