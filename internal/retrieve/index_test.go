// SPDX-License-Identifier: MIT

package retrieve

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newIndex(t *testing.T) *Index {
	t.Helper()
	ix, err := OpenIndex(t.TempDir())
	if err != nil {
		t.Fatalf("OpenIndex: %v", err)
	}
	return ix
}

func mustAdd(t *testing.T, ix *Index, id, text string) {
	t.Helper()
	if err := ix.Add(id, text); err != nil {
		t.Fatalf("Add(%s): %v", id, err)
	}
}

func ids(rs []Result) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.DocID
	}
	return out
}

func TestIndex_FindsTheDocumentWithTheQueryTerm(t *testing.T) {
	ix := newIndex(t)
	mustAdd(t, ix, "a", "rotate the signing key in internal/auth/token.go")
	mustAdd(t, ix, "b", "add pagination to the results list")
	mustAdd(t, ix, "c", "write a haiku about the sea")

	got := ix.Lexical("signing key rotation", 3)
	if len(got) == 0 || got[0].DocID != "a" {
		t.Fatalf("want a first, got %v", ids(got))
	}
}

// The claim the whole issue rests on: an exact identifier is what separates two
// otherwise identical prompts, and BM25 is what notices.
func TestIndex_SeparatesPromptsThatDifferOnOneIdentifier(t *testing.T) {
	ix := newIndex(t)
	mustAdd(t, ix, "branch", "how do I delete this branch")
	mustAdd(t, ix, "repo", "how do I delete this repo")

	got := ix.Lexical("how do I delete this branch", 2)
	if len(got) != 2 {
		t.Fatalf("want both scored, got %v", ids(got))
	}
	if got[0].DocID != "branch" {
		t.Fatalf("want branch first, got %v", ids(got))
	}
	if got[0].Score <= got[1].Score {
		t.Fatalf("the exact match must outscore the near miss: %v", got)
	}
}

func TestIndex_UnknownTermMatchesNothing(t *testing.T) {
	ix := newIndex(t)
	mustAdd(t, ix, "a", "rotate the signing key")
	if got := ix.Lexical("kubernetes helm chart", 5); len(got) != 0 {
		t.Fatalf("want no matches, got %v", ids(got))
	}
}

func TestIndex_EmptyQueryAndEmptyIndex(t *testing.T) {
	ix := newIndex(t)
	if got := ix.Lexical("anything", 5); got != nil {
		t.Errorf("empty index returned %v", ids(got))
	}
	mustAdd(t, ix, "a", "some text")
	if got := ix.Lexical("", 5); got != nil {
		t.Errorf("empty query returned %v", ids(got))
	}
	if got := ix.Lexical("!!! ...", 5); got != nil {
		t.Errorf("punctuation-only query returned %v", ids(got))
	}
}

// A secret must never become a term. Asserting the placeholder too, so removing
// Redact fails here even if some other path happened to empty the text.
func TestIndex_SecretNeverBecomesATerm(t *testing.T) {
	const key = "AKIAIOSFODNN7EXAMPLE"
	ix := newIndex(t)
	mustAdd(t, ix, "a", "deploy with "+key+" please")

	if got := ix.Lexical(key, 5); len(got) != 0 {
		t.Fatalf("the secret is queryable: %v", ids(got))
	}
	if got := ix.Lexical("redacted", 5); len(got) == 0 {
		t.Fatal("want the redaction placeholder indexed in the secret's place")
	}

	raw, err := os.ReadFile(filepath.Join(ix.dir, "docs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), strings.ToLower(key)) {
		t.Fatal("the secret reached the index file")
	}
}

func TestIndex_AddIsIdempotent(t *testing.T) {
	ix := newIndex(t)
	for range 5 {
		mustAdd(t, ix, "a", "rotate the signing key")
	}
	if got := ix.Stat().Docs; got != 1 {
		t.Fatalf("want 1 document, got %d", got)
	}
}

func TestIndex_RefusesAnEmptyID(t *testing.T) {
	if err := newIndex(t).Add("", "text"); err != ErrBadDocID {
		t.Fatalf("want ErrBadDocID, got %v", err)
	}
}

func TestIndex_ReopenSeesWhatWasWritten(t *testing.T) {
	dir := t.TempDir()
	ix, err := OpenIndex(dir)
	if err != nil {
		t.Fatal(err)
	}
	mustAdd(t, ix, "a", "rotate the signing key")
	mustAdd(t, ix, "b", "add pagination")

	re, err := OpenIndex(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := re.Stat().Docs; got != 2 {
		t.Fatalf("want 2 after reopen, got %d", got)
	}
	if got := re.Lexical("signing key", 1); len(got) == 0 || got[0].DocID != "a" {
		t.Fatalf("query broke after reopen: %v", ids(got))
	}
}

// An interrupted append leaves a partial final line. Skipping it loses one
// document; refusing to open would lose the index.
func TestIndex_CorruptTrailingLineIsSkipped(t *testing.T) {
	dir := t.TempDir()
	ix, _ := OpenIndex(dir)
	mustAdd(t, ix, "a", "rotate the signing key")

	f, err := os.OpenFile(filepath.Join(dir, "docs.jsonl"), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"sid":"b","n":3,"t":{"broken":`)
	f.Close()

	re, err := OpenIndex(dir)
	if err != nil {
		t.Fatalf("a corrupt line must not fail the open: %v", err)
	}
	if got := re.Stat().Docs; got != 1 {
		t.Fatalf("want 1 readable document, got %d", got)
	}
}

func TestIndex_EvictsOldestFirstWithinBudget(t *testing.T) {
	ix := newIndex(t)
	mustAdd(t, ix, "doc0", "alpha bravo charlie delta echo")
	one := ix.Stat().Bytes
	ix.SetBudget(one * 4)

	for i := 1; i < 30; i++ {
		mustAdd(t, ix, fmt.Sprintf("doc%d", i), "alpha bravo charlie delta echo")
	}

	st := ix.Stat()
	if st.Bytes > one*4 {
		t.Fatalf("index is %d bytes over a %d budget", st.Bytes, one*4)
	}
	if st.Docs == 0 || st.Docs > 4 {
		t.Fatalf("want at most 4 documents, got %d", st.Docs)
	}
	if !ix.Has("doc29") {
		t.Error("the newest document was evicted")
	}
	if ix.Has("doc0") {
		t.Error("the oldest document survived eviction")
	}
}

// Eviction rewrites the file. What it leaves must be readable and consistent,
// which is what a reopen checks that an in-memory assertion cannot.
func TestIndex_EvictionLeavesAConsistentFile(t *testing.T) {
	dir := t.TempDir()
	ix, _ := OpenIndex(dir)
	mustAdd(t, ix, "doc0", "alpha bravo charlie")
	ix.SetBudget(ix.Stat().Bytes * 3)
	for i := 1; i < 20; i++ {
		mustAdd(t, ix, fmt.Sprintf("doc%d", i), "alpha bravo charlie")
	}

	re, err := OpenIndex(dir)
	if err != nil {
		t.Fatal(err)
	}
	if re.Stat().Docs != ix.Stat().Docs {
		t.Fatalf("reopen sees %d documents, memory says %d", re.Stat().Docs, ix.Stat().Docs)
	}
	f, err := os.Open(filepath.Join(dir, "docs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if !strings.HasPrefix(sc.Text(), "{") || !strings.HasSuffix(sc.Text(), "}") {
			t.Fatalf("eviction left a partial line: %q", sc.Text())
		}
	}
}

func TestIndex_StatReportsAges(t *testing.T) {
	ix := newIndex(t)
	if st := ix.Stat(); st.Oldest != nil || st.Newest != nil {
		t.Error("an empty index must report no timestamps, not year 1")
	}
	mustAdd(t, ix, "a", "rotate the signing key")
	st := ix.Stat()
	if st.Oldest == nil || st.Newest == nil || st.Terms == 0 {
		t.Fatalf("bad stats: %+v", st)
	}
}

func TestIndex_LongDocumentIsTruncatedNotRefused(t *testing.T) {
	ix := newIndex(t)
	mustAdd(t, ix, "a", "needle "+strings.Repeat("filler ", MaxDocBytes))
	if got := ix.Lexical("needle", 1); len(got) != 1 {
		t.Fatalf("a long prompt must still be indexed: %v", ids(got))
	}
}

// Scores must be bit-identical between calls, not merely close.
//
// Floating-point addition is not associative, so summing a query's terms in Go
// map order gives a score that differs in the last place per call, and two
// documents within an ULP then sort differently when nothing changed.
//
// Uses the real corpus deliberately. A synthetic one where every term carries
// the same weight is order-independent by construction, so it passes whether
// the order is enforced or not: the first draft of this test did exactly that
// and let the bug through.
func TestIndex_ScoresAreBitIdenticalBetweenCalls(t *testing.T) {
	ix := newIndex(t)
	docs := loadCorpus(t, ix)
	if len(docs) < 50 {
		t.Skipf("corpus too small: %d", len(docs))
	}

	const q = "fix the dispatch cost budget when a local head streams a trace span with no tier"
	first := ix.Lexical(q, 50)
	if len(first) < 5 {
		t.Fatalf("need several scored documents, got %d", len(first))
	}

	for range 200 {
		got := ix.Lexical(q, 50)
		if len(got) != len(first) {
			t.Fatalf("result count changed: %d then %d", len(first), len(got))
		}
		for i := range first {
			if got[i] != first[i] {
				t.Fatalf("call differed at %d: %+v then %+v", i, first[i], got[i])
			}
		}
	}
}

// loadCorpus indexes testdata/corpus.txt and returns the lines.
func loadCorpus(t *testing.T, ix *Index) []string {
	t.Helper()
	f, err := os.Open("testdata/corpus.txt")
	if err != nil {
		t.Skipf("no corpus: %v", err)
	}
	defer f.Close()

	var docs []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		mustAdd(t, ix, fmt.Sprintf("doc%d", len(docs)), line)
		docs = append(docs, line)
	}
	return docs
}
