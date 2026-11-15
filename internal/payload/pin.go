// SPDX-License-Identifier: MIT

package payload

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/ankit373/hydra/internal/util"
)

func pinsPath(dir string) string { return filepath.Join(dir, "pins.json") }

// Pin keeps refs readable until owner unpins them: eviction skips any pack
// holding one. It replaces owner's earlier pins. A manifest pins its chunks too.
func (s *Store) Pin(owner string, refs []string) error {
	if owner == "" {
		return fmt.Errorf("payload: pin needs an owner")
	}
	hashes, err := s.expand(refs)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, err := util.Lock(util.LockPath(indexPath(s.dir)))
	if err != nil {
		return err
	}
	defer lock.Unlock()
	// Another process may have evicted a ref since expand read it.
	if err := s.reopenIndexLocked(); err != nil {
		return err
	}
	for _, h := range hashes {
		if _, ok := s.index[h]; !ok {
			return fmt.Errorf("%w: %s", ErrNotFound, h)
		}
	}
	s.pins[owner] = hashes
	return s.writePinsLocked()
}

// Unpin drops everything owner pinned. Unknown owners are not an error.
func (s *Store) Unpin(owner string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, err := util.Lock(util.LockPath(indexPath(s.dir)))
	if err != nil {
		return err
	}
	defer lock.Unlock()
	if err := s.reopenIndexLocked(); err != nil {
		return err
	}
	if _, ok := s.pins[owner]; !ok {
		return nil
	}
	delete(s.pins, owner)
	return s.writePinsLocked()
}

// expand adds the chunks each manifest ref lists, so pinning a payload keeps
// its text, not just the list of chunk names.
func (s *Store) expand(refs []string) ([]string, error) {
	seen := map[string]bool{}
	for _, ref := range refs {
		seen[ref] = true
		s.mu.Lock()
		e, ok := s.index[ref]
		s.mu.Unlock()
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, ref)
		}
		if !e.Manifest {
			continue
		}
		raw, err := s.Get(ref)
		if err != nil {
			return nil, err
		}
		m, err := decodeManifest(raw)
		if err != nil {
			return nil, fmt.Errorf("payload: manifest %s is unreadable: %w", ref, err)
		}
		for _, part := range m.Parts {
			for _, h := range part.Chunks {
				seen[h] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for h := range seen {
		out = append(out, h)
	}
	sort.Strings(out)
	return out, nil
}

// pinnedLocked is the set of hashes some owner pinned. The caller holds s.mu.
func (s *Store) pinnedLocked() map[string]bool {
	out := map[string]bool{}
	for _, hs := range s.pins {
		for _, h := range hs {
			out[h] = true
		}
	}
	return out
}

func (s *Store) loadPins() error {
	s.pins = map[string][]string{}
	raw, err := os.ReadFile(pinsPath(s.dir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	// A corrupt pin file is an error, not an empty set: reading it as empty
	// would let the next eviction delete what a pack still points to.
	if err := json.Unmarshal(raw, &s.pins); err != nil {
		return fmt.Errorf("payload: %s is unreadable: %w", pinsPath(s.dir), err)
	}
	if s.pins == nil { // the file held JSON null
		s.pins = map[string][]string{}
	}
	return nil
}

// writePinsLocked is temp-then-rename, like rewriteIndexLocked, so a crash
// leaves the old pins rather than none.
func (s *Store) writePinsLocked() error {
	raw, err := json.Marshal(s.pins)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, "pins-*.json.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), pinsPath(s.dir))
}
