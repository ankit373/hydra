// SPDX-License-Identifier: MIT

package ope

import (
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
)

// Counterfactual evaluation answers "what would this other routing policy have
// cost, and how often would it have been right" from logs the current policy
// produced, without running it.
//
// It only works where the logged policy had some chance of taking the action
// the evaluated policy would take. Where it had none, the question is not hard
// but *unidentifiable*: no amount of data answers it. Returning a confident
// number there is the failure mode this package exists to prevent, so
// Evaluate refuses instead.

// ErrInsufficientSupport reports that the logged policy did not explore enough
// of what the evaluated policy would do. The estimate is refused rather than
// returned wide: a number with a huge interval still gets read as a number.
var ErrInsufficientSupport = errors.New("ope: the log does not support this policy")

// ErrNoPropensity reports that no row carried a usable propensity at all. That
// is a different diagnosis from no overlap, and it has a different remedy:
// these rows predate propensity logging, so no policy is evaluable against
// them and no setting changes that retroactively.
var ErrNoPropensity = errors.New("ope: no row carries a usable propensity")

// DefaultLevel is the reported confidence level.
const DefaultLevel = 0.95

// DefaultMinESS is the smallest effective sample size that still yields an
// answer. Below roughly this many effective observations the bootstrap is
// describing a handful of rows, however many were logged.
const DefaultMinESS = 10

// DefaultBootstrap is the resample count. 2000 is enough for a stable
// percentile interval at two decimal places and costs milliseconds.
const DefaultBootstrap = 2000

// DefaultClip caps an importance weight. Unclipped self-normalised IPS is
// unbiased but its variance is driven by the single largest weight, so one rare
// action the evaluated policy loves can swamp everything else.
const DefaultClip = 20

// CounterfactualSample is one logged decision plus what the evaluated policy
// would have done in the same context.
type CounterfactualSample struct {
	// Value is the observed outcome — cost in dollars, or 1/0 for success.
	Value float64

	// LoggedProb is π_behaviour(a|x): the probability the router actually
	// assigned to the action it took. This is `act_prob` on the dispatch row.
	LoggedProb float64

	// TargetProb is π_target(a|x): the probability the evaluated policy would
	// have taken that same action. 0 means it would have done something else,
	// so the row carries no information about the target policy — but it still
	// counts toward the support diagnostics, which is the point.
	TargetProb float64
}

// Options configures Evaluate. The zero value is usable and means the defaults.
type Options struct {
	Level     float64 // confidence level; 0 means DefaultLevel
	ClipAt    float64 // weight cap; 0 means DefaultClip, negative means no clipping
	MinESS    float64 // refuse below this; 0 means DefaultMinESS
	Bootstrap int     // resamples; 0 means DefaultBootstrap
	Seed      uint64  // fixed so the same log gives the same interval twice
}

// Estimate is an off-policy estimate with the diagnostics needed to decide
// whether to believe it.
type Estimate struct {
	Mean  float64 `json:"mean"`
	Lo    float64 `json:"lo"`
	Hi    float64 `json:"hi"`
	Level float64 `json:"level"`

	// N is rows with a usable probability pair; Supporting is the subset the
	// evaluated policy would actually have taken. The gap between them is how
	// much of the log the question ignores.
	N          int `json:"n"`
	Supporting int `json:"supporting"`
	Skipped    int `json:"skipped"`
	Clipped    int `json:"clipped"`

	// ESS is the effective sample size, (Σw)²/Σw². It is the honest count: a
	// thousand rows dominated by one weight are worth about one observation.
	ESS float64 `json:"ess"`

	// Method names the estimator and the variance control actually applied, so
	// a reader is never left guessing whether clipping was in effect.
	Method string `json:"method"`
}

// Evaluate estimates the mean outcome under the evaluated policy.
//
// Self-normalised inverse propensity scoring with a percentile bootstrap
// interval. Self-normalised because plain Horvitz-Thompson can leave the range
// of the observed values entirely when weights are large; percentile bootstrap
// because the weighted mean's sampling distribution is skewed enough that a
// symmetric normal interval understates the upper tail.
func Evaluate(samples []CounterfactualSample, opts Options) (Estimate, error) {
	level := opts.Level
	if level <= 0 || level >= 1 {
		level = DefaultLevel
	}
	minESS := opts.MinESS
	if minESS <= 0 {
		minESS = DefaultMinESS
	}
	resamples := opts.Bootstrap
	if resamples <= 0 {
		resamples = DefaultBootstrap
	}
	clip := opts.ClipAt
	switch {
	case clip == 0:
		clip = DefaultClip
	case clip < 0:
		clip = math.Inf(1) // explicitly unclipped
	}

	est := Estimate{Level: level}
	var (
		values  []float64
		weights []float64
	)
	for _, s := range samples {
		if !(s.LoggedProb > 0 && s.LoggedProb <= 1) || isNaN(s.Value) ||
			s.TargetProb < 0 || s.TargetProb > 1 || isNaN(s.TargetProb) {
			est.Skipped++
			continue
		}
		est.N++
		if s.TargetProb == 0 {
			continue // in the log, but not on this policy's path
		}
		w := s.TargetProb / s.LoggedProb
		if w > clip {
			w = clip
			est.Clipped++
		}
		est.Supporting++
		values = append(values, s.Value)
		weights = append(weights, w)
	}

	if est.N == 0 {
		if est.Skipped == 0 {
			return est, fmt.Errorf("%w: there are no rows to evaluate", ErrNoPropensity)
		}
		return est, fmt.Errorf("%w: all %d rows were skipped, so nothing can be evaluated against them",
			ErrNoPropensity, est.Skipped)
	}
	if len(weights) == 0 {
		return est, fmt.Errorf("%w: none of the %d usable rows took an action this policy would take",
			ErrInsufficientSupport, est.N)
	}
	est.ESS = effectiveSampleSize(weights)
	if est.ESS < minESS {
		return est, fmt.Errorf("%w: effective sample size %.1f is below %.0f — %d rows overlap but the weights concentrate on too few",
			ErrInsufficientSupport, est.ESS, minESS, est.Supporting)
	}

	est.Mean = weightedMean(values, weights)
	est.Lo, est.Hi = bootstrapInterval(values, weights, level, resamples, opts.Seed)
	est.Method = "self-normalized IPS, percentile bootstrap"
	if math.IsInf(clip, 1) {
		est.Method += ", weights unclipped"
	} else {
		est.Method += fmt.Sprintf(", weights clipped at %g", clip)
	}
	return est, nil
}

// weightedMean is the self-normalised estimate: Σwv / Σw.
func weightedMean(values, weights []float64) float64 {
	var num, den float64
	for i, w := range weights {
		num += values[i] * w
		den += w
	}
	if den == 0 {
		return 0
	}
	return num / den
}

// effectiveSampleSize is Kish's (Σw)²/Σw². With equal weights it equals the
// row count; as one weight dominates it falls toward 1.
func effectiveSampleSize(weights []float64) float64 {
	var sum, sumSq float64
	for _, w := range weights {
		sum += w
		sumSq += w * w
	}
	if sumSq == 0 {
		return 0
	}
	return sum * sum / sumSq
}

// bootstrapInterval resamples rows with replacement and returns the percentile
// interval of the weighted mean.
//
// Seeded rather than randomly initialised: the same log must produce the same
// interval twice, or a reader cannot tell a changed estimate from a rerun.
func bootstrapInterval(values, weights []float64, level float64, resamples int, seed uint64) (lo, hi float64) {
	n := len(values)
	rng := rand.New(rand.NewPCG(seed, 0x9E3779B97F4A7C15))
	means := make([]float64, 0, resamples)
	for b := 0; b < resamples; b++ {
		var num, den float64
		for i := 0; i < n; i++ {
			j := rng.IntN(n)
			num += values[j] * weights[j]
			den += weights[j]
		}
		if den > 0 {
			means = append(means, num/den)
		}
	}
	if len(means) == 0 {
		return 0, 0
	}
	sort.Float64s(means)
	tail := (1 - level) / 2
	return percentile(means, tail), percentile(means, 1-tail)
}

// percentile reads a sorted slice at q using nearest-rank, which needs no
// interpolation assumption about a distribution that is not normal anyway.
func percentile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(q * float64(len(sorted)-1))
	if i < 0 {
		i = 0
	}
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	return sorted[i]
}
