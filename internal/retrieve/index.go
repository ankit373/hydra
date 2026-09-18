// SPDX-License-Identifier: MIT

// Package retrieve finds past dispatches that resemble a prompt.
//
// Two scorers over the same documents: a lexical BM25 index that needs no model
// at all, and the dense cosine half that reads internal/embed's vectors when a
// model exists. Fused by rank, so the dense half is an upgrade rather than a
// precondition and a fresh install still retrieves.
//
// The lexical half is why: this corpus is code, where the discriminating tokens
// are identifiers and flags, and that is exactly where embeddings are weakest.
package retrieve

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/policy"
	"github.com/ankit373/hydra/internal/util"
)

// MaxDocBytes caps one stored document. A prompt carrying a whole file would
// otherwise dominate the index's bytes and, being one document, contribute
// almost nothing to what a query can find.
const MaxDocBytes = 64 << 10

// ErrBadDocID reports an empty document id. Refused rather than accepted: a
// document nothing can name is one nothing can retrieve.
var ErrBadDocID = errors.New("retrieve: document id must not be empty")

// Dir is where the lexical index lives.
func Dir() string { return filepath.Join(config.Dir(), "retrieve") }

func docsPath(dir string) string { return filepath.Join(dir, "docs.jsonl") }

// doc is one indexed prompt, held as a bag of words rather than as text.
//
// A bag, deliberately: it is what BM25 scores, it is smaller, and it is less
// recoverable than the prompt, since word order and punctuation are gone. That
// is a reduction in exposure, not an elimination of it, which is why this
// shares internal/embed's opt-in rather than claiming it needs none.
type doc struct {
	SpanID string         `json:"sid"`
	TS     int64          `json:"ts"`
	Len    int            `json:"n"`
	Terms  map[string]int `json:"t"`
}

// Stats reports what the index holds.
type Stats struct {
	Docs   int        `json:"docs"`
	Terms  int        `json:"terms"`
	Bytes  int64      `json:"bytes"`
	Budget int64      `json:"budget"`
	Oldest *time.Time `json:"oldest,omitempty"`
	Newest *time.Time `json:"newest,omitempty"`
}

// Index is a bounded BM25 index over past prompts. Safe for concurrent use.
type Index struct {
	mu     sync.Mutex
	dir    string
	budget int64

	docs  []doc
	byID  map[string]int
	df    map[string]int // term -> how many documents contain it
	total int            // summed document length, for the corpus mean
	size  int64
}

// OpenIndex loads the index at dir, creating it if absent.
func OpenIndex(dir string) (*Index, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	ix := &Index{dir: dir, budget: DefaultBudgetBytes,
		byID: map[string]int{}, df: map[string]int{}}
	if err := ix.load(); err != nil {
		return nil, err
	}
	return ix, nil
}

// DefaultBudgetBytes bounds the index. A bag of words for a typical prompt
// costs a few hundred bytes, an order of magnitude under a vector, so this
// holds far more documents than the vector store at the same number.
const DefaultBudgetBytes int64 = 64 << 20

// SetBudget bounds the index in bytes. Zero or less disables eviction.
func (ix *Index) SetBudget(b int64) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.budget = b
}

// load reads every document, skipping any line that will not parse.
//
// A truncated or corrupt final line is the shape an interrupted append leaves.
// Skipping it loses one document; refusing to open would lose the index.
func (ix *Index) load() error {
	f, err := os.Open(docsPath(ix.dir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), MaxDocBytes*2)
	var size int64
	for sc.Scan() {
		line := sc.Bytes()
		size += int64(len(line)) + 1
		var d doc
		if json.Unmarshal(line, &d) != nil || d.SpanID == "" {
			continue
		}
		ix.addLoaded(d)
	}
	ix.size = size
	return sc.Err()
}

// addLoaded puts a document into the in-memory structures without writing it.
func (ix *Index) addLoaded(d doc) {
	if i, ok := ix.byID[d.SpanID]; ok {
		// A duplicate id can only come from a rewritten file; the later entry
		// wins, and the earlier one's term counts must come back out.
		ix.removeAt(i)
	}
	ix.byID[d.SpanID] = len(ix.docs)
	ix.docs = append(ix.docs, d)
	ix.total += d.Len
	for t := range d.Terms {
		ix.df[t]++
	}
}

// removeAt empties the document at i in place, keeping every other index stable.
func (ix *Index) removeAt(i int) {
	d := ix.docs[i]
	if d.SpanID == "" {
		return
	}
	for t := range d.Terms {
		if ix.df[t]--; ix.df[t] <= 0 {
			delete(ix.df, t)
		}
	}
	ix.total -= d.Len
	delete(ix.byID, d.SpanID)
	ix.docs[i] = doc{}
}

// Add indexes one prompt under a document id.
//
// The text is redacted before it is tokenised, so a secret never becomes a
// term. A document already indexed is left alone: the same prompt produces the
// same bag, so rewriting it would cost bytes to record what is already there.
func (ix *Index) Add(docID, text string) error {
	if docID == "" {
		return ErrBadDocID
	}
	if len(text) > MaxDocBytes {
		text = text[:MaxDocBytes]
	}
	clean, _ := policy.Redact(text)
	terms, n := Bag(clean)
	if n == 0 {
		return nil
	}

	ix.mu.Lock()
	defer ix.mu.Unlock()
	if _, ok := ix.byID[docID]; ok {
		return nil
	}

	d := doc{SpanID: docID, TS: time.Now().UnixNano(), Len: n, Terms: terms}
	line, err := json.Marshal(d)
	if err != nil {
		return err
	}

	lk, err := util.Lock(util.LockPath(docsPath(ix.dir)))
	if err != nil {
		return err
	}
	defer lk.Unlock()

	f, err := os.OpenFile(docsPath(ix.dir), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	ix.addLoaded(d)
	ix.size += int64(len(line)) + 1
	return ix.evictLocked()
}

// evictLocked drops the oldest documents until the index fits its budget.
//
// Lines vary in length, so the kept set is computed from the newest backwards
// and the file is rewritten. Temp-then-rename, with the read side confined to
// its own function: Windows refuses to rename over a path that still has an
// open handle, which is a real failure and not a hypothetical one.
func (ix *Index) evictLocked() error {
	if ix.budget <= 0 || ix.size <= ix.budget {
		return nil
	}

	keep := make([]doc, 0, len(ix.docs))
	var bytes int64
	for i := len(ix.docs) - 1; i >= 0; i-- {
		if ix.docs[i].SpanID == "" {
			continue
		}
		line, err := json.Marshal(ix.docs[i])
		if err != nil {
			continue
		}
		if bytes+int64(len(line))+1 > ix.budget {
			break
		}
		bytes += int64(len(line)) + 1
		keep = append(keep, ix.docs[i])
	}
	// Collected newest-first; the file is oldest-first.
	for l, r := 0, len(keep)-1; l < r; l, r = l+1, r-1 {
		keep[l], keep[r] = keep[r], keep[l]
	}

	tmp := docsPath(ix.dir) + ".tmp"
	if err := writeDocs(tmp, keep); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, docsPath(ix.dir)); err != nil {
		os.Remove(tmp)
		return err
	}

	ix.docs, ix.byID, ix.df, ix.total, ix.size = nil, map[string]int{}, map[string]int{}, 0, 0
	for _, d := range keep {
		ix.addLoaded(d)
	}
	ix.size = bytes
	return nil
}

// writeDocs writes docs to path, closing the file before it returns.
func writeDocs(path string, docs []doc) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, d := range docs {
		line, err := json.Marshal(d)
		if err != nil {
			f.Close()
			return err
		}
		if _, err := w.Write(append(line, '\n')); err != nil {
			f.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Lexical ranks documents against a query by BM25, best first.
func (ix *Index) Lexical(query string, k int) []Result {
	bag, _ := Bag(query)
	if len(bag) == 0 {
		return nil
	}
	// Sorted once, so every document is scored by the same summation order.
	terms := make([]string, 0, len(bag))
	for t := range bag {
		terms = append(terms, t)
	}
	sort.Strings(terms)

	ix.mu.Lock()
	defer ix.mu.Unlock()

	n := len(ix.byID)
	if n == 0 {
		return nil
	}
	avg := float64(ix.total) / float64(n)

	var out []Result
	for i := range ix.docs {
		d := &ix.docs[i]
		if d.SpanID == "" {
			continue
		}
		if s := score(terms, d.Terms, d.Len, n, ix.df, avg); s > 0 {
			out = append(out, Result{DocID: d.SpanID, Score: s})
		}
	}
	sortResults(out)
	return Top(out, k)
}

// Has reports whether a document is indexed.
func (ix *Index) Has(docID string) bool {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	_, ok := ix.byID[docID]
	return ok
}

// Stat reports what the index holds.
func (ix *Index) Stat() Stats {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	st := Stats{Docs: len(ix.byID), Terms: len(ix.df), Bytes: ix.size, Budget: ix.budget}
	for i := range ix.docs {
		if ix.docs[i].SpanID == "" {
			continue
		}
		t := time.Unix(0, ix.docs[i].TS)
		if st.Oldest == nil || t.Before(*st.Oldest) {
			st.Oldest = &t
		}
		if st.Newest == nil || t.After(*st.Newest) {
			st.Newest = &t
		}
	}
	return st
}
