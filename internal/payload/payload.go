// SPDX-License-Identifier: MIT

// Package payload is the opt-in store for prompt and response text.
//
// Payloads are ~79% of trace bytes, carry the least decision value per byte,
// and are the only trace class with real privacy risk, they are verbatim
// source code and prompts. So this is off by default, gated on PII, and treated
// as a cache rather than a record.
//
// Two measurements shaped the design. A loose content-addressed store is ~30x
// larger on disk than a packed one, because every small blob is charged a whole
// filesystem block; so blobs are addressed by hash but stored in packs. And
// compressing small blobs individually is weak (~2.2x) until a trained
// dictionary is supplied, which takes it to ~6.3x, the structure Hydra's
// prompts share is exactly what a dictionary captures.
package payload

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/klauspost/compress/zstd"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/policy"
	"github.com/ankit373/hydra/internal/util"
)

// MaxBlobBytes caps one stored payload. A store must not be able to fill a disk
// because a model was pointed at a huge file; past the cap the payload is
// refused and the caller records that it was, rather than silently truncating.
const MaxBlobBytes = 4 << 20

// DictSampleTarget is how many blobs are collected before a dictionary is
// trained. Fewer samples produce a dictionary that overfits the first few
// prompts and helps nothing afterwards.
const DictSampleTarget = 128

// ErrTooLarge reports a payload above MaxBlobBytes.
var ErrTooLarge = errors.New("payload: exceeds the size cap")

// ErrDisabled reports a store that has not been opted into.
var ErrDisabled = errors.New("payload: capture is disabled")

// ErrNotFound reports a hash with no stored content.
var ErrNotFound = errors.New("payload: no content for this hash")

// ErrBadKeepProb reports an inclusion probability outside (0,1]. Refused rather
// than defaulted: a stored payload whose admission probability is unknown is
// worse than one not stored at all, because it silently biases anything
// computed from the set.
var ErrBadKeepProb = errors.New("payload: keep probability must be in (0,1]")

// Dir is where packs, the index and the dictionary live.
func Dir() string { return filepath.Join(config.Dir(), "payloads") }

// DictPath is the trained dictionary for a store rooted at dir.
//
// Store-relative, not package-global: a frame written with a dictionary cannot
// be decoded without that exact dictionary, so a store that trains into one
// path and reads from another loses its contents irrecoverably.
func DictPath(dir string) string { return filepath.Join(dir, "trace.dict") }

func packPath(dir string) string  { return filepath.Join(dir, "blobs.pack") }
func indexPath(dir string) string { return filepath.Join(dir, "blobs.idx") }

// Entry locates one blob inside a pack.
type Entry struct {
	Hash string `json:"hash"`
	Off  int64  `json:"off"`
	Len  int64  `json:"len"`
	Raw  int64  `json:"raw"` // uncompressed size, for reporting only
	Dict bool   `json:"dict"`

	// PII records that the content matched a detector and was redacted before
	// it was written. PIITypes names what matched, which is the part worth
	// keeping, the kind of secret is a finding, the secret itself is a
	// liability.
	PII      bool     `json:"pii,omitempty"`
	PIITypes []string `json:"pii_types,omitempty"`

	// KeepProb is the probability this payload was admitted. Payloads are
	// sampled, and a sampled record without its inclusion probability cannot be
	// weighted back to the population, the same defect that inverted head
	// rankings in #605. Always written, never omitempty: an absent probability
	// is unusable and a zero one divides by zero.
	KeepProb float64 `json:"keep_prob"`
}

// Store is a content-addressed payload store. Safe for concurrent use.
type Store struct {
	mu    sync.Mutex
	dir   string
	dict  []byte
	index map[string]Entry
}

// Open prepares a store, loading any existing index and dictionary.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, index: map[string]Entry{}}
	if err := s.loadIndex(); err != nil {
		return nil, err
	}
	if raw, err := os.ReadFile(DictPath(dir)); err == nil {
		s.dict = raw
	}
	return s, nil
}

func (s *Store) loadIndex() error {
	f, err := os.Open(indexPath(s.dir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		s.index[e.Hash] = e
	}
	return sc.Err()
}

// Hash is the content address.
func Hash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:16])
}

// Put stores content and returns its hash. Storing the same content twice is
// free: the second call finds the hash and writes nothing, which is the whole
// point of addressing by content when one system prompt repeats across
// thousands of dispatches.
func (s *Store) Put(content string, keepProb float64) (string, error) {
	if len(content) > MaxBlobBytes {
		return "", ErrTooLarge
	}
	if !(keepProb > 0 && keepProb <= 1) { // positive test so NaN lands here
		return "", ErrBadKeepProb
	}
	// Redaction happens before hashing, so the content address names what was
	// actually stored. Hashing the raw text would leave a fingerprint of the
	// secret in the index, which is the thing being kept off disk.
	content, piiTypes := policy.Redact(content)
	h := Hash(content)

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.index[h]; ok {
		return h, nil
	}

	var buf strings.Builder
	opts := []zstd.EOption{zstd.WithEncoderLevel(zstd.SpeedBestCompression)}
	usedDict := false
	if len(s.dict) > 0 {
		opts = append(opts, zstd.WithEncoderDict(s.dict))
		usedDict = true
	}
	enc, err := zstd.NewWriter(&builderWriter{&buf}, opts...)
	if err != nil {
		return "", err
	}
	if _, err := enc.Write([]byte(content)); err != nil {
		enc.Close()
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	frame := []byte(buf.String())

	pack, err := os.OpenFile(packPath(s.dir), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	defer pack.Close()
	off, err := pack.Seek(0, io.SeekEnd)
	if err != nil {
		return "", err
	}
	if _, err := pack.Write(frame); err != nil {
		return "", err
	}
	e := Entry{Hash: h, Off: off, Len: int64(len(frame)), Raw: int64(len(content)),
		Dict: usedDict, PII: len(piiTypes) > 0, PIITypes: piiTypes, KeepProb: keepProb}

	idx, err := os.OpenFile(indexPath(s.dir), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	defer idx.Close()
	line, err := json.Marshal(e)
	if err != nil {
		return "", err
	}
	// Pack before index: a crash between them orphans bytes in the pack, which
	// is harmless. The reverse would leave an index entry pointing at nothing.
	if err := pack.Sync(); err != nil {
		return "", err
	}
	if _, err := fmt.Fprintln(idx, string(line)); err != nil {
		return "", err
	}
	s.index[h] = e
	return h, nil
}

// Get returns stored content by hash.
func (s *Store) Get(h string) (string, error) {
	s.mu.Lock()
	e, ok := s.index[h]
	dict := s.dict
	s.mu.Unlock()
	if !ok {
		return "", ErrNotFound
	}
	f, err := os.Open(packPath(s.dir))
	if err != nil {
		return "", err
	}
	defer f.Close()
	sec := io.NewSectionReader(f, e.Off, e.Len)

	var opts []zstd.DOption
	if e.Dict {
		if len(dict) == 0 {
			// The dictionary a frame was written with is required to read it.
			// Losing it is unrecoverable, so say so rather than return garbage.
			return "", fmt.Errorf("payload: blob %s needs the dictionary at %s, which is missing", h, DictPath(s.dir))
		}
		opts = append(opts, zstd.WithDecoderDicts(dict))
	}
	dec, err := zstd.NewReader(sec, opts...)
	if err != nil {
		return "", err
	}
	defer dec.Close()
	raw, err := io.ReadAll(dec)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// Has reports whether content is already stored, without decompressing it.
func (s *Store) Has(h string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.index[h]
	return ok
}

// Len reports how many distinct blobs are stored.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.index)
}

// Stats reports stored size against the raw bytes those blobs represent.
type Stats struct {
	Blobs     int   `json:"blobs"`
	RawBytes  int64 `json:"raw_bytes"`
	PackBytes int64 `json:"pack_bytes"`
	WithDict  int   `json:"with_dict"`
	WithPII   int   `json:"with_pii"`
	DiskBytes int64 `json:"disk_bytes"`
}

// Stat summarises the store.
func (s *Store) Stat() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	var st Stats
	st.Blobs = len(s.index)
	for _, e := range s.index {
		st.RawBytes += e.Raw
		st.PackBytes += e.Len
		if e.Dict {
			st.WithDict++
		}
		if e.PII {
			st.WithPII++
		}
	}
	// Blocks charged, not logical length: the packed-vs-loose difference this
	// design exists for is entirely in that gap.
	if info, err := os.Stat(packPath(s.dir)); err == nil {
		st.DiskBytes = util.DiskBytes(info)
	}
	if info, err := os.Stat(indexPath(s.dir)); err == nil {
		st.DiskBytes += util.DiskBytes(info)
	}
	return st
}

// TrainDict builds a dictionary from the store's own content and saves it.
//
// Trained from the user's corpus rather than shipped: the gain comes from the
// structure *these* prompts share, and a generic dictionary captures none of
// it. Existing blobs keep their frames, each records whether it used a
// dictionary, so old and new coexist without a migration.
func (s *Store) TrainDict(maxDictBytes int) error {
	s.mu.Lock()
	hashes := make([]string, 0, len(s.index))
	for h := range s.index {
		hashes = append(hashes, h)
	}
	s.mu.Unlock()

	if len(hashes) < DictSampleTarget {
		return fmt.Errorf("payload: need %d blobs to train a dictionary, have %d", DictSampleTarget, len(hashes))
	}
	samples := make([][]byte, 0, len(hashes))
	for _, h := range hashes {
		content, err := s.Get(h)
		if err != nil {
			continue
		}
		samples = append(samples, []byte(content))
	}
	if len(samples) == 0 {
		return errors.New("payload: no readable samples to train from")
	}
	// History seeds the dictionary's back-reference window and is what bounds
	// its size; klauspost takes no explicit size option.
	history := make([]byte, 0, maxDictBytes)
	for _, sample := range samples {
		if len(history)+len(sample) > maxDictBytes {
			break
		}
		history = append(history, sample...)
	}
	dict, err := buildDict(samples, history)
	if err != nil {
		return err
	}
	if err := os.WriteFile(DictPath(s.dir), dict, 0o600); err != nil {
		return err
	}
	s.mu.Lock()
	s.dict = dict
	s.mu.Unlock()
	return nil
}

// builderWriter adapts strings.Builder to io.Writer.
type builderWriter struct{ b *strings.Builder }

func (w *builderWriter) Write(p []byte) (int, error) { return w.b.Write(p) }

// ErrDegenerateCorpus reports a corpus the dictionary builder cannot model.
var ErrDegenerateCorpus = errors.New("payload: corpus is too uniform to train a dictionary from")

// buildDict wraps zstd.BuildDict, which panics with an integer divide by zero
// on a corpus that compresses to no literals at all.
//
// That is not a hypothetical input here, it is the expected one. Hydra's
// payloads are dominated by one system prompt repeated across thousands of
// dispatches, so a young store's samples are near-identical and every byte
// after the first is a back-reference. Training is a background convenience;
// it must never be able to take down the process that was doing the routing.
func buildDict(samples [][]byte, history []byte) (dict []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			dict, err = nil, fmt.Errorf("%w (%v)", ErrDegenerateCorpus, r)
		}
	}()
	return zstd.BuildDict(zstd.BuildDictOptions{ID: 1, Contents: samples, History: history})
}
