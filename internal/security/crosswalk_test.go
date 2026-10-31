// SPDX-License-Identifier: MIT

package security

import (
	"sort"
	"testing"
)

// The crosswalk is the part of this package most likely to be quietly wrong,
// so the invariant worth a test is the honesty one: nothing here is measured,
// and nothing may render as though it were.

func TestCrosswalk_EveryEntryIsMarkedCurated(t *testing.T) {
	for class := range crosswalkTable {
		refs := crosswalk(class)
		if len(refs) == 0 {
			t.Errorf("class %q is in the table but crosswalk() returned nothing", class)
		}
		for _, f := range refs {
			if !f.Curated {
				t.Errorf("%s → %s/%s is not marked Curated", class, f.Framework, f.Control)
			}
			if f.Framework == "" || f.Control == "" {
				t.Errorf("%s has an incomplete ref: framework=%q control=%q", class, f.Framework, f.Control)
			}
		}
	}
}

// crosswalk() copies before stamping Curated; if it stamped the table entries
// in place the source data would be mutated by reading it.
func TestCrosswalk_DoesNotMutateTheTable(t *testing.T) {
	_ = crosswalk(ClassExposure)
	for _, f := range crosswalkTable[ClassExposure] {
		if f.Curated {
			t.Fatal("crosswalk() wrote Curated back into crosswalkTable, the table is shared state")
		}
	}
}

func TestCrosswalk_UnknownClassMapsToNothing(t *testing.T) {
	if refs := crosswalk(RiskClass("not-a-class")); refs != nil {
		t.Errorf("got %d refs for an unknown class, want nil, a missing mapping must not be invented", len(refs))
	}
}

func TestFrameworks_IsTheSortedUniqueSetFromTheTable(t *testing.T) {
	got := Frameworks()
	if len(got) == 0 {
		t.Fatal("Frameworks() returned nothing")
	}
	if !sort.StringsAreSorted(got) {
		t.Errorf("Frameworks() = %v, want sorted", got)
	}
	seen := map[string]bool{}
	for _, f := range got {
		if seen[f] {
			t.Errorf("Frameworks() repeated %q", f)
		}
		seen[f] = true
	}
	// Every framework named anywhere in the table must be reachable, or a
	// per-framework rollup silently drops a standard.
	for class, refs := range crosswalkTable {
		for _, f := range refs {
			if !seen[f.Framework] {
				t.Errorf("%s references %q but Frameworks() omits it", class, f.Framework)
			}
		}
	}
}

func TestFrameworkExposure_CountsOpenRisksOnly(t *testing.T) {
	reg := RiskRegister{Risks: []Risk{
		{ID: "R-1", Status: StatusOpen, Frameworks: crosswalk(ClassExposure)},
		{ID: "R-2", Status: StatusMitigated, Frameworks: crosswalk(ClassExposure)},
	}}

	got := FrameworkExposure(reg)
	if got["OWASP LLM"] != 1 {
		t.Errorf("OWASP LLM = %d, want 1, a mitigated risk is not open exposure", got["OWASP LLM"])
	}
	for f, n := range got {
		if n != 1 {
			t.Errorf("%s = %d, want 1", f, n)
		}
	}
}

func TestFrameworkExposure_EmptyRegisterIsEmpty(t *testing.T) {
	if got := FrameworkExposure(RiskRegister{}); len(got) != 0 {
		t.Errorf("got %v, want no entries for an empty register", got)
	}
}
