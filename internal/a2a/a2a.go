// SPDX-License-Identifier: MIT

// Package a2a models agent-to-agent handoffs with causal ordering. Each handoff
// carries a vector clock so Hydra can tell whether two handoffs are sequential
// (one happened-before the other) or concurrent — and, when concurrent, whether
// they touch overlapping files and therefore conflict.
package a2a

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Clock is a vector clock: agent ID → event counter.
type Clock map[string]uint64

// Tick returns a copy of the clock with the given agent's counter incremented.
// The receiver is not mutated, so callers can keep prior snapshots.
func (c Clock) Tick(agent string) Clock {
	out := c.clone()
	out[agent]++
	return out
}

// Merge returns the component-wise maximum of two clocks — the causal history
// known to an agent that has observed both.
func (c Clock) Merge(o Clock) Clock {
	out := c.clone()
	for k, v := range o {
		if v > out[k] {
			out[k] = v
		}
	}
	return out
}

func (c Clock) clone() Clock {
	out := make(Clock, len(c))
	for k, v := range c {
		out[k] = v
	}
	return out
}

// Ordering is the causal relationship between two clocks.
type Ordering int

const (
	Equal      Ordering = iota // identical histories
	Before                     // c happened-before o
	After                      // o happened-before c
	Concurrent                 // neither — independent, possibly conflicting
)

func (o Ordering) String() string {
	switch o {
	case Equal:
		return "equal"
	case Before:
		return "before"
	case After:
		return "after"
	default:
		return "concurrent"
	}
}

// Compare classifies the causal relationship of c to o. c is Before o iff every
// component of c is ≤ o and at least one is strictly less (and symmetrically for
// After); if both directions show a strictly-greater component, they are
// Concurrent.
func (c Clock) Compare(o Clock) Ordering {
	cLess, cGreater := false, false
	for _, k := range unionKeys(c, o) {
		switch {
		case c[k] < o[k]:
			cLess = true
		case c[k] > o[k]:
			cGreater = true
		}
	}
	switch {
	case !cLess && !cGreater:
		return Equal
	case cLess && !cGreater:
		return Before
	case cGreater && !cLess:
		return After
	default:
		return Concurrent
	}
}

func unionKeys(a, b Clock) []string {
	seen := make(map[string]bool, len(a)+len(b))
	var keys []string
	for k := range a {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	for k := range b {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	return keys
}

// Handoff is the structured context passed between agents. It is the typed form
// of ~/.hydra/logs/last_handoff.json.
type Handoff struct {
	From        string   `json:"from"`
	Model       string   `json:"model,omitempty"`
	Task        string   `json:"task"`
	Files       []string `json:"files,omitempty"`
	Conventions string   `json:"conventions,omitempty"`
	Context     string   `json:"context,omitempty"`
	PriorOutput string   `json:"prior_output,omitempty"`
	Clock       Clock    `json:"clock,omitempty"`
}

// Load reads a handoff from disk. A missing file returns a nil handoff and no
// error so callers can treat "no prior handoff" as the empty causal history.
func Load(path string) (*Handoff, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var h Handoff
	if err := json.Unmarshal(raw, &h); err != nil {
		return nil, err
	}
	return &h, nil
}

// Save writes the handoff atomically-ish (dir created, 0600).
func (h *Handoff) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

// PromptBlock renders the handoff as the structured context block prepended to a
// downstream agent's prompt.
func (h *Handoff) PromptBlock(task string) string {
	files := ""
	for i, f := range h.Files {
		if i > 0 {
			files += ", "
		}
		files += f
	}
	return "A2A HANDOFF from: " + h.From +
		"\nFiles in scope: " + files +
		"\nConventions:\n" + h.Conventions +
		"\nPrior output:\n" + h.PriorOutput +
		"\nContext:\n" + h.Context +
		"\n\nTASK:\n" + task
}

// ConflictsWith reports whether two handoffs are concurrent (neither
// happened-before the other) AND touch at least one common file — the signature
// of an unsynchronized edit collision.
func (h *Handoff) ConflictsWith(o *Handoff) bool {
	if h == nil || o == nil {
		return false
	}
	if h.Clock.Compare(o.Clock) != Concurrent {
		return false
	}
	return overlaps(h.Files, o.Files)
}

func overlaps(a, b []string) bool {
	set := make(map[string]bool, len(a))
	for _, f := range a {
		set[f] = true
	}
	for _, f := range b {
		if set[f] {
			return true
		}
	}
	return false
}
