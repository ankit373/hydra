// SPDX-License-Identifier: MIT

package ground

import (
	"slices"
	"testing"
)

func TestSentences_SplitsSentencesNotNumbers(t *testing.T) {
	got := Sentences("Costs fell by 3.5 percent. She said \"it rose. Then fell.\" Did it? Yes.\n1. Revenue was $1,200.\nOK.")
	want := []string{"Costs fell by 3.5 percent.", `She said "it rose. Then fell."`, "Did it?", "1. Revenue was $1,200."}
	if !slices.Equal(got, want) {
		t.Fatalf("sentences = %q, want %q", got, want)
	}
}

func TestQuotes_FoundWordForWordOnly(t *testing.T) {
	got := Quotes(`It said "the board   voted to close" and "no".`)
	if !slices.Equal(got, []string{"the board   voted to close"}) {
		t.Fatalf("Quotes = %q, want the one quote of three or more words", got)
	}
	if !QuoteIn(got[0], "THE Board voted to close all editions") || QuoteIn("the board voted to stay", "the board voted to close") {
		t.Error("QuoteIn must fold case and spacing and nothing else")
	}
}
