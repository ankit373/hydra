// SPDX-License-Identifier: MIT

package trust

import "testing"

func newTestCalibrator(t *testing.T) *Calibrator {
	t.Helper()
	c, err := New("")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// A source nobody has judged must report no evidence, not a prior dressed up
// as one: every cell starts at the Laplace pseudo-count, and counting those
// would hand a brand-new head 2 commitments it never made.
func TestCommitments_UnknownSourceHasNoEvidence(t *testing.T) {
	c := newTestCalibrator(t)
	if correct, total := c.Commitments("ollama/never-seen"); correct != 0 || total != 0 {
		t.Errorf("got %d/%d, want 0/0 for a source with no records", correct, total)
	}

	// One recorded outcome is one commitment, not one plus the prior.
	if err := c.Update("ollama/seen", "go", true, OutcomeCorrect); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if correct, total := c.Commitments("ollama/seen"); correct != 1 || total != 1 {
		t.Errorf("got %d/%d after one correct outcome, want 1/1", correct, total)
	}
}

// Commitments answers "when this head backs an answer, how often is it right",
// so only the said-correct row counts. A head that declined to endorse an
// answer said nothing about its own output either way.
func TestCommitments_CountsOnlyTheAnswersTheSourceBacked(t *testing.T) {
	c := newTestCalibrator(t)
	const src = "ollama/qwen3:0.6b"

	for range 3 {
		if err := c.Update(src, "go", true, OutcomeCorrect); err != nil { // TP
			t.Fatal(err)
		}
	}
	if err := c.Update(src, "go", true, OutcomeIncorrect); err != nil { // FP
		t.Fatal(err)
	}
	// Neither of these is a commitment: the source voted against the answer.
	if err := c.Update(src, "go", false, OutcomeCorrect); err != nil { // FN
		t.Fatal(err)
	}
	if err := c.Update(src, "go", false, OutcomeIncorrect); err != nil { // TN
		t.Fatal(err)
	}

	correct, total := c.Commitments(src)
	if correct != 3 || total != 4 {
		t.Errorf("got %d/%d, want 3/4: only the said-correct row is a commitment", correct, total)
	}
}

// A head ranking has no task and so no domain, so the pooled count is the one
// it can read. Keeping this per-domain would leave every head unmeasured at
// rank time however much history it had.
func TestCommitments_PoolsAcrossDomains(t *testing.T) {
	c := newTestCalibrator(t)
	const src = "ollama/qwen3:0.6b"

	for _, d := range []string{"go", "ts", "sql"} {
		if err := c.Update(src, d, true, OutcomeCorrect); err != nil {
			t.Fatal(err)
		}
		if err := c.Update(src, d, true, OutcomeIncorrect); err != nil {
			t.Fatal(err)
		}
	}
	if correct, total := c.Commitments(src); correct != 3 || total != 6 {
		t.Errorf("got %d/%d across three domains, want 3/6", correct, total)
	}
}

// Pooling must not pool across sources as well, or every head on the machine
// would rank on one shared number.
func TestCommitments_DoesNotPoolAcrossSources(t *testing.T) {
	c := newTestCalibrator(t)
	if err := c.Update("ollama/a", "go", true, OutcomeCorrect); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		if err := c.Update("ollama/b", "go", true, OutcomeIncorrect); err != nil {
			t.Fatal(err)
		}
	}
	if correct, total := c.Commitments("ollama/a"); correct != 1 || total != 1 {
		t.Errorf("source a got %d/%d, want 1/1", correct, total)
	}
	if correct, total := c.Commitments("ollama/b"); correct != 0 || total != 4 {
		t.Errorf("source b got %d/%d, want 0/4", correct, total)
	}
}

// An unknown outcome trains nothing, so it must not inflate the denominator
// either: a commitment nobody judged is not a commitment that failed.
func TestCommitments_IgnoresUnjudgedOutcomes(t *testing.T) {
	c := newTestCalibrator(t)
	const src = "ollama/qwen3:0.6b"
	if err := c.Update(src, "go", true, OutcomeCorrect); err != nil {
		t.Fatal(err)
	}
	if err := c.Update(src, "go", true, OutcomeUnknown); err != nil {
		t.Fatal(err)
	}
	if correct, total := c.Commitments(src); correct != 1 || total != 1 {
		t.Errorf("got %d/%d, want 1/1: an unjudged run is not a failed one", correct, total)
	}
}
