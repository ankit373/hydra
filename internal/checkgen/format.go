// SPDX-License-Identifier: MIT

package checkgen

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"unicode"
)

// schemaFormats are the JSON Schema formats read as a pattern; any other format is not checked.
var schemaFormats = map[string]string{
	"date":  `\d{4}-\d{2}-\d{2}`,
	"uuid":  `[0-9a-fA-F]{8}(?:-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}`,
	"email": `[^@\s]+@[^@\s]+\.[^@\s]+`,
}

// lookalikes maps a character to the ones a scan or a model reads it as.
var lookalikes = map[rune]string{
	'0': "O", 'O': "0", '1': "Il", 'I': "1", 'l': "1",
	'5': "S", 'S': "5", '8': "B", 'B': "8", '2': "Z", 'Z': "2",
}

// columnMin is the fewest same-length values a column needs before its other values say what a position takes.
const columnMin = 4

// anchored is p as a whole-value match, the way JSON Schema's pattern is meant for codes.
func anchored(p string) (*regexp.Regexp, error) { return regexp.Compile(`^(?:` + p + `)$`) }

// badPattern says why s does not match the field's pattern, and names the one-character
// swap that would make it match, if there is one. Empty means it matches.
func badPattern(f Field, s string) (detail string, unusable bool) {
	re, err := anchored(f.Pattern)
	if err != nil {
		return err.Error(), true
	}
	if re.MatchString(s) {
		return "", false
	}
	detail = fmt.Sprintf("is %q, which does not match %s", s, f.Pattern)
	if fix, ok := swapped(s, re.MatchString); ok {
		detail += fmt.Sprintf("; %q would", fix)
	}
	return detail, false
}

// swapped is s with one character replaced by a lookalike so that ok holds, scanning left to right.
func swapped(s string, ok func(string) bool) (string, bool) {
	rs := []rune(s)
	for i, r := range rs {
		for _, alt := range lookalikes[r] {
			rs[i] = alt
			if try := string(rs); ok(try) {
				return try, true
			}
		}
		rs[i] = r
	}
	return "", false
}

// columnMisfit reads a list of strings as one column of codes. A value with a lookalike
// where every other value of its length has the other class (a letter O among digits) is
// the misfit. Nothing is inferred from fewer than columnMin values of one length.
func columnMisfit(items []any) (string, bool) {
	byLen := map[int][][]rune{}
	for _, it := range items {
		if s, ok := it.(string); ok {
			rs := []rune(s)
			byLen[len(rs)] = append(byLen[len(rs)], rs)
		}
	}
	for _, n := range slices.Sorted(maps.Keys(byLen)) {
		group := byLen[n]
		if len(group) < columnMin {
			continue
		}
		for pos := range n {
			var digits, letters int
			for _, rs := range group {
				digits, letters = digits+b2i(unicode.IsDigit(rs[pos])), letters+b2i(unicode.IsLetter(rs[pos]))
			}
			// One value of one class against all the others of the other class, and nothing else there.
			if digits+letters != len(group) || min(digits, letters) != 1 || max(digits, letters) != len(group)-1 {
				continue
			}
			for _, rs := range group {
				odd := unicode.IsDigit(rs[pos]) == (digits == 1)
				if odd && lookalikes[rs[pos]] != "" {
					return fmt.Sprintf("has %q, with %q at position %d where the other %d values have %s",
						string(rs), rs[pos], pos+1, len(group)-1, classOf(!unicode.IsDigit(rs[pos]))), true
				}
			}
		}
	}
	return "", false
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func classOf(digit bool) string {
	if digit {
		return "a digit"
	}
	return "a letter"
}
