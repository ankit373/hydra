// SPDX-License-Identifier: MIT

package embed

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T, model string) *Store {
	t.Helper()
	s, err := OpenStore(t.TempDir(), model)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	return s
}

func vec(dim int, v float32) []float32 {
	out := make([]float32, dim)
	for i := range out {
		out[i] = v
	}
	return out
}

func TestStore_RoundTrip(t *testing.T) {
	s := newTestStore(t, "m")
	want := []float32{1, -2.5, 3.25, 0}
	if err := s.Put(spanID(1), want); err != nil {
		t.Fatal(err)
	}
	got, ok := s.Get(spanID(1))
	if !ok {
		t.Fatal("stored vector not found")
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("at %d want %v got %v", i, want[i], got[i])
		}
	}
	if st := s.Stat(); st.Dim != 4 || st.Count != 1 || st.Model != "m" {
		t.Fatalf("bad stats: %+v", st)
	}
}

// The same text and model yield the same vector, so a repeated dispatch must
// cost one record, not two.
func TestStore_PutIsIdempotentPerSpan(t *testing.T) {
	s := newTestStore(t, "m")
	for range 5 {
		if err := s.Put(spanID(1), vec(8, 1)); err != nil {
			t.Fatal(err)
		}
	}
	if got := s.Stat().Count; got != 1 {
		t.Fatalf("want 1 record, got %d", got)
	}
}

func TestStore_DimensionMismatchRefused(t *testing.T) {
	s := newTestStore(t, "m")
	if err := s.Put(spanID(1), vec(8, 1)); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(spanID(2), vec(16, 1)); !errors.Is(err, ErrDimMismatch) {
		t.Fatalf("want ErrDimMismatch, got %v", err)
	}
	if got := s.Stat().Count; got != 1 {
		t.Fatalf("a refused vector must not be stored, count=%d", got)
	}
}

func TestStore_BadSpanIDRefused(t *testing.T) {
	s := newTestStore(t, "m")
	for _, bad := range []string{"", "short", spanID(1) + "extra"} {
		if err := s.Put(bad, vec(4, 1)); !errors.Is(err, ErrBadSpanID) {
			t.Fatalf("span %q: want ErrBadSpanID, got %v", bad, err)
		}
	}
}

func TestStore_ReopenSeesWhatWasWritten(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir, "m")
	if err != nil {
		t.Fatal(err)
	}
	for i := range 10 {
		if err := s.Put(spanID(i), vec(6, float32(i))); err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := OpenStore(dir, "m")
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Stat().Count; got != 10 {
		t.Fatalf("want 10 after reopen, got %d", got)
	}
	v, ok := reopened.Get(spanID(7))
	if !ok || v[0] != 7 {
		t.Fatalf("wrong vector after reopen: %v %v", v, ok)
	}
}

// Vectors from two models are not comparable. Keeping the old ones would let a
// query compare across two spaces with no surface saying so.
func TestStore_ModelChangeInvalidates(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir, "nomic-embed-text")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(spanID(1), vec(768, 1)); err != nil {
		t.Fatal(err)
	}

	s2, err := OpenStore(dir, "mxbai-embed-large")
	if err != nil {
		t.Fatal(err)
	}
	st := s2.Stat()
	if st.Count != 0 || st.Dim != 0 {
		t.Fatalf("old model's vectors survived: %+v", st)
	}
	// The new model is free to use a different width.
	if err := s2.Put(spanID(1), vec(1024, 1)); err != nil {
		t.Fatalf("new model refused its own dimension: %v", err)
	}
}

func TestStore_SameModelReopenKeepsData(t *testing.T) {
	dir := t.TempDir()
	s, _ := OpenStore(dir, "m")
	if err := s.Put(spanID(1), vec(4, 1)); err != nil {
		t.Fatal(err)
	}
	s2, _ := OpenStore(dir, "m")
	if got := s2.Stat().Count; got != 1 {
		t.Fatalf("same model must keep its vectors, got %d", got)
	}
}

// An interrupted append leaves a partial record. Reading its bytes as a vector
// would invent a neighbour out of whatever the crash left behind.
func TestStore_PartialTrailingRecordIsDropped(t *testing.T) {
	dir := t.TempDir()
	s, _ := OpenStore(dir, "m")
	for i := range 3 {
		if err := s.Put(spanID(i), vec(4, float32(i))); err != nil {
			t.Fatal(err)
		}
	}
	f, err := os.OpenFile(dataPath(dir), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte("truncated"))
	f.Close()

	reopened, err := OpenStore(dir, "m")
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Stat().Count; got != 3 {
		t.Fatalf("want 3 whole records, got %d", got)
	}
}

func TestStore_EvictsOldestFirstWithinBudget(t *testing.T) {
	dir := t.TempDir()
	s, _ := OpenStore(dir, "m")
	const dim = 16
	if err := s.Put(spanID(0), vec(dim, 0)); err != nil {
		t.Fatal(err)
	}
	rs := s.recordSize()
	// Room for exactly 4 records.
	s.SetBudget(rs * 4)

	for i := 1; i < 20; i++ {
		if err := s.Put(spanID(i), vec(dim, float32(i))); err != nil {
			t.Fatal(err)
		}
	}

	st := s.Stat()
	if st.Count != 4 {
		t.Fatalf("want 4 records within budget, got %d", st.Count)
	}
	if st.Bytes > rs*4 {
		t.Fatalf("store is %d bytes over a %d budget", st.Bytes, rs*4)
	}
	// The newest survive, the oldest are gone.
	for i := 16; i < 20; i++ {
		if _, ok := s.Get(spanID(i)); !ok {
			t.Fatalf("newest record %d was evicted", i)
		}
	}
	for i := range 16 {
		if _, ok := s.Get(spanID(i)); ok {
			t.Fatalf("oldest record %d survived eviction", i)
		}
	}
}

// A budget too small for one record empties the store rather than keeping one,
// which would report a budget the store does not honour.
func TestStore_BudgetBelowOneRecordEmpties(t *testing.T) {
	s := newTestStore(t, "m")
	if err := s.Put(spanID(1), vec(64, 1)); err != nil {
		t.Fatal(err)
	}
	s.SetBudget(8)
	if err := s.Put(spanID(2), vec(64, 1)); err != nil {
		t.Fatal(err)
	}
	if got := s.Stat().Count; got != 0 {
		t.Fatalf("want an empty store, got %d", got)
	}
}

func TestStore_EmptyStoreReadsCleanly(t *testing.T) {
	s := newTestStore(t, "m")
	if _, ok := s.Get(spanID(1)); ok {
		t.Fatal("empty store returned a vector")
	}
	if st := s.Stat(); st.Count != 0 || st.Dim != 0 {
		t.Fatalf("bad empty stats: %+v", st)
	}
}

func TestOpenStore_RefusesAnEmptyModel(t *testing.T) {
	if _, err := OpenStore(t.TempDir(), ""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
}

// The measured cost per stored vector, for the PR. Absolute rather than a
// ratio: how well a corpus compresses is an accident, a byte cost is not.
func TestStore_MeasuredBytesPerVector(t *testing.T) {
	const dim = 768
	s := newTestStore(t, "nomic-embed-text")
	const n = 500
	for i := range n {
		if err := s.Put(spanID(i), vec(dim, float32(i))); err != nil {
			t.Fatal(err)
		}
	}
	st := s.Stat()
	per := st.Bytes / int64(st.Count)
	fi, err := os.Stat(filepath.Join(s.dir, "vectors.dat"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("dim=%d records=%d bytes=%d on-disk=%d per-vector=%dB budget-holds=%d",
		dim, st.Count, st.Bytes, fi.Size(), per, DefaultBudgetBytes/per)
	if per != spanIDLen+tsLen+dim*4 {
		t.Fatalf("record width drifted from the layout: %d", per)
	}
}

func TestStore_EachWalksInWriteOrder(t *testing.T) {
	s := newTestStore(t, "m")
	for i := range 5 {
		if err := s.Put(spanID(i), vec(4, float32(i))); err != nil {
			t.Fatal(err)
		}
	}
	var order []float32
	var last time.Time
	err := s.Each(func(id string, ts time.Time, v []float32) bool {
		order = append(order, v[0])
		if ts.Before(last) {
			t.Fatalf("timestamps went backwards at %s", id)
		}
		last = ts
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, got := range order {
		if got != float32(i) {
			t.Fatalf("at %d want %v got %v", i, i, got)
		}
	}
}

func TestStore_EachStopsWhenAskedTo(t *testing.T) {
	s := newTestStore(t, "m")
	for i := range 5 {
		if err := s.Put(spanID(i), vec(4, float32(i))); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	if err := s.Each(func(string, time.Time, []float32) bool { n++; return n < 2 }); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("want 2 visits, got %d", n)
	}
}

func TestStore_EachOnAnEmptyStore(t *testing.T) {
	s := newTestStore(t, "m")
	calls := 0
	if err := s.Each(func(string, time.Time, []float32) bool { calls++; return true }); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("want no visits, got %d", calls)
	}
}

// Eviction rewrites the file underneath a walk. On Unix the open handle pins
// the old inode, so the walk stays consistent rather than reading a half-new
// file, which is the property Each's doc comment claims.
func TestStore_EachSurvivesAConcurrentEviction(t *testing.T) {
	s := newTestStore(t, "m")
	for i := range 20 {
		if err := s.Put(spanID(i), vec(8, float32(i))); err != nil {
			t.Fatal(err)
		}
	}
	seen := 0
	err := s.Each(func(id string, _ time.Time, v []float32) bool {
		if seen == 0 {
			s.SetBudget(s.recordSize() * 2)
			_ = s.Put(spanID(999), vec(8, 999))
		}
		seen++
		return true
	})
	if err != nil {
		t.Fatalf("the walk failed under eviction: %v", err)
	}
	if seen == 0 {
		t.Fatal("the walk visited nothing")
	}
}
