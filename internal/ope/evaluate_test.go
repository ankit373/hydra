// SPDX-License-Identifier: MIT

package ope

import (
	"errors"
	"math"
	"math/rand/v2"
	"testing"
)

// The estimator's whole job is recovering a quantity the log does not show
// directly. A simulation with a known answer is the only test that checks that,
// rather than checking it is self-consistent.
func TestEvaluate_RecoversAKnownCounterfactualMean(t *testing.T) {
	// Two heads. The router picks the cheap one 80% of the time. We evaluate
	// "always use the expensive head", whose true mean cost is 0.10.
	const (
		cheapCost = 0.01
		dearCost  = 0.10
		pCheap    = 0.8
	)
	rng := rand.New(rand.NewPCG(7, 11))
	var samples []CounterfactualSample
	for i := 0; i < 20000; i++ {
		if rng.Float64() < pCheap {
			samples = append(samples, CounterfactualSample{
				Value: cheapCost, LoggedProb: pCheap, TargetProb: 0, // target would not pick cheap
			})
			continue
		}
		samples = append(samples, CounterfactualSample{
			Value: dearCost, LoggedProb: 1 - pCheap, TargetProb: 1,
		})
	}

	est, err := Evaluate(samples, Options{Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(est.Mean-dearCost) > 0.001 {
		t.Errorf("mean = %.4f, want %.4f", est.Mean, dearCost)
	}
	if est.Lo > dearCost || est.Hi < dearCost {
		t.Errorf("the %.0f%% interval [%.4f, %.4f] excludes the true value %.4f",
			est.Level*100, est.Lo, est.Hi, dearCost)
	}
	if est.Supporting >= est.N {
		t.Errorf("Supporting=%d of N=%d, the rows the target would not take were counted as support",
			est.Supporting, est.N)
	}
}

// A naive average over the same log answers a different question. This is the
// finding that motivated recording propensities at all: the two disagree, and
// the naive one looks perfectly reasonable.
func TestEvaluate_BeatsTheNaiveAverageOnASkewedLog(t *testing.T) {
	// Success rate is 0.9 for the head the target policy uses, but the router
	// picked it rarely, so a plain average over the log is dominated by the
	// other head's 0.3.
	rng := rand.New(rand.NewPCG(3, 5))
	var samples []CounterfactualSample
	var naiveSum, naiveN float64
	for i := 0; i < 20000; i++ {
		if rng.Float64() < 0.9 { // common head, worse outcomes, not on target path
			v := boolVal(rng.Float64() < 0.3)
			samples = append(samples, CounterfactualSample{Value: v, LoggedProb: 0.9, TargetProb: 0})
			naiveSum += v
			naiveN++
			continue
		}
		v := boolVal(rng.Float64() < 0.9)
		samples = append(samples, CounterfactualSample{Value: v, LoggedProb: 0.1, TargetProb: 1})
		naiveSum += v
		naiveN++
	}

	est, err := Evaluate(samples, Options{Seed: 2})
	if err != nil {
		t.Fatal(err)
	}
	naive := naiveSum / naiveN
	if math.Abs(est.Mean-0.9) > 0.02 {
		t.Errorf("estimate = %.3f, want ~0.90", est.Mean)
	}
	if math.Abs(naive-0.9) < 0.1 {
		t.Fatalf("the naive average %.3f is not actually misleading here, so this proves nothing", naive)
	}
	t.Logf("off-policy %.3f vs naive average %.3f (truth 0.90)", est.Mean, naive)
}

// Refusing is the point. A policy the router never explored is unidentifiable,
// not merely uncertain, and a wide interval still reads as an answer.
func TestEvaluate_RefusesWhenNothingOverlaps(t *testing.T) {
	var samples []CounterfactualSample
	for i := 0; i < 500; i++ {
		samples = append(samples, CounterfactualSample{Value: 1, LoggedProb: 1, TargetProb: 0})
	}
	est, err := Evaluate(samples, Options{})
	if !errors.Is(err, ErrInsufficientSupport) {
		t.Fatalf("error = %v, want ErrInsufficientSupport", err)
	}
	if est.Mean != 0 {
		t.Errorf("a refused estimate still reported a mean of %v", est.Mean)
	}
	if est.N != 500 {
		t.Errorf("N = %d, want the 500 usable rows counted even though none support", est.N)
	}
}

// Overlap alone is not support: a handful of rows carrying all the weight is
// still a handful of rows, however long the log is.
func TestEvaluate_RefusesWhenWeightConcentratesOnTooFewRows(t *testing.T) {
	var samples []CounterfactualSample
	for i := 0; i < 5000; i++ {
		samples = append(samples, CounterfactualSample{Value: 1, LoggedProb: 1, TargetProb: 0})
	}
	for i := 0; i < 3; i++ {
		samples = append(samples, CounterfactualSample{Value: 1, LoggedProb: 0.001, TargetProb: 1})
	}
	_, err := Evaluate(samples, Options{})
	if !errors.Is(err, ErrInsufficientSupport) {
		t.Fatalf("error = %v, want ErrInsufficientSupport", err)
	}
	if !contains(err.Error(), "effective sample size") {
		t.Errorf("the refusal does not say why: %v", err)
	}
}

// ESS is the honest count. Equal weights make it the row count; one dominant
// weight collapses it toward one.
func TestEffectiveSampleSize(t *testing.T) {
	equal := []float64{1, 1, 1, 1, 1, 1, 1, 1, 1, 1}
	if got := effectiveSampleSize(equal); math.Abs(got-10) > 1e-9 {
		t.Errorf("ESS of ten equal weights = %v, want 10", got)
	}
	dominated := []float64{1000, 1, 1, 1, 1}
	if got := effectiveSampleSize(dominated); got > 1.1 {
		t.Errorf("ESS with one dominant weight = %v, want near 1", got)
	}
	if got := effectiveSampleSize(nil); got != 0 {
		t.Errorf("ESS of nothing = %v, want 0", got)
	}
}

// Unclipped, a single rare action the target policy loves drives the variance.
// Clipping is a bias-variance trade the reader has to be told about, which is
// why Method names it.
func TestEvaluate_ClipsWeightsAndSaysSo(t *testing.T) {
	var samples []CounterfactualSample
	for i := 0; i < 300; i++ {
		samples = append(samples, CounterfactualSample{Value: 1, LoggedProb: 0.5, TargetProb: 1})
	}
	// One action the router almost never took, which the evaluated policy would
	// always take. Its weight is 10,000x every other row's.
	samples = append(samples, CounterfactualSample{Value: 100, LoggedProb: 0.0001, TargetProb: 1})

	clipped, err := Evaluate(samples, Options{Seed: 4})
	if err != nil {
		t.Fatalf("the clipped estimate was refused: %v", err)
	}
	if clipped.Clipped != 1 {
		t.Errorf("Clipped = %d, want 1", clipped.Clipped)
	}
	if !contains(clipped.Method, "clipped at") {
		t.Errorf("Method %q does not say weights were clipped", clipped.Method)
	}

	// Unclipped, that one row is the entire estimate: ESS collapses to ~1 and
	// the answer is refused. That refusal is the estimator working, clipping
	// is what turns 301 rows into an answerable question.
	_, err = Evaluate(samples, Options{Seed: 4, ClipAt: -1})
	if !errors.Is(err, ErrInsufficientSupport) {
		t.Errorf("unclipped error = %v, want ErrInsufficientSupport from the collapsed ESS", err)
	}

	// Forced through anyway, the outlier drags the mean far above the 1.0 that
	// 300 of the 301 rows observed, the bias clipping trades variance for.
	forced, err := Evaluate(samples, Options{Seed: 4, ClipAt: -1, MinESS: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(forced.Method, "unclipped") {
		t.Errorf("Method %q does not say weights were unclipped", forced.Method)
	}
	if forced.Mean <= clipped.Mean {
		t.Errorf("unclipped mean %.3f did not exceed clipped %.3f, so clipping changed nothing",
			forced.Mean, clipped.Mean)
	}
	t.Logf("clipped %.3f (ESS %.1f) vs unclipped %.3f (ESS %.1f)",
		clipped.Mean, clipped.ESS, forced.Mean, forced.ESS)
}

// A corrupt row must be skipped and counted, never treated as certain, the
// same rule SelfNormalized follows.
func TestEvaluate_SkipsUnusableRows(t *testing.T) {
	good := CounterfactualSample{Value: 1, LoggedProb: 0.5, TargetProb: 1}
	samples := []CounterfactualSample{
		{Value: 1, LoggedProb: 0, TargetProb: 1},    // no propensity
		{Value: 1, LoggedProb: -0.5, TargetProb: 1}, // negative
		{Value: 1, LoggedProb: 1.5, TargetProb: 1},  // above 1
		{Value: math.NaN(), LoggedProb: 0.5, TargetProb: 1},
		{Value: 1, LoggedProb: 0.5, TargetProb: 2}, // target out of range
		{Value: 1, LoggedProb: 0.5, TargetProb: math.NaN()},
	}
	for i := 0; i < 60; i++ {
		samples = append(samples, good)
	}
	est, err := Evaluate(samples, Options{Seed: 5})
	if err != nil {
		t.Fatal(err)
	}
	if est.Skipped != 6 {
		t.Errorf("Skipped = %d, want 6", est.Skipped)
	}
	if est.N != 60 {
		t.Errorf("N = %d, want 60 usable rows", est.N)
	}
}

// The same log must produce the same interval twice, or a reader cannot tell a
// changed estimate from a rerun.
func TestEvaluate_IsDeterministicForAGivenSeed(t *testing.T) {
	var samples []CounterfactualSample
	rng := rand.New(rand.NewPCG(9, 9))
	for i := 0; i < 400; i++ {
		samples = append(samples, CounterfactualSample{
			Value: rng.Float64(), LoggedProb: 0.3 + rng.Float64()*0.5, TargetProb: 1,
		})
	}
	a, err := Evaluate(samples, Options{Seed: 42})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Evaluate(samples, Options{Seed: 42})
	if err != nil {
		t.Fatal(err)
	}
	if a.Lo != b.Lo || a.Hi != b.Hi || a.Mean != b.Mean {
		t.Errorf("two runs disagreed: %+v vs %+v", a, b)
	}
}

// A narrower level must not produce a wider interval.
func TestEvaluate_IntervalWidensWithConfidence(t *testing.T) {
	var samples []CounterfactualSample
	rng := rand.New(rand.NewPCG(13, 17))
	for i := 0; i < 800; i++ {
		samples = append(samples, CounterfactualSample{
			Value: boolVal(rng.Float64() < 0.6), LoggedProb: 0.5, TargetProb: 1,
		})
	}
	narrow, err := Evaluate(samples, Options{Level: 0.80, Seed: 6})
	if err != nil {
		t.Fatal(err)
	}
	wide, err := Evaluate(samples, Options{Level: 0.99, Seed: 6})
	if err != nil {
		t.Fatal(err)
	}
	if (wide.Hi - wide.Lo) < (narrow.Hi - narrow.Lo) {
		t.Errorf("the 99%% interval [%.3f,%.3f] is narrower than the 80%% one [%.3f,%.3f]",
			wide.Lo, wide.Hi, narrow.Lo, narrow.Hi)
	}
}

// Self-normalisation's guarantee: the estimate stays inside the observed range.
// Plain Horvitz-Thompson does not promise this.
func TestEvaluate_StaysWithinTheObservedRange(t *testing.T) {
	var samples []CounterfactualSample
	rng := rand.New(rand.NewPCG(21, 23))
	lo, hi := math.Inf(1), math.Inf(-1)
	for i := 0; i < 1000; i++ {
		v := 5 + rng.Float64()*10
		lo, hi = math.Min(lo, v), math.Max(hi, v)
		samples = append(samples, CounterfactualSample{
			Value: v, LoggedProb: 0.05 + rng.Float64()*0.9, TargetProb: 1,
		})
	}
	est, err := Evaluate(samples, Options{Seed: 8})
	if err != nil {
		t.Fatal(err)
	}
	if est.Mean < lo || est.Mean > hi {
		t.Errorf("estimate %.3f is outside the observed range [%.3f, %.3f]", est.Mean, lo, hi)
	}
}

func TestEvaluate_NoSamples(t *testing.T) {
	err := Evaluate2Err(t, nil)
	if !errors.Is(err, ErrNoPropensity) {
		t.Errorf("error = %v, want ErrNoPropensity", err)
	}
	if !contains(err.Error(), "no rows to evaluate") {
		t.Errorf("error %q reads as if rows were skipped when there were none", err)
	}
}

// A log written before propensity logging existed is a different diagnosis from
// no overlap, and it has a different remedy: no setting fixes rows already
// written. Telling someone to raise explore_rate there sends them after a
// setting that cannot help.
func TestEvaluate_DistinguishesAMissingPropensityFromNoOverlap(t *testing.T) {
	var stale []CounterfactualSample
	for i := 0; i < 100; i++ {
		stale = append(stale, CounterfactualSample{Value: 1, LoggedProb: 0, TargetProb: 1})
	}
	if err := Evaluate2Err(t, stale); !errors.Is(err, ErrNoPropensity) {
		t.Errorf("a log with no propensities gave %v, want ErrNoPropensity", err)
	}

	var noOverlap []CounterfactualSample
	for i := 0; i < 100; i++ {
		noOverlap = append(noOverlap, CounterfactualSample{Value: 1, LoggedProb: 1, TargetProb: 0})
	}
	err := Evaluate2Err(t, noOverlap)
	if !errors.Is(err, ErrInsufficientSupport) {
		t.Errorf("a log with propensities but no overlap gave %v, want ErrInsufficientSupport", err)
	}
	if errors.Is(err, ErrNoPropensity) {
		t.Error("no-overlap was reported as a missing propensity, which has the wrong remedy")
	}
}

func Evaluate2Err(t *testing.T, samples []CounterfactualSample) error {
	t.Helper()
	_, err := Evaluate(samples, Options{})
	if err == nil {
		t.Fatal("Evaluate returned no error where one was expected")
	}
	return err
}

func boolVal(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
