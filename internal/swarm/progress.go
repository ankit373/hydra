// SPDX-License-Identifier: MIT

package swarm

import (
	"sync"

	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/trust"
)

// ProgressKind names what happened during a fan-out.
type ProgressKind int

const (
	// ProgressSelected fires once, after selection and before any head runs,
	// so a surface can draw the whole roster with nothing started yet.
	ProgressSelected ProgressKind = iota
	// ProgressStarted fires as a head begins.
	ProgressStarted
	// ProgressFinished fires as a head ends, however it ended; Attempt says
	// which, so a failed head is never left rendered as still running.
	ProgressFinished
	// ProgressEvidence fires on the SPRT path only, once a head's answer has
	// been weighed. A head finishing says nothing about how close Λ is to the
	// threshold, and that is the whole reason an ensemble stops early.
	ProgressEvidence
)

// Progress is one event in a fan-out's life. Fields the kind does not carry
// are zero.
type Progress struct {
	Kind  ProgressKind
	Heads []provider.Head // ProgressSelected: every head that will be engaged
	Head  provider.Head   // ProgressStarted, ProgressFinished

	// Attempt is the completed record on ProgressFinished, priced, so a
	// surface can show spend as it accrues rather than only at the end.
	Attempt Attempt

	// Evidence and Threshold are ProgressEvidence only: the ledger entry just
	// recorded, and the Λ the run is walking toward.
	Evidence  trust.Evidence
	Threshold float64
}

// progressSink serializes delivery. A fan-out reports from one goroutine per
// head, so without this every surface would need a lock of its own, which is a
// second place to get it wrong.
type progressSink struct {
	mu sync.Mutex
	fn func(Progress)
}

// emit is nil-safe at both levels: no sink, or a sink with no callback, costs
// a comparison and nothing else.
func (s *progressSink) emit(p Progress) {
	if s == nil || s.fn == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fn(p)
}
