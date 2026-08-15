// SPDX-License-Identifier: MIT

package trust

import (
	"math"
	"path/filepath"
	"testing"
)

func TestClusterByAgreement_GroupsByEquivalence(t *testing.T) {
	got := ClusterByAgreement([]string{"a", "a", "b", "a"}, TextEquivalence)
	if len(got) != 2 {
		t.Fatalf("got %d groups, want 2: %+v", len(got), got)
	}
	if len(got[0]) != 3 || len(got[1]) != 1 {
		t.Errorf("group sizes = %v, want [3 1]", []int{len(got[0]), len(got[1])})
	}
}

func TestClusterByAgreement_NilEquivDefaultsToText(t *testing.T) {
	got := ClusterByAgreement([]string{"same", "SAME"}, nil)
	if len(got) != 1 {
		t.Errorf("got %d groups, want 1 (case-insensitive default)", len(got))
	}
}

// A run with fewer than two familied sources carries no correlation signal
// and must not be recorded — an empty Family means "independent, nothing to
// discount," so there is nothing to measure.
func TestRecordCoAgreement_SkipsUnfamiliedSourcesAndTinyRuns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coagreement.jsonl")

	RecordCoAgreement(path, "go", []string{"a"}, []string{"fam"}, []string{"x"}, TextEquivalence)
	if got := loadCoAgreement(path); len(got) != 0 {
		t.Errorf("a single-source run was recorded: %+v", got)
	}

	RecordCoAgreement(path, "go", []string{"a", "b"}, []string{"", ""}, []string{"x", "y"}, TextEquivalence)
	if got := loadCoAgreement(path); len(got) != 0 {
		t.Errorf("a run with no familied sources was recorded: %+v", got)
	}

	RecordCoAgreement(path, "go", []string{"a", "b"}, []string{"famA", "famA"}, []string{"x", "x"}, TextEquivalence)
	if got := loadCoAgreement(path); len(got) != 1 {
		t.Errorf("a real two-familied-source run was not recorded: %+v", got)
	}
}

// loadCoAgreement caches its result per path — an SPRT run or swarm judge may
// call FamilyDiscount/FamilyCoupling several times against the same file
// while it's stable — but a real append (the next run's RecordCoAgreement)
// must never be masked by a stale cache entry.
func TestLoadCoAgreement_CacheInvalidatesOnRealAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coagreement.jsonl")

	RecordCoAgreement(path, "go", []string{"a", "b"}, []string{"famA", "famA"}, []string{"x", "x"}, TextEquivalence)
	if got := loadCoAgreement(path); len(got) != 1 {
		t.Fatalf("got %d record(s) after first append, want 1", len(got))
	}

	RecordCoAgreement(path, "python", []string{"c", "d", "e"}, []string{"famB", "famB", "famB"}, []string{"x", "x", "y"}, TextEquivalence)
	if got := loadCoAgreement(path); len(got) != 2 {
		t.Errorf("got %d record(s) after second append, want 2 — the cache served a stale read", len(got))
	}
}

func TestFamilyCoupling_BelowThresholdIsNotOK(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coagreement.jsonl")
	for i := 0; i < minCoAgreementSamples-1; i++ {
		RecordCoAgreement(path, "go", []string{"a", "b"}, []string{"fam", "fam"}, []string{"x", "x"}, TextEquivalence)
	}
	if _, ok := FamilyCoupling(path, "fam"); ok {
		t.Error("FamilyCoupling reported ok below minCoAgreementSamples")
	}
	if got := FamilyDiscount(path, "fam"); got != defaultCorrelationDiscount {
		t.Errorf("FamilyDiscount = %v below threshold, want the flat default %v", got, defaultCorrelationDiscount)
	}
}

// Two same-family sources that always echo each other, alongside a
// different-family source that never agrees with either, is the textbook
// "these two are one vote" case — J must measure close to 1.
func TestFamilyCoupling_HighExcessAgreementMeasuresNearOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coagreement.jsonl")
	for i := 0; i < minCoAgreementSamples; i++ {
		RecordCoAgreement(path, "go",
			[]string{"a", "b", "c"},
			[]string{"famX", "famX", "famY"},
			[]string{"same-answer", "same-answer", "different-answer"},
			TextEquivalence)
	}
	j, ok := FamilyCoupling(path, "famX")
	if !ok {
		t.Fatal("FamilyCoupling reported not-ok with plenty of samples")
	}
	if j < 0.9 {
		t.Errorf("J = %.3f, want close to 1 for a family that always agrees while cross-family never does", j)
	}
	if discount := FamilyDiscount(path, "famX"); discount > 0.15 {
		t.Errorf("FamilyDiscount = %.3f, want close to 0 for a near-perfectly-coupled family", discount)
	}
	if _, warn := FalseConsensusWarning(path, "famX"); !warn {
		t.Error("a family measured at J≈1 did not trigger the false-consensus warning")
	}
}

// Same-family and cross-family pairs agreeing at the same rate is the
// textbook "not actually correlated" case — J must measure close to 0, and
// the discount must be close to 1 (no discount).
func TestFamilyCoupling_NoExcessAgreementMeasuresNearZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coagreement.jsonl")
	// Half of each round's same-family pair agrees, matching the rate at
	// which the third (different-family) source also happens to agree.
	for i := 0; i < minCoAgreementSamples; i++ {
		bAnswer := "x"
		if i%2 == 0 {
			bAnswer = "y" // disagrees with a half the time
		}
		cAnswer := "x"
		if i%2 == 0 {
			cAnswer = "y" // the cross-family source tracks the same rate
		}
		RecordCoAgreement(path, "go",
			[]string{"a", "b", "c"},
			[]string{"famX", "famX", "famY"},
			[]string{"x", bAnswer, cAnswer},
			TextEquivalence)
	}
	j, ok := FamilyCoupling(path, "famX")
	if !ok {
		t.Fatal("FamilyCoupling reported not-ok with plenty of samples")
	}
	if j > 0.1 {
		t.Errorf("J = %.3f, want close to 0 when same-family agreement matches cross-family baseline", j)
	}
	if discount := FamilyDiscount(path, "famX"); discount < 0.9 {
		t.Errorf("FamilyDiscount = %.3f, want close to 1 (no discount) for an uncorrelated family", discount)
	}
	if _, warn := FalseConsensusWarning(path, "famX"); warn {
		t.Error("an uncorrelated family triggered the false-consensus warning")
	}
}

func TestKnownFamilies_ReturnsDistinctSortedNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coagreement.jsonl")
	RecordCoAgreement(path, "go", []string{"a", "b"}, []string{"zeta", "alpha"}, []string{"x", "y"}, TextEquivalence)
	RecordCoAgreement(path, "go", []string{"a", "b"}, []string{"zeta", "alpha"}, []string{"x", "y"}, TextEquivalence)

	got := KnownFamilies(path)
	if len(got) != 2 || got[0] != "alpha" || got[1] != "zeta" {
		t.Errorf("KnownFamilies = %v, want [alpha zeta]", got)
	}
}

// A missing log must degrade to "not enough data," never a panic or error.
func TestFamilyCoupling_MissingLogIsGracefullyNotOK(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.jsonl")
	if _, ok := FamilyCoupling(path, "anything"); ok {
		t.Error("a missing co-agreement log reported ok")
	}
	if got := FamilyDiscount(path, "anything"); math.Abs(got-defaultCorrelationDiscount) > 1e-9 {
		t.Errorf("FamilyDiscount = %v, want the flat default with no log at all", got)
	}
}
