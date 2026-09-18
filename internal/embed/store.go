// SPDX-License-Identifier: MIT

package embed

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/util"
)

// spanIDLen is the width of a stored span id. Held as the 16 raw ASCII bytes
// runlog prints rather than the 8 it encodes, so this package never has to
// agree with runlog about the encoding, only about the width.
const spanIDLen = 16

// tsLen is the record's timestamp, unix nanoseconds. Kept per record because
// eviction is oldest-first and a store that cannot say what is oldest cannot
// evict honestly.
const tsLen = 8

// DefaultBudgetBytes bounds the store. A 768-dim vector costs 3 KB, so this
// holds roughly 21,000 dispatches, which is more history than any of the
// features reading it need.
const DefaultBudgetBytes int64 = 64 << 20

// ErrDimMismatch reports a vector whose width is not the store's.
var ErrDimMismatch = errors.New("embed: vector dimension does not match the store")

// ErrBadSpanID reports a span id that is not exactly spanIDLen bytes. Refused
// rather than padded: a padded id never matches the span it came from, so the
// vector is unreachable and the store silently grows with records nobody reads.
var ErrBadSpanID = errors.New("embed: span id must be 16 characters")

// Dir is where vectors and their metadata live.
func Dir() string { return filepath.Join(config.Dir(), "embeddings") }

// meta is the store's identity. The model and dimension are part of the key:
// vectors from nomic-embed-text and mxbai-embed-large are not comparable, and
// mixing them returns a confident wrong neighbour rather than no neighbour.
type meta struct {
	V     int    `json:"v"`
	Model string `json:"model"`
	Dim   int    `json:"dim"`
}

// Stats reports what the store holds.
type Stats struct {
	Model  string `json:"model"`
	Dim    int    `json:"dim"`
	Count  int    `json:"count"`
	Bytes  int64  `json:"bytes"`
	Budget int64  `json:"budget"`
	// Pointers because omitempty does nothing for a struct: a store with no
	// vectors otherwise reports a timestamp of year 1 as though it were a
	// reading.
	Oldest *time.Time `json:"oldest,omitempty"`
	Newest *time.Time `json:"newest,omitempty"`
}

// Store is a bounded, fixed-width store of vectors keyed by span id.
//
// Records are fixed width because the model fixes the dimension, which makes
// eviction a single contiguous copy of the newest tail rather than a rewrite
// that has to parse what it keeps.
type Store struct {
	mu     sync.Mutex
	dir    string
	model  string
	dim    int
	budget int64

	// offsets maps span id to its record offset, so a repeated dispatch stores
	// one vector rather than two.
	offsets map[string]int64
	size    int64
	oldest  time.Time
	newest  time.Time
}

func metaPath(dir string) string { return filepath.Join(dir, "meta.json") }
func dataPath(dir string) string { return filepath.Join(dir, "vectors.dat") }

// OpenStore prepares a store for one model, resetting it if the model changed.
//
// A model change invalidates rather than appends: the old vectors are not wrong,
// they are unanswerable, and keeping them would let a query compare across two
// spaces without any surface saying so.
func OpenStore(dir, model string) (*Store, error) {
	if model == "" {
		return nil, ErrUnavailable
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, model: model, budget: DefaultBudgetBytes, offsets: map[string]int64{}}

	m, err := readMeta(dir)
	switch {
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return nil, err
	case err == nil && m.Model == model:
		s.dim = m.Dim
	case err == nil:
		// Different model. Drop the data before adopting the new identity, or a
		// crash between the two leaves records that meta.json says are readable.
		if err := os.Remove(dataPath(dir)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	if err := writeMeta(dir, meta{V: 1, Model: model, Dim: s.dim}); err != nil {
		return nil, err
	}
	if err := s.loadIndex(); err != nil {
		return nil, err
	}
	return s, nil
}

func readMeta(dir string) (meta, error) {
	b, err := os.ReadFile(metaPath(dir))
	if err != nil {
		return meta{}, err
	}
	var m meta
	err = json.Unmarshal(b, &m)
	return m, err
}

func writeMeta(dir string, m meta) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp := metaPath(dir) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, metaPath(dir))
}

// SetBudget bounds the store in bytes. Zero or less disables eviction, which is
// only sensible for a test that wants the store to grow freely.
func (s *Store) SetBudget(b int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.budget = b
}

// recordSize is the on-disk width of one record, zero until the dimension is known.
func (s *Store) recordSize() int64 {
	if s.dim == 0 {
		return 0
	}
	return spanIDLen + tsLen + int64(s.dim)*4
}

// Model and Dim report the store's identity.
func (s *Store) Model() string { return s.model }
func (s *Store) Dim() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dim
}

// loadIndex scans the data file, recording where each span's vector sits.
//
// A trailing partial record is truncated rather than read: an append that was
// interrupted leaves one, and treating its bytes as a vector would invent a
// neighbour out of whatever the crash left behind.
func (s *Store) loadIndex() error {
	rs := s.recordSize()
	if rs == 0 {
		return nil
	}
	f, err := os.Open(dataPath(s.dir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return err
	}
	whole := (fi.Size() / rs) * rs
	if whole != fi.Size() {
		if err := os.Truncate(dataPath(s.dir), whole); err != nil {
			return err
		}
	}

	hdr := make([]byte, spanIDLen+tsLen)
	for off := int64(0); off < whole; off += rs {
		if _, err := f.ReadAt(hdr, off); err != nil {
			return err
		}
		s.offsets[string(hdr[:spanIDLen])] = off
		s.observe(time.Unix(0, int64(binary.LittleEndian.Uint64(hdr[spanIDLen:]))))
	}
	s.size = whole
	return nil
}

func (s *Store) observe(t time.Time) {
	if s.oldest.IsZero() || t.Before(s.oldest) {
		s.oldest = t
	}
	if t.After(s.newest) {
		s.newest = t
	}
}

// Put stores one vector. A span already stored is left alone: the same text and
// model produce the same vector, so a second write would cost bytes to record
// what is already there.
func (s *Store) Put(spanID string, vec []float32) error {
	if len(spanID) != spanIDLen {
		return fmt.Errorf("%w: got %d", ErrBadSpanID, len(spanID))
	}
	if len(vec) == 0 {
		return ErrDimMismatch
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.dim == 0 {
		s.dim = len(vec)
		if err := writeMeta(s.dir, meta{V: 1, Model: s.model, Dim: s.dim}); err != nil {
			return err
		}
	}
	if len(vec) != s.dim {
		return fmt.Errorf("%w: store is %d, vector is %d", ErrDimMismatch, s.dim, len(vec))
	}
	if _, ok := s.offsets[spanID]; ok {
		return nil
	}

	// Cross-process: two hyctl runs appending at once would otherwise interleave
	// halves of two records, the defect internal/payload takes the same lock for.
	lk, err := util.Lock(util.LockPath(dataPath(s.dir)))
	if err != nil {
		return err
	}
	defer lk.Unlock()

	now := time.Now()
	rec := make([]byte, s.recordSize())
	copy(rec, spanID)
	binary.LittleEndian.PutUint64(rec[spanIDLen:], uint64(now.UnixNano()))
	for i, v := range vec {
		binary.LittleEndian.PutUint32(rec[spanIDLen+tsLen+i*4:], math.Float32bits(v))
	}

	f, err := os.OpenFile(dataPath(s.dir), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	off, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(rec); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	s.offsets[spanID] = off
	s.size = off + int64(len(rec))
	s.observe(now)
	return s.evictLocked()
}

// evictLocked drops the oldest records until the store fits its budget.
//
// Records are append-ordered and fixed width, so the newest that fit are one
// contiguous range, copied to a temp file and renamed. Nothing is parsed, so
// eviction cannot corrupt what it keeps.
func (s *Store) evictLocked() error {
	rs := s.recordSize()
	if s.budget <= 0 || rs == 0 || s.size <= s.budget {
		return nil
	}
	keep := (s.budget / rs) * rs
	if keep <= 0 {
		// The budget cannot hold one record. Emptying is the honest outcome:
		// keeping one would report a budget the store does not honour.
		if err := os.Remove(dataPath(s.dir)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		s.offsets, s.size = map[string]int64{}, 0
		s.oldest, s.newest = time.Time{}, time.Time{}
		return nil
	}

	tmp := dataPath(s.dir) + ".tmp"
	if err := copyTail(dataPath(s.dir), tmp, s.size-keep, keep); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dataPath(s.dir)); err != nil {
		os.Remove(tmp)
		return err
	}

	s.offsets, s.size = map[string]int64{}, 0
	s.oldest, s.newest = time.Time{}, time.Time{}
	return s.loadIndex()
}

// copyTail writes n bytes of src from off into dst, closing both handles before
// it returns.
//
// A function of its own so the read handle cannot outlive the copy: Windows
// refuses to rename over a path that still has one open, which is not a
// hypothetical, it failed exactly this way in CI while Unix was green.
func copyTail(src, dst string, off, n int64) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, io.NewSectionReader(in, off, n)); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Get returns the vector stored for a span.
func (s *Store) Get(spanID string) ([]float32, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	off, ok := s.offsets[spanID]
	if !ok || s.dim == 0 {
		return nil, false
	}
	f, err := os.Open(dataPath(s.dir))
	if err != nil {
		return nil, false
	}
	defer f.Close()

	buf := make([]byte, int64(s.dim)*4)
	if _, err := f.ReadAt(buf, off+spanIDLen+tsLen); err != nil {
		return nil, false
	}
	return decodeVec(buf, s.dim), true
}

func decodeVec(buf []byte, dim int) []float32 {
	v := make([]float32, dim)
	for i := 0; i < dim; i++ {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(buf[i*4:]))
	}
	return v
}

// Stat reports what the store holds.
func (s *Store) Stat() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Stats{
		Model:  s.model,
		Dim:    s.dim,
		Count:  len(s.offsets),
		Bytes:  s.size,
		Budget: s.budget,
		Oldest: nonZero(s.oldest),
		Newest: nonZero(s.newest),
	}
}

func nonZero(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// StoredStats reads a store's identity and size without opening it for writing.
//
// For surfaces that only want to report: `hyctl security` must be able to say
// whether vectors exist without creating the directory that would make its own
// answer true.
func StoredStats(dir string) (Stats, bool) {
	m, err := readMeta(dir)
	if err != nil || m.Dim == 0 {
		return Stats{}, false
	}
	fi, err := os.Stat(dataPath(dir))
	if err != nil {
		return Stats{Model: m.Model, Dim: m.Dim}, true
	}
	rs := int64(spanIDLen + tsLen + m.Dim*4)
	return Stats{Model: m.Model, Dim: m.Dim, Count: int(fi.Size() / rs), Bytes: fi.Size()}, true
}
