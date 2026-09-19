// SPDX-License-Identifier: MIT

// Package cache answers a dispatch from a previous one's answer, and refuses
// far more often than it answers.
//
// A cache that guesses returns a confident answer to a question nobody asked,
// which is the one failure mode none of the surrounding machinery can catch:
// every other routing decision degrades to a worse head, this one degrades to
// the wrong answer. So an exact match on the normalized prompt is the only
// thing served without an argument, and every near match has to pass both a
// measured similarity and the content-token gate in match.go.
package cache

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/embed"
	"github.com/ankit373/hydra/internal/util"
)

// Dir is where the cache lives, beside the other opt-in stores.
func Dir() string { return filepath.Join(config.Dir(), "cache") }

// DefaultBudgetBytes bounds the store on disk. Answers are text, so this holds
// a few thousand of them; past it the oldest are dropped, which is the same
// posture internal/payload takes.
const DefaultBudgetBytes int64 = 32 << 20

// DefaultMaxEntries bounds the scan a near match costs. Every lookup compares
// against every stored vector, so the entry count is a latency budget as much
// as a space one: an exhaustive scan of a few hundred vectors is microseconds
// and needs no index to go wrong.
const DefaultMaxEntries = 512

// MaxResponseBytes refuses to store an answer larger than this. A cache of
// enormous answers evicts everything else to hold one of them.
const MaxResponseBytes = 256 << 10

// ErrNoResponse rejects an entry with nothing to serve. Storing one would make
// a later hit return an empty answer as though the head had produced it.
var ErrNoResponse = errors.New("cache: an entry needs a response")

// Entry is one answer, and enough about how it was produced to say so when it
// is served.
type Entry struct {
	Key      string `json:"key"`
	Prompt   string `json:"prompt"`
	Response string `json:"response"`
	Head     string `json:"head"`
	Model    string `json:"model,omitempty"`
	Enum     string `json:"enum,omitempty"`
	Domain   string `json:"domain,omitempty"`
	// CostUSD is what producing this answer cost. A hit avoids approximately
	// that, which is the only defensible reading of "spend avoided": what the
	// same work cost last time, not a guess at what it would cost now.
	CostUSD float64   `json:"cost_usd,omitempty"`
	TS      time.Time `json:"ts"`
	// Vec is the prompt's embedding, base64 little-endian float32. Absent when
	// no embedding model was available, which leaves the entry exact-match only
	// rather than unusable.
	Vec string `json:"vec,omitempty"`
}

// Hit is a served answer and the evidence for serving it.
type Hit struct {
	Entry
	// Similarity is 1 for an exact match on the normalized prompt, and the
	// measured cosine otherwise.
	Similarity float64
	Exact      bool
	Age        time.Duration
}

// Stats is what the store has done, persisted so a report reads a machine's
// history rather than one process's.
type Stats struct {
	Entries int   `json:"entries"`
	Bytes   int64 `json:"bytes"`
	Hits    int64 `json:"hits"`
	Exact   int64 `json:"exact"`
	Near    int64 `json:"near"`
	Misses  int64 `json:"misses"`
	// Refused counts prompts a similarity alone would have served and the
	// content-token gate stopped. It is the number that says whether the gate
	// is doing anything, so it is reported rather than folded into misses.
	Refused    int64      `json:"refused"`
	Evicted    int64      `json:"evicted"`
	AvoidedUSD float64    `json:"avoided_usd"`
	Oldest     *time.Time `json:"oldest,omitempty"`
	Newest     *time.Time `json:"newest,omitempty"`
}

// counters is the persisted half of Stats: what cannot be recomputed from the
// entries on disk.
type counters struct {
	Hits       int64   `json:"hits"`
	Exact      int64   `json:"exact"`
	Near       int64   `json:"near"`
	Misses     int64   `json:"misses"`
	Refused    int64   `json:"refused"`
	Evicted    int64   `json:"evicted"`
	AvoidedUSD float64 `json:"avoided_usd"`
}

// Store is the bounded set of answers on this machine.
type Store struct {
	mu      sync.Mutex
	dir     string
	budget  int64
	maxN    int
	entries []Entry
	vecs    [][]float32
	terms   []map[string]bool
	byKey   map[string]int
	bytes   int64
	stats   counters
}

func entriesPath(dir string) string  { return filepath.Join(dir, "answers.jsonl") }
func countersPath(dir string) string { return filepath.Join(dir, "counters.json") }

// OpenDir loads the store, creating the directory if it is not there. A corrupt
// line is skipped rather than fatal: a cache that will not open would stop
// dispatches it exists to make cheaper.
func OpenDir(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, budget: DefaultBudgetBytes, maxN: DefaultMaxEntries, byKey: map[string]int{}}
	if err := s.load(); err != nil {
		return nil, err
	}
	s.stats = readCounters(dir)
	return s, nil
}

// SetBudget bounds the store on disk. A non-positive budget keeps the default,
// so a misread config cannot turn the bound off.
func (s *Store) SetBudget(b int64) {
	if b <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.budget = b
}

func (s *Store) load() error {
	raw, err := os.ReadFile(entriesPath(s.dir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	for {
		var e Entry
		if err := dec.Decode(&e); err != nil {
			break
		}
		if e.Key == "" || e.Response == "" {
			continue
		}
		s.addLoaded(e)
	}
	return nil
}

// addLoaded appends an entry and everything derived from it. A repeated key
// keeps the later one, which is what Put already means by storing again.
func (s *Store) addLoaded(e Entry) {
	if i, seen := s.byKey[e.Key]; seen {
		s.removeAt(i)
	}
	s.entries = append(s.entries, e)
	s.vecs = append(s.vecs, decodeVec(e.Vec))
	s.terms = append(s.terms, content(e.Prompt))
	s.byKey[e.Key] = len(s.entries) - 1
	s.bytes += entryBytes(e)
}

func (s *Store) removeAt(i int) {
	s.bytes -= entryBytes(s.entries[i])
	delete(s.byKey, s.entries[i].Key)
	s.entries = append(s.entries[:i], s.entries[i+1:]...)
	s.vecs = append(s.vecs[:i], s.vecs[i+1:]...)
	s.terms = append(s.terms[:i], s.terms[i+1:]...)
	for k, at := range s.byKey {
		if at > i {
			s.byKey[k] = at - 1
		}
	}
}

// Put stores an answer, replacing any earlier one for the same prompt.
func (s *Store) Put(e Entry) error {
	if e.Response == "" {
		return ErrNoResponse
	}
	if len(e.Response) > MaxResponseBytes {
		return fmt.Errorf("cache: response is %d bytes, over the %d limit", len(e.Response), MaxResponseBytes)
	}
	e.Prompt = Normalize(e.Prompt)
	e.Key = Key(e.Prompt)
	if e.TS.IsZero() {
		e.TS = time.Now().UTC()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	lock, err := util.Lock(util.LockPath(entriesPath(s.dir)))
	if err != nil {
		return err
	}
	defer lock.Unlock()

	s.addLoaded(e)
	if err := s.evictLocked(); err != nil {
		return err
	}
	return s.writeLocked()
}

// PutVec is Put with the prompt's embedding attached.
func (s *Store) PutVec(e Entry, vec []float32) error {
	e.Vec = encodeVec(vec)
	return s.Put(e)
}

// evictLocked drops the oldest entries until the store is inside both bounds.
// Oldest first, because the alternative is deciding which answer is worth
// keeping, and nothing here can measure that.
func (s *Store) evictLocked() error {
	for len(s.entries) > 0 && (s.bytes > s.budget || len(s.entries) > s.maxN) {
		s.removeAt(0)
		s.stats.Evicted++
	}
	return nil
}

func (s *Store) writeLocked() error {
	tmp, err := os.CreateTemp(s.dir, ".answers-*")
	if err != nil {
		return err
	}
	enc := json.NewEncoder(tmp)
	for _, e := range s.entries {
		if err := enc.Encode(e); err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			return err
		}
	}
	// Closed before the rename: Windows refuses to replace a file another
	// handle still holds, and a deferred close would still hold this one.
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), entriesPath(s.dir)); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return writeCounters(s.dir, s.stats)
}

// Outcome is what a lookup found, and is deliberately inert: nothing is
// counted until a caller says the answer was actually used. A preview has to
// be able to ask what would happen without the report afterwards claiming it
// did.
type Outcome struct {
	Hit   Hit
	Found bool
	// Refused marks a prompt the similarity alone would have served and the
	// content-token gate stopped. Counted separately because it is the only
	// evidence about whether that gate earns its place.
	Refused bool
}

// Lookup answers from the store, or reports that it would not.
//
// An exact match on the normalized prompt is served unconditionally: it is the
// same question, byte for byte, once whitespace and case-insensitive framing
// are gone. A near match has to clear both the similarity threshold and the
// content-token gate.
func (s *Store) Lookup(prompt string, vec []float32, threshold float64) Outcome {
	norm := Normalize(prompt)

	s.mu.Lock()
	defer s.mu.Unlock()

	if i, ok := s.byKey[Key(norm)]; ok {
		return Outcome{Hit: s.hitLocked(i, 1, true), Found: true}
	}
	best, sim := s.nearestLocked(vec, threshold)
	if best < 0 {
		return Outcome{}
	}
	if !sameQuestion(content(norm), s.terms[best]) {
		return Outcome{Refused: true}
	}
	return Outcome{Hit: s.hitLocked(best, sim, false), Found: true}
}

// Record folds an outcome into the persisted tallies. Separate from Lookup so
// that asking what would happen and saying that it did are two decisions.
func (s *Store) Record(o Outcome) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case o.Found:
		s.stats.Hits++
		if o.Hit.Exact {
			s.stats.Exact++
		} else {
			s.stats.Near++
		}
		s.stats.AvoidedUSD += o.Hit.CostUSD
	case o.Refused:
		s.stats.Refused++
		s.stats.Misses++
	default:
		s.stats.Misses++
	}
	_ = writeCounters(s.dir, s.stats)
}

// nearestLocked is the closest stored vector at or above threshold, or -1.
// Exhaustive because the entry count is bounded: an approximate index would
// add a second way to be wrong for a scan that costs microseconds.
func (s *Store) nearestLocked(vec []float32, threshold float64) (int, float64) {
	if len(vec) == 0 || threshold <= 0 {
		return -1, 0
	}
	best, bestSim := -1, threshold
	for i, stored := range s.vecs {
		if len(stored) != len(vec) {
			continue
		}
		if sim := embed.Cosine(stored, vec); sim >= bestSim {
			// >= so the first of two identical scores wins, which is the
			// older one: a tie must not depend on iteration luck.
			if sim > bestSim || best < 0 {
				best, bestSim = i, sim
			}
		}
	}
	if best < 0 {
		return -1, 0
	}
	return best, bestSim
}

func (s *Store) hitLocked(i int, sim float64, exact bool) Hit {
	e := s.entries[i]
	return Hit{Entry: e, Similarity: sim, Exact: exact, Age: time.Since(e.TS)}
}

// Stat is what the store holds and what it has done.
func (s *Store) Stat() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statLocked()
}

func (s *Store) statLocked() Stats {
	st := Stats{
		Entries: len(s.entries), Bytes: s.bytes,
		Hits: s.stats.Hits, Exact: s.stats.Exact, Near: s.stats.Near,
		Misses: s.stats.Misses, Refused: s.stats.Refused,
		Evicted: s.stats.Evicted, AvoidedUSD: s.stats.AvoidedUSD,
	}
	for _, e := range s.entries {
		if st.Oldest == nil || e.TS.Before(*st.Oldest) {
			ts := e.TS
			st.Oldest = &ts
		}
		if st.Newest == nil || e.TS.After(*st.Newest) {
			ts := e.TS
			st.Newest = &ts
		}
	}
	return st
}

// StoredStats reports a cache without opening it for writing, which is what a
// report needs. The second result is false when there is no cache at all, so
// "off" and "on but empty" stay distinguishable.
func StoredStats(dir string) (Stats, bool) {
	if _, err := os.Stat(entriesPath(dir)); err != nil {
		if c := readCounters(dir); c != (counters{}) {
			return Stats{Hits: c.Hits, Exact: c.Exact, Near: c.Near, Misses: c.Misses,
				Refused: c.Refused, Evicted: c.Evicted, AvoidedUSD: c.AvoidedUSD}, true
		}
		return Stats{}, false
	}
	s := &Store{dir: dir, budget: DefaultBudgetBytes, maxN: DefaultMaxEntries, byKey: map[string]int{}}
	if err := s.load(); err != nil {
		return Stats{}, false
	}
	s.stats = readCounters(dir)
	return s.statLocked(), true
}

func readCounters(dir string) counters {
	var c counters
	raw, err := os.ReadFile(countersPath(dir))
	if err != nil {
		return c
	}
	_ = json.Unmarshal(raw, &c)
	return c
}

func writeCounters(dir string, c counters) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".counters-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), countersPath(dir)); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

// entryBytes is what an entry costs on disk, near enough for a budget: the
// text plus the encoded vector, which is everything that scales with it.
func entryBytes(e Entry) int64 {
	return int64(len(e.Prompt) + len(e.Response) + len(e.Vec) + len(e.Head) + len(e.Model) + 64)
}

func encodeVec(vec []float32) string {
	if len(vec) == 0 {
		return ""
	}
	buf := make([]byte, 4*len(vec))
	for i, f := range vec {
		binary.LittleEndian.PutUint32(buf[4*i:], math.Float32bits(f))
	}
	return base64.StdEncoding.EncodeToString(buf)
}

func decodeVec(s string) []float32 {
	if s == "" {
		return nil
	}
	buf, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(buf)%4 != 0 {
		return nil
	}
	out := make([]float32, len(buf)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(buf[4*i:]))
	}
	return out
}
