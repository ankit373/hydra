// SPDX-License-Identifier: MIT

package signals

import (
	"strings"
	"unicode"
)

// Script names reported by language.script. "other" covers a prompt whose
// letters are in none of the tracked scripts; "" means no letters at all.
const (
	ScriptLatin      = "latin"
	ScriptCyrillic   = "cyrillic"
	ScriptHan        = "han"
	ScriptKana       = "kana"
	ScriptHangul     = "hangul"
	ScriptArabic     = "arabic"
	ScriptHebrew     = "hebrew"
	ScriptGreek      = "greek"
	ScriptDevanagari = "devanagari"
	ScriptThai       = "thai"
	ScriptOther      = "other"
)

var scriptTable = []struct {
	name  string
	table *unicode.RangeTable
}{
	{ScriptLatin, unicode.Latin},
	{ScriptCyrillic, unicode.Cyrillic},
	{ScriptHan, unicode.Han},
	{ScriptHangul, unicode.Hangul},
	{ScriptArabic, unicode.Arabic},
	{ScriptHebrew, unicode.Hebrew},
	{ScriptGreek, unicode.Greek},
	{ScriptDevanagari, unicode.Devanagari},
	{ScriptThai, unicode.Thai},
}

// DominantScript counts letters per script and returns the most common, or ""
// when the prompt carries no letters at all. Digits and punctuation are not
// letters in any script and would otherwise make every number-heavy prompt Latin.
func DominantScript(s string) string {
	counts := map[string]int{}
	for _, r := range s {
		if !unicode.IsLetter(r) {
			continue
		}
		// Hiragana and Katakana are one script for routing: both mean Japanese.
		if unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) {
			counts[ScriptKana]++
			continue
		}
		named := false
		for _, e := range scriptTable {
			if unicode.Is(e.table, r) {
				counts[e.name]++
				named = true
				break
			}
		}
		if !named {
			counts[ScriptOther]++
		}
	}
	best, bestN := "", 0
	for name, n := range counts {
		// Ties broken by name so one prompt does not script two ways across runs,
		// since map iteration is unordered (the #1 cause of flaky routing here).
		if n > bestN || (n == bestN && name < best) {
			best, bestN = name, n
		}
	}
	return best
}

// LanguageCode is ISO 639-1 only where the script settles the language: kana
// means Japanese, hangul means Korean, Han alone means Chinese. Empty
// otherwise, because telling English from French needs a model or a threshold
// nobody has measured, and a guessed default would route on a coin flip (#1041).
func LanguageCode(s string) string {
	var kana, hangul, han int
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Hiragana, r), unicode.Is(unicode.Katakana, r):
			kana++
		case unicode.Is(unicode.Hangul, r):
			hangul++
		case unicode.Is(unicode.Han, r):
			han++
		}
	}
	switch {
	case kana > 0 && kana >= hangul:
		return "ja"
	case hangul > 0:
		return "ko"
	case han > 0:
		return "zh"
	}
	return ""
}

// Structure is the shape of a prompt, counted rather than classified.
type Structure struct {
	Questions    int
	ListItems    int
	CodeFences   int
	OrderedSteps bool
}

// Shape counts the markers vLLM SR's `structure` family routes on. A run of
// "???" is one question, not three, or emphasis would read as interrogation.
func Shape(s string) Structure {
	var out Structure
	prevQuestion := false
	for _, r := range s {
		if r == '?' {
			if !prevQuestion {
				out.Questions++
			}
			prevQuestion = true
			continue
		}
		prevQuestion = false
	}
	fences, numbered := 0, 0
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "```") {
			fences++
			continue
		}
		switch {
		case isBulletItem(t):
			out.ListItems++
		case isNumberedItem(t):
			out.ListItems++
			numbered++
		}
	}
	// Pairs, so an unterminated fence does not report a block that never closed.
	out.CodeFences = fences / 2
	out.OrderedSteps = numbered >= 2
	return out
}

func isBulletItem(t string) bool {
	if len(t) < 2 {
		return false
	}
	return (t[0] == '-' || t[0] == '*' || t[0] == '+') && t[1] == ' '
}

// isNumberedItem matches "1. " and "1) ", the ordered-workflow markers. The
// separator is required, or a sentence opening with a year would be a step.
func isNumberedItem(t string) bool {
	i := 0
	for i < len(t) && t[i] >= '0' && t[i] <= '9' {
		i++
	}
	if i == 0 || i+1 >= len(t) {
		return false
	}
	return (t[i] == '.' || t[i] == ')') && t[i+1] == ' '
}
