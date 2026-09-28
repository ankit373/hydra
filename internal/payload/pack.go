// SPDX-License-Identifier: MIT

package payload

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// packNumbers lists the store's pack files, oldest first. Pack numbers ascend
// with age, so the ordering is also the eviction order.
func packNumbers(dir string) ([]int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []int
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "blobs-") || !strings.HasSuffix(name, ".pack") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "blobs-"), ".pack"))
		if err != nil {
			continue
		}
		out = append(out, n)
	}
	sort.Ints(out)
	return out, nil
}

// newestPack is the pack new blobs are appended to, 1 for an empty store.
func newestPack(dir string) (int, error) {
	nums, err := packNumbers(dir)
	if err != nil {
		return 0, err
	}
	if len(nums) == 0 {
		return 1, nil
	}
	return nums[len(nums)-1], nil
}

// packLimit is how large one pack grows before the next is started.
//
// Derived from the budget rather than fixed, so eviction granularity always
// scales with it. A fixed size larger than the budget can never evict at all,
// because the only pack is the one being written and that one is never
// dropped: the store then grows without bound past a limit it reports honouring.
func (s *Store) packLimit() int64 {
	if s.budget <= 0 {
		return MaxPackBytes
	}
	limit := s.budget / packsPerBudget
	if limit < minPackBytes {
		limit = minPackBytes
	}
	if limit > MaxPackBytes {
		limit = MaxPackBytes
	}
	return limit
}

// rotateIfFull starts a new pack when the current one has no room for frame.
// Rotation is what makes eviction a file delete rather than a compaction: a
// pack is append-only, so reclaiming space inside one would mean rewriting it.
func (s *Store) rotateIfFull(frameLen int64) error {
	info, err := os.Stat(packPath(s.dir, s.pack))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Size()+frameLen > s.packLimit() {
		s.pack++
	}
	return nil
}

// evictLocked drops whole packs, oldest first, until the store is inside its
// budget. The caller holds the lock.
//
// Deterministic by age, not random: sampling would need every survivor to carry
// an inclusion probability, and a trace you can open only one time in ten is
// not the detail this store exists to provide. Age eviction does bias any
// analysis over payload text toward recent runs, which is why cost, rollup and
// evalset, the things that must stay correctable to the population, are not
// stored here.
func (s *Store) evictLocked() error {
	if s.budget <= 0 {
		return nil
	}
	total := int64(0)
	for _, e := range s.index {
		total += e.Len
	}
	if total <= s.budget {
		return nil
	}
	nums, err := packNumbers(s.dir)
	if err != nil {
		return err
	}
	for _, n := range nums {
		if total <= s.budget {
			break
		}
		// Never evict the pack still being written: it holds the payload that
		// was just stored, and dropping it would make a fresh run unreadable.
		if n == s.pack {
			continue
		}
		for h, e := range s.index {
			if e.Pack == n {
				total -= e.Len
				delete(s.index, h)
			}
		}
		if err := os.Remove(packPath(s.dir, n)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return s.rewriteIndexLocked()
}

// rewriteIndexLocked replaces the append-only index with what survived
// eviction. Temp-then-rename, so an interrupted rewrite leaves the old index
// rather than a truncated one.
func (s *Store) rewriteIndexLocked() error {
	tmp, err := os.CreateTemp(s.dir, "blobs-*.idx.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	for _, e := range s.index {
		line, err := json.Marshal(e)
		if err != nil {
			tmp.Close()
			return err
		}
		if _, err := fmt.Fprintln(tmp, string(line)); err != nil {
			tmp.Close()
			return err
		}
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(s.dir, "blobs.idx"))
}

// reopenIndexLocked refreshes the in-memory index from disk. Called while
// holding the cross-process lock, because another `hyctl` may have added blobs
// or evicted packs since this store was opened.
func (s *Store) reopenIndexLocked() error {
	s.index = map[string]Entry{}
	if err := s.loadIndex(); err != nil {
		return err
	}
	p, err := newestPack(s.dir)
	if err != nil {
		return err
	}
	s.pack = p
	return nil
}

// appendIndexLine appends one entry and closes the file before returning.
//
// Closed rather than deferred: eviction renames a fresh index over this path,
// and Windows refuses to rename over a file that is still open ("Access is
// denied"). On Unix the deferred close was harmless, which is exactly why this
// only showed up on the Windows leg of CI.
func appendIndexLine(path, line string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(f, line); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
