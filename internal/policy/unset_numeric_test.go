// SPDX-License-Identifier: MIT

package policy

import "testing"

// A Spec that never carried a field compares as 0, and 0 satisfies every _lt
// and _lte rule written for the low end of the range. So an untiered task
// matched rules meant for the strongest tiers, and a dispatch with no file
// matched rules meant for small files (#848).

func TestMatchCondition_UnknownTierMatchesNeitherDirection(t *testing.T) {
	spec := Spec{} // no tier: nothing routed yet, or the caller has no enum
	for _, c := range []struct {
		key string
		val interface{}
	}{
		{"enum_tier_lte", 3},
		{"enum_tier_lt", 3},
		{"enum_tier_gte", 0},
		{"enum_tier_gt", -1},
	} {
		if matchCondition(c.key, c.val, spec) {
			t.Errorf("%s: %v matched an unknown tier; unknown satisfies no comparison", c.key, c.val)
		}
	}
}

// The half that must keep working: a real tier compares exactly as before, or
// the fix has turned every tier rule off rather than fixing one.
func TestMatchCondition_RealTierStillCompares(t *testing.T) {
	spec := Spec{EnumTier: 3}
	if !matchCondition("enum_tier_lte", 3, spec) {
		t.Error("enum_tier_lte 3 did not match tier 3")
	}
	if !matchCondition("enum_tier_gte", 1, spec) {
		t.Error("enum_tier_gte 1 did not match tier 3")
	}
	if matchCondition("enum_tier_lt", 3, spec) {
		t.Error("enum_tier_lt 3 matched tier 3")
	}
}

// Same shape for the file fields, which hyctl dispatch does not supply at all.
func TestMatchCondition_NoFileMatchesNoFileSizeRule(t *testing.T) {
	spec := Spec{Prompt: "explain this"} // a bare dispatch: no file anywhere
	if matchCondition("file_lines_lt", 50, spec) {
		t.Error("file_lines_lt 50 matched a spec with no file")
	}
	if matchCondition("file_count_lte", 2, spec) {
		t.Error("file_count_lte 2 matched a spec with no file")
	}
}

func TestMatchCondition_RealFileStillCompares(t *testing.T) {
	spec := Spec{File: "main.go", FileLines: 20, FileCount: 1}
	if !matchCondition("file_lines_lt", 50, spec) {
		t.Error("file_lines_lt 50 did not match a 20-line file")
	}
	if !matchCondition("file_count_lte", 2, spec) {
		t.Error("file_count_lte 2 did not match a single file")
	}
}

// prompt_length and context_pct are deliberately left alone: 0 is a real value
// for both. Pinning that here so the next person changing numericUnset has to
// decide rather than extend it by reflex.
func TestMatchCondition_ZeroIsRealForPromptLengthAndContextPct(t *testing.T) {
	spec := Spec{}
	if !matchCondition("prompt_length_lte", 10, spec) {
		t.Error("prompt_length_lte 10 did not match length 0, which is a real length")
	}
	if !matchCondition("context_pct_lt", 75, spec) {
		t.Error("context_pct_lt 75 did not match 0%, which is real context pressure")
	}
}

// A misspelled field name is the same defect wearing a different hat:
// specField answers "" for it, which compares as 0. The existing coverage case
// only tried `no_such_field_gt: 1`, which was false for the same accidental
// reason the real bug hid behind, that 0 > 1 happens not to hold.
func TestMatchCondition_MisspelledFieldMatchesNothing(t *testing.T) {
	spec := Spec{File: "main.go", FileLines: 20, FileCount: 1, EnumTier: 3}
	for _, key := range []string{"enum_teir_lte", "no_such_field_lt", "no_such_field_lte"} {
		if matchCondition(key, 100, spec) {
			t.Errorf("%s matched; a typo must refuse rather than match everything", key)
		}
	}
}

// A non-numeric field compared numerically is the third face of it: workspace
// is a string, so toFloat reads it as 0 and every _lt rule holds.
func TestMatchCondition_StringFieldComparedNumericallyMatchesNothing(t *testing.T) {
	spec := Spec{Workspace: "hydra-self"}
	if matchCondition("workspace_lt", 5, spec) {
		t.Error("workspace_lt 5 matched a string field read as 0")
	}
}

// The end-to-end shape, through the engine rather than one condition: the rule
// that bit hyctl edit in the wild.
func TestEngine_UntieredEditDoesNotGetTheStrongTierEditMode(t *testing.T) {
	e := &FilePolicyEngine{pf: policyFile{
		Defaults: map[string]interface{}{"edit_mode": "rewrite"},
		Rules: []policyRule{{
			Name:  "strong_tier_prefer_sr_blocks",
			When:  map[string]interface{}{"enum_tier_lte": 3, "file_lines_gt": 200},
			Apply: map[string]interface{}{"edit_mode": "sr_blocks"},
		}},
	}}

	untiered := e.Decide(Spec{File: "main.go", FileLines: 300, FileCount: 1})
	if untiered.EditMode != "rewrite" {
		t.Errorf("EditMode = %q with no tier, want rewrite: a 1b local head cannot "+
			"hold SR-block markers, and nothing said a strong tier would run", untiered.EditMode)
	}

	tiered := e.Decide(Spec{File: "main.go", FileLines: 300, FileCount: 1, EnumTier: 2})
	if tiered.EditMode != "sr_blocks" {
		t.Errorf("EditMode = %q at tier 2, want sr_blocks: the rule must still fire "+
			"when a tier is actually known", tiered.EditMode)
	}
}
