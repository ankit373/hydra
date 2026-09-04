// SPDX-License-Identifier: MIT

package payload

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ankit373/hydra/internal/util"
)

func openStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPutGetRoundTrip(t *testing.T) {
	s := openStore(t)
	const content = "package main\n\nfunc main() { println(\"hi\") }\n"

	h, err := s.Put(content, 1)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(h)
	if err != nil {
		t.Fatal(err)
	}
	if got != content {
		t.Errorf("Get returned %q, want %q", got, content)
	}
	if !s.Has(h) {
		t.Error("Has says the blob it just stored is absent")
	}
}

// One system prompt repeats across thousands of dispatches. Addressing by
// content is the only reason the store stays bounded, so a second Put of the
// same text must write nothing.
func TestPutIsContentAddressedAndDeduplicates(t *testing.T) {
	s := openStore(t)
	const content = "the same system prompt, every time"

	h1, err := s.Put(content, 1)
	if err != nil {
		t.Fatal(err)
	}
	before := s.Stat().PackBytes

	h2, err := s.Put(content, 1)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Errorf("the same content hashed to %q then %q", h1, h2)
	}
	if s.Len() != 1 {
		t.Errorf("store holds %d blobs after storing one thing twice", s.Len())
	}
	if after := s.Stat().PackBytes; after != before {
		t.Errorf("pack grew from %d to %d on a duplicate Put", before, after)
	}
}

// A sampled record with no inclusion probability cannot be weighted back to the
// population. Refusing it is the point: a silently-defaulted probability biases
// everything computed from the set, and nothing would say so.
func TestPutRefusesAnUnusableKeepProb(t *testing.T) {
	s := openStore(t)
	for _, p := range []float64{0, -1, 1.5, nan()} {
		if _, err := s.Put("content", p); err != ErrBadKeepProb {
			t.Errorf("Put(keepProb=%v) error = %v, want ErrBadKeepProb", p, err)
		}
	}
	if s.Len() != 0 {
		t.Errorf("a refused Put still stored %d blobs", s.Len())
	}
}

func TestPutRecordsTheInclusionProbability(t *testing.T) {
	s := openStore(t)
	if _, err := s.Put("sampled at a tenth", 0.1); err != nil {
		t.Fatal(err)
	}
	entries := readIndex(t, s.dir)
	if len(entries) != 1 {
		t.Fatalf("index holds %d entries, want 1", len(entries))
	}
	if entries[0].KeepProb != 0.1 {
		t.Errorf("KeepProb = %v, want 0.1", entries[0].KeepProb)
	}
	// Never omitempty: a missing probability is indistinguishable from zero,
	// and zero divides by zero in the estimator.
	raw, err := os.ReadFile(indexPath(s.dir))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "keep_prob") {
		t.Errorf("keep_prob is absent from the serialized index:\n%s", raw)
	}
}

// The store must not hold the secret it detected. Marking it and writing it
// anyway would be the worst of both: the privacy risk kept, the finding
// recorded as if it had been handled.
func TestPutRedactsSecretsBeforeWriting(t *testing.T) {
	s := openStore(t)
	const secret = "AKIAIOSFODNN7EXAMPLE"
	content := "deploy with key " + secret + " and mail ops@example.com"

	h, err := s.Put(content, 1)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(h)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, secret) {
		t.Errorf("the stored blob still contains the AWS key:\n%s", got)
	}
	if strings.Contains(got, "ops@example.com") {
		t.Errorf("the stored blob still contains the email:\n%s", got)
	}
	if !strings.Contains(got, "[REDACTED:aws access key id]") {
		t.Errorf("the redaction is unlabelled, so the finding is lost:\n%s", got)
	}

	// The bytes on disk are the real test — Get could be redacting on read.
	raw, err := os.ReadFile(packPath(s.dir))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Error("the pack file contains the raw secret")
	}

	e := readIndex(t, s.dir)[0]
	if !e.PII {
		t.Error("the entry is not marked as having contained PII")
	}
	if len(e.PIITypes) == 0 {
		t.Error("the entry records no PII types, so what was found is unknown")
	}
}

// Redaction happens before hashing. Hashing the raw text would leave a
// fingerprint of the secret in the index — an offline guess-and-compare oracle
// for exactly the value being kept off disk.
func TestHashAddressesTheRedactedContent(t *testing.T) {
	s := openStore(t)
	content := "token: ghp_" + strings.Repeat("a", 36)

	h, err := s.Put(content, 1)
	if err != nil {
		t.Fatal(err)
	}
	if h == Hash(content) {
		t.Error("the blob is addressed by the hash of the unredacted text, " +
			"which leaks the secret into the index")
	}
	got, _ := s.Get(h)
	if h != Hash(got) {
		t.Error("the hash does not address what was stored")
	}
}

// Content with nothing sensitive must survive byte-for-byte. A redactor that
// mangles ordinary source is worse than no redactor.
func TestPutLeavesOrdinaryContentUntouched(t *testing.T) {
	s := openStore(t)
	const content = "func add(a, b int) int { return a + b }"

	h, err := s.Put(content, 1)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(h)
	if err != nil {
		t.Fatal(err)
	}
	if got != content {
		t.Errorf("ordinary content came back as %q, want %q", got, content)
	}
	if readIndex(t, s.dir)[0].PII {
		t.Error("ordinary source was marked as PII")
	}
}

func TestPutRefusesOversizedContent(t *testing.T) {
	s := openStore(t)
	if _, err := s.Put(strings.Repeat("x", MaxBlobBytes+1), 1); err != ErrTooLarge {
		t.Errorf("error = %v, want ErrTooLarge", err)
	}
	if s.Len() != 0 {
		t.Error("an oversized payload was stored anyway")
	}
}

func TestGetUnknownHash(t *testing.T) {
	s := openStore(t)
	if _, err := s.Get("deadbeef"); err != ErrNotFound {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

// The index survives the process; a reopened store must find what the previous
// one wrote, or the pack is unreadable bytes.
func TestIndexSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.Put("persisted across processes", 1)
	if err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Get(h)
	if err != nil {
		t.Fatalf("a reopened store cannot read what the first one wrote: %v", err)
	}
	if got != "persisted across processes" {
		t.Errorf("Get returned %q", got)
	}
}

// A dictionary-compressed frame cannot be decoded without that exact
// dictionary. TrainDict used to write to a package-global path while Open read
// a store-relative one, so any store not at the default location silently lost
// every blob written after training.
func TestDictionaryIsStoreRelativeSoBlobsSurviveReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	seedCorpus(t, s, DictSampleTarget)
	if err := s.TrainDict(16 << 10); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(DictPath(dir)); err != nil {
		t.Fatalf("the dictionary is not beside its store: %v", err)
	}

	h, err := s.Put("written after the dictionary existed", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !readEntry(t, dir, h).Dict {
		t.Fatal("the blob was not dictionary-compressed, so this proves nothing")
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Get(h)
	if err != nil {
		t.Fatalf("a reopened store cannot decode its own dictionary frame: %v", err)
	}
	if got != "written after the dictionary existed" {
		t.Errorf("Get returned %q", got)
	}
}

// Losing the dictionary is unrecoverable. Saying so is the only honest
// behaviour; returning empty content would read as "this payload was blank".
func TestGetSaysSoWhenTheDictionaryIsGone(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	seedCorpus(t, s, DictSampleTarget)
	if err := s.TrainDict(16 << 10); err != nil {
		t.Fatal(err)
	}
	h, err := s.Put("needs the dictionary", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(DictPath(dir)); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Get(h)
	if err == nil {
		t.Fatalf("Get returned %q and no error with the dictionary deleted", got)
	}
	if !strings.Contains(err.Error(), "dictionary") {
		t.Errorf("error %q does not name the dictionary as the cause", err)
	}
}

func TestTrainDictRefusesTooSmallACorpus(t *testing.T) {
	s := openStore(t)
	seedCorpus(t, s, 5)
	if err := s.TrainDict(16 << 10); err == nil {
		t.Error("TrainDict accepted 5 samples; a dictionary trained on a handful " +
			"of prompts overfits them and helps nothing afterwards")
	}
}

// zstd.BuildDict panics with an integer divide by zero on a corpus that
// compresses to no literals. That input is not exotic here — it is what a young
// store looks like when one system prompt repeats. Training is a background
// convenience and must never take down the process doing the routing.
func TestTrainDictRefusesADegenerateCorpus(t *testing.T) {
	s := openStore(t)
	for i := 0; i < DictSampleTarget; i++ {
		// One system prompt, one varying task number — the realistic shape of a
		// young Hydra corpus, and the input that actually reproduces the panic.
		if _, err := s.Put(fmt.Sprintf(
			"You are a routing head. Task %d: implement the handler and return only code.", i), 1); err != nil {
			t.Fatal(err)
		}
	}
	err := s.TrainDict(16 << 10)
	if err == nil {
		t.Fatal("TrainDict succeeded on a corpus with no literals")
	}
	if !errors.Is(err, ErrDegenerateCorpus) {
		t.Errorf("error = %v, want ErrDegenerateCorpus", err)
	}
	// The store must still work; a failed training is not a broken store.
	h, err := s.Put("still usable afterwards", 1)
	if err != nil {
		t.Fatalf("the store is unusable after a failed training: %v", err)
	}
	if got, err := s.Get(h); err != nil || got != "still usable afterwards" {
		t.Errorf("Get after failed training returned %q, %v", got, err)
	}
}

// The measurement the design rests on: a loose content-addressed store is
// several times larger *on disk* than a packed one at the same logical size,
// because every small blob is charged a whole filesystem block.
func TestPackedStorageIsSmallerOnDiskThanLoose(t *testing.T) {
	s := openStore(t)
	loose := t.TempDir()

	const n = 200
	for i := 0; i < n; i++ {
		content := fmt.Sprintf("dispatch %d: route this task to the cheapest head that clears the bar\n", i)
		if _, err := s.Put(content, 1); err != nil {
			t.Fatal(err)
		}
		// The same content, one file per blob — the obvious design.
		name := filepath.Join(loose, Hash(content))
		if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	packed := s.Stat().DiskBytes
	var looseBytes int64
	entries, err := os.ReadDir(loose)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		looseBytes += util.DiskBytes(info)
	}

	if packed >= looseBytes {
		t.Errorf("packed %d bytes on disk vs loose %d — packing bought nothing", packed, looseBytes)
	}
	t.Logf("packed %d B vs loose %d B on disk (%.1fx) for %d blobs", packed, looseBytes,
		float64(looseBytes)/float64(packed), n)
}

// Put holds a lock across a read-modify-append of the pack. If it did not, two
// goroutines would record the same offset and one blob would decode as the
// other's bytes.
func TestConcurrentPutsAreSafeAndAllReadBack(t *testing.T) {
	s := openStore(t)
	const n = 40

	var wg sync.WaitGroup
	hashes := make([]string, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			hashes[i], errs[i] = s.Put(fmt.Sprintf("concurrent blob number %d", i), 1)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("Put %d failed: %v", i, err)
		}
	}
	for i, h := range hashes {
		got, err := s.Get(h)
		if err != nil {
			t.Fatalf("blob %d does not read back: %v", i, err)
		}
		if want := fmt.Sprintf("concurrent blob number %d", i); got != want {
			t.Fatalf("blob %d decoded as %q, want %q — offsets were interleaved", i, got, want)
		}
	}
}

func TestStatSummarisesTheStore(t *testing.T) {
	s := openStore(t)
	if _, err := s.Put("plain content", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put("mail me at ops@example.com", 1); err != nil {
		t.Fatal(err)
	}
	st := s.Stat()
	if st.Blobs != 2 {
		t.Errorf("Blobs = %d, want 2", st.Blobs)
	}
	if st.WithPII != 1 {
		t.Errorf("WithPII = %d, want 1", st.WithPII)
	}
	if st.RawBytes == 0 || st.PackBytes == 0 || st.DiskBytes == 0 {
		t.Errorf("Stat reports a zero size somewhere: %+v", st)
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────

func nan() float64 { var z float64; return z / z }

// seedCorpus writes samples that share a preamble but differ in body, which is
// what a real payload corpus looks like: one system prompt, many tasks. A
// corpus of near-identical strings compresses to no literals at all and the
// dictionary builder cannot model it (see TestTrainDictRefusesADegenerateCorpus).
func seedCorpus(t *testing.T, s *Store, n int) {
	t.Helper()
	const preamble = "You are a routing head. Return only code, no prose.\n\n"
	bodies := []string{
		"func Handler(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(payload) }",
		"SELECT id, name, created_at FROM accounts WHERE tenant = $1 ORDER BY created_at DESC LIMIT 50",
		"export const useStore = create((set) => ({ count: 0, inc: () => set((s) => ({ count: s.count + 1 })) }))",
		"def normalise(rows): return [{k.lower(): v for k, v in r.items()} for r in rows]",
		"resource \"aws_s3_bucket\" \"logs\" { bucket = var.name  force_destroy = false }",
		"impl Iterator for Chunks { type Item = Vec<u8>; fn next(&mut self) -> Option<Self::Item> { self.inner.next() } }",
		"CREATE INDEX CONCURRENTLY idx_events_actor ON events (actor_id, occurred_at DESC)",
	}
	for i := 0; i < n; i++ {
		body := bodies[i%len(bodies)]
		if _, err := s.Put(fmt.Sprintf("%sTask %d (%d): %s", preamble, i, i*7919, body), 1); err != nil {
			t.Fatal(err)
		}
	}
}

func readIndex(t *testing.T, dir string) []Entry {
	t.Helper()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]Entry, 0, len(s.index))
	for _, e := range s.index {
		out = append(out, e)
	}
	return out
}

func readEntry(t *testing.T, dir, hash string) Entry {
	t.Helper()
	for _, e := range readIndex(t, dir) {
		if e.Hash == hash {
			return e
		}
	}
	t.Fatalf("no index entry for %s", hash)
	return Entry{}
}
