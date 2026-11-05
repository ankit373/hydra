// SPDX-License-Identifier: MIT

package trust

import (
	"errors"
	"fmt"

	"github.com/ankit373/hydra/internal/reliability"
	"github.com/ankit373/hydra/internal/stats"
)

// thresholdGrid is how finely the accept threshold is searched. The grid is fixed
// before any data is seen, which is what lets one union bound cover every point on it.
const thresholdGrid = 100

// lowestStep is the smallest grid step tried. Λ starts at 0, so a cutoff at or below
// even odds would let Run accept on no evidence at all.
const lowestStep = thresholdGrid/2 + 1

// errNoThreshold reports that no threshold on the grid meets the target at the
// stated confidence, so the caller keeps the bound it already has.
var errNoThreshold = errors.New("trust: no accept threshold meets the target")

// threshold is an accept threshold chosen on held-out runs, with what it was
// asked for and what it was chosen on. The zero value means none was chosen.
type threshold struct {
	accept     float64 // stop once the leader's confidence reaches this
	target     float64 // bound on the error rate among accepted runs
	confidence float64 // probability that bound holds
	split      int     // held-out runs it was chosen on
	accepted   int     // of those, how many it accepts
	wrong      int     // and how many of those stopped on a wrong answer
}

// heldOutRun is one Run, on the same sources and calibration, checked afterwards. It must
// reach the top cutoff or run out of sources, so every grid cutoff's stop was seen.
type heldOutRun struct {
	ledger    []Evidence
	correct   map[string]bool // candidate -> verified correct; a missing one counts as wrong
	exhausted bool            // every source was asked
}

// complete reports whether the run saw every cutoff on the grid. One stopped at a lower
// bound, such as Wald's at 0.95, would count as never stopping at every cutoff above it.
func (r heldOutRun) complete() bool {
	if r.exhausted {
		return true
	}
	top := logit(float64(thresholdGrid-1) / thresholdGrid)
	return len(r.ledger) > 0 && r.ledger[len(r.ledger)-1].LambdaAfter >= top
}

// stop replays Run's own test, Λ >= logit(accept), and reports whether the first
// leader to pass it was right, or false if the run never got there.
func (r heldOutRun) stop(accept float64) (correct, stopped bool) {
	bound := logit(accept)
	for _, e := range r.ledger {
		if e.LambdaAfter >= bound {
			return r.correct[e.Candidate], true
		}
	}
	return false, false
}

// chooseThreshold returns the lowest grid cutoff whose accepted error rate an exact binomial
// test puts under target, each at (1-confidence)/tests. O(grid * ledger entries) overall.
func chooseThreshold(heldOut []heldOutRun, target, confidence float64) (threshold, error) {
	if len(heldOut) < reliability.MinObservations {
		need := reliability.MinObservations
		return threshold{}, fmt.Errorf("%w: have %d, need %d", reliability.ErrTooFew, len(heldOut), need)
	}
	if target <= 0 || target >= 1 {
		return threshold{}, fmt.Errorf("trust: target error rate must be in (0,1), got %v", target)
	}
	if confidence <= 0 || confidence >= 1 {
		return threshold{}, fmt.Errorf("trust: confidence must be in (0,1), got %v", confidence)
	}

	for i, r := range heldOut {
		if !r.complete() {
			return threshold{}, fmt.Errorf("trust: held-out run %d stopped short of the top cutoff", i)
		}
	}

	level := (1 - confidence) / (thresholdGrid - lowestStep)
	var chosen threshold
	for step := thresholdGrid - 1; step >= lowestStep; step-- {
		accept := float64(step) / thresholdGrid
		accepted, wrong := 0, 0
		for _, r := range heldOut {
			correct, stopped := r.stop(accept)
			if !stopped {
				continue
			}
			accepted++
			if !correct {
				wrong++
			}
		}
		if stats.BinomialCDF(wrong, accepted, target) > level {
			continue
		}
		chosen = threshold{
			accept:     accept,
			target:     target,
			confidence: confidence,
			split:      len(heldOut),
			accepted:   accepted,
			wrong:      wrong,
		}
	}
	if chosen.accept == 0 {
		return threshold{}, errNoThreshold
	}
	return chosen, nil
}
