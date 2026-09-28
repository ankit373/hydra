// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/signals"
)

// The constraint #903 turns on: a machine with no rules must print exactly what
// it printed before signals existed. Asserted rather than claimed in a comment,
// because "byte-identical" reduces to this layer writing nothing at all.
func TestPrintRuleDecision_WritesNothingWhenNoRuleFired(t *testing.T) {
	for _, verbose := range []bool{true, false} {
		var b bytes.Buffer
		printRuleDecision(&b, signals.Decision{
			Action:  signals.Action{Type: signals.ActionFallthrough},
			Signals: signals.Set{"pii.any": false, "graph.blast_radius": float64(3)},
		}, verbose)
		if b.Len() != 0 {
			t.Fatalf("verbose=%v wrote %q; it must write nothing", verbose, b.String())
		}
	}
}

// A fallthrough that a rule chose is still a rule firing, and worth one line.
func TestPrintRuleDecision_NamesTheRuleThatFired(t *testing.T) {
	var b bytes.Buffer
	printRuleDecision(&b, signals.Decision{
		Rule:    "secrets stay local",
		Action:  signals.Action{Type: signals.ActionRoute, LocalOnly: true},
		Signals: signals.Set{"pii.any": true, "pii.ssn.matched": false},
	}, false)

	out := b.String()
	if !strings.Contains(out, "secrets stay local") || !strings.Contains(out, "local-only") {
		t.Fatalf("got %q", out)
	}
	// Not verbose: the signals line is for --dry-run.
	if strings.Contains(out, "signals:") {
		t.Errorf("non-verbose output listed signals: %q", out)
	}
}

// Only the true signals. The full set is every PII detector on every run, which
// buries the line that matters.
func TestPrintRuleDecision_VerboseListsOnlyWhatIsTrue(t *testing.T) {
	var b bytes.Buffer
	printRuleDecision(&b, signals.Decision{
		Rule:   "r",
		Action: signals.Action{Type: signals.ActionBlock, Reason: "no"},
		Signals: signals.Set{
			"pii.any":            true,
			"pii.ssn.matched":    false,
			"injection.matched":  false,
			"graph.blast_radius": float64(42),
		},
	}, true)

	out := b.String()
	if !strings.Contains(out, "pii.any") {
		t.Errorf("a true signal is missing: %q", out)
	}
	if strings.Contains(out, "pii.ssn.matched") || strings.Contains(out, "injection.matched") {
		t.Errorf("a false signal was listed: %q", out)
	}
	// A number is always shown with its value: "false" is meaningful for a
	// bool, but a radius of 0 and an absent radius are different things.
	if !strings.Contains(out, "graph.blast_radius=42") {
		t.Errorf("the number is missing its value: %q", out)
	}
}

func TestDescribeAction(t *testing.T) {
	cases := []struct {
		a    signals.Action
		want string
	}{
		{signals.Action{Type: signals.ActionFallthrough}, "fall through"},
		{signals.Action{Type: signals.ActionRoute, LocalOnly: true}, "local-only"},
		{signals.Action{Type: signals.ActionRoute, Tier: "expert"}, "tier=expert"},
		{signals.Action{Type: signals.ActionRoute, Enum: "SIMPLE"}, "enum=SIMPLE"},
		{signals.Action{Type: signals.ActionRequireConfidence, Value: 0.95}, "95.0%"},
		{signals.Action{Type: signals.ActionBlock, Reason: "needs a human"}, "needs a human"},
	}
	for _, c := range cases {
		if got := describeAction(c.a); !strings.Contains(got, c.want) {
			t.Errorf("%v: got %q, want it to mention %q", c.a.Type, got, c.want)
		}
	}
}
