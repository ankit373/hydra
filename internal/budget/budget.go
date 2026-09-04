// SPDX-License-Identifier: MIT

package budget

import (
	"sync"
	"time"
)

// Mode represents the current budget pressure level for a model.
type Mode int

const (
	ModeNormal    Mode = iota // 0–49%
	ModeCompact               // 50–64%
	ModeCaution               // 65–69%
	ModeWarning               // 70–74%
	ModeCritical              // 75–79%
	ModeEmergency             // 80%+
)

func (m Mode) String() string {
	switch m {
	case ModeCompact:
		return "compact"
	case ModeCaution:
		return "caution"
	case ModeWarning:
		return "warning"
	case ModeCritical:
		return "critical"
	case ModeEmergency:
		return "emergency"
	default:
		return "normal"
	}
}

// Snapshot is an immutable point-in-time view of a model's budget state.
type Snapshot struct {
	ModelID   string
	Used      int
	Window    int
	Pct       int
	Mode      Mode
	Source    string // "real" or "estimate"
	UpdatedAt time.Time
}

// ModeFor computes the budget mode from a percentage.
func ModeFor(pct int) Mode {
	switch {
	case pct >= 80:
		return ModeEmergency
	case pct >= 75:
		return ModeCritical
	case pct >= 70:
		return ModeWarning
	case pct >= 65:
		return ModeCaution
	case pct >= 50:
		return ModeCompact
	default:
		return ModeNormal
	}
}

// Tracker holds the latest snapshot for a single model. Safe for concurrent use.
// modelID is set once at construction and never mutated — no lock needed for reads.
type Tracker struct {
	modelID string
	mu      sync.RWMutex
	snap    Snapshot
}

func (t *Tracker) Update(used, window int, source string) Snapshot {
	pct := 0
	if window > 0 {
		// Round to nearest, not truncate: a true 74.95% must round to 75 so it
		// isn't silently reported one point under the ModeCritical boundary.
		pct = (used*100 + window/2) / window
	}
	if pct > 100 {
		pct = 100
	}
	s := Snapshot{
		ModelID:   t.modelID,
		Used:      used,
		Window:    window,
		Pct:       pct,
		Mode:      ModeFor(pct),
		Source:    source,
		UpdatedAt: time.Now().UTC(),
	}
	t.mu.Lock()
	t.snap = s
	t.mu.Unlock()
	return s
}

func (t *Tracker) Snapshot() Snapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.snap
}

// Registry manages Trackers for every model seen at runtime.
type Registry struct {
	mu      sync.RWMutex
	models  map[string]*Tracker
	windows map[string]int
}

// NewRegistry returns a Registry seeded with the given window sizes.
func NewRegistry(windows map[string]int) *Registry {
	if windows == nil {
		windows = map[string]int{}
	}
	return &Registry{
		models:  map[string]*Tracker{},
		windows: windows,
	}
}

// Record updates (or creates) the Tracker for modelID with fresh token counts.
// source should be "real" when the count came from the API, "estimate" otherwise.
func (r *Registry) Record(modelID string, used int, source string) Snapshot {
	r.mu.Lock()
	t, ok := r.models[modelID]
	if !ok {
		t = &Tracker{modelID: modelID}
		r.models[modelID] = t
	}
	window := windowFor(r.windows, modelID)
	r.mu.Unlock()

	return t.Update(used, window, source)
}

// Get returns the latest snapshot for modelID, or a zero Snapshot if unseen.
func (r *Registry) Get(modelID string) Snapshot {
	r.mu.RLock()
	t := r.models[modelID]
	r.mu.RUnlock()
	if t == nil {
		return Snapshot{ModelID: modelID}
	}
	return t.Snapshot()
}

// All returns snapshots for every tracked model, in no guaranteed order.
// Trackers are collected under r.mu then snapshotted outside it to avoid
// holding two locks simultaneously.
func (r *Registry) All() []Snapshot {
	r.mu.RLock()
	trackers := make([]*Tracker, 0, len(r.models))
	for _, t := range r.models {
		trackers = append(trackers, t)
	}
	r.mu.RUnlock()

	out := make([]Snapshot, 0, len(trackers))
	for _, t := range trackers {
		out = append(out, t.Snapshot())
	}
	return out
}
