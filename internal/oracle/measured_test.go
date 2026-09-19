// SPDX-License-Identifier: MIT

package oracle

import (
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/trust"
)

// LLR always returns a number, because the priors always yield one. Measured
// is what separates that number from one anybody has observed.
func TestMeasured_TellsAPriorFromAMeasurement(t *testing.T) {
	cal, err := trust.New("")
	if err != nil {
		t.Fatal(err)
	}
	const src = "verifier:grounding"

	if ok, why := Measured(cal, src, "go"); ok || !strings.Contains(why, "nothing recorded") {
		t.Errorf("an unobserved source read as measured: ok=%v why=%q", ok, why)
	}

	// #771's shape: positives accumulate, specificity stays pinned, and the
	// LLR looks like evidence arriving when it is not.
	for range 5 {
		if err := cal.Update(src, "go", true, trust.OutcomeCorrect); err != nil {
			t.Fatal(err)
		}
	}
	ok, why := Measured(cal, src, "go")
	if ok {
		t.Error("five positives and no negative read as measured")
	}
	if !strings.Contains(why, "no negative") {
		t.Errorf("why = %q, want it to name the missing negative", why)
	}

	if err := cal.Update(src, "go", false, trust.OutcomeIncorrect); err != nil {
		t.Fatal(err)
	}
	if ok, why := Measured(cal, src, "go"); !ok {
		t.Errorf("a source with both verdicts read as unmeasured: %q", why)
	}
}

// The store files an unspecified domain under "default". Comparing a caller's
// raw "" against that is the half-normalized key, where a source with a full
// history reads as never observed (#888).
func TestMeasured_NormalizesTheDomainTheWayTheStoreDoes(t *testing.T) {
	cal, err := trust.New("")
	if err != nil {
		t.Fatal(err)
	}
	const src = "verifier:grounding"
	if err := cal.Update(src, "", true, trust.OutcomeCorrect); err != nil {
		t.Fatal(err)
	}
	if err := cal.Update(src, "", false, trust.OutcomeIncorrect); err != nil {
		t.Fatal(err)
	}

	for _, domain := range []string{"", "  ", trust.DefaultDomain} {
		if ok, why := Measured(cal, src, domain); !ok {
			t.Errorf("domain %q read as unmeasured: %s", domain, why)
		}
	}
}

// A nil store is not a measurement either, and asking it must not panic.
func TestMeasured_NilStore(t *testing.T) {
	if ok, why := Measured(nil, "verifier:x", "go"); ok || why == "" {
		t.Errorf("ok=%v why=%q", ok, why)
	}
}
