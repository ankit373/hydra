// SPDX-License-Identifier: MIT

package cache

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := OpenDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func put(t *testing.T, s *Store, prompt, response string) {
	t.Helper()
	if err := s.Put(Entry{Prompt: prompt, Response: response, Head: "h1"}); err != nil {
		t.Fatal(err)
	}
}

// unit is a normalized vector pointing mostly along one axis, so two of them
// have a cosine a test can reason about without a model.
func unit(dim, axis int, off float64) []float32 {
	v := make([]float32, dim)
	for i := range v {
		v[i] = float32(off)
	}
	v[axis] = 1
	return v
}

// The safe core: the same question, byte for byte once normalized, is served.
func TestLookup_ExactMatchIsServed(t *testing.T) {
	s := open(t)
	put(t, s, "rotate the signing key", "use hyctl edit")

	out := s.Lookup("  rotate   the signing key ", nil, DefaultThreshold)
	if !out.Found {
		t.Fatal("the same prompt was not served")
	}
	if !out.Hit.Exact || out.Hit.Similarity != 1 {
		t.Errorf("exact=%v similarity=%v, want an exact match", out.Hit.Exact, out.Hit.Similarity)
	}
	if out.Hit.Response != "use hyctl edit" {
		t.Errorf("served %q", out.Hit.Response)
	}
}

// A near match needs both gates. The vectors here are close enough on their
// own, so only the content gate can refuse, which is the point.
func TestLookup_NearMatchNeedsBothGates(t *testing.T) {
	s := open(t)
	vec := unit(8, 0, 0.30)
	if err := s.PutVec(Entry{Prompt: "what does --max-cost do", Response: "a ceiling", Head: "h1"}, vec); err != nil {
		t.Fatal(err)
	}

	// Different question, similar vector: refused, and counted as refused
	// rather than as a plain miss.
	out := s.Lookup("what does --max-heads do", vec, 0.5)
	if out.Found {
		t.Fatalf("served a different question at similarity %.3f", out.Hit.Similarity)
	}
	if !out.Refused {
		t.Error("the refusal was recorded as an ordinary miss, so the gate looks idle")
	}

	// Same question, function words differing: served.
	out = s.Lookup("please what does --max-cost do", vec, 0.5)
	if !out.Found {
		t.Fatal("refused a restatement of the same question")
	}
	if out.Hit.Exact {
		t.Error("a near match reported itself as exact")
	}
}

// Below the threshold nothing is served whatever the words say, or the cosine
// would be decoration.
func TestLookup_ThresholdIsEnforced(t *testing.T) {
	s := open(t)
	stored := unit(8, 0, 0)
	if err := s.PutVec(Entry{Prompt: "rotate the signing key", Response: "x", Head: "h1"}, stored); err != nil {
		t.Fatal(err)
	}
	far := unit(8, 1, 0)

	if out := s.Lookup("rotate the signing keys now", far, 0.95); out.Found {
		t.Errorf("served at similarity %.3f, under the 0.95 asked for", out.Hit.Similarity)
	}
}

// A store with no vectors is exact-match only rather than unusable, which is
// what a machine with no embedding model gets.
func TestLookup_WithoutVectorsIsExactOnly(t *testing.T) {
	s := open(t)
	put(t, s, "rotate the signing key", "x")

	if out := s.Lookup("rotate the signing key", nil, DefaultThreshold); !out.Found {
		t.Error("the exact match stopped working without an embedder")
	}
	if out := s.Lookup("please rotate the signing key", nil, DefaultThreshold); out.Found {
		t.Error("a near match was served with no vector to measure it with")
	}
}

// Nothing is counted until a caller says the answer was used, so a preview can
// ask what would happen without the report claiming it did.
func TestLookup_CountsNothingUntilRecorded(t *testing.T) {
	s := open(t)
	put(t, s, "rotate the signing key", "x")

	s.Lookup("rotate the signing key", nil, DefaultThreshold)
	s.Lookup("something else entirely", nil, DefaultThreshold)
	if st := s.Stat(); st.Hits != 0 || st.Misses != 0 {
		t.Errorf("a lookup alone moved the tallies: %d hits, %d misses", st.Hits, st.Misses)
	}

	s.Record(s.Lookup("rotate the signing key", nil, DefaultThreshold))
	s.Record(s.Lookup("something else entirely", nil, DefaultThreshold))
	st := s.Stat()
	if st.Hits != 1 || st.Exact != 1 || st.Misses != 1 {
		t.Errorf("hits=%d exact=%d misses=%d, want one of each", st.Hits, st.Exact, st.Misses)
	}
}

// Spend avoided is what the same answer cost to produce, which is the only
// figure anything here has actually measured.
func TestRecord_AvoidedIsWhatTheAnswerCost(t *testing.T) {
	s := open(t)
	if err := s.Put(Entry{Prompt: "q", Response: "a", Head: "h1", CostUSD: 0.0125}); err != nil {
		t.Fatal(err)
	}
	s.Record(s.Lookup("q", nil, DefaultThreshold))
	s.Record(s.Lookup("q", nil, DefaultThreshold))

	if got := s.Stat().AvoidedUSD; got != 0.025 {
		t.Errorf("avoided $%.4f over two hits of a $0.0125 answer, want $0.0250", got)
	}
}

// Oldest first, on both bounds. Deciding which answer is worth keeping would
// need a measurement nothing here has.
func TestPut_EvictsOldestFirst(t *testing.T) {
	s := open(t)
	s.maxN = 3
	for i := range 5 {
		if err := s.Put(Entry{
			Prompt: fmt.Sprintf("question number %d", i), Response: "a", Head: "h1",
			TS: time.Now().Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}
	st := s.Stat()
	if st.Entries != 3 {
		t.Fatalf("%d entries, want 3", st.Entries)
	}
	if st.Evicted != 2 {
		t.Errorf("evicted %d, want 2", st.Evicted)
	}
	if out := s.Lookup("question number 0", nil, DefaultThreshold); out.Found {
		t.Error("the oldest answer survived eviction")
	}
	if out := s.Lookup("question number 4", nil, DefaultThreshold); !out.Found {
		t.Error("the newest answer was evicted")
	}
}

func TestPut_EvictsOnTheByteBudget(t *testing.T) {
	s := open(t)
	s.SetBudget(1200)
	for i := range 20 {
		if err := s.Put(Entry{
			Prompt:   fmt.Sprintf("question number %d", i),
			Response: string(make([]byte, 200)) + fmt.Sprint(i), Head: "h1",
		}); err != nil {
			t.Fatal(err)
		}
	}
	st := s.Stat()
	if st.Bytes > 1200 {
		t.Errorf("%d bytes stored against a 1200 budget", st.Bytes)
	}
	if st.Entries == 0 {
		t.Error("the budget evicted everything, including the entry being written")
	}
}

// A non-positive budget keeps the default rather than turning the bound off: a
// misread config must not make the store unbounded.
func TestSetBudget_IgnoresNonPositive(t *testing.T) {
	s := open(t)
	s.SetBudget(-1)
	s.SetBudget(0)
	if s.budget != DefaultBudgetBytes {
		t.Errorf("budget = %d, want the default %d", s.budget, DefaultBudgetBytes)
	}
}

// Asking the same question again replaces the stored answer instead of
// stacking a second copy of it.
func TestPut_SameQuestionReplaces(t *testing.T) {
	s := open(t)
	put(t, s, "rotate the signing key", "old answer")
	put(t, s, "rotate the signing key", "new answer")

	if st := s.Stat(); st.Entries != 1 {
		t.Errorf("%d entries, want 1", st.Entries)
	}
	out := s.Lookup("rotate the signing key", nil, DefaultThreshold)
	if out.Hit.Response != "new answer" {
		t.Errorf("served %q, want the newer answer", out.Hit.Response)
	}
}

// An entry with nothing to serve would make a later hit return an empty answer
// as though a head had produced it.
func TestPut_RefusesAnEmptyOrOversizeAnswer(t *testing.T) {
	s := open(t)
	if err := s.Put(Entry{Prompt: "q", Response: "", Head: "h1"}); err == nil {
		t.Error("stored an entry with no response")
	}
	big := string(make([]byte, MaxResponseBytes+1))
	if err := s.Put(Entry{Prompt: "q", Response: big, Head: "h1"}); err == nil {
		t.Error("stored an answer over the size limit")
	}
}

// The store outlives the process, counters included, or a report describes one
// invocation rather than a machine.
func TestStore_SurvivesReopening(t *testing.T) {
	dir := t.TempDir()
	first, err := OpenDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.PutVec(Entry{Prompt: "rotate the key", Response: "a", Head: "h1", CostUSD: 0.01}, unit(4, 0, 0)); err != nil {
		t.Fatal(err)
	}
	first.Record(first.Lookup("rotate the key", nil, DefaultThreshold))

	second, err := OpenDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	st := second.Stat()
	if st.Entries != 1 || st.Hits != 1 {
		t.Fatalf("reopened with %d entries and %d hits, want 1 and 1", st.Entries, st.Hits)
	}
	out := second.Lookup("rotate the key", nil, DefaultThreshold)
	if !out.Found || out.Hit.CostUSD != 0.01 {
		t.Errorf("the reopened entry lost what it cost: %+v", out.Hit)
	}
	// And the vector survived, or every reopened store is exact-match only.
	if len(second.vecs[0]) != 4 {
		t.Errorf("the stored vector came back as %d dimensions", len(second.vecs[0]))
	}
}

// A vector of a different width belongs to a different model, and comparing
// the two would produce a number with no meaning.
func TestLookup_SkipsVectorsOfAnotherWidth(t *testing.T) {
	s := open(t)
	if err := s.PutVec(Entry{Prompt: "rotate the key", Response: "a", Head: "h1"}, unit(4, 0, 0)); err != nil {
		t.Fatal(err)
	}
	if out := s.Lookup("please rotate the key", unit(8, 0, 0), 0.1); out.Found {
		t.Error("compared vectors of two different widths")
	}
}

// A corrupt line must not stop the cache opening: it exists to make dispatches
// cheaper, and refusing to start would make them impossible.
func TestOpenDir_SkipsCorruptLines(t *testing.T) {
	dir := t.TempDir()
	body := `{"key":"a","prompt":"rotate the key","response":"a","ts":"2026-01-01T00:00:00Z"}
not json at all
{"key":"b","prompt":"list the heads","response":"b","ts":"2026-01-01T00:00:00Z"}
`
	if err := os.WriteFile(filepath.Join(dir, "answers.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenDir(dir)
	if err != nil {
		t.Fatalf("a corrupt line stopped the cache opening: %v", err)
	}
	if got := s.Stat().Entries; got != 1 {
		t.Errorf("%d entries, want 1: the decoder stops at the bad line", got)
	}
}

// StoredStats reports without opening for writing, and tells "off" apart from
// "on but empty".
func TestStoredStats_DistinguishesAbsentFromEmpty(t *testing.T) {
	dir := t.TempDir()
	if _, present := StoredStats(dir); present {
		t.Error("reported a cache that was never created")
	}
	s, err := OpenDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	put(t, s, "rotate the key", "a")
	st, present := StoredStats(dir)
	if !present || st.Entries != 1 {
		t.Errorf("present=%v entries=%d, want true and 1", present, st.Entries)
	}
}

// Encoding is little-endian float32 both ways, so a store written on one run
// is readable on the next.
func TestVecRoundTrip(t *testing.T) {
	in := []float32{0, 1, -1, 0.5, 3.25}
	out := decodeVec(encodeVec(in))
	if len(out) != len(in) {
		t.Fatalf("got %d floats, want %d", len(out), len(in))
	}
	for i := range in {
		if in[i] != out[i] {
			t.Errorf("element %d: %v != %v", i, out[i], in[i])
		}
	}
	if encodeVec(nil) != "" || decodeVec("") != nil {
		t.Error("an absent vector did not round-trip as absent")
	}
	if decodeVec("not base64 at all!") != nil {
		t.Error("garbage decoded to a vector")
	}
}

// A near hit and a refusal have to move different tallies, or the report
// cannot say whether the content gate did anything.
func TestRecord_SeparatesNearHitsFromRefusals(t *testing.T) {
	s := open(t)
	vec := unit(8, 0, 0.30)
	if err := s.PutVec(Entry{Prompt: "what does --max-cost do", Response: "a ceiling", Head: "h1"}, vec); err != nil {
		t.Fatal(err)
	}
	s.Record(s.Lookup("please what does --max-cost do", vec, 0.5))
	s.Record(s.Lookup("what does --max-heads do", vec, 0.5))

	st := s.Stat()
	if st.Near != 1 || st.Exact != 0 {
		t.Errorf("near=%d exact=%d, want one near hit", st.Near, st.Exact)
	}
	if st.Refused != 1 || st.Misses != 1 {
		t.Errorf("refused=%d misses=%d, want the refusal counted as both", st.Refused, st.Misses)
	}
}

// Dir is where the store lives, and it has to sit under the Hydra home rather
// than wherever the process happens to be.
func TestDir_IsUnderTheHydraHome(t *testing.T) {
	t.Setenv("HYDRA_HOME", filepath.Join(t.TempDir(), "home"))
	if got := Dir(); !strings.HasSuffix(got, filepath.Join("home", "cache")) {
		t.Errorf("Dir() = %q, want it under the configured home", got)
	}
}

// A counters file left behind by an older version, or truncated by a crash,
// must not stop the store opening.
func TestOpenDir_ToleratesUnreadableCounters(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "counters.json"), []byte("{oh no"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenDir(dir)
	if err != nil {
		t.Fatalf("unreadable counters stopped the store opening: %v", err)
	}
	if st := s.Stat(); st.Hits != 0 {
		t.Errorf("garbage counters were read as %d hits", st.Hits)
	}
}

// Counters outlive the entries: a machine that evicted everything has still
// served what it served, and a report that forgot would flatter the cache.
func TestStoredStats_ReadsCountersWithoutEntries(t *testing.T) {
	dir := t.TempDir()
	if err := writeCounters(dir, counters{Hits: 3, Misses: 7, Refused: 2}); err != nil {
		t.Fatal(err)
	}
	st, present := StoredStats(dir)
	if !present {
		t.Fatal("a cache with counters and no entries read as absent")
	}
	if st.Hits != 3 || st.Misses != 7 || st.Refused != 2 {
		t.Errorf("got %+v, want the counters that were written", st)
	}
}
