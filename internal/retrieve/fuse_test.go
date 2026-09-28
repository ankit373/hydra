// SPDX-License-Identifier: MIT

package retrieve

import (
	"slices"
	"testing"
)

func TestFuse_OneListKeepsItsOrder(t *testing.T) {
	in := []Result{{DocID: "a", Score: 9}, {DocID: "b", Score: 5}, {DocID: "c", Score: 1}}
	if got := ids(Fuse(in)); !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Fatalf("got %v", got)
	}
}

// Rank, not score: the loser on points wins on agreement, which is the whole
// reason a weighted sum of a BM25 score and a cosine is the wrong combiner.
func TestFuse_AgreementBeatsOneStrongOpinion(t *testing.T) {
	lex := []Result{{DocID: "a", Score: 100}, {DocID: "b", Score: 2}}
	den := []Result{{DocID: "b", Score: 0.9}, {DocID: "a", Score: 0.1}}

	got := ids(Fuse(lex, den))
	if len(got) != 2 {
		t.Fatalf("want both, got %v", got)
	}
	// a is 1st and 2nd, b is 2nd and 1st: a genuine tie, broken by id.
	if !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("got %v", got)
	}

	// Now b is agreed-on and a is not.
	den = []Result{{DocID: "b", Score: 0.9}, {DocID: "c", Score: 0.5}, {DocID: "a", Score: 0.1}}
	if got := ids(Fuse(lex, den)); got[0] != "b" {
		t.Fatalf("want b first once both lists rank it above a, got %v", got)
	}
}

func TestFuse_IsDeterministic(t *testing.T) {
	lex := []Result{{DocID: "a", Score: 1}, {DocID: "b", Score: 1}, {DocID: "c", Score: 1}}
	den := []Result{{DocID: "c", Score: 1}, {DocID: "b", Score: 1}, {DocID: "a", Score: 1}}
	first := ids(Fuse(lex, den))
	for range 50 {
		if got := ids(Fuse(lex, den)); !slices.Equal(got, first) {
			t.Fatalf("order changed between calls: %v then %v", first, got)
		}
	}
}

func TestFuse_EmptyInputs(t *testing.T) {
	if got := Fuse(); len(got) != 0 {
		t.Errorf("got %v", ids(got))
	}
	if got := Fuse(nil, nil); len(got) != 0 {
		t.Errorf("got %v", ids(got))
	}
	if got := ids(Fuse(nil, []Result{{DocID: "a", Score: 1}})); !slices.Equal(got, []string{"a"}) {
		t.Errorf("got %v", got)
	}
}

func TestTop(t *testing.T) {
	in := []Result{{DocID: "a"}, {DocID: "b"}, {DocID: "c"}}
	if got := Top(in, 2); len(got) != 2 {
		t.Errorf("want 2, got %d", len(got))
	}
	if got := Top(in, 0); len(got) != 3 {
		t.Errorf("k=0 means no limit, got %d", len(got))
	}
	if got := Top(in, 99); len(got) != 3 {
		t.Errorf("want 3, got %d", len(got))
	}
}

func TestIDF_NeverNegative(t *testing.T) {
	// A term in every document must contribute nothing, never subtract.
	if got := idf(100, 100); got < 0 {
		t.Fatalf("idf went negative: %v", got)
	}
	if got := idf(0, 0); got != 0 {
		t.Fatalf("want 0 for an empty corpus, got %v", got)
	}
	// Rarer must be worth more.
	if idf(100, 1) <= idf(100, 50) {
		t.Fatal("a rare term must outweigh a common one")
	}
}
