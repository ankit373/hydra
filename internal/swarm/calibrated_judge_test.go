// SPDX-License-Identifier: MIT

package swarm

import (
	"context"
	"testing"

	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/trust"
)

const testDomain = "calibrated-judge-test"

func attemptWith(id, family string, capScore int, output string) Attempt {
	return Attempt{
		Head:   provider.Head{ID: id, Name: id, Provider: family, CapScore: capScore},
		Status: StatusOK,
		Output: output,
	}
}

// seedReliable feeds a calibrator enough correct, self-reported-correct
// observations that se and sp both land well above 0.5 — an informative,
// well-calibrated source, in contrast to one nobody has ever called
// hyctl trust record for (D=0, uninformative prior).
func seedReliable(t *testing.T, cal *trust.Calibrator, source, domain string) {
	t.Helper()
	for i := 0; i < 20; i++ {
		if err := cal.Update(source, domain, true, trust.OutcomeCorrect); err != nil {
			t.Fatalf("seeding calibration for %s: %v", source, err)
		}
	}
	for i := 0; i < 4; i++ {
		if err := cal.Update(source, domain, false, trust.OutcomeIncorrect); err != nil {
			t.Fatalf("seeding calibration for %s: %v", source, err)
		}
	}
}

func newTestCalibrator(t *testing.T) *trust.Calibrator {
	t.Helper()
	cal, err := trust.New("") // in-memory only
	if err != nil {
		t.Fatalf("trust.New: %v", err)
	}
	return cal
}

// A calibrated, historically-reliable low-CapScore source must be able to
// beat an uncalibrated high-CapScore one — this is the entire point of the
// rework: CapScore alone (today's fallback) cannot express "I have never
// seen this source be right before."
func TestCalibratedJudge_CalibratedWinnerOverridesCapScore(t *testing.T) {
	cal := newTestCalibrator(t)
	seedReliable(t, cal, "model:reliable", testDomain)
	// "model:flashy" is left entirely uncalibrated (D=0).

	attempts := []Attempt{
		attemptWith("model:reliable", "acme", 40, "the answer is 42"),
		attemptWith("model:flashy", "other", 99, "the answer is 7"),
	}

	j := newCalibratedJudge(cal, testDomain, nil)
	verdict, err := j.Judge(context.Background(), "what is the answer?", attempts)
	if err != nil {
		t.Fatalf("Judge returned error: %v", err)
	}
	if verdict.WinnerIndex != 0 {
		t.Errorf("WinnerIndex = %d, want 0 (the calibrated source), CapScore-based pick would be 1", verdict.WinnerIndex)
	}
}

// Zero calibration history anywhere must produce an error (not an arbitrary
// pick), so CompositeJudge falls through to LLMJudge/CapScoreJudge — any
// install that has never run `hyctl trust record` gets byte-identical
// behavior to before CalibratedJudge existed.
func TestCalibratedJudge_NoCalibrationData_ErrorsForFallback(t *testing.T) {
	cal := newTestCalibrator(t)
	attempts := []Attempt{
		attemptWith("model:a", "acme", 40, "the answer is 42"),
		attemptWith("model:b", "other", 99, "the answer is 7"),
	}

	j := newCalibratedJudge(cal, testDomain, nil)
	_, err := j.Judge(context.Background(), "prompt", attempts)
	if err == nil {
		t.Fatal("expected an error with zero calibration data, got a verdict — CompositeJudge cannot fall back")
	}
}

// The single-success case must short-circuit without consulting calibration
// at all — mirrors LLMJudge's own trivial case.
func TestCalibratedJudge_SingleSuccess_ShortCircuits(t *testing.T) {
	cal := newTestCalibrator(t)
	attempts := []Attempt{
		attemptWith("model:a", "acme", 40, "the only answer"),
		failedAttempt("model:b"),
	}

	j := newCalibratedJudge(cal, testDomain, nil)
	verdict, err := j.Judge(context.Background(), "prompt", attempts)
	if err != nil {
		t.Fatalf("Judge returned error: %v", err)
	}
	if verdict.WinnerIndex != 0 {
		t.Errorf("WinnerIndex = %d, want 0", verdict.WinnerIndex)
	}
	if verdict.Reason != "only one successful response" {
		t.Errorf("Reason = %q, want the trivial-case message", verdict.Reason)
	}
}

// A repeat vote from an already-counted model family must be discounted:
// two agreeing votes from the SAME family must win by a smaller posterior
// margin than two agreeing votes from DIFFERENT families, holding every
// other input identical. This is trust.CorrelationDiscount doing its job —
// a swarm of near-duplicate models must not out-vote a single dissenter
// through sheer headcount.
func TestCalibratedJudge_SameFamilyRepeatIsDiscounted(t *testing.T) {
	posteriorFor := func(secondFamily string) float64 {
		cal := newTestCalibrator(t)
		seedReliable(t, cal, "model:x1", testDomain)
		seedReliable(t, cal, "model:x2", testDomain)
		seedReliable(t, cal, "model:y1", testDomain)

		attempts := []Attempt{
			attemptWith("model:x1", "acme", 80, "42"),
			attemptWith("model:x2", secondFamily, 80, "42"), // agrees with x1
			attemptWith("model:y1", "other", 80, "43"),      // dissents
		}

		j := newCalibratedJudge(cal, testDomain, nil)
		verdict, err := j.Judge(context.Background(), "prompt", attempts)
		if err != nil {
			t.Fatalf("Judge returned error: %v", err)
		}
		if verdict.WinnerIndex != 0 && verdict.WinnerIndex != 1 {
			t.Fatalf("expected the \"42\" cluster to win regardless of discount, got winner index %d", verdict.WinnerIndex)
		}
		return float64(verdict.Scores[verdict.WinnerIndex])
	}

	sameFamilyPosterior := posteriorFor("acme")   // x2 shares x1's family → discounted
	diffFamilyPosterior := posteriorFor("other2") // x2 is independent → full weight

	if sameFamilyPosterior >= diffFamilyPosterior {
		t.Errorf("same-family posterior = %.0f%%, different-family posterior = %.0f%%; "+
			"a same-family repeat vote must win by a strictly smaller margin than an independent one",
			sameFamilyPosterior, diffFamilyPosterior)
	}
}

func TestRankByCalibratedScore_NoDataFallsBackToCapScoreOrder(t *testing.T) {
	cal := newTestCalibrator(t)
	attempts := []Attempt{
		attemptWith("model:a", "acme", 40, "a"),
		attemptWith("model:b", "other", 90, "b"),
		attemptWith("model:c", "third", 70, "c"),
	}

	rankByCalibratedScore(attempts, cal, testDomain)

	// Fresh install, zero calibration data: must rank exactly like rankByCapScore.
	want := map[string]int{"model:b": 1, "model:c": 2, "model:a": 3}
	for _, a := range attempts {
		if a.Rank != want[a.Head.ID] {
			t.Errorf("%s: Rank = %d, want %d (CapScore-order fallback)", a.Head.ID, a.Rank, want[a.Head.ID])
		}
	}
}

func TestRankByCalibratedScore_PrefersCalibratedOverCapScore(t *testing.T) {
	cal := newTestCalibrator(t)
	seedReliable(t, cal, "model:reliable", testDomain)
	attempts := []Attempt{
		attemptWith("model:reliable", "acme", 40, "a"), // low CapScore, high D
		attemptWith("model:flashy", "other", 99, "b"),  // high CapScore, D=0
	}

	rankByCalibratedScore(attempts, cal, testDomain)

	for _, a := range attempts {
		if a.Head.ID == "model:reliable" && a.Rank != 1 {
			t.Errorf("model:reliable Rank = %d, want 1 — calibration must outrank a bare CapScore lead", a.Rank)
		}
	}
}
