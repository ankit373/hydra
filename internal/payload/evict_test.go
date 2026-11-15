// SPDX-License-Identifier: MIT

package payload

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"testing"
)

// fill writes n distinct blobs of roughly size bytes each. Content is drawn
// from real source so it compresses like real payloads rather than to nothing.
func fill(t *testing.T, s *Store, n, size int) []string {
	t.Helper()
	return fillFrom(t, s, 0, n, size)
}

// fillFrom is fill with blob numbers starting at first, so a second call
// writes new content rather than deduplicating against the first.
func fillFrom(t *testing.T, s *Store, first, n, size int) []string {
	t.Helper()
	body := strings.Join(corpus(t), "\n")
	refs := make([]string, 0, n)
	for i := first; i < first+n; i++ {
		start := (i * 7919) % (len(body) - size)
		content := fmt.Sprintf("// blob %d\n", i) + body[start:start+size]
		h, err := s.Put(content, 1)
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, h)
	}
	return refs
}

func TestEviction_KeepsTheStoreInsideItsBudget(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Small enough that several packs are forced, so eviction has whole packs
	// to drop rather than only the one being written.
	s.SetBudget(64 << 10)
	fill(t, s, 60, 8<<10)

	// Budget plus one pack: eviction runs after the write, so the store is
	// allowed to be one pack over before the next drop.
	st := s.Stat()
	if limit := int64(64<<10) + s.packLimit(); st.PackBytes > limit {
		t.Fatalf("store holds %d B, over the %d B budget by more than one pack", st.PackBytes, 64<<10)
	}
	if st.Blobs == 0 {
		t.Fatal("eviction emptied the store entirely")
	}
	t.Logf("%d blobs, %d packs, %d B held against a %d B budget",
		st.Blobs, st.Packs, st.PackBytes, 64<<10)
}

// Oldest first, so the run you are looking at now is the one that survives.
func TestEviction_DropsTheOldestPackFirst(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.SetBudget(48 << 10)
	refs := fill(t, s, 60, 8<<10)

	var firstAlive, lastAlive bool
	if _, err := s.Get(refs[0]); err == nil {
		firstAlive = true
	}
	if _, err := s.Get(refs[len(refs)-1]); err == nil {
		lastAlive = true
	}
	if !lastAlive {
		t.Fatal("the most recent payload was evicted; eviction is not oldest-first")
	}
	if firstAlive {
		t.Fatal("nothing was evicted, so this proves nothing about the order")
	}
}

// A ref whose pack is gone must read as not-found. Returning what survived
// would present half a prompt as the whole one.
func TestEviction_EvictedRefReadsAsNotFound(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.SetBudget(48 << 10)
	refs := fill(t, s, 60, 8<<10)

	if _, err := s.Get(refs[0]); err != ErrNotFound {
		t.Fatalf("evicted ref returned %v, want ErrNotFound", err)
	}
	if _, err := s.Load(refs[0]); err != ErrNotFound {
		t.Fatalf("Load of an evicted ref returned %v, want ErrNotFound", err)
	}
}

// The index must not outlive the packs it points at, or a reopened store
// promises blobs that are no longer on disk.
func TestEviction_LeavesTheIndexConsistentWithDisk(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.SetBudget(48 << 10)
	fill(t, s, 60, 8<<10)

	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Len() != s.Len() {
		t.Fatalf("reopened store has %d blobs, the live one has %d", reopened.Len(), s.Len())
	}
	for h := range reopened.index {
		if _, err := reopened.Get(h); err != nil {
			t.Fatalf("index names blob %s, which cannot be read: %v", h, err)
		}
	}
	// And no pack file is left behind with no entries pointing into it.
	nums, err := packNumbers(dir)
	if err != nil {
		t.Fatal(err)
	}
	live := map[int]bool{}
	for _, e := range reopened.index {
		live[e.Pack] = true
	}
	for _, n := range nums {
		if !live[n] {
			info, _ := os.Stat(packPath(dir, n))
			t.Fatalf("pack %d survives with no index entries (%d B orphaned)", n, info.Size())
		}
	}
}

// Zero budget means unbounded, which only a test should want. It must not read
// as "evict everything".
func TestEviction_ZeroBudgetKeepsEverything(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.SetBudget(0)
	refs := fill(t, s, 20, 8<<10)
	for _, h := range refs {
		if _, err := s.Get(h); err != nil {
			t.Fatalf("blob %s was dropped under an unbounded budget: %v", h, err)
		}
	}
}

// Two Stores over one directory stand in for two `hyctl` processes: each has
// its own mutex, so only the cross-process lock keeps their pack offsets from
// interleaving. Without it the index records offsets into the other's frames
// and every read after the collision decodes garbage.
func TestConcurrentStores_DoNotCorruptEachOthersOffsets(t *testing.T) {
	dir := t.TempDir()
	body := strings.Join(corpus(t), "\n")

	const writers, each = 4, 12
	refs := make(chan string, writers*each)
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			s, err := Open(dir) // a separate Store, as a separate process would have
			if err != nil {
				t.Error(err)
				return
			}
			for i := 0; i < each; i++ {
				start := ((w*each + i) * 4093) % (len(body) - 4096)
				content := fmt.Sprintf("// writer %d blob %d\n", w, i) + body[start:start+4096]
				h, err := s.Put(content, 1)
				if err != nil {
					t.Errorf("writer %d: %v", w, err)
					return
				}
				refs <- h
			}
		}(w)
	}
	wg.Wait()
	close(refs)

	reader, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for h := range refs {
		got, err := reader.Get(h)
		if err != nil {
			t.Fatalf("blob %s written by a concurrent store is unreadable: %v", h, err)
		}
		if Hash(got) != h {
			t.Fatalf("blob %s decoded to different content; offsets interleaved", h)
		}
		n++
	}
	if n != writers*each {
		t.Fatalf("read back %d blobs, wrote %d", n, writers*each)
	}
}

func pinTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.SetBudget(48 << 10)
	return s, dir
}

func TestEvict_SkipsPinnedPack(t *testing.T) {
	s, _ := pinTestStore(t)
	first := fill(t, s, 1, 8<<10)
	if err := s.Pin("pack-a", first); err != nil {
		t.Fatal(err)
	}
	refs := fillFrom(t, s, 1, 59, 8<<10)
	if _, err := s.Get(first[0]); err != nil {
		t.Fatalf("pinned blob was evicted: %v", err)
	}
	evicted := 0
	for _, h := range refs[:10] {
		if _, err := s.Get(h); err == ErrNotFound {
			evicted++
		}
	}
	if evicted == 0 {
		t.Fatal("every old unpinned blob survived, so eviction never ran")
	}
}

// Pinning a manifest alone would keep the list of chunk names and lose the text.
func TestEvict_PinnedManifestKeepsItsChunks(t *testing.T) {
	s, _ := pinTestStore(t)
	body := strings.Join(corpus(t), "\n")[:16<<10]
	ref, err := s.PutSegments([]Segment{{Label: "user", Content: body}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	want, err := s.Load(ref)
	if err != nil {
		t.Fatal(err)
	}
	hashes, err := s.expand([]string{ref})
	if err != nil {
		t.Fatal(err)
	}
	spread := false
	for _, h := range hashes {
		spread = spread || s.index[h].Pack != s.index[ref].Pack
	}
	if !spread {
		t.Fatal("every chunk shares the manifest's pack, so this proves nothing")
	}
	if err := s.Pin("pack-a", []string{ref}); err != nil {
		t.Fatal(err)
	}
	fill(t, s, 60, 8<<10)
	got, err := s.Load(ref)
	if err != nil {
		t.Fatalf("pinned manifest lost its chunks: %v", err)
	}
	if got != want {
		t.Fatal("pinned manifest loads different text")
	}
}

func TestEvict_StopsWhenOnlyPinnedPacksRemain(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	s, _ := pinTestStore(t)
	var refs []string
	for i := 0; i < 30; i++ {
		refs = append(refs, fillFrom(t, s, i, 1, 8<<10)...)
		if err := s.Pin("all", refs); err != nil {
			t.Fatal(err)
		}
	}
	for _, h := range refs {
		if _, err := s.Get(h); err != nil {
			t.Fatalf("pinned blob %s was evicted: %v", h, err)
		}
	}
	st := s.Stat()
	if st.PackBytes <= 48<<10 || st.PinnedBytes != st.PackBytes {
		t.Fatalf("pack %d B, pinned %d B: want every byte pinned and over budget", st.PackBytes, st.PinnedBytes)
	}
	if n := strings.Count(logs.String(), "held by pinned packs"); n != 1 {
		t.Fatalf("warned %d times, want once:\n%s", n, logs.String())
	}
}

func TestPin_SurvivesReopen(t *testing.T) {
	s, dir := pinTestStore(t)
	first := fill(t, s, 1, 8<<10)
	if err := s.Pin("pack-a", first); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	reopened.SetBudget(48 << 10)
	fillFrom(t, reopened, 1, 59, 8<<10)
	if _, err := reopened.Get(first[0]); err != nil {
		t.Fatalf("pin was lost on reopen: %v", err)
	}
}

func TestUnpin_MakesPackEvictableAgain(t *testing.T) {
	s, _ := pinTestStore(t)
	first := fill(t, s, 1, 8<<10)
	if err := s.Pin("pack-a", first); err != nil {
		t.Fatal(err)
	}
	fillFrom(t, s, 1, 30, 8<<10)
	if err := s.Unpin("pack-a"); err != nil {
		t.Fatal(err)
	}
	fillFrom(t, s, 31, 30, 8<<10)
	if _, err := s.Get(first[0]); err != ErrNotFound {
		t.Fatalf("unpinned blob still readable (%v)", err)
	}
	if st := s.Stat(); st.PinnedBytes != 0 {
		t.Fatalf("%d B still reported pinned after unpin", st.PinnedBytes)
	}
}

// Pinning a ref that is already gone must fail, not promise text that is lost.
func TestPin_RefusesUnknownRef(t *testing.T) {
	s, _ := pinTestStore(t)
	if err := s.Pin("pack-a", []string{Hash("never stored")}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}
