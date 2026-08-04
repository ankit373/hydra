// SPDX-License-Identifier: MIT

package policy

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ankit373/hydra/internal/testutil"
)

// The policy layer decides whether a prompt may leave the machine and how an
// edit is allowed to proceed. Both fail dangerously in the permissive
// direction, so the tests assert what must be refused, not just what is allowed.

// The PII rule is the one that keeps a prompt containing secrets off a cloud
// head. It must fire when enabled and must not exist when disabled — a rule
// that silently never matches is the same as no rule.
func TestDefaultRules_PIIRuleIsPresentOnlyWhenEnabled(t *testing.T) {
	if got := DefaultRules(false); len(got) != 0 {
		t.Errorf("local-only disabled produced %d rules: %+v", len(got), got)
	}

	rules := DefaultRules(true)
	if len(rules) != 1 {
		t.Fatalf("local-only enabled produced %d rules, want 1", len(rules))
	}
	if rules[0].Name != "pii-local-only" {
		t.Errorf("rule name = %q", rules[0].Name)
	}
	if !rules[0].Apply.LocalOnly {
		t.Error("the PII rule does not set LocalOnly — a prompt with secrets could " +
			"still be sent to a cloud head")
	}
}

// Evaluate returns the FIRST matching rule, and stamps its name as the reason.
// Order matters: a later permissive rule must not override an earlier deny.
func TestEngine_EvaluateReturnsTheFirstMatchWithItsReason(t *testing.T) {
	e := New([]Rule{
		{Name: "first", Condition: func(Request) bool { return true },
			Apply: Action{LocalOnly: true}},
		{Name: "second", Condition: func(Request) bool { return true },
			Apply: Action{LocalOnly: false}},
	})

	got := e.Evaluate(Request{Prompt: "anything"})
	if !got.LocalOnly {
		t.Error("a later rule overrode the first match")
	}
	if got.Reason != "first" {
		t.Errorf("Reason = %q, want the matching rule's name — without it the user "+
			"cannot tell why a dispatch was constrained", got.Reason)
	}
}

// No match is no restriction, not a deny — Hydra routes normally unless a rule
// says otherwise.
func TestEngine_NoMatchIsNoRestriction(t *testing.T) {
	e := New([]Rule{
		{Name: "never", Condition: func(Request) bool { return false },
			Apply: Action{LocalOnly: true}},
	})
	got := e.Evaluate(Request{Prompt: "x"})
	if got.LocalOnly || got.Reason != "" {
		t.Errorf("a non-matching ruleset produced %+v", got)
	}

	// An empty engine behaves the same way.
	if got := New(nil).Evaluate(Request{Prompt: "x"}); got.LocalOnly {
		t.Errorf("an empty engine restricted the request: %+v", got)
	}
}

// End to end: a prompt containing a secret must be constrained to local heads
// by the default ruleset.
func TestEngine_PIIPromptIsForcedLocal(t *testing.T) {
	e := New(DefaultRules(true))

	secret := "here is my key AKIAIOSFODNN7EXAMPLE please use it"
	if got := e.Evaluate(Request{Prompt: secret}); !got.LocalOnly {
		t.Error("a prompt containing an AWS-shaped key was not forced local")
	}
	if got := e.Evaluate(Request{Prompt: "write a function that adds two numbers"}); got.LocalOnly {
		t.Error("an ordinary prompt was forced local — over-triggering pushes every " +
			"task to the weakest head")
	}
}

// ── file policy ──────────────────────────────────────────────────────────────

func TestLoadFilePolicy_UsesTheEmbeddedRegistry(t *testing.T) {
	testutil.NewSandbox(t)

	e, err := LoadFilePolicy("")
	if err != nil {
		t.Fatalf("loading with no on-disk policy failed: %v — this is what every "+
			"installed binary does", err)
	}
	fp := e.Decide(Spec{File: "/repo/a.go", FileExtension: "go"})
	if fp.EditMode == "" {
		t.Error("the decided policy has no edit mode")
	}
	if fp.MaxWallSeconds <= 0 {
		t.Error("the decided policy has no wall-clock cap; an edit could run forever")
	}
}

func TestLoadFilePolicy_MalformedYAMLIsAnError(t *testing.T) {
	s := testutil.NewSandbox(t)
	dir := filepath.Join(s.HydraHome, "registry")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "policy.yaml"), []byte("rules: [oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFilePolicy(s.HydraHome); err == nil {
		t.Error("a malformed policy.yaml loaded without error — edits would run " +
			"under defaults the operator never chose")
	}
}

// Defaults must be safe on their own, because they apply whenever no rule
// matches — which is the common case.
func TestDecide_DefaultsAreSafe(t *testing.T) {
	e := &FilePolicyEngine{}
	fp := e.Decide(Spec{File: "/x/y.go"})

	if fp.EditMode != "rewrite" {
		t.Errorf("default EditMode = %q, want rewrite", fp.EditMode)
	}
	if !fp.EscalateOnFail {
		t.Error("the default does not escalate on failure; a failed edit would stop silently")
	}
	if !fp.TrackTokens {
		t.Error("the default does not track tokens; spend would go unrecorded")
	}
	if fp.DiffSizeCapPct <= 0 || fp.DiffSizeCapPct > 100 {
		t.Errorf("default DiffSizeCapPct = %d, outside 1–100", fp.DiffSizeCapPct)
	}
	if fp.MaxWallSeconds <= 0 {
		t.Errorf("default MaxWallSeconds = %d — an edit could run forever", fp.MaxWallSeconds)
	}
}

// A rule's when-block must match on every documented condition, and a rule that
// does not match must not apply. Getting this wrong applies someone else's
// policy to an edit.
func TestDecide_RuleMatchingByCondition(t *testing.T) {
	cases := []struct {
		name string
		when map[string]interface{}
		spec Spec
		want bool
	}{
		{"always", map[string]interface{}{"always": true}, Spec{}, true},
		{"empty when matches", map[string]interface{}{}, Spec{}, true},
		{"extension match", map[string]interface{}{"file_extension": "go"},
			Spec{FileExtension: "go"}, true},
		{"extension mismatch", map[string]interface{}{"file_extension": "go"},
			Spec{FileExtension: "ts"}, false},
		{"task type match", map[string]interface{}{"task_type": "edit"},
			Spec{TaskType: "edit"}, true},
		{"all conditions must hold", map[string]interface{}{
			"file_extension": "go", "task_type": "edit"},
			Spec{FileExtension: "go", TaskType: "review"}, false},
		{"unknown key never matches", map[string]interface{}{"no_such_field": "x"},
			Spec{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchWhen(tc.when, tc.spec); got != tc.want {
				t.Errorf("matchWhen(%v, %+v) = %v, want %v", tc.when, tc.spec, got, tc.want)
			}
		})
	}
}

// Later rules layer over earlier ones, so an operator can set a broad default
// and then tighten it for a specific case.
func TestDecide_LaterRulesOverrideEarlierOnes(t *testing.T) {
	e := &FilePolicyEngine{pf: policyFile{
		Rules: []policyRule{
			{When: map[string]interface{}{"always": true},
				Apply: map[string]interface{}{"max_retries": 1}},
			{When: map[string]interface{}{"file_extension": "go"},
				Apply: map[string]interface{}{"max_retries": 5}},
		},
	}}

	if got := e.Decide(Spec{FileExtension: "go"}).MaxRetries; got != 5 {
		t.Errorf("MaxRetries = %d for a .go file, want the later rule's 5", got)
	}
	if got := e.Decide(Spec{FileExtension: "ts"}).MaxRetries; got != 1 {
		t.Errorf("MaxRetries = %d for a .ts file, want the broad rule's 1", got)
	}
}

// The when-block operators are how an operator writes "only for big files" or
// "only outside a playbook". Each has to mean what it says: a comparison that
// silently reads as equality would apply a rule to every file.
func TestMatchCondition_EveryOperator(t *testing.T) {
	spec := Spec{
		File: "/repo/internal/a.go", FileLines: 500, FileCount: 3,
		FileExtension: "go", TaskType: "edit", EnumTier: 6,
		Workspace: "hydra", Prompt: "add pagination", PromptLength: 14,
		ContextPct: 40, HasGit: true, InPlaybook: false, StageName: "",
	}
	cases := []struct {
		key  string
		val  interface{}
		want bool
	}{
		// implicit eq
		{"file_extension", "go", true},
		{"file_extension", "ts", false},
		{"has_git", true, true},
		{"in_playbook", false, true},

		// explicit eq / ne
		{"task_type_eq", "edit", true},
		{"task_type_ne", "edit", false},
		{"task_type_ne", "review", true},

		// numeric comparisons
		{"file_lines_gt", 100, true},
		{"file_lines_gt", 1000, false},
		{"file_lines_lt", 1000, true},
		{"file_lines_lt", 100, false},
		{"file_lines_gte", 500, true},
		{"file_lines_lte", 500, true},
		{"file_lines_gte", 501, false},
		{"context_pct_gte", 75, false},

		// numeric comparison against a string, which is how YAML often loads it
		{"file_lines_gt", "100", true},
		{"file_lines_gt", "1000", false},

		// contains
		{"prompt_contains", "pagination", true},
		{"prompt_contains", "authentication", false},
		{"file_contains", "internal", true},

		// present
		{"stage_name_present", true, false},
		{"workspace_present", true, true},

		// in
		{"file_extension_in", []interface{}{"go", "ts"}, true},
		{"file_extension_in", []interface{}{"py", "rb"}, false},

		// an unknown field can never match, rather than matching everything
		{"no_such_field_gt", 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			if got := matchCondition(tc.key, tc.val, spec); got != tc.want {
				t.Errorf("matchCondition(%q, %v) = %v, want %v", tc.key, tc.val, got, tc.want)
			}
		})
	}
}

// toFloat turns a YAML scalar into a number for comparisons. A non-numeric
// value must degrade to 0 with a warning rather than panic — but the warning
// matters, because 0 silently makes "file_lines_gt: many" match every file.
func TestToFloat_EveryScalarShape(t *testing.T) {
	cases := []struct {
		in   interface{}
		want float64
	}{
		{42, 42},
		{int64(42), 42},
		{42.5, 42.5},
		{float32(2.5), 2.5},
		{true, 1},
		{false, 0},
		{"42", 42},
		{"2.5", 2.5},
		{"not-a-number", 0},
		{nil, 0},
		{[]string{"x"}, 0},
	}
	for _, tc := range cases {
		if got := toFloat(tc.in); got != tc.want {
			t.Errorf("toFloat(%#v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// specField is the only bridge from a YAML key to a Spec field. A typo must
// yield nothing rather than a zero value that compares equal to something.
func TestSpecField_KnownAndUnknownFields(t *testing.T) {
	spec := Spec{File: "/a.go", FileLines: 10, FileExtension: "go", EnumTier: 3}

	if got := specField("file_extension", spec); fmtV(got) != "go" {
		t.Errorf("specField(file_extension) = %v", got)
	}
	if got := specField("enum_tier", spec); fmtV(got) != "3" {
		t.Errorf("specField(enum_tier) = %v", got)
	}
	// An unknown field resolves to "" rather than nil, so it is indistinguishable
	// from a present-but-empty one. That is fine for every operator except a
	// literal equality against "": `when: {file_extensionn: ""}` — a typo — would
	// match every spec. Narrow, but a policy rule that matches everything because
	// of a misspelling is the wrong direction to fail in, so it is recorded here
	// rather than left as folklore.
	if got := specField("not_a_field", spec); fmtV(got) != "" {
		t.Errorf("specField(not_a_field) = %v, want the empty value", got)
	}
	if !matchCondition("not_a_field", "", spec) {
		t.Error("a typo'd field compared against \"\" no longer matches; if that was " +
			"fixed deliberately, this test should assert the new behaviour")
	}
}

func fmtV(v interface{}) string { return fmt.Sprintf("%v", v) }
