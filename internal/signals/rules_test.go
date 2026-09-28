// SPDX-License-Identifier: MIT

package signals

import (
	"fmt"
	"strings"
	"testing"
)

func mustEngine(t *testing.T, yaml string) *Engine {
	t.Helper()
	e, err := Parse([]byte(yaml), nil)
	if err != nil {
		t.Fatalf("Parse: %v\n%s", err, yaml)
	}
	return e
}

func TestParse_RefusesWhatItCannotCheck(t *testing.T) {
	cases := map[string]string{
		"unknown signal": `
version: 1
rules:
  - name: typo
    when: pii.aws_acess_key_id.matched
    action: {type: route, local_only: true}`,

		"unknown action type": `
version: 1
rules:
  - name: r
    action: {type: teleport}`,

		"no action type": `
version: 1
rules:
  - name: r
    action: {}`,

		"unnamed rule": `
version: 1
rules:
  - priority: 1
    action: {type: fallthrough}`,

		"duplicate rule name": `
version: 1
rules:
  - name: same
    action: {type: fallthrough}
  - name: same
    action: {type: fallthrough}`,

		"route that pins nothing": `
version: 1
rules:
  - name: r
    action: {type: route}`,

		"confidence out of range": `
version: 1
rules:
  - name: r
    action: {type: require_confidence, value: 1.5}`,

		"confidence of zero": `
version: 1
rules:
  - name: r
    action: {type: require_confidence, value: 0}`,

		"block with no reason": `
version: 1
rules:
  - name: r
    action: {type: block}`,

		"malformed expression": `
version: 1
rules:
  - name: r
    when: pii.any &&
    action: {type: fallthrough}`,

		"when is not a condition": `
version: 1
rules:
  - name: r
    when: graph.blast_radius
    action: {type: fallthrough}`,

		"unnamed keyword": `
version: 1
keywords:
  - any: ["x"]`,

		"keyword with no phrases": `
version: 1
keywords:
  - name: empty
    any: []`,

		"keywords colliding after normalisation": `
version: 1
keywords:
  - name: "my set"
    any: ["a"]
  - name: "my-set"
    any: ["b"]`,

		"unsupported version": `
version: 99
rules: []`,
	}
	for name, yaml := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(yaml), nil); err == nil {
				t.Fatal("parsed clean, want a refusal")
			}
		})
	}
}

// The refusal has to name the rule, or an operator with twenty rules is told
// only that one of them is wrong.
func TestParse_ErrorNamesTheOffendingRule(t *testing.T) {
	_, err := Parse([]byte(`
version: 1
rules:
  - name: the bad one
    when: nope.matched
    action: {type: fallthrough}`), nil)
	if err == nil || !strings.Contains(err.Error(), "the bad one") {
		t.Fatalf("got %v, want it to name the rule", err)
	}
}

func TestParse_ExtraValidatorCanRefuse(t *testing.T) {
	yaml := `
version: 1
rules:
  - name: r
    action: {type: route, tier: nonsense}`
	_, err := Parse([]byte(yaml), func(a Action) error {
		if a.Tier == "nonsense" {
			return fmt.Errorf("no such tier")
		}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "no such tier") {
		t.Fatalf("got %v, want the validator's refusal", err)
	}
}

func TestEvaluate_HighestPriorityFirstMatchWins(t *testing.T) {
	e := mustEngine(t, `
version: 1
keywords:
  - name: production
    any: ["prod"]
rules:
  - name: low
    priority: 1
    when: keyword.production.matched
    action: {type: route, local_only: true}
  - name: high
    priority: 100
    when: keyword.production.matched
    action: {type: block, reason: "no"}`)

	d := e.Evaluate(Input{Prompt: "ship to prod"})
	if d.Rule != "high" {
		t.Fatalf("want the higher priority rule, got %q", d.Rule)
	}
	if d.Action.Type != ActionBlock {
		t.Fatalf("got action %q", d.Action.Type)
	}
}

// Equal priorities break on name, so the same file always evaluates the same
// way. Map order here is how routing became a coin flip once before (#765).
func TestEvaluate_EqualPrioritiesAreDeterministic(t *testing.T) {
	yaml := `
version: 1
rules:
  - name: zebra
    priority: 5
    when: pii.any
    action: {type: block, reason: "z"}
  - name: alpha
    priority: 5
    when: pii.any
    action: {type: block, reason: "a"}`
	for range 50 {
		e := mustEngine(t, yaml)
		d := e.Evaluate(Input{Prompt: "key AKIAIOSFODNN7EXAMPLE"})
		if d.Rule != "alpha" {
			t.Fatalf("got %q, want alpha every time", d.Rule)
		}
	}
}

func TestEvaluate_NoMatchFallsThrough(t *testing.T) {
	e := mustEngine(t, `
version: 1
rules:
  - name: only on pii
    when: pii.any
    action: {type: block, reason: "no"}`)
	d := e.Evaluate(Input{Prompt: "write a haiku"})
	if d.Rule != "" || d.Action.Type != ActionFallthrough || d.Fired() {
		t.Fatalf("got %+v", d)
	}
}

// A nil engine is the normal state of a machine with no rules file. It must
// evaluate, not panic, and it must fall through.
func TestEvaluate_NilEngineFallsThrough(t *testing.T) {
	var e *Engine
	d := e.Evaluate(Input{Prompt: "anything"})
	if d.Rule != "" || d.Action.Type != ActionFallthrough {
		t.Fatalf("got %+v", d)
	}
	if len(d.Signals) == 0 {
		t.Error("a nil engine must still collect signals")
	}
	if e.Rules() != nil || e.Signals() != nil || e.Inert() != nil {
		t.Error("a nil engine must report nothing rather than panic")
	}
}

func TestEvaluate_UnconditionalRuleMatches(t *testing.T) {
	e := mustEngine(t, `
version: 1
rules:
  - name: default
    priority: 0
    action: {type: route, local_only: true}`)
	d := e.Evaluate(Input{Prompt: "anything"})
	if d.Rule != "default" || !d.Action.LocalOnly {
		t.Fatalf("got %+v", d)
	}
}

func TestDecision_FiredIsFalseForFallthrough(t *testing.T) {
	e := mustEngine(t, `
version: 1
rules:
  - name: explicit default
    action: {type: fallthrough}`)
	d := e.Evaluate(Input{Prompt: "x"})
	if d.Rule != "explicit default" {
		t.Fatalf("the rule must be reported as matched, got %q", d.Rule)
	}
	if d.Fired() {
		t.Error("a fallthrough match changed nothing, so Fired must be false")
	}
}

func TestInert_ReportsRulesAfterAnUnconditionalOne(t *testing.T) {
	e := mustEngine(t, `
version: 1
rules:
  - name: catch all
    priority: 100
    action: {type: fallthrough}
  - name: never reached
    priority: 50
    when: pii.any
    action: {type: block, reason: "no"}`)
	inert := e.Inert()
	if len(inert) != 1 || !strings.Contains(inert[0], "never reached") {
		t.Fatalf("got %v", inert)
	}
}

func TestInert_EmptyWhenEveryRuleIsReachable(t *testing.T) {
	e := mustEngine(t, `
version: 1
rules:
  - name: a
    priority: 100
    when: pii.any
    action: {type: block, reason: "no"}
  - name: b
    priority: 50
    action: {type: fallthrough}`)
	if got := e.Inert(); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestEngine_ReportsItsRulesAndSignals(t *testing.T) {
	e := mustEngine(t, `
version: 1
keywords:
  - name: production
    any: ["prod"]
rules:
  - name: r
    when: keyword.production.matched && graph.blast_radius > 3
    action: {type: fallthrough}`)

	if got := e.Rules(); len(got) != 1 || got[0].Name != "r" {
		t.Fatalf("got %v", got)
	}
	names := e.Signals()
	if len(names) == 0 {
		t.Fatal("no signals reported")
	}
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Fatalf("signals are not sorted: %v", names)
		}
	}
	ref := e.Rules()[0].Referenced()
	if len(ref) != 2 || ref[0] != SigBlastRadius || ref[1] != KeywordSignal("production") {
		t.Fatalf("got %v", ref)
	}
}

// An empty file and a missing version are both valid: the first install has no
// rules and must behave exactly as it did before rules existed.
func TestParse_EmptyIsValidAndFallsThrough(t *testing.T) {
	for _, yaml := range []string{"", "version: 1\nrules: []\n", "rules: []\n"} {
		e, err := Parse([]byte(yaml), nil)
		if err != nil {
			t.Fatalf("%q: %v", yaml, err)
		}
		if d := e.Evaluate(Input{Prompt: "x"}); d.Rule != "" || d.Action.Type != ActionFallthrough {
			t.Fatalf("%q gave %+v", yaml, d)
		}
	}
}

func TestParse_RejectsMalformedYAML(t *testing.T) {
	if _, err := Parse([]byte("rules: [: :"), nil); err == nil {
		t.Fatal("malformed YAML parsed clean")
	}
}

// The embedded registry file must load and declare nothing, or every install
// starts with routing it never asked for.
func TestLoad_EmbeddedDefaultDeclaresNoRules(t *testing.T) {
	e, err := Load("", nil)
	if err != nil {
		t.Fatalf("the embedded signals.yaml does not load: %v", err)
	}
	if got := e.Rules(); len(got) != 0 {
		t.Fatalf("the shipped file declares %d rule(s): %v", len(got), got)
	}
	if d := e.Evaluate(Input{Prompt: "anything"}); d.Fired() {
		t.Fatalf("the shipped file changed a dispatch: %+v", d)
	}
}
