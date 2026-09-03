// SPDX-License-Identifier: MIT

package api

// Intra-line diff: which part of a changed line actually changed.
//
// A line-level diff says a line was replaced; it does not say what on it moved.
// Reviewing an agent's edit is mostly the second question, and re-reading two
// nearly identical 100-character lines to spot one renamed identifier is the
// work the view should be doing.
//
// Hand-rolled over tokens with the same bounded-LCS shape diffLines already
// uses, and for the same stated reason: this module stays separable from hyctl,
// so a dependency for something this small is not worth it.

// maxWordCells bounds the token-level table the way maxLCSCells bounds the
// line-level one. A pair of very long lines falls back to no marking rather
// than to a slow one — an unmarked line still renders correctly.
const maxWordCells = 1 << 16 // 256x256 tokens

// markPairs annotates 1:1 replacements in place.
//
// Only a single removed line followed by a single added line is paired. A
// multi-line block has no defensible pairing — guessing which removed line
// became which added one invents a relationship the diff never established —
// so those lines are left unmarked, which is how they render today.
func markPairs(lines []DiffLine) {
	for i := 0; i+1 < len(lines); i++ {
		if lines[i].Op != "-" || lines[i+1].Op != "+" {
			continue
		}
		// A run longer than one on either side is a block replacement.
		if i > 0 && lines[i-1].Op == "-" {
			continue
		}
		if i+2 < len(lines) && lines[i+2].Op == "+" {
			continue
		}
		del, add := words(lines[i].Text), words(lines[i+1].Text)
		if len(del)*len(add) > maxWordCells {
			continue
		}
		dSpans, aSpans, sharedWords := wordSpans(del, add)
		// No word survived, so this is a rewrite rather than a modification,
		// and marking every token adds nothing over the line's own colour.
		//
		// Punctuation and spacing are excluded from that count on purpose:
		// "alpha beta gamma" and "xxxxx yyyyy zzzzz" share both spaces, so a
		// test that counted any common token would call a total rewrite a
		// modification and highlight the whole line.
		if sharedWords == 0 {
			i++
			continue
		}
		// One side having nothing unique is normal and still worth marking: a
		// deletion inside a line leaves the added side with no unique tokens
		// at all, and requiring both sides skipped exactly that case.
		if len(dSpans) > 0 || len(aSpans) > 0 {
			lines[i].Spans = dSpans
			lines[i+1].Spans = aSpans
		}
		i++ // the added line is consumed
	}
}

// words splits a line into tokens with their byte offsets, keeping runs of
// identifier characters together and every other byte separate.
//
// Splitting on whitespace alone is too coarse for code: `foo(bar)` and
// `foo(baz)` are one token each, so the whole call reads as changed. Splitting
// per byte is too fine and marks matching punctuation as noise.
func words(s string) []token {
	var out []token
	i := 0
	for i < len(s) {
		if isWordByte(s[i]) {
			j := i
			for j < len(s) && isWordByte(s[j]) {
				j++
			}
			out = append(out, token{Text: s[i:j], Start: i, End: j})
			i = j
			continue
		}
		out = append(out, token{Text: s[i : i+1], Start: i, End: i + 1})
		i++
	}
	return out
}

func isWordByte(c byte) bool {
	return c == '_' ||
		(c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9')
}

type token struct {
	Text  string
	Start int
	End   int
}

// wordSpans returns the byte ranges unique to each side, via an LCS over
// tokens, plus how many matched tokens were actual words. Adjacent ranges are
// merged so the view draws one highlight over `os.Getenv` rather than three.
func wordSpans(del, add []token) (dSpans, aSpans []Span, sharedWords int) {
	n, m := len(del), len(add)
	if n == 0 || m == 0 {
		return nil, nil, 0
	}
	// lcs[i][j] = length of the longest common token subsequence of del[i:] and add[j:].
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if del[i].Text == add[j].Text {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}

	i, j := 0, 0
	for i < n && j < m {
		switch {
		case del[i].Text == add[j].Text:
			if isWordByte(del[i].Text[0]) {
				sharedWords++
			}
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			dSpans = append(dSpans, Span{Start: del[i].Start, End: del[i].End})
			i++
		default:
			aSpans = append(aSpans, Span{Start: add[j].Start, End: add[j].End})
			j++
		}
	}
	for ; i < n; i++ {
		dSpans = append(dSpans, Span{Start: del[i].Start, End: del[i].End})
	}
	for ; j < m; j++ {
		aSpans = append(aSpans, Span{Start: add[j].Start, End: add[j].End})
	}
	return mergeSpans(dSpans), mergeSpans(aSpans), sharedWords
}

// mergeSpans joins touching ranges. Tokens are split finely on purpose, so
// without this a renamed call renders as a row of separate highlights.
func mergeSpans(in []Span) []Span {
	if len(in) == 0 {
		return nil
	}
	out := []Span{in[0]}
	for _, s := range in[1:] {
		last := &out[len(out)-1]
		if s.Start <= last.End {
			if s.End > last.End {
				last.End = s.End
			}
			continue
		}
		out = append(out, s)
	}
	return out
}
