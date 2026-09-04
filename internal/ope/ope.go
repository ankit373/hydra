// SPDX-License-Identifier: MIT

// Package ope recovers population quantities from non-uniformly sampled logs.
//
// Averaging a sampled log answers a question nobody asked: the mean over what
// was kept, not the mean over what happened. Keeping every failure and a
// fraction of successes understates success rates badly enough to invert the
// ranking of two heads, which then changes routing — a closed loop with no
// error signal in it. Weighting each row by the inverse of the probability it
// was kept removes the bias.
package ope

import "errors"

// ErrNoUsableSamples reports that nothing had a usable inclusion probability,
// so no estimate exists. Distinct from a zero mean, which is a real answer.
var ErrNoUsableSamples = errors.New("ope: no samples with a positive inclusion probability")

// Sample is one logged outcome and the probability its row was retained.
type Sample struct {
	Value float64 // quantity being averaged; 1/0 for a success indicator
	Prob  float64 // inclusion probability, in (0,1]
}

// SelfNormalized returns the inverse-probability-weighted mean of the samples
// and the number skipped for an unusable probability.
//
// Self-normalised rather than plain Horvitz-Thompson: dividing by the summed
// weights instead of the sample count keeps the estimate within the range of
// the observed values, which plain HT does not guarantee when weights are
// large or the sample is small.
//
// A non-positive or non-finite probability is skipped rather than treated as
// certain: it is a corrupt row, and dividing by it would silently dominate the
// estimate. Callers must surface a non-zero skipped count rather than ignore it.
func SelfNormalized(samples []Sample) (mean float64, skipped int, err error) {
	var num, den float64
	for _, s := range samples {
		if !(s.Prob > 0 && s.Prob <= 1) || isNaN(s.Value) {
			skipped++
			continue
		}
		w := 1 / s.Prob
		num += s.Value * w
		den += w
	}
	if den == 0 {
		return 0, skipped, ErrNoUsableSamples
	}
	return num / den, skipped, nil
}

func isNaN(f float64) bool { return f != f }
