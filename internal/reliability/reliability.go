// SPDX-License-Identifier: MIT

// Package reliability measures whether a stated probability is honest: when
// Hydra says 90%, is it right 90% of the time. This is the calibration of the
// *output*, distinct from internal/trust's per-source se/sp, which calibrates
// the inputs that produce it.
package reliability

import (
	"errors"
	"fmt"
	"math"
	"sort"
)

// MinObservations is the fewest pairs that yield a report. A reliability
// diagram over a handful of runs is noise that reads as a measurement, so it is
// refused the way internal/ope refuses below its effective-sample floor.
const MinObservations = 20

// DefaultBins is the bin count when a caller names none. Ten equal-width bins
// is the convention reliability diagrams are read in.
const DefaultBins = 10

// ErrTooFew reports that there are not enough observations to say anything.
var ErrTooFew = errors.New("reliability: too few scored observations")

// Observation is one forecast and what actually happened.
type Observation struct {
	Predicted float64 // stated probability of correctness, in [0,1]
	Correct   bool    // ground truth, from an oracle or a human
}

// Bin is one column of the reliability diagram.
type Bin struct {
	Lo            float64 `json:"lo"`
	Hi            float64 `json:"hi"`
	N             int     `json:"n"`
	MeanPredicted float64 `json:"mean_predicted"`
	Observed      float64 `json:"observed"` // fraction actually correct
}

// Gap is signed miscalibration for the bin: positive means overconfident.
func (b Bin) Gap() float64 { return b.MeanPredicted - b.Observed }

// Report is the full verdict on a set of forecasts.
type Report struct {
	N     int     `json:"n"`
	Brier float64 `json:"brier"`

	// Murphy decomposition: Brier = Reliability - Resolution + Uncertainty.
	// Reliability alone is the miscalibration, and lower is better. Resolution
	// is how much the forecasts discriminate, and higher is better: a
	// forecaster that always states the base rate is perfectly reliable and
	// completely useless, which only the decomposition makes visible.
	Reliability float64 `json:"reliability"`
	Resolution  float64 `json:"resolution"`
	Uncertainty float64 `json:"uncertainty"`

	ECE      float64 `json:"ece"` // expected calibration error, count-weighted
	MCE      float64 `json:"mce"` // worst single bin
	BaseRate float64 `json:"base_rate"`
	Bins     []Bin   `json:"bins"`
}

// Overconfident reports whether the forecasts are, on balance, too sure of
// themselves. The count-weighted signed gap, so bins that disagree cancel
// rather than both counting as error.
func (r Report) Overconfident() bool { return r.SignedGap() > 0 }

// SignedGap is the count-weighted mean of predicted minus observed.
func (r Report) SignedGap() float64 {
	if r.N == 0 {
		return 0
	}
	var acc float64
	for _, b := range r.Bins {
		acc += float64(b.N) * b.Gap()
	}
	return acc / float64(r.N)
}

// Evaluate scores the observations into bins equal-width bins.
//
// bins <= 0 uses DefaultBins. A predicted value outside [0,1] is an error
// rather than something to clamp: it means the caller handed over something
// that is not a probability, and silently repairing it would hide that.
func Evaluate(obs []Observation, bins int) (Report, error) {
	if len(obs) < MinObservations {
		return Report{}, fmt.Errorf("%w: have %d, need %d", ErrTooFew, len(obs), MinObservations)
	}
	if bins <= 0 {
		bins = DefaultBins
	}
	for i, o := range obs {
		if math.IsNaN(o.Predicted) || o.Predicted < 0 || o.Predicted > 1 {
			return Report{}, fmt.Errorf("reliability: observation %d predicted %v, not a probability", i, o.Predicted)
		}
	}

	n := float64(len(obs))
	type acc struct {
		n           int
		sumP, sumOK float64
	}
	buckets := make([]acc, bins)
	var brier, correct float64
	for _, o := range obs {
		outcome := 0.0
		if o.Correct {
			outcome = 1
		}
		brier += (o.Predicted - outcome) * (o.Predicted - outcome)
		correct += outcome

		// The top edge belongs to the last bin; without this a forecast of
		// exactly 1.0 indexes one past the end.
		k := int(o.Predicted * float64(bins))
		if k >= bins {
			k = bins - 1
		}
		buckets[k].n++
		buckets[k].sumP += o.Predicted
		buckets[k].sumOK += outcome
	}

	base := correct / n
	rep := Report{
		N:           len(obs),
		Brier:       brier / n,
		Uncertainty: base * (1 - base),
		BaseRate:    base,
	}
	width := 1 / float64(bins)
	for k, b := range buckets {
		if b.n == 0 {
			continue // an empty bin contributes nothing and renders as nothing
		}
		bn := float64(b.n)
		bin := Bin{
			Lo:            float64(k) * width,
			Hi:            float64(k+1) * width,
			N:             b.n,
			MeanPredicted: b.sumP / bn,
			Observed:      b.sumOK / bn,
		}
		gap := bin.Gap()
		rep.Reliability += bn * gap * gap
		rep.Resolution += bn * (bin.Observed - base) * (bin.Observed - base)
		rep.ECE += bn * math.Abs(gap)
		if a := math.Abs(gap); a > rep.MCE {
			rep.MCE = a
		}
		rep.Bins = append(rep.Bins, bin)
	}
	rep.Reliability /= n
	rep.Resolution /= n
	rep.ECE /= n
	sort.Slice(rep.Bins, func(i, j int) bool { return rep.Bins[i].Lo < rep.Bins[j].Lo })
	return rep, nil
}
