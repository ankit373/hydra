package entropy

import (
	"math/rand"
	"strings"
	"testing"
)

// variedText returns n bytes of high-entropy printable content (deterministic).
func variedText(n int) string {
	rng := rand.New(rand.NewSource(1))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(32 + rng.Intn(95)) // printable ASCII
	}
	return string(b)
}

func TestSignalDensity_DenseVsRedundant(t *testing.T) {
	redundant := strings.Repeat("the quick brown fox. ", 20000) // ~420 KB, highly repetitive
	dense := variedText(40000)                                  // 40 KB, near-random

	rRed := SignalDensity(redundant)
	rDense := SignalDensity(dense)

	if rRed >= rDense {
		t.Errorf("redundant ρ (%.3f) should be far below dense ρ (%.3f)", rRed, rDense)
	}
	if rRed > 0.1 {
		t.Errorf("repetitive text ρ = %.3f, expected < 0.1", rRed)
	}
	if rDense < 0.5 {
		t.Errorf("varied text ρ = %.3f, expected > 0.5", rDense)
	}
}

func TestSignalDensity_Empty(t *testing.T) {
	if got := SignalDensity(""); got != 0 {
		t.Errorf("SignalDensity(empty) = %v, want 0", got)
	}
}

// Law 5: a bloated, low-density window carries fewer useful tokens than a much
// smaller high-density one.
func TestUsefulTokens_Law5Ordering(t *testing.T) {
	bloated := strings.Repeat("boilerplate config line = true\n", 30000) // ~930 KB, tiny ρ
	curated := variedText(50000)                                         // 50 KB, high ρ

	if TokenEstimate(bloated) <= TokenEstimate(curated) {
		t.Fatal("test setup: bloated should be far longer than curated")
	}
	if UsefulTokens(curated) <= UsefulTokens(bloated) {
		t.Errorf("curated useful (%.0f) should exceed bloated useful (%.0f) despite being %d× shorter",
			UsefulTokens(curated), UsefulTokens(bloated),
			TokenEstimate(bloated)/TokenEstimate(curated))
	}
}

func TestGovernor_Assess(t *testing.T) {
	g := Governor{}
	redundant := strings.Repeat("x = 1\n", 5000)
	dense := variedText(20000)

	if rec := g.Assess(redundant); !rec.Compact {
		t.Errorf("low-density context should recommend compaction (ρ=%.3f)", rec.Snap.Density)
	}
	if rec := g.Assess(dense); rec.Compact {
		t.Errorf("high-density context should not recommend compaction (ρ=%.3f)", rec.Snap.Density)
	}
}

func TestRegressed(t *testing.T) {
	prev := Snapshot{Tokens: 100, UsefulTokens: 80}
	// Grew in length but useful tokens fell → regressed.
	if !Regressed(prev, Snapshot{Tokens: 200, UsefulTokens: 75}) {
		t.Error("bigger + less useful should be a regression")
	}
	// Grew and added useful info → not a regression.
	if Regressed(prev, Snapshot{Tokens: 200, UsefulTokens: 120}) {
		t.Error("bigger + more useful is not a regression")
	}
	// Shrank → not a regression by this definition.
	if Regressed(prev, Snapshot{Tokens: 50, UsefulTokens: 40}) {
		t.Error("shrinking is not a regression")
	}
}

func TestMeasure(t *testing.T) {
	snap := Measure(variedText(8000))
	if snap.Tokens != 2000 { // 8000/4
		t.Errorf("Tokens = %d, want 2000", snap.Tokens)
	}
	if snap.Density <= 0 || snap.Density > 1 {
		t.Errorf("Density = %v, want (0,1]", snap.Density)
	}
	if snap.UsefulTokens <= 0 {
		t.Errorf("UsefulTokens = %v, want > 0", snap.UsefulTokens)
	}
}
