// SPDX-License-Identifier: MIT

package trust

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// coRecords is 30 tasks where two "fam" sources agree on 24 and an "other"
// source agrees with both on 6, so fam's coupling is measured, not defaulted.
func coRecords(add func(domain string, ids, fams, texts []string)) {
	for i := range 30 {
		a, b, c := "x", "x", "y"
		if i%5 == 0 {
			b = "z"
		}
		if i%5 == 1 {
			c = "x"
		}
		add("d", []string{"a", "b", "c"}, []string{"fam", "fam", "other"}, []string{a, b, c})
	}
}

func TestCoAgreement_DiscountMatchesFamilyDiscount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coagreement.jsonl")
	var mem CoAgreement
	coRecords(func(d string, ids, fams, texts []string) {
		RecordCoAgreement(path, d, ids, fams, texts, TextEquivalence)
		mem.Add(d, ids, fams, texts, TextEquivalence)
	})
	// One more task with a single familied source records nothing either way.
	RecordCoAgreement(path, "d", []string{"a", "q"}, []string{"fam", ""}, []string{"x", "x"}, TextEquivalence)
	mem.Add("d", []string{"a", "q"}, []string{"fam", ""}, []string{"x", "x"}, TextEquivalence)
	for _, fam := range []string{"fam", "other", "none"} {
		if got, want := mem.Discount(fam), FamilyDiscount(path, fam); got != want {
			t.Errorf("%s: in memory %v, from file %v", fam, got, want)
		}
	}
	// fam: 24 of 30 same-family pairs agree and 12 of 60 cross pairs: J = 0.8 - 0.2.
	if d := mem.Discount("fam"); d < 0.4-1e-12 || d > 0.4+1e-12 {
		t.Fatalf("fam discount %v, want 1 - (0.8 - 0.2) = 0.4", d)
	}
	mem.Add("d", []string{"a", "b"}, []string{"fam", "fam"}, []string{"x", "y"}, TextEquivalence)
	if d := mem.Discount("fam"); d > 0.4-1e-9 && d < 0.4+1e-9 {
		t.Fatal("Add did not clear the cached discount")
	}
}

func TestRun_WithCoAgreementReadsMemoryAndWritesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HYDRA_HOME", home)
	cal, _ := New("")
	calibrateSymmetric(cal, "s", "d", 0.9, 200)
	run := func(opts ...RunOption) {
		t.Helper()
		if _, err := Run(context.Background(), Task{Domain: "d"}, nSources("s", 2, 0.01),
			&scriptExec{seq: []string{"x", "x"}}, cal, Target{Confidence: 0.95}, opts...); err != nil {
			t.Fatal(err)
		}
	}
	run(WithCoAgreement(&CoAgreement{}))
	if _, err := os.Stat(DefaultCoAgreementPath()); !os.IsNotExist(err) {
		t.Fatalf("WithCoAgreement wrote %s (%v)", DefaultCoAgreementPath(), err)
	}
	two := []Source{{ID: "s", Family: "f", EstCostUSD: 0.01}, {ID: "s", Family: "f", EstCostUSD: 0.01}}
	if _, err := Run(context.Background(), Task{Domain: "d"}, two,
		&scriptExec{seq: []string{"x", "x"}}, cal, Target{Confidence: 0.95}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(DefaultCoAgreementPath()); err != nil {
		t.Fatalf("Run with no options no longer records co-agreement: %v", err)
	}
}
