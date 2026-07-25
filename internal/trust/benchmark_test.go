// SPDX-License-Identifier: MIT

package trust

import "testing"

func TestBenchmark_MeasuredLaw3(t *testing.T) {
	r := Benchmark(5000, 42)

	if len(r.Cases) != 2 {
		t.Fatalf("cases = %d, want 2 (easy, hard)", len(r.Cases))
	}
	easy, hard := r.Cases[0], r.Cases[1]

	// Easy tasks stop sooner than hard ones.
	if easy.MeanSamples >= hard.MeanSamples {
		t.Errorf("easy mean (%.2f) should be < hard mean (%.2f)", easy.MeanSamples, hard.MeanSamples)
	}
	// Blended beats a fixed-5 swarm.
	if r.Blended.SavedPct <= 0 {
		t.Errorf("blended saved %.1f%%, want > 0 vs fixed-%d", r.Blended.SavedPct, r.FixedN)
	}
	// The SPRT holds its confidence target: ≥95% accuracy on every difficulty.
	if easy.Accuracy < 0.95 || hard.Accuracy < 0.95 || r.Blended.Accuracy < 0.95 {
		t.Errorf("accuracy below target: easy=%.4f hard=%.4f blended=%.4f",
			easy.Accuracy, hard.Accuracy, r.Blended.Accuracy)
	}
}

func TestBenchmark_Deterministic(t *testing.T) {
	a := Benchmark(2000, 7)
	b := Benchmark(2000, 7)
	if a.Blended.MeanSamples != b.Blended.MeanSamples || a.Blended.Accuracy != b.Blended.Accuracy {
		t.Error("Benchmark should be deterministic for a fixed seed")
	}
}
