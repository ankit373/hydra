package policy

import (
	"testing"
)

func TestMatchWhen_Empty(t *testing.T) {
	if !matchWhen(nil, Spec{}) {
		t.Fatal("empty when should always match")
	}
}

func TestMatchWhen_Always(t *testing.T) {
	when := map[string]interface{}{"always": true}
	if !matchWhen(when, Spec{}) {
		t.Fatal("always: true should match any spec")
	}
}

func TestMatchCondition_EQ(t *testing.T) {
	spec := Spec{FileExtension: "ts"}
	if !matchCondition("file_extension_eq", "ts", spec) {
		t.Error("eq: ts == ts should match")
	}
	if matchCondition("file_extension_eq", "go", spec) {
		t.Error("eq: ts != go should not match")
	}
}

func TestMatchCondition_GT_LT(t *testing.T) {
	spec := Spec{FileLines: 500}
	if !matchCondition("file_lines_gt", 200, spec) {
		t.Error("500 > 200 should match")
	}
	if matchCondition("file_lines_gt", 500, spec) {
		t.Error("500 > 500 should not match")
	}
	if !matchCondition("file_lines_gte", 500, spec) {
		t.Error("500 >= 500 should match")
	}
	if !matchCondition("file_lines_lt", 1000, spec) {
		t.Error("500 < 1000 should match")
	}
}

func TestMatchCondition_In(t *testing.T) {
	spec := Spec{FileExtension: "ts"}
	in := []interface{}{"ts", "tsx"}
	if !matchCondition("file_extension_in", in, spec) {
		t.Error("ts in [ts, tsx] should match")
	}
	in2 := []interface{}{"go", "sh"}
	if matchCondition("file_extension_in", in2, spec) {
		t.Error("ts not in [go, sh] should not match")
	}
}

func TestMatchCondition_Contains(t *testing.T) {
	spec := Spec{Prompt: "rename the function"}
	if !matchCondition("prompt_contains", "rename", spec) {
		t.Error("contains rename should match")
	}
}

func TestMatchCondition_Bool(t *testing.T) {
	spec := Spec{HasGit: true}
	if !matchCondition("has_git_eq", "true", spec) {
		t.Error("has_git true eq true should match")
	}
}

func TestDecide_DefaultsOnly(t *testing.T) {
	e := &FilePolicyEngine{}
	fp := e.Decide(Spec{})
	if fp.EditMode != "rewrite" {
		t.Errorf("default edit_mode should be rewrite, got %s", fp.EditMode)
	}
	if !fp.TrackTokens {
		t.Error("default track_tokens should be true")
	}
	if fp.DiffSizeCapPct != 90 {
		t.Errorf("default diff_size_cap_pct should be 90, got %d", fp.DiffSizeCapPct)
	}
}

func TestMatchCondition_MultipleConditions(t *testing.T) {
	spec := Spec{FileLines: 1200, HasGit: true}
	when := map[string]interface{}{
		"file_count_gt": 3,
		"has_git_eq":    "true",
	}
	// file_count is 0, not > 3 — should not match
	if matchWhen(when, spec) {
		t.Error("file_count 0 > 3 should not match — all conditions must hold")
	}

	spec2 := Spec{FileCount: 5, HasGit: true}
	if !matchWhen(when, spec2) {
		t.Error("file_count 5 > 3 and has_git true — should match")
	}
}
