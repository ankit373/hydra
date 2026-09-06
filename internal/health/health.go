// SPDX-License-Identifier: MIT

// Package health keeps heads that cannot serve out of rotation.
//
// Two questions, deliberately separate. Whether a head could ever work is a
// precondition (executor.Unroutable); whether one that used to work still does
// is this package, and the only honest answer to the second is "try it again
// later", so a failure parks a head for a while rather than condemning it.
package health

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/provider"
)

// How long a head sits out. The first failure is cheap to retry, a head that
// keeps failing is not, so the wait doubles up to a ceiling that still lets a
// fixed head return without anyone restarting anything.
const (
	softFailuresBeforeOpen = 2
	baseCooldown           = 1 * time.Minute
	maxCooldown            = 30 * time.Minute
)

// Kind separates a failure that might not recur from one that certainly will
// until something changes on the machine.
type Kind int

const (
	// Transient is a timeout, a 5xx, a dropped connection.
	Transient Kind = iota
	// Fatal is a missing binary or a model the provider does not have. Retrying
	// it in a second only produces the same error, so it opens on first sight.
	Fatal
)

type entry struct {
	Reason   string    `json:"reason"`
	Kind     Kind      `json:"kind"`
	Failures int       `json:"failures"`
	LastFail time.Time `json:"lastFail"`
	RetryAt  time.Time `json:"retryAt"`
}

// Store is the per-head breaker state, shared between the CLI and the app
// through one file.
type Store struct {
	path  string
	mu    sync.Mutex
	heads map[string]*entry
	dirty bool
}

// DefaultPath is the shared breaker file, beside the run logs.
func DefaultPath() string { return filepath.Join(config.Dir(), "logs", "health.json") }

// Open reads the store, and returns an empty one when it cannot.
//
// Unlike internal/pending this never fails loudly: breaker state is a cache of
// what was observed, not work someone is waiting on. Refusing to dispatch
// because a cache file is corrupt would turn a bad file into a total outage,
// and starting empty only costs one retry of a head that is genuinely down.
func Open(path string) *Store {
	s := &Store{path: path, heads: map[string]*entry{}}
	raw, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	var heads map[string]*entry
	if err := json.Unmarshal(raw, &heads); err != nil || heads == nil {
		return s
	}
	s.heads = heads
	return s
}

// Blocked reports whether a head is parked, and why.
//
// Once RetryAt passes the head is let through on trial: the breaker is not
// closed until something actually succeeds, so a still-broken head fails once
// and backs off further rather than being retried on every dispatch.
func (s *Store) Blocked(id string, now time.Time) (string, bool) {
	if s == nil {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.heads[id]
	if !ok || !now.Before(e.RetryAt) {
		return "", false
	}
	return e.Reason + ", retrying " + humanizeUntil(e.RetryAt, now), true
}

// Fail records a failed execution and parks the head if it has earned it.
func (s *Store) Fail(id, reason string, k Kind, now time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.heads[id]
	if !ok {
		e = &entry{}
		s.heads[id] = e
	}
	e.Failures++
	e.Reason = reason
	e.Kind = k
	e.LastFail = now
	if k == Fatal || e.Failures >= softFailuresBeforeOpen {
		e.RetryAt = now.Add(cooldown(e.Failures))
	}
	s.dirty = true
}

// Pass clears a head's record. A success is the only thing that closes a
// breaker, so a head recovers by working, not by waiting.
func (s *Store) Pass(id string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.heads[id]; !ok {
		return
	}
	delete(s.heads, id)
	s.dirty = true
}

// Flush writes the store when something changed. Temp-then-rename, so a reader
// racing a writer sees one whole file or the other, never a half-written one.
func (s *Store) Flush() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dirty {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s.heads, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".health.*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, s.path); err != nil {
		os.Remove(name)
		return err
	}
	s.dirty = false
	return nil
}

// cooldown backs off from the first opened failure, capped so a head that was
// fixed hours ago is not still parked.
func cooldown(failures int) time.Duration {
	d := baseCooldown
	for i := softFailuresBeforeOpen; i < failures && d < maxCooldown; i++ {
		d *= 2
	}
	if d > maxCooldown {
		return maxCooldown
	}
	return d
}

// fatalSignals are stderr texts that mean the head cannot serve until the
// machine changes. Matching text is unpleasant, but a CLI head's failure
// arrives as a subprocess exit code and a line of stderr, and treating "this
// model does not exist" as transient retries it every minute forever.
var fatalSignals = []string{
	"executable file not found",
	"invalid model selection",
	"is not recognized as a known model",
	"no such model",
	"model not found",
}

// Classify decides how long a failure should park a head.
func Classify(err error) Kind {
	if err == nil {
		return Transient
	}
	if errors.Is(err, exec.ErrNotFound) {
		return Fatal
	}
	msg := strings.ToLower(err.Error())
	for _, s := range fatalSignals {
		if strings.Contains(msg, s) {
			return Fatal
		}
	}
	return Transient
}

// humanizeUntil renders the wait, not the wall-clock time: "retrying in 4m" is
// actionable where a timestamp needs arithmetic to read.
func humanizeUntil(at, now time.Time) string {
	d := at.Sub(now)
	switch {
	case d < time.Minute:
		return "in under a minute"
	case d < time.Hour:
		return "in " + strings.TrimSuffix(d.Round(time.Minute).String(), "0s")
	default:
		return "in " + d.Round(time.Minute).String()
	}
}

// Reason explains why a head cannot be dispatched to right now, or returns ""
// when it can.
//
// One function so a surface that lists heads and the router that skips them
// cannot disagree, which is the same reason executor.Unroutable exists (#248).
// The precondition is checked first: "agy is not installed" is a better answer
// than "failed 3 minutes ago", and it is the cause of the other.
func Reason(s *Store, h provider.Head, now time.Time) string {
	if why := executor.Unroutable(h); why != "" {
		return why
	}
	if why, parked := s.Blocked(h.ID, now); parked {
		return why
	}
	return ""
}
