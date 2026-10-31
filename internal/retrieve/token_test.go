// SPDX-License-Identifier: MIT

package retrieve

import (
	"slices"
	"strings"
	"testing"
)

func TestTokenize_KeepsTheWholeIdentifierAndItsParts(t *testing.T) {
	got := Tokenize("ErrNoPropensity")
	for _, want := range []string{"errnopropensity", "err", "no", "propensity"} {
		if !slices.Contains(got, want) {
			t.Errorf("want %q in %v", want, got)
		}
	}
}

func TestTokenize_SplitsOnPunctuationAndDigits(t *testing.T) {
	cases := map[string][]string{
		"--max-cost":        {"max", "cost"},
		"internal/awsconf":  {"internal", "awsconf"},
		"signal: killed":    {"signal", "killed"},
		"HTTPServer2":       {"http", "server", "2"},
		"claude-sonnet-4.5": {"claude", "sonnet", "4", "5"},
		"snake_case_ident":  {"snake", "case", "ident"},
	}
	for in, want := range cases {
		got := Tokenize(in)
		for _, w := range want {
			if !slices.Contains(got, w) {
				t.Errorf("%q: want %q in %v", in, w, got)
			}
		}
	}
}

// The acronym boundary: the last capital starts the next word.
func TestSplitCase_AcronymBoundary(t *testing.T) {
	if got := splitCase("HTTPServer"); !slices.Equal(got, []string{"HTTP", "Server"}) {
		t.Errorf("got %v", got)
	}
	if got := splitCase("OTLPExport"); !slices.Equal(got, []string{"OTLP", "Export"}) {
		t.Errorf("got %v", got)
	}
}

// A base64 blob or a minified line is one enormous run nobody ever queries, and
// indexing it whole costs bytes to store a term nothing can match.
func TestTokenize_DropsOverlongRuns(t *testing.T) {
	long := strings.Repeat("a", MaxTermLen+1)
	for _, tok := range Tokenize(long) {
		if len(tok) > MaxTermLen {
			t.Fatalf("kept a %d-character term", len(tok))
		}
	}
}

func TestTokenize_EmptyAndPunctuationOnly(t *testing.T) {
	for _, in := range []string{"", "   ", "--- ... ///"} {
		if got := Tokenize(in); len(got) != 0 {
			t.Errorf("%q yielded %v", in, got)
		}
	}
}

func TestBag_CountsAndTotals(t *testing.T) {
	bag, n := Bag("cost cost budget")
	if bag["cost"] != 2 || bag["budget"] != 1 {
		t.Errorf("bad counts: %v", bag)
	}
	if n != 3 {
		t.Errorf("want 3 tokens, got %d", n)
	}
}
