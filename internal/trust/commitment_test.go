// SPDX-License-Identifier: MIT

package trust

import (
	"os"
	"path/filepath"
	"testing"
)

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

// The domain-scoped read is what routing narrows on, so it must return that
// domain's cell and nothing else.
func TestCommitmentsIn_ReadsOneDomain(t *testing.T) {
	c := newTestCalibrator(t)
	const src = "ollama/qwen3:0.6b"

	for range 4 {
		if err := c.Update(src, "go", true, OutcomeCorrect); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Update(src, "go", true, OutcomeIncorrect); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := c.Update(src, "sql", true, OutcomeIncorrect); err != nil {
			t.Fatal(err)
		}
	}

	if correct, total := c.CommitmentsIn(src, "go"); correct != 4 || total != 5 {
		t.Errorf("go: got %d/%d, want 4/5", correct, total)
	}
	if correct, total := c.CommitmentsIn(src, "sql"); correct != 0 || total != 3 {
		t.Errorf("sql: got %d/%d, want 0/3", correct, total)
	}
	// And the pooled read still covers both, or the leave-one-out prior the
	// ranking builds from would be wrong in the other direction.
	if correct, total := c.Commitments(src); correct != 4 || total != 8 {
		t.Errorf("pooled: got %d/%d, want 4/8", correct, total)
	}
}

// A domain nothing has been recorded under is no evidence, never the prior
// dressed up as evidence.
func TestCommitmentsIn_UnrecordedDomainIsEmpty(t *testing.T) {
	c := newTestCalibrator(t)
	const src = "ollama/qwen3:0.6b"
	if err := c.Update(src, "go", true, OutcomeCorrect); err != nil {
		t.Fatal(err)
	}
	if correct, total := c.CommitmentsIn(src, "rust"); correct != 0 || total != 0 {
		t.Errorf("got %d/%d, want 0/0 for a domain with no rows", correct, total)
	}
}

// The reader has to spell the domain the way every writer does, or it finds an
// empty cell and reports a head as never measured when it has a full history
// (#785). Update normalizes through Domain; so must this.
func TestCommitmentsIn_NormalizesTheDomainLikeTheWriters(t *testing.T) {
	c := newTestCalibrator(t)
	const src = "ollama/qwen3:0.6b"
	// An empty domain is recorded under DefaultDomain by Update.
	if err := c.Update(src, "", true, OutcomeCorrect); err != nil {
		t.Fatal(err)
	}
	if correct, total := c.CommitmentsIn(src, ""); correct != 1 || total != 1 {
		t.Errorf("empty domain: got %d/%d, want 1/1", correct, total)
	}
	if correct, total := c.CommitmentsIn(src, DefaultDomain); correct != 1 || total != 1 {
		t.Errorf("%s: got %d/%d, want 1/1, the same cell by its other spelling", DefaultDomain, correct, total)
	}
}

// #888: writers disagreed about the unspecified domain. `hyctl oracle verify`
// defaults --domain to "" and `hyctl dispatch` to "default", so the same
// unspecified domain produced two cells and readers only ever looked at one.
func TestApply_UnspecifiedDomainIsOneCell(t *testing.T) {
	c := newTestCalibrator(t)
	const src = "verifier:go-test"

	if err := c.Update(src, "", true, OutcomeCorrect); err != nil { // oracle verify
		t.Fatal(err)
	}
	if err := c.Update(src, DefaultDomain, true, OutcomeCorrect); err != nil { // dispatch
		t.Fatal(err)
	}

	if correct, total := c.CommitmentsIn(src, DefaultDomain); correct != 2 || total != 2 {
		t.Errorf("got %d/%d, want 2/2: the two spellings are still separate cells", correct, total)
	}
	// And the report shows one row, not the same source twice under two names.
	var rows int
	for _, st := range c.Report() {
		if st.Source == src {
			rows++
		}
	}
	if rows != 1 {
		t.Errorf("calibration reports %d rows for %s, want 1", rows, src)
	}
}

// A store written before the fix must fold on replay rather than keep both.
func TestLoad_FoldsRowsWrittenUnderTheEmptyDomain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "calibration.jsonl")
	rows := `{"ts":"2026-01-01T00:00:00Z","source":"s","domain":"","said_correct":true,"outcome":1}
{"ts":"2026-01-01T00:00:01Z","source":"s","domain":"default","said_correct":true,"outcome":1}
`
	if err := os.WriteFile(path, []byte(rows), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if correct, total := c.CommitmentsIn("s", DefaultDomain); correct != 2 || total != 2 {
		t.Errorf("got %d/%d after replay, want 2/2 folded into one cell", correct, total)
	}
}
