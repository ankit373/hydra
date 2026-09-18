// SPDX-License-Identifier: MIT

package rank

import (
	"math"
	"sort"

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
	// Correct and Total pool every domain.
	Correct int
	Total   int
	// InDomainCorrect and InDomainTotal are the subset of those recorded in
	// the one domain being routed for. Both zero when no domain is known,
	// which is every ranking probe produces, and the estimator then reduces
	// to the pooled one exactly.
	InDomainCorrect int
	InDomainTotal   int
}

// consulted reports whether either level held enough evidence to be read.
func (m Measurement) consulted() bool {
	return m.Total-m.InDomainTotal >= MinCommitments || m.InDomainTotal >= MinCommitments
}

// Lookup returns a head's measured history. A head with none returns the zero
// Measurement, so "no lookup at all" and "nothing measured yet" are one path.
type Lookup func(headID string) Measurement

// Score is why a head ranks where it does: what the catalogue declared, what
// it was actually ranked on, and how much evidence separated the two.
type Score struct {
	Declared  int `json:"declared"`
	Effective int `json:"effective"`
	// N is the commitments consulted, InDomain how many of those were recorded
	// in the domain being routed for. Both are zero when no evidence cleared
	// the floor, so a count here always means one was read. Kept separate from
	// Adjusted: a head measured at exactly its declared rate was still
	// measured, and reporting no evidence for it would be a lie.
	N        int `json:"n"`
	InDomain int `json:"in_domain,omitempty"`
}

// Adjusted reports whether measurement moved this head at all.
func (s Score) Adjusted() bool { return s.Effective != s.Declared }

// EffectiveScore is the declared score updated by what the head actually did
// everywhere: the mean of a Beta posterior whose prior sits on the declared
// score with MinCommitments pseudo-observations behind it. This is the pooled
// ranking, which is all a machine scan can compute, having no task and so no
// domain (#815).
//
// Deliberately not a penalty table, per quantization scheme or anything else.
// A fabricated magnitude would be indistinguishable from a measured one, so
// the only thing that moves a score is the head's own verified history. With
// no evidence the posterior *is* the prior and this returns declared
// unchanged, which is why that case needs no special branch to be exact.
func EffectiveScore(declared int, m Measurement) int {
	return posterior(declared, m.Correct, m.Total)
}

// EffectiveScoreIn ranks a head for one domain: its work in *other* domains
// locates a prior, and its work in this one updates that.
//
// Borrowing is bounded at MinCommitments pseudo-observations, which is the
// whole difference between this and the pooled score. Letting it accumulate
// instead made the result very nearly domain-independent, because a head whose
// record sits entirely in one domain contributes the same total either way:
// measured end to end, a head with 500 judged answers elsewhere and *none*
// here outranked one with 40 here at the same rate, 98 to 89. Evidence from a
// domain nobody asked about must not read as evidence about this one, or the
// ranking is pooled with extra steps.
//
// The bound is the constant that already exists, not a new one: other domains
// are worth what the catalogue's own score is worth, no more. How much skill
// actually transfers between domains is a real number, but it is one to
// measure from internal/evalset history rather than assert here, the same call
// #765 made about a quantization penalty.
//
// The leave-one-out is not optional either. Pooled counts include this
// domain's own rows, so using them as its prior would let the same evidence
// vote twice, once as its own expectation.
func EffectiveScoreIn(declared int, m Measurement) int {
	elsewhere := atMost(m.Correct-m.InDomainCorrect, m.Total-m.InDomainTotal, MinCommitments)
	base := posterior(declared, elsewhere.Correct, elsewhere.Total)
	return posterior(base, m.InDomainCorrect, m.InDomainTotal)
}

// atMost scales a count down to at most n observations, keeping its rate. A
// bound on how loud evidence can be, never on what it says.
func atMost(correct, total, n int) Measurement {
	if total <= n {
		return Measurement{Correct: correct, Total: total}
	}
	return Measurement{Correct: int(math.Round(float64(correct) * float64(n) / float64(total))), Total: n}
}

// posterior is the mean of a Beta posterior whose prior sits on declared with
// MinCommitments pseudo-observations behind it, refusing to move below the
// floor. Counts are clamped because the two levels arrive independently and a
// negative one would be read as evidence rather than as the mistake it is.
func posterior(declared, correct, total int) int {
	if total < MinCommitments {
		return declared
	}
	if correct < 0 {
		correct = 0
	}
	prior := float64(MinCommitments) * float64(declared) / 100
	rate := (float64(correct) + prior) / float64(total+MinCommitments)
	return int(math.Round(rate * 100))
}

// ByMeasured is ByCapScore ranked on EffectiveScore, returning the scores it
// ranked on so a caller can explain the order it got rather than recompute it
// and risk disagreeing. A nil lookup ranks on declared scores alone.
func ByMeasured(heads []provider.Head, lookup Lookup) ([]provider.Head, map[string]Score) {
	scores := scoreWith(heads, lookup, EffectiveScore)
	return rankBy(heads, scores), scores
}

// SortMeasured orders an already-deduplicated head list, which is what a
// dispatch holds once it has filtered the probe ranking down to the candidates
// a tier and a policy allow.
//
// Separate from ByMeasured because that one deduplicates as well. Running it
// again here happens to collapse nothing today, since probe already did it,
// but a reorder silently depending on that would break the moment it stopped
// being true, and dropping a candidate from a fallback chain is not a failure
// anyone would trace back to a sort.
func SortMeasured(heads []provider.Head, lookup Lookup) ([]provider.Head, map[string]Score) {
	scores := Scores(heads, lookup)
	sorted := append([]provider.Head(nil), heads...)
	sort.Slice(sorted, func(i, j int) bool { return rankLess(sorted[i], sorted[j], scores) })
	return sorted, scores
}

// Scores ranks for a domain, and is what a caller renders to explain an order
// it did not compute itself. Same scorer SortMeasured uses, so an explanation
// cannot describe an order nobody got.
func Scores(heads []provider.Head, lookup Lookup) map[string]Score {
	return scoreWith(heads, lookup, EffectiveScoreIn)
}

func scoreWith(heads []provider.Head, lookup Lookup, eff func(int, Measurement) int) map[string]Score {
	scores := make(map[string]Score, len(heads))
	for _, h := range heads {
		if _, seen := scores[h.ID]; seen {
			continue
		}
		var m Measurement
		if lookup != nil {
			m = lookup(h.ID)
		}
		sc := Score{Declared: h.CapScore, Effective: eff(h.CapScore, m)}
		if m.consulted() {
			// Reporting a count no level was allowed to read would name
			// evidence the ranking never used.
			sc.N, sc.InDomain = m.Total, m.InDomainTotal
		}
		scores[h.ID] = sc
	}
	return scores
}
