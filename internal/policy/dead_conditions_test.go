// SPDX-License-Identifier: MIT

package policy

import (
	"reflect"
	"strings"
	"testing"
)

func engineWith(rules ...policyRule) *FilePolicyEngine {
	return &FilePolicyEngine{pf: policyFile{Rules: rules}}
}

// A condition naming a field the evaluator does not know can never be
// satisfied, so its rule never fires. An operator reading the file cannot tell
// that from a rule that simply has not come up yet (#854).
func TestDeadConditions_ReportsAMisspelledField(t *testing.T) {
	e := engineWith(policyRule{
		Name: "strong_tier_prefer_sr_blocks",
		When: map[string]interface{}{"enum_teir_lte": 3},
	})
	dead := e.DeadConditions()
	if len(dead) != 1 {
		t.Fatalf("got %d dead condition(s), want 1: %+v", len(dead), dead)
	}
	if dead[0].Rule != "strong_tier_prefer_sr_blocks" || dead[0].Key != "enum_teir_lte" {
		t.Errorf("dead = %+v, want the rule and key that cannot match", dead[0])
	}
	if !strings.Contains(dead[0].Reason, "enum_teir") {
		t.Errorf("Reason = %q, want it to name the unknown field so the typo is visible", dead[0].Reason)
	}
}

// The half that matters more: every real field must be recognised, or the
// report cries wolf on a working policy and gets ignored.
func TestDeadConditions_EveryRealFieldAndOperatorIsLive(t *testing.T) {
	var rules []policyRule
	for field := range specFieldNames() {
		for _, suffix := range condSuffixes {
			rules = append(rules, policyRule{
				Name: field + suffix,
				When: map[string]interface{}{field + suffix: 1},
			})
		}
		// The bare form, which matchCondition reads as _eq.
		rules = append(rules, policyRule{Name: field, When: map[string]interface{}{field: 1}})
	}
	if dead := engineWith(rules...).DeadConditions(); len(dead) != 0 {
		t.Errorf("real fields reported dead: %+v", dead)
	}
}

// `always` is the deliberate match-everything marker matchWhen skips, not an
// unknown field.
func TestDeadConditions_AlwaysIsNotDead(t *testing.T) {
	e := engineWith(policyRule{Name: "test_caps", When: map[string]interface{}{"always": true}})
	if dead := e.DeadConditions(); len(dead) != 0 {
		t.Errorf("`always` reported dead: %+v", dead)
	}
}

// The shipped policy must not ship a dead rule. This is the regression guard
// for the file itself, not for the detector.
func TestDeadConditions_ShippedPolicyHasNone(t *testing.T) {
	e, err := LoadFilePolicy(t.TempDir()) // no override, so the embedded copy
	if err != nil {
		t.Fatal(err)
	}
	if dead := e.DeadConditions(); len(dead) != 0 {
		t.Errorf("registry/policy.yaml ships %d condition(s) that can never match: %+v", len(dead), dead)
	}
}

// specFieldNames is read off the struct so a new Spec field cannot leave the
// detector calling it unknown. If that ever drifts, every rule naming the new
// field is reported dead, which is the loudest possible wrong answer.
func TestSpecFieldNames_CoversEveryStructField(t *testing.T) {
	names := specFieldNames()
	if got, want := len(names), reflect.TypeOf(Spec{}).NumField(); got != want {
		t.Errorf("specFieldNames has %d entries for %d struct fields: a field with no "+
			"json tag is invisible to the detector", got, want)
	}
	// And each one resolves in specField, or the two disagree about what exists.
	for name := range names {
		if specField(name, Spec{}) == nil {
			t.Errorf("%q is a known field name that specField does not resolve", name)
		}
	}
}
