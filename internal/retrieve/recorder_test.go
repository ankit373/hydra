// SPDX-License-Identifier: MIT

package retrieve

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/embed"
)

func TestRecorder_NilSearcherAcceptsAndDiscards(t *testing.T) {
	r := NewRecorder(nil)
	r.Record("a", "some text")
	r.Close(time.Second)
	if i, d, f := r.Counts(); i|d|f != 0 {
		t.Fatalf("want all zero, got %d %d %d", i, d, f)
	}
	if s, d, f := r.Embeddings(); s|d|f != 0 {
		t.Fatalf("want all zero, got %d %d %d", s, d, f)
	}
}

func TestRecorder_IndexesWhatItIsGiven(t *testing.T) {
	s := newSearcher(t, embed.Unavailable{}, false)
	r := NewRecorder(s)
	r.Record("0123456789abcdef", "rotate the signing key")
	r.Close(5 * time.Second)

	indexed, _, failed := r.Counts()
	if indexed != 1 || failed != 0 {
		t.Fatalf("indexed=%d failed=%d", indexed, failed)
	}
	if hits := s.Index().Lexical("signing key", 1); len(hits) != 1 {
		t.Fatalf("the prompt is not retrievable: %v", ids(hits))
	}
}

// The guarantee the type exists for. The lexical write is cheap, but the
// embedding behind it is not, and neither may be felt by the dispatch.
func TestRecorder_NeverBlocks(t *testing.T) {
	s := newSearcher(t, embed.Unavailable{}, false)
	r := NewRecorder(s)

	start := time.Now()
	for i := range QueueDepth * 4 {
		r.Record(fmt.Sprintf("doc%d", i), "some prompt text")
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("Record blocked for %v; it must return at once", el)
	}
	r.Close(5 * time.Second)
}

func TestRecorder_IgnoresEmptyInput(t *testing.T) {
	s := newSearcher(t, embed.Unavailable{}, false)
	r := NewRecorder(s)
	r.Record("", "text")
	r.Record("id", "")
	r.Close(time.Second)
	if got := s.Index().Stat().Docs; got != 0 {
		t.Fatalf("want nothing indexed, got %d", got)
	}
}

func TestRecorder_CloseIsIdempotent(t *testing.T) {
	r := NewRecorder(newSearcher(t, embed.Unavailable{}, false))
	r.Close(time.Second)
	r.Close(time.Second)
}

// A duplicate id can only come from a rewritten file. The later entry wins, and
// the earlier one's term counts have to come back out, or every document
// frequency it contributed is double-counted forever.
func TestIndex_DuplicateIDOnDiskDoesNotDoubleCount(t *testing.T) {
	dir := t.TempDir()
	ix, err := OpenIndex(dir)
	if err != nil {
		t.Fatal(err)
	}
	mustAdd(t, ix, "a", "alpha bravo")
	mustAdd(t, ix, "b", "charlie delta")

	// Append a second entry for "a" with different terms, as a rewrite would.
	f, err := os.OpenFile(filepath.Join(dir, "docs.jsonl"), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"sid":"a","ts":1,"n":2,"t":{"echo":1,"foxtrot":1}}` + "\n")
	f.Close()

	re, err := OpenIndex(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := re.Stat().Docs; got != 2 {
		t.Fatalf("want 2 distinct documents, got %d", got)
	}
	// The superseded terms must be gone, not merely shadowed.
	if hits := re.Lexical("alpha", 5); len(hits) != 0 {
		t.Errorf("the superseded document is still matchable: %v", ids(hits))
	}
	if hits := re.Lexical("echo foxtrot", 5); len(hits) != 1 || hits[0].DocID != "a" {
		t.Errorf("the winning entry is not matchable: %v", ids(hits))
	}
}
