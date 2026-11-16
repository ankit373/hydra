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
