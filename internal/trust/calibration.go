// SPDX-License-Identifier: MIT

package trust

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ankit373/hydra/internal/config"
)

// laplacePrior is the pseudo-count added to every confusion cell so a brand-new
// source starts at se=sp=0.5 (D≈0) and earns trust only from real observations.
const laplacePrior = 1.0

// calibKey namespaces a confusion posterior by source and domain.
type calibKey struct{ source, domain string }

// confusion holds Beta-Bernoulli pseudo-counts for one (source, domain).
//
//	              actually correct   actually incorrect
//	says correct        TP                  FP
//	says incorrect      FN                  TN
//
// sensitivity se = TP/(TP+FN) = P(says correct | correct)
// specificity sp = TN/(TN+FP) = P(says incorrect | incorrect)
type confusion struct {
	TP, FP, TN, FN float64
}

func newConfusion() *confusion {
	return &confusion{TP: laplacePrior, FP: laplacePrior, TN: laplacePrior, FN: laplacePrior}
}

func (c *confusion) se() float64 { return c.TP / (c.TP + c.FN) }
func (c *confusion) sp() float64 { return c.TN / (c.TN + c.FP) }

// observations returns the number of real (non-prior) samples recorded.
func (c *confusion) observations() float64 {
	return (c.TP + c.FP + c.TN + c.FN) - 4*laplacePrior
}

// Stat is one row of a calibration report, the human/JSON-facing view.
type Stat struct {
	Source string  `json:"source"`
	Domain string  `json:"domain"`
	N      float64 `json:"n"`  // real observations (excludes prior)
	Se     float64 `json:"se"` // sensitivity
	Sp     float64 `json:"sp"` // specificity
	D      float64 `json:"d"`  // diagnostic power (nats), expected |LLR|
}

// Calibrator maintains an online confusion posterior per (source, domain) and
// derives calibrated LLR and diagnostic power from it. Safe for concurrent use.
type Calibrator struct {
	mu    sync.RWMutex
	store map[calibKey]*confusion
	path  string // "" disables persistence (used by tests)
}

// New constructs a Calibrator. When path is non-empty it replays any existing
// records from that file so calibration survives process restarts; subsequent
// updates are appended there. An empty path keeps everything in memory.
func New(path string) (*Calibrator, error) {
	c := &Calibrator{store: map[calibKey]*confusion{}, path: path}
	if path == "" {
		return c, nil
	}
	if err := c.load(); err != nil {
		return nil, err
	}
	return c, nil
}

// DefaultPath is where calibration is persisted for the CLI (~/.hydra/calibration.jsonl).
func DefaultPath() string {
	return filepath.Join(config.Dir(), "calibration.jsonl")
}

// record is one persisted training event.
type record struct {
	TS          string `json:"ts"`
	Source      string `json:"source"`
	Domain      string `json:"domain"`
	SaidCorrect bool   `json:"said_correct"`
	Outcome     int    `json:"outcome"`
}

// load replays the jsonl file into the in-memory posterior, resuming from the
// last snapshot (calibration_snapshot.go) instead of the start when one exists.
func (c *Calibrator) load() error {
	f, err := os.Open(c.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return err
	}

	var startOffset int64
	if snap, offset, ok := loadSnapshot(snapshotPath(c.path), f, info.Size()); ok {
		c.store = snap
		startOffset = offset
	}
	if startOffset > 0 {
		if _, err := f.Seek(startOffset, io.SeekStart); err != nil {
			return err
		}
	}

	// Bounded by the snapshot's own threshold except on the very first load of
	// a pre-existing history, no worse than the old full-file replay.
	delta, err := io.ReadAll(f)
	if err != nil {
		return err
	}

	var replayed int
	for _, line := range strings.Split(string(delta), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var r record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			continue // skip malformed rows
		}
		c.apply(r.Source, r.Domain, r.SaidCorrect, Outcome(r.Outcome))
		replayed++
	}

	if replayed >= snapshotThreshold {
		_ = saveSnapshot(snapshotPath(c.path), c.store, startOffset+int64(len(delta)))
	}
	return nil
}

// apply folds one observation into the in-memory posterior (no persistence).
func (c *Calibrator) apply(source, domain string, saidCorrect bool, actual Outcome) {
	if actual == OutcomeUnknown {
		return
	}
	key := calibKey{source, domain}
	conf := c.store[key]
	if conf == nil {
		conf = newConfusion()
		c.store[key] = conf
	}
	switch {
	case saidCorrect && actual == OutcomeCorrect:
		conf.TP++
	case saidCorrect && actual == OutcomeIncorrect:
		conf.FP++
	case !saidCorrect && actual == OutcomeCorrect:
		conf.FN++
	case !saidCorrect && actual == OutcomeIncorrect:
		conf.TN++
	}
}

// Update records one observation: a source said correct/incorrect and the
// ground-truth outcome came back. It updates the posterior and, when a path is
// set, appends the event so it survives restarts. OutcomeUnknown is ignored.
func (c *Calibrator) Update(source, domain string, saidCorrect bool, actual Outcome) error {
	if actual == OutcomeUnknown {
		return nil
	}
	c.mu.Lock()
	c.apply(source, domain, saidCorrect, actual)
	c.mu.Unlock()

	if c.path == "" {
		return nil
	}
	return c.append(record{
		TS:          time.Now().UTC().Format(time.RFC3339),
		Source:      source,
		Domain:      domain,
		SaidCorrect: saidCorrect,
		Outcome:     int(actual),
	})
}

func (c *Calibrator) append(r record) error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(c.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(f, string(raw))
	return err
}

// LLR returns the calibrated log-likelihood-ratio contribution (nats) of a
// verdict from this source. Positive nats are evidence the answer is correct.
//   - says correct:   ln( se / (1−sp) )
//   - says incorrect: ln( (1−se) / sp )
//
// An uncalibrated / coin-flip source (se+sp≈1) yields LLR≈0.
func (c *Calibrator) LLR(source, domain string, saidCorrect bool) float64 {
	se, sp := c.rates(source, domain)
	if saidCorrect {
		return math.Log(se / (1 - sp))
	}
	return math.Log((1 - se) / sp)
}

// D is the diagnostic power of a source (nats): the expected LLR of its verdict
// given a truly-correct item, i.e. KL(Bern(se) ‖ Bern(1−sp)). It is ≥0 always,
// and 0 exactly when se+sp=1 (the source carries no information, Law 2). Use it
// to order which source to sample next (most evidence first).
func (c *Calibrator) D(source, domain string) float64 {
	se, sp := c.rates(source, domain)
	return se*math.Log(se/(1-sp)) + (1-se)*math.Log((1-se)/sp)
}

// rates returns clamped se/sp so LLR/D never hit ±Inf from a degenerate cell.
func (c *Calibrator) rates(source, domain string) (se, sp float64) {
	c.mu.RLock()
	conf := c.store[calibKey{source, domain}]
	c.mu.RUnlock()
	if conf == nil {
		return 0.5, 0.5 // unknown source: uninformative
	}
	return clamp01(conf.se()), clamp01(conf.sp())
}

// clamp01 keeps a probability strictly inside (0,1) so logs stay finite even
// before enough samples accumulate (the Laplace prior already guarantees this,
// but clamp is a cheap belt-and-suspenders against future prior changes).
func clamp01(p float64) float64 {
	const eps = 1e-9
	if p < eps {
		return eps
	}
	if p > 1-eps {
		return 1 - eps
	}
	return p
}

// Report returns per-(source,domain) calibration stats, most-diagnostic first.
func (c *Calibrator) Report() []Stat {
	c.mu.RLock()
	stats := make([]Stat, 0, len(c.store))
	for k, conf := range c.store {
		se, sp := clamp01(conf.se()), clamp01(conf.sp())
		stats = append(stats, Stat{
			Source: k.source,
			Domain: k.domain,
			N:      conf.observations(),
			Se:     se,
			Sp:     sp,
			D:      se*math.Log(se/(1-sp)) + (1-se)*math.Log((1-se)/sp),
		})
	}
	c.mu.RUnlock()

	sort.Slice(stats, func(i, j int) bool { return stats[i].D > stats[j].D })
	return stats
}
