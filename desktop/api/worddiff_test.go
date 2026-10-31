// SPDX-License-Identifier: MIT

package api

import (
	"strings"
	"testing"
)

// slice renders what the view will draw, so an assertion reads as the text a
// person would see highlighted rather than as byte offsets.
func slice(text string, spans []Span) []string {
	out := []string{}
	for _, s := range spans {
		out = append(out, text[s.Start:s.End])
	}
	return out
}

func TestMarkPairs_HighlightsOnlyWhatMoved(t *testing.T) {
	lines := []DiffLine{
		{Op: "-", Text: `	key := os.Getenv("JWT_SECRET")`},
		{Op: "+", Text: `	key := loadSigningKey()`},
	}
	markPairs(lines)

	if len(lines[0].Spans) == 0 || len(lines[1].Spans) == 0 {
		t.Fatalf("a 1:1 replacement should be marked: %+v", lines)
	}
	// The shared prefix must not be marked; that is the whole point.
	for _, got := range slice(lines[0].Text, lines[0].Spans) {
		if strings.Contains(got, "key :=") {
			t.Errorf("the unchanged prefix was marked: %q", got)
		}
	}
	if joined := strings.Join(slice(lines[1].Text, lines[1].Spans), "|"); !strings.Contains(joined, "loadSigningKey") {
		t.Errorf("the added call was not marked, got %q", joined)
	}
}

// Splitting on whitespace alone would make `foo(bar)` one token, so the whole
// call reads as changed when only the argument moved.
func TestMarkPairs_MarksInsideACall(t *testing.T) {
	lines := []DiffLine{
		{Op: "-", Text: `return tok.SignedString([]byte(key))`},
		{Op: "+", Text: `return tok.SignedString(key)`},
	}
	markPairs(lines)

	joined := strings.Join(slice(lines[0].Text, lines[0].Spans), "|")
	if !strings.Contains(joined, "byte") {
		t.Errorf("the removed conversion was not marked, got %q", joined)
	}
	if strings.Contains(joined, "SignedString") {
		t.Errorf("the unchanged call name was marked: %q", joined)
	}
}

// Pairing removed lines to added ones inside a block invents a relationship the
// diff never established, so a block replacement is left unmarked.
func TestMarkPairs_LeavesMultiLineBlocksAlone(t *testing.T) {
	lines := []DiffLine{
		{Op: "-", Text: "one"},
		{Op: "-", Text: "two"},
		{Op: "+", Text: "uno"},
		{Op: "+", Text: "dos"},
	}
	markPairs(lines)
	for i, l := range lines {
		if len(l.Spans) != 0 {
			t.Errorf("line %d in a block replacement was marked: %+v", i, l.Spans)
		}
	}
}

// A line with nothing in common is a rewrite. Marking all of it adds nothing
// over the line's own add/remove colour.
func TestMarkPairs_DoesNotMarkAWholesaleRewrite(t *testing.T) {
	lines := []DiffLine{
		{Op: "-", Text: "alpha beta gamma"},
		{Op: "+", Text: "xxxxx yyyyy zzzzz"},
	}
	markPairs(lines)
	if len(lines[0].Spans) != 0 || len(lines[1].Spans) != 0 {
		t.Errorf("a full rewrite should stay unmarked, got %+v / %+v", lines[0].Spans, lines[1].Spans)
	}
}

func TestMarkPairs_IgnoresAdditionsAndRemovalsAlone(t *testing.T) {
	lines := []DiffLine{
		{Op: " ", Text: "context"},
		{Op: "+", Text: "brand new line"},
		{Op: " ", Text: "context"},
		{Op: "-", Text: "deleted line"},
	}
	markPairs(lines)
	for i, l := range lines {
		if len(l.Spans) != 0 {
			t.Errorf("line %d should have no intra-line detail: %+v", i, l.Spans)
		}
	}
}

// Adjacent tokens must merge, or a renamed call renders as a row of separate
// highlights instead of one.
func TestMergeSpans_JoinsTouchingRanges(t *testing.T) {
	got := mergeSpans([]Span{{0, 3}, {3, 7}, {9, 11}})
	if len(got) != 2 || got[0] != (Span{0, 7}) || got[1] != (Span{9, 11}) {
		t.Errorf("mergeSpans = %+v, want [{0 7} {9 11}]", got)
	}
	if mergeSpans(nil) != nil {
		t.Error("mergeSpans(nil) should stay nil")
	}
}

// Spans must always be sliceable against their own line: an off-by-one here
// panics the view rather than mis-rendering.
func TestMarkPairs_SpansAlwaysSliceTheirOwnLine(t *testing.T) {
	cases := [][2]string{
		{"", "x"},
		{"x", ""},
		{"a", "a"},
		{"  leading", "  leading tweak"},
		{"trailing  ", "trailing"},
		{"日本語 key", "日本語 value"},
		{strings.Repeat("tok ", 200), strings.Repeat("tok ", 199) + "end"},
	}
	for _, c := range cases {
		lines := []DiffLine{{Op: "-", Text: c[0]}, {Op: "+", Text: c[1]}}
		markPairs(lines)
		for _, l := range lines {
			for _, s := range l.Spans {
				if s.Start < 0 || s.End > len(l.Text) || s.Start >= s.End {
					t.Errorf("span %+v is not a valid range in %q (len %d)", s, l.Text, len(l.Text))
				}
			}
		}
	}
}

// A pair of very long lines must fall back to no marking rather than to a slow
// one. An unmarked line still renders correctly.
func TestMarkPairs_BoundsTheTable(t *testing.T) {
	long := strings.Repeat("a b ", 400) // ~800 tokens each side
	lines := []DiffLine{{Op: "-", Text: long + "x"}, {Op: "+", Text: long + "y"}}
	markPairs(lines)
	// Either it marked cheaply or it declined; what matters is it returned and
	// produced valid spans.
	for _, l := range lines {
		for _, s := range l.Spans {
			if s.End > len(l.Text) {
				t.Fatalf("span %+v out of range for len %d", s, len(l.Text))
			}
		}
	}
}
