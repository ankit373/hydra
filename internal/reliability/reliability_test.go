// SPDX-License-Identifier: MIT

package reliability

import (
	"errors"
	"math"
	"math/rand"
	"testing"
)

// forecaster builds n observations whose stated probability is p and whose
// outcomes are correct at rate actual, so calibration error is dialled in.
func forecaster(rng *rand.Rand, n int, p, actual float64) []Observation {
	out := make([]Observation, n)
	for i := range out {
		out[i] = Observation{Predicted: p, Correct: rng.Float64() < actual}
	}
	return out
}

// The decomposition is the reason to compute three numbers instead of one, so
// the identity has to hold exactly, not approximately by luck.
func TestEvaluate_MurphyDecompositionHolds(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	var obs []Observation
	for _, p := range []float64{0.15, 0.35, 0.55, 0.75, 0.95} {
		obs = append(obs, forecaster(rng, 200, p, p)...)
	}
	rep, err := Evaluate(obs, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := rep.Reliability - rep.Resolution + rep.Uncertainty
	if math.Abs(rep.Brier-want) > 1e-12 {
		t.Errorf("Brier = %.12f but reliability-resolution+uncertainty = %.12f", rep.Brier, want)
	}
}

// A forecaster whose stated probabilities match reality has near-zero
// miscalibration, and real discrimination.
func TestEvaluate_HonestForecasterIsReliableAndResolute(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	var obs []Observation
	for _, p := range []float64{0.1, 0.3, 0.5, 0.7, 0.9} {
		obs = append(obs, forecaster(rng, 1500, p, p)...)
	}
	rep, err := Evaluate(obs, 10)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Reliability > 0.002 {
		t.Errorf("reliability = %.5f, want near 0 for an honest forecaster", rep.Reliability)
	}
	if rep.Resolution < 0.05 {
		t.Errorf("resolution = %.5f, want substantial: these forecasts do discriminate", rep.Resolution)
	}
	if math.Abs(rep.SignedGap()) > 0.02 {
		t.Errorf("signed gap = %.4f, want near 0", rep.SignedGap())
	}
}

// Always stating the base rate is perfectly calibrated and completely useless.
// One number cannot say that; the decomposition can, and this is why it exists.
func TestEvaluate_BaseRateForecasterHasNoResolution(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	obs := forecaster(rng, 3000, 0.6, 0.6)
	rep, err := Evaluate(obs, 10)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Resolution > 1e-9 {
		t.Errorf("resolution = %.12f, want 0: a single forecast value discriminates nothing", rep.Resolution)
	}
	if rep.Reliability > 0.001 {
		t.Errorf("reliability = %.5f, want near 0: it is still honest", rep.Reliability)
	}
}

// The case this exists to catch: Λ accumulated from estimated LLRs states 90%
// and is right far less often. The report has to name that as overconfidence.
func TestEvaluate_DetectsOverconfidence(t *testing.T) {
	rng := rand.New(rand.NewSource(19))
	obs := forecaster(rng, 2000, 0.9, 0.65)
	rep, err := Evaluate(obs, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Overconfident() {
		t.Errorf("stated 0.90 against a 0.65 outcome rate and Overconfident() = false (gap %.4f)", rep.SignedGap())
	}
	if rep.ECE < 0.2 {
		t.Errorf("ECE = %.4f, want ≥0.2 for a 25-point miscalibration", rep.ECE)
	}
	if math.Abs(rep.MCE-rep.ECE) > 1e-9 {
		t.Errorf("with one populated bin MCE (%.4f) must equal ECE (%.4f)", rep.MCE, rep.ECE)
	}
}

// Underconfidence is the mirror image and must not read as overconfidence.
func TestEvaluate_UnderconfidenceIsNotOverconfidence(t *testing.T) {
	rng := rand.New(rand.NewSource(23))
	rep, err := Evaluate(forecaster(rng, 2000, 0.6, 0.9), 10)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Overconfident() {
		t.Error("stated 0.60 against a 0.90 outcome rate and Overconfident() = true")
	}
	if rep.SignedGap() >= 0 {
		t.Errorf("signed gap = %.4f, want negative", rep.SignedGap())
	}
}

// A forecast of exactly 1.0 sits on the top edge and must land in the last bin
// rather than indexing past the end.
func TestEvaluate_BinEdgesAreClosedAtTheTop(t *testing.T) {
	obs := make([]Observation, MinObservations)
	for i := range obs {
		obs[i] = Observation{Predicted: 1, Correct: true}
	}
	rep, err := Evaluate(obs, 10)
	if err != nil {
		t.Fatalf("a certain forecast must not error: %v", err)
	}
	if len(rep.Bins) != 1 {
		t.Fatalf("populated %d bins, want 1", len(rep.Bins))
	}
	if b := rep.Bins[0]; b.Hi != 1 || b.N != MinObservations {
		t.Errorf("bin = %+v, want the top bin holding every observation", b)
	}
	if rep.Brier != 0 {
		t.Errorf("Brier = %v, want 0 for always-certain-and-always-right", rep.Brier)
	}
}

// Empty bins are skipped rather than rendered as 0% observed, which would draw
// a diagram claiming the forecaster was wrong where it never spoke.
func TestEvaluate_EmptyBinsAreOmitted(t *testing.T) {
	rng := rand.New(rand.NewSource(31))
	obs := append(forecaster(rng, 50, 0.05, 0.05), forecaster(rng, 50, 0.95, 0.95)...)
	rep, err := Evaluate(obs, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Bins) != 2 {
		t.Errorf("populated %d bins, want 2; the eight untouched ones must not appear", len(rep.Bins))
	}
	for i := 1; i < len(rep.Bins); i++ {
		if rep.Bins[i-1].Lo >= rep.Bins[i].Lo {
			t.Errorf("bins not ordered by lower edge: %+v", rep.Bins)
		}
	}
}

func TestEvaluate_RefusesWhatItCannotMeasure(t *testing.T) {
	rng := rand.New(rand.NewSource(41))
	if _, err := Evaluate(forecaster(rng, MinObservations-1, 0.9, 0.9), 10); !errors.Is(err, ErrTooFew) {
		t.Errorf("err = %v, want ErrTooFew: a diagram over a handful of runs is noise", err)
	}
	bad := forecaster(rng, MinObservations, 0.5, 0.5)
	bad[3].Predicted = 1.7
	if _, err := Evaluate(bad, 10); err == nil {
		t.Error("a predicted value outside [0,1] must error rather than be clamped")
	}
	bad[3].Predicted = math.NaN()
	if _, err := Evaluate(bad, 10); err == nil {
		t.Error("NaN must error")
	}
}

// bins <= 0 is a caller that did not choose, not a request for zero bins.
func TestEvaluate_DefaultsTheBinCount(t *testing.T) {
	rng := rand.New(rand.NewSource(53))
	obs := forecaster(rng, 500, 0.5, 0.5)
	for _, bins := range []int{0, -1} {
		rep, err := Evaluate(obs, bins)
		if err != nil {
			t.Fatalf("bins=%d: %v", bins, err)
		}
		if len(rep.Bins) == 0 {
			t.Errorf("bins=%d produced no bins", bins)
		}
		for _, b := range rep.Bins {
			if w := b.Hi - b.Lo; math.Abs(w-1.0/DefaultBins) > 1e-9 {
				t.Errorf("bins=%d gave width %v, want %v", bins, w, 1.0/DefaultBins)
			}
		}
	}
}

// A zero report must not claim anything, since callers render it on the refusal
// path too.
func TestReport_ZeroValueIsInert(t *testing.T) {
	var r Report
	if r.SignedGap() != 0 || r.Overconfident() {
		t.Errorf("zero Report claims a gap of %v", r.SignedGap())
	}
}
