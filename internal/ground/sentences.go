// SPDX-License-Identifier: MIT

package ground

import (
	"regexp"
	"strings"
	"unicode"
)

// Sentences splits on . ! ? before a space or the end, and on line breaks. A
// stop inside quotes or a number does not split; a sentence with no content word is dropped.
func Sentences(text string) []string {
	rs := []rune(text)
	var out []string
	var cur strings.Builder
	quoted := false
	flush := func() {
		if s := strings.TrimSpace(cur.String()); hasContentWord(s) {
			out = append(out, s)
		}
		cur.Reset()
	}
	for i, r := range rs {
		if r == '\n' {
			flush()
			quoted = false
			continue
		}
		cur.WriteRune(r)
		switch r {
		case '"':
			quoted = !quoted
		case '“':
			quoted = true
		case '”':
			quoted = false
		}
		atBreak := i+1 == len(rs) || unicode.IsSpace(rs[i+1])
		stop := strings.ContainsRune(".!?", r) ||
			(strings.ContainsRune("\"”", r) && i > 0 && strings.ContainsRune(".!?", rs[i-1]))
		if stop && !quoted && atBreak && !listMarker.MatchString(strings.TrimSpace(cur.String())) {
			flush()
		}
	}
	flush()
	return out
}

// listMarker is a numbered list item's "1." or "a)", which starts a sentence rather than ending one.
var listMarker = regexp.MustCompile(`^(\d+|[a-z])[.)]$`)

// replyWords say nothing a source could support.
var replyWords = map[string]bool{"yes": true, "yeah": true, "okay": true, "sure": true, "thanks": true, "hello": true}

// hasContentWord is a digit, or a word of three or more letters other than a reply word.
func hasContentWord(s string) bool {
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if strings.ContainsFunc(w, unicode.IsDigit) || (len([]rune(w)) >= 3 && !replyWords[w]) {
			return true
		}
	}
	return false
}
