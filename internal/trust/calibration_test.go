// SPDX-License-Identifier: MIT

package trust

import (
	"math"
	"path/filepath"
	"testing"
)

// feed records tp/fp/tn/fn observations for a source.
func feed(t *testing.T, c *Calibrator, source, domain string, tp, fp, tn, fn int) {
	t.Helper()
	for i := 0; i < tp; i++ {
		mustUpdate(t, c, source, domain, true, OutcomeCorrect)
	}
	for i := 0; i < fp; i++ {
		mustUpdate(t, c, source, domain, true, OutcomeIncorrect)
	}
	for i := 0; i < tn; i++ {
		mustUpdate(t, c, source, domain, false, OutcomeIncorrect)
	}
	for i := 0; i < fn; i++ {
		mustUpdate(t, c, source, domain, false, OutcomeCorrect)
	}
}

func mustUpdate(t *testing.T, c *Calibrator, s, d string, said bool, o Outcome) {
	t.Helper()
	if err := c.Update(s, d, said, o); err != nil {
		t.Fatalf("Update: %v", err)
	}
}

// A source right 90% of the time (se=sp=0.9) must converge D to the analytic
// value ln(9)·0.8 ≈ 1.758 nats; a coin-flip source (se=sp=0.5) must converge to 0.
func TestD_ConvergesToAnalytic(t *testing.T) {
	c, _ := New("")

	// 2000 truth-balanced samples, source 90% accurate.
	feed(t, c, "model:good", "go", 900, 100, 900, 100)
	analytic := math.Log(9) * 0.8 // 1.7578…
	if got := c.D("model:good", "go"); math.Abs(got-analytic) > 0.05 {
		t.Errorf("D(90%% source) = %.4f, want ≈%.4f", got, analytic)
	}

	// Coin flip: says correct half the time regardless of truth.
	feed(t, c, "model:coin", "go", 500, 500, 500, 500)
	if got := c.D("model:coin", "go"); math.Abs(got) > 0.01 {
		t.Errorf("D(coin flip) = %.4f, want ≈0", got)
	}
}

func TestLLR_Signs(t *testing.T) {
	c, _ := New("")
	feed(t, c, "s", "d", 900, 100, 900, 100) // se≈sp≈0.9

	pos := c.LLR("s", "d", true)  // ≈ ln(0.9/0.1) = ln 9 ≈ 2.197
	neg := c.LLR("s", "d", false) // ≈ ln(0.1/0.9) = −ln 9

	if math.Abs(pos-math.Log(9)) > 0.05 {
		t.Errorf("LLR(said correct) = %.4f, want ≈%.4f", pos, math.Log(9))
	}
	if math.Abs(neg+math.Log(9)) > 0.05 {
		t.Errorf("LLR(said incorrect) = %.4f, want ≈%.4f", neg, -math.Log(9))
	}
	if pos <= 0 || neg >= 0 {
		t.Errorf("expected +LLR for correct and −LLR for incorrect, got %.3f / %.3f", pos, neg)
	}
}

// An unseen source carries no information: LLR and D are exactly 0.
func TestUnknownSourceIsUninformative(t *testing.T) {
	c, _ := New("")
	if d := c.D("never-seen", "go"); d != 0 {
		t.Errorf("D(unknown) = %v, want 0", d)
	}
	if l := c.LLR("never-seen", "go", true); l != 0 {
		t.Errorf("LLR(unknown) = %v, want 0", l)
	}
}

func TestOutcomeUnknownDoesNotTrain(t *testing.T) {
	c, _ := New("")
	if err := c.Update("s", "d", true, OutcomeUnknown); err != nil {
		t.Fatal(err)
	}
	// No stat row should exist, and D stays 0.
	if len(c.Report()) != 0 {
		t.Errorf("Report has %d rows, want 0 after only-unknown update", len(c.Report()))
	}
}

// Calibration must survive a process restart via jsonl replay.
func TestPersistenceReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "calibration.jsonl")

	c1, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	feed(t, c1, "model:good", "go", 90, 10, 90, 10)
	want := c1.D("model:good", "go")

	// Fresh calibrator over the same file replays the history.
	c2, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	got := c2.D("model:good", "go")
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("D after replay = %.6f, want %.6f (state not persisted)", got, want)
	}
	if n := c2.Report()[0].N; n != 200 {
		t.Errorf("observations after replay = %v, want 200", n)
	}
}

func TestReport_SortedByD(t *testing.T) {
	c, _ := New("")
	feed(t, c, "weak", "go", 60, 40, 60, 40)  // ~0.6 accurate → low D
	feed(t, c, "strong", "go", 95, 5, 95, 5)  // ~0.95 → high D
	rep := c.Report()
	if len(rep) != 2 {
		t.Fatalf("Report rows = %d, want 2", len(rep))
	}
	if rep[0].Source != "strong" {
		t.Errorf("most-diagnostic first: got %q, want strong", rep[0].Source)
	}
	if rep[0].D <= rep[1].D {
		t.Errorf("Report not sorted by D descending: %.3f then %.3f", rep[0].D, rep[1].D)
	}
}
