// SPDX-License-Identifier: MIT

package trust

import (
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/ankit373/hydra/internal/reliability"
)

// thresholdGrid is how finely the accept threshold is searched. The grid is fixed
// before any data is seen, which is what lets one union bound cover every point on it.
const thresholdGrid = 100

// ErrNoThreshold reports that no threshold on the grid meets the target at the
// stated confidence, so the caller keeps the bound it already has.
var ErrNoThreshold = errors.New("trust: no accept threshold meets the target")

// Threshold is an accept threshold chosen on held-out verdicts, with what it
// was asked for and what it was chosen on. The zero value means none was chosen.
type Threshold struct {
	Accept     float64 `json:"accept"`     // accept an answer stated at or above this
	Target     float64 `json:"target"`     // bound on the error rate among accepted answers
	Confidence float64 `json:"confidence"` // probability that bound holds
	Split      int     `json:"split"`      // held-out pairs it was chosen on
	Accepted   int     `json:"accepted"`   // of those, how many it accepts
	Errors     int     `json:"errors"`     // and how many of those were wrong
}

// ChooseThreshold returns the lowest grid threshold whose accepted error rate an exact binomial
// test puts under target. Each test runs at (1-confidence)/thresholdGrid, so all passes hold at once.
func ChooseThreshold(heldOut []reliability.Observation, target, confidence float64) (Threshold, error) {
	if len(heldOut) < reliability.MinObservations {
		need := reliability.MinObservations
		return Threshold{}, fmt.Errorf("%w: have %d, need %d", reliability.ErrTooFew, len(heldOut), need)
	}
	if target <= 0 || target >= 1 {
		return Threshold{}, fmt.Errorf("trust: target error rate must be in (0,1), got %v", target)
	}
	if confidence <= 0 || confidence >= 1 {
		return Threshold{}, fmt.Errorf("trust: confidence must be in (0,1), got %v", confidence)
	}
	if err := checkProbabilities(heldOut); err != nil {
		return Threshold{}, err
	}
	sorted := slices.Clone(heldOut)
	slices.SortFunc(sorted, func(a, b reliability.Observation) int {
		return compareFloat(b.Predicted, a.Predicted)
	})

	level := (1 - confidence) / thresholdGrid
	var chosen Threshold
	accepted, errs := 0, 0
	for step := thresholdGrid - 1; step >= 1; step-- {
		accept := float64(step) / thresholdGrid
		for accepted < len(sorted) && sorted[accepted].Predicted >= accept {
			if !sorted[accepted].Correct {
				errs++
			}
			accepted++
		}
		if binomialCDF(errs, accepted, target) > level {
			continue
		}
		chosen = Threshold{
			Accept:     accept,
			Target:     target,
			Confidence: confidence,
			Split:      len(heldOut),
			Accepted:   accepted,
			Errors:     errs,
		}
	}
	if chosen.Accept == 0 {
		return Threshold{}, ErrNoThreshold
	}
	return chosen, nil
}

// binomialCDF is P(X <= k) for X ~ Binomial(n, p), summed in log space so a
// large n does not underflow each term before it is added.
func binomialCDF(k, n int, p float64) float64 {
	lnP, lnQ := math.Log(p), math.Log1p(-p)
	lnNFact, _ := math.Lgamma(float64(n + 1))
	var sum float64
	for i := 0; i <= k; i++ {
		lnIFact, _ := math.Lgamma(float64(i + 1))
		lnRestFact, _ := math.Lgamma(float64(n - i + 1))
		lnChoose := lnNFact - lnIFact - lnRestFact
		sum += math.Exp(lnChoose + float64(i)*lnP + float64(n-i)*lnQ)
	}
	return math.Min(sum, 1)
}
