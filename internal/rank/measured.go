// SPDX-License-Identifier: MIT

package rank

import (
	"math"

	"github.com/ankit373/hydra/internal/provider"
)

// MinCommitments is how many judged answers a head needs before anything it
// did on this machine moves its score. Below it the declared score stands
// exactly as it did before there was a measurement at all, so a fresh install
// ranks byte-identically.
//
// It doubles as the prior's strength below, which is the whole calibration:
// the catalogue's declared score is worth as many observations as we require
// before we will listen to a measurement at all. One number, not two.
const MinCommitments = 20

// Measurement is what this machine's verified history says about a head: how
// many answers it committed to that something later judged, and how many of
// those held up.
type Measurement struct {
	Correct int
	Total   int
}

// Lookup returns a head's measured history. A head with none returns the zero
// Measurement, so "no lookup at all" and "nothing measured yet" are one path.
type Lookup func(headID string) Measurement

// Score is why a head ranks where it does: what the catalogue declared, what
// it was actually ranked on, and how much evidence separated the two.
type Score struct {
	Declared  int `json:"declared"`
	Effective int `json:"effective"`
	N         int `json:"n"` // commitments behind Effective; below MinCommitments the two are equal
}

// Adjusted reports whether measurement moved this head at all.
func (s Score) Adjusted() bool { return s.Effective != s.Declared }

// EffectiveScore is the declared score updated by what the head actually did:
// the mean of a Beta posterior whose prior sits on the declared score with
// MinCommitments pseudo-observations behind it.
//
// Deliberately not a penalty table. A per-scheme number ("Q4 costs 7 points")
// would be indistinguishable from a measured one and this repo refuses those
// elsewhere, so the only thing that moves a score here is the head's own
// verified history. With no evidence the posterior *is* the prior and this
// returns declared unchanged, which is why the no-evidence case needs no
// special branch to be exact.
func EffectiveScore(declared int, m Measurement) int {
	if m.Total < MinCommitments {
		return declared
	}
	prior := float64(MinCommitments) * float64(declared) / 100
	rate := (float64(m.Correct) + prior) / float64(m.Total+MinCommitments)
	return int(math.Round(rate * 100))
}

// ByMeasured is ByCapScore ranked on EffectiveScore, returning the scores it
// ranked on so a caller can explain the order it got rather than recompute it
// and risk disagreeing. A nil lookup ranks on declared scores alone.
func ByMeasured(heads []provider.Head, lookup Lookup) ([]provider.Head, map[string]Score) {
	scores := make(map[string]Score, len(heads))
	for _, h := range heads {
		if _, seen := scores[h.ID]; seen {
			continue
		}
		var m Measurement
		if lookup != nil {
			m = lookup(h.ID)
		}
		eff := EffectiveScore(h.CapScore, m)
		if m.Total < MinCommitments {
			// Reporting the count behind an adjustment that did not happen
			// would read as evidence the ranking used.
			m.Total = 0
		}
		scores[h.ID] = Score{Declared: h.CapScore, Effective: eff, N: m.Total}
	}
	return rankBy(heads, scores), scores
}
