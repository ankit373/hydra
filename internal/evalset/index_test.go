// SPDX-License-Identifier: MIT

package evalset

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// writeRecordBehindAddsBack appends a record the way Add would, without telling
// the sidecar, which is what a crash between the corpus append and the header
// commit leaves behind.
func writeRecordBehindAddsBack(t *testing.T, path string, e Example) {
	t.Helper()
	e.V = SchemaVersion
	e.TS = "2026-09-08T00:00:00Z"
	e.CandidateHash = Hash(e.Candidate)
	if e.TaskHash == "" {
		e.TaskHash = Hash(e.Domain + "\x00" + e.Source)
	}
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(append(raw, '\n')); err != nil {
		t.Fatal(err)
	}
}

func headerSize(t *testing.T, path string) int64 {
	t.Helper()
	raw, err := os.ReadFile(indexPath(path))
	if err != nil {
		t.Fatalf("no sidecar: %v", err)
	}
	if len(raw) < idxHdrLen || string(raw[:len(idxMagic)]) != idxMagic {
		t.Fatalf("sidecar has no header: %q", raw[:min(len(raw), 64)])
	}
	var n int64
	if _, err := fmt.Sscanf(strings.TrimSpace(string(raw[len(idxMagic):idxHdrLen])), "%d", &n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A corpus written before the sidecar existed must still dedup, or upgrading
// duplicates every example anyone already had.
func TestAddRebuildsAMissingIndex(t *testing.T) {
	p := filepath.Join(t.TempDir(), "examples.jsonl")
	e := ex("go", "func main() {}", true)
	writeRecordBehindAddsBack(t, p, e)
	if _, err := os.Stat(indexPath(p)); !os.IsNotExist(err) {
		t.Fatalf("expected no sidecar yet, stat err = %v", err)
	}

	added, err := Add(p, e)
	if err != nil || added {
		t.Fatalf("added=%v err=%v, want false: the record is already in the corpus", added, err)
	}
	if got, err := Load(p); err != nil || len(got) != 1 {
		t.Fatalf("corpus has %d records, err=%v, want 1", len(got), err)
	}
}

// The stale case is a crash between the corpus append and the header commit.
// The sidecar then describes a shorter corpus, and trusting it would re-add
// everything written since.
func TestAddRebuildsAStaleIndex(t *testing.T) {
	p := filepath.Join(t.TempDir(), "examples.jsonl")
	if _, err := Add(p, ex("go", "package a", true)); err != nil {
		t.Fatal(err)
	}
	behind := ex("go", "package b", true)
	writeRecordBehindAddsBack(t, p, behind)

	added, err := Add(p, behind)
	if err != nil || added {
		t.Fatalf("added=%v err=%v, want false: the sidecar was stale, not authoritative", added, err)
	}
	if got, _ := Load(p); len(got) != 2 {
		t.Fatalf("corpus has %d records, want 2", len(got))
	}
	if h, sz := headerSize(t, p), fileSize(p); h != sz {
		t.Errorf("sidecar header says %d bytes, corpus is %d", h, sz)
	}
}

// Garbage in the sidecar must cost a rebuild, never a wrong dedup answer: it is
// derived data, so the corpus is always the authority.
func TestAddRebuildsACorruptIndex(t *testing.T) {
	p := filepath.Join(t.TempDir(), "examples.jsonl")
	e := ex("go", "package a", true)
	if _, err := Add(p, e); err != nil {
		t.Fatal(err)
	}
	for _, junk := range []string{"", "not a header at all", idxMagic + "xx"} {
		if err := os.WriteFile(indexPath(p), []byte(junk), 0o600); err != nil {
			t.Fatal(err)
		}
		added, err := Add(p, e)
		if err != nil || added {
			t.Fatalf("junk %q: added=%v err=%v, want false", junk, added, err)
		}
	}
	if got, _ := Load(p); len(got) != 1 {
		t.Fatalf("corpus has %d records, want 1", len(got))
	}
}

// Two hyctl processes verifying at once is ordinary. The lock is what stops the
// corpus and its sidecar diverging; without it both can append the same key.
func TestAddIsSerializedUnderConcurrency(t *testing.T) {
	p := filepath.Join(t.TempDir(), "examples.jsonl")
	e := ex("go", "package a", true)

	const n = 8
	var wg sync.WaitGroup
	results := make([]bool, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = Add(p, e)
		}(i)
	}
	wg.Wait()

	wrote := 0
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
		if results[i] {
			wrote++
		}
	}
	if wrote != 1 {
		t.Errorf("%d of %d concurrent Adds reported writing the same example, want exactly 1", wrote, n)
	}
	if got, _ := Load(p); len(got) != 1 {
		t.Errorf("corpus has %d records, want 1", len(got))
	}
	if h, sz := headerSize(t, p), fileSize(p); h != sz {
		t.Errorf("sidecar header says %d bytes, corpus is %d", h, sz)
	}
}

// Distinct examples all land, and the sidecar tracks the corpus exactly, which
// is what lets the next Add skip the rescan.
func TestAddKeepsSidecarInStepAcrossManyWrites(t *testing.T) {
	p := filepath.Join(t.TempDir(), "examples.jsonl")
	const n = 40
	for i := 0; i < n; i++ {
		added, err := Add(p, ex("go", fmt.Sprintf("package p%d", i), i%2 == 0))
		if err != nil || !added {
			t.Fatalf("add %d: added=%v err=%v", i, added, err)
		}
	}
	if got, _ := Load(p); len(got) != n {
		t.Fatalf("corpus has %d records, want %d", len(got), n)
	}
	if h, sz := headerSize(t, p), fileSize(p); h != sz {
		t.Fatalf("sidecar header says %d bytes, corpus is %d", h, sz)
	}
	// Every one of them must still read as a duplicate.
	for i := 0; i < n; i++ {
		if added, err := Add(p, ex("go", fmt.Sprintf("package p%d", i), i%2 == 0)); err != nil || added {
			t.Fatalf("re-add %d: added=%v err=%v, want false", i, added, err)
		}
	}
}

// A caller may set its own TaskHash, so the key is not a fixed width. Packing
// the sidecar as fixed-width records would corrupt it for exactly this input.
func TestAddHandlesACallerSuppliedTaskHash(t *testing.T) {
	p := filepath.Join(t.TempDir(), "examples.jsonl")
	e := ex("go", "package a", true)
	e.TaskHash = "a-task-hash-of-quite-a-different-length"
	if added, err := Add(p, e); err != nil || !added {
		t.Fatalf("added=%v err=%v", added, err)
	}
	if added, err := Add(p, e); err != nil || added {
		t.Fatalf("re-add: added=%v err=%v, want false", added, err)
	}
}

// A realistic candidate: oracle-verified examples are source files, and it is
// unmarshalling those that made the old rescan expensive.
var benchBody = strings.Repeat("\tif err != nil { return fmt.Errorf(\"wrap: %w\", err) }\n", 40)

func BenchmarkAddIntoCorpusOf(b *testing.B) {
	for _, n := range []int{0, 100, 500, 1000, 5000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			p := filepath.Join(b.TempDir(), "examples.jsonl")
			for i := 0; i < n; i++ {
				if _, err := Add(p, Example{
					Domain: "go", Source: "oracle:test",
					Candidate: fmt.Sprintf("// %d\n%s", i, benchBody),
				}); err != nil {
					b.Fatal(err)
				}
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := Add(p, Example{
					Domain: "go", Source: "oracle:test",
					Candidate: fmt.Sprintf("// bench %d %d\n%s", n, i, benchBody),
				}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
