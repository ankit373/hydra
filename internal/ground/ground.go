// SPDX-License-Identifier: MIT

// Package ground checks an answer against the context the dispatch was given.
//
// Not a hallucination detector. It answers one narrow question with high
// precision: does the answer state a number, date, name, path, flag or symbol
// that appears nowhere in what the model was handed? That is extrinsic
// hallucination, where a model contradicts ground truth sitting in its own
// context, and it is the only part of the problem a check with no trained model
// can speak to.
//
// Everything else it declines to judge. With no fenced context there is no
// verdict at all, because "nothing to check against" and "checked and fine"
// are different facts and only one of them is evidence.
package ground

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
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
	// KindDate is a written calendar date, compared in a canonical form.
	KindDate Kind = "date"
	// KindName is a proper name of two or more words: a person, a company.
	KindName Kind = "name"
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
	promptDates, _ := dates(prompt)
	for _, d := range promptDates {
		for _, keys := range d.days {
			for _, k := range keys {
				supported[k] = true
				// "3 March 2026" in the source supports "March 3"; not the reverse.
				if len(k) == len("date:yyyy-mm-dd") {
					supported["date:"+k[len("date:yyyy-"):]] = true
				}
			}
		}
	}
	v := Verdict{Checked: true, Grounded: true, Context: ctxBytes}
	seen := map[string]bool{}
	claim := func(text, key string, kind Kind, ok bool) {
		if seen[key] {
			return
		}
		seen[key] = true
		v.Claims++
		if ok {
			return
		}
		v.Grounded = false
		if len(v.Unsupported) < MaxUnsupported {
			v.Unsupported = append(v.Unsupported, Claim{Text: text, Kind: kind})
		}
	}

	answerDates, rest := dates(answer)
	for _, d := range answerDates {
		claim(d.text, fmt.Sprint(d.days), KindDate, d.supportedBy(supported))
	}

	pw := lowerWords(prompt)
	source := make(map[string]bool, len(pw))
	for _, w := range pw {
		source[w] = true
	}
	promptWords := " " + strings.Join(pw, " ") + " "
	for _, n := range names(rest, lowercaseUse(prompt, answer), source) {
		words := lowerWords(n)
		claim(n, "name:"+strings.Join(words, " "), KindName, nameSupported(words, promptWords))
	}

	cited := citations(rest)
	var words, present int
	for _, tok := range tokenize(rest) {
		low := strings.ToLower(tok)
		words++
		if supported[low] {
			present++
		}
		if kind, checkable := claimKind(tok); checkable && !cited[low] {
			claim(tok, low, kind, supported[low])
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

// dateRE matches the written dates a summary restates: ISO, d/m/yyyy, "3 March
// 2026" and "March 3, 2026", the last two with an optional year and day range
// ("July 29–30"). Months must be capitalised, so the verb "may" is never one.
var dateRE = regexp.MustCompile(`\b(?:(\d{4})-(\d{2})-(\d{2})|(\d{1,2})/(\d{1,2})/(\d{4})|` +
	dayPat + `\s+(?:of\s+)?` + monthPat + `\.?` + yearPat + `|` +
	monthPat + `\.?\s+` + dayPat + yearPat + `)\b`)

const (
	dayPat   = `(\d{1,2})(?:st|nd|rd|th)?(?:\s*[–-]\s*(\d{1,2})(?:st|nd|rd|th)?)?`
	monthPat = `(Jan(?:uary)?|Feb(?:ruary)?|Mar(?:ch)?|Apr(?:il)?|May|June?|July?|Aug(?:ust)?|Sep(?:t(?:ember)?)?|Oct(?:ober)?|Nov(?:ember)?|Dec(?:ember)?)`
	yearPat  = `(?:,?\s+(\d{4}))?`
)

var months = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}

// dateClaim is one written date or day range. Each day lists the canonical
// forms that would support it: one, or two for an ambiguous 03/04/2026.
type dateClaim struct {
	text string
	days [][]string
}

// dates finds the written dates in text and returns text with them blanked, so
// a day is not checked a second time as a number. Run before tokenize, which
// splits a date into words. Relative dates ("last Tuesday") match nothing.
func dates(text string) ([]dateClaim, string) {
	var out []dateClaim
	rest := []byte(text)
	for _, m := range dateRE.FindAllStringSubmatchIndex(text, -1) {
		g := func(i int) string {
			if m[2*i] < 0 {
				return ""
			}
			return text[m[2*i]:m[2*i+1]]
		}
		var year string
		var days [][][2]string // per day, the (month, day) readings
		switch {
		case g(1) != "":
			year, days = g(1), [][][2]string{{{g(2), g(3)}}}
		case g(4) != "":
			year, days = g(6), [][][2]string{{{g(5), g(4)}, {g(4), g(5)}}} // d/m, then m/d
		case g(9) != "":
			year, days = g(10), monthDays(g(9), g(7), g(8))
		default:
			year, days = g(14), monthDays(g(11), g(12), g(13))
		}
		d := dateClaim{text: text[m[0]:m[1]]}
		for _, readings := range days {
			var keys []string
			for _, r := range readings {
				if k, ok := dateKey(year, r[0], r[1]); ok {
					keys = append(keys, k)
				}
			}
			if len(keys) == 0 {
				d.days = nil
				break
			}
			d.days = append(d.days, keys)
		}
		if d.days == nil {
			continue // 31/31/2026 is not a date, so it stays with the number rule
		}
		out = append(out, d)
		for i := m[0]; i < m[1]; i++ {
			rest[i] = ' '
		}
	}
	return out, string(rest)
}

// supportedBy reports whether every day of the claim has a reading in supported.
func (d dateClaim) supportedBy(supported map[string]bool) bool {
	for _, keys := range d.days {
		found := false
		for _, k := range keys {
			found = found || supported[k]
		}
		if !found {
			return false
		}
	}
	return true
}

// monthDays is the one or two days a written date names, as (month, day).
func monthDays(month, day, toDay string) [][][2]string {
	days := [][][2]string{{{month, day}}}
	if toDay != "" {
		days = append(days, [][2]string{{month, toDay}})
	}
	return days
}

// dateKey is the canonical form, date:yyyy-mm-dd or date:mm-dd, or false when
// the parts are not a calendar day.
func dateKey(year, month, day string) (string, bool) {
	mo, err := strconv.Atoi(month)
	if err != nil {
		mo = months[strings.ToLower(month[:3])]
	}
	dd, _ := strconv.Atoi(day)
	if mo < 1 || mo > 12 || dd < 1 || dd > 31 {
		return "", false
	}
	if year == "" {
		return fmt.Sprintf("date:%02d-%02d", mo, dd), true
	}
	return fmt.Sprintf("date:%s-%02d-%02d", year, mo, dd), true
}

// names returns the proper names in text: runs of two or more capitalised
// words, one of them never used in lowercase, so Task Manager is ordinary words
// and Priya Raman is a name. A sentence's first word joins only when it is not
// ordinary and the source has it, so "Then Raman" is not a name. Scripts with
// no capitals (CJK, Arabic, Devanagari) give no signal and never form one.
func names(text string, ordinary, source map[string]bool) []string {
	var out, run, parts []string
	var full int  // words in run that are more than an initial
	var rare bool // a word in run that is never used in lowercase
	flush := func() {
		if full >= 2 && rare {
			out = append(out, strings.Join(run, " "))
		}
		run, parts, full, rare = nil, nil, 0, false
	}
	sentenceStart := true
	for _, field := range strings.Fields(text) {
		if strings.IndexAny(field, opening) == 0 {
			flush()
		}
		core, end := nameWord(field)
		low := strings.ToLower(core)
		initial := len(core) == 1 && core[0] >= 'A' && core[0] <= 'Z' && end == "."
		switch {
		case initial && !sentenceStart:
			run = append(run, core+".")
		case isNameWord(core) && !(sentenceStart && (ordinary[low] || !source[low])):
			run, parts = append(append(run, parts...), core), nil
			full, rare = full+1, rare || !ordinary[low]
		case nameParts[core] && full > 0:
			parts = append(parts, core)
		default:
			flush()
		}
		if initial {
			continue // Jerome H. Powell: the dot ends neither the name nor the sentence
		}
		sentenceStart = strings.ContainsAny(end, ".!?")
		if end != "" {
			flush()
		}
	}
	flush()
	return out
}

// nameParts are the lowercase words that sit inside a name, as in Mike van der Hoorn.
var nameParts = map[string]bool{"al": true, "bin": true, "da": true, "de": true, "del": true,
	"der": true, "di": true, "du": true, "la": true, "le": true, "van": true, "von": true}

const opening = "\"“‘(["

// nameWord splits a field into the word and what ends it. A possessive ends a
// name, so "Spencer's," is ("Spencer", "'s,").
func nameWord(field string) (core, end string) {
	core = strings.TrimLeft(field, opening)
	word := strings.TrimRight(core, ".,;:!?)]\"”’")
	for _, s := range []string{"'s", "’s", "'"} {
		if w, ok := strings.CutSuffix(word, s); ok {
			word = w
			break
		}
	}
	return word, core[len(word):]
}

// nameSupported wants the last two words of a name together in the prompt and
// the rest anywhere in it, so a reworded title still matches: "Home Affairs
// Minister Tony Burke" for "Tony Burke, Minister for Home Affairs".
func nameSupported(words []string, promptWords string) bool {
	if !strings.Contains(promptWords, " "+strings.Join(words[len(words)-2:], " ")+" ") {
		return false
	}
	for _, w := range words[:len(words)-2] {
		if !strings.Contains(promptWords, " "+w+" ") {
			return false
		}
	}
	return true
}

// isNameWord is a Latin word with a leading capital and a lowercase letter
// after it, so Spencer and O'Donnell are, and PII and the are not.
func isNameWord(w string) bool {
	var lower bool
	for i, r := range w {
		switch {
		case unicode.IsLetter(r) && !unicode.Is(unicode.Latin, r):
			return false
		case i == 0 && !unicode.IsUpper(r):
			return false
		case unicode.IsLower(r):
			lower = true
		case !unicode.IsLetter(r) && r != '\'' && r != '’' && r != '-':
			return false
		}
	}
	return lower
}

// lowerWords is text as lowercase words without their punctuation, for
// matching a name case-insensitively and across line breaks.
func lowerWords(text string) []string {
	fields := strings.Fields(text)
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if core, _ := nameWord(f); core != "" {
			out = append(out, strings.ToLower(core))
		}
	}
	return out
}

// lowercaseUse is every word the texts write starting in lowercase, lowercased.
func lowercaseUse(texts ...string) map[string]bool {
	out := map[string]bool{}
	for _, text := range texts {
		for _, f := range strings.Fields(text) {
			if core, _ := nameWord(f); core != "" && unicode.IsLower([]rune(core)[0]) {
				out[strings.ToLower(core)] = true
			}
		}
	}
	return out
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
