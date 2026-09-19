// SPDX-License-Identifier: MIT

package dispatch

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/signals"
	"github.com/ankit373/hydra/internal/trust"
)

// The other half of #903's byte-identity constraint: a fallthrough decision
// must leave every option exactly as the caller set it.
func TestApplyDecision_FallthroughChangesNothing(t *testing.T) {
	want := Options{TierHint: "8", Enum: "SIMPLE", LocalOnly: false}
	got := want
	for _, d := range []signals.Decision{
		{},
		{Action: signals.Action{Type: signals.ActionFallthrough}},
		{Rule: "explicit default", Action: signals.Action{Type: signals.ActionFallthrough}},
	} {
		if err := applyDecision(d, &got); err != nil {
			t.Fatalf("%+v: %v", d, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%+v mutated options: %+v want %+v", d, got, want)
		}
	}
}

func TestApplyDecision_RoutePinsWhatTheCallerLeftOpen(t *testing.T) {
	opts := Options{}
	dec := signals.Decision{Rule: "r", Action: signals.Action{
		Type: signals.ActionRoute, Tier: "expert", Enum: "COMPLEX", LocalOnly: true,
	}}
	if err := applyDecision(dec, &opts); err != nil {
		t.Fatal(err)
	}
	if opts.TierHint != "expert" || opts.Enum != "COMPLEX" || !opts.LocalOnly {
		t.Fatalf("got %+v", opts)
	}
}

// A rule is a default for the dispatches nobody spoke about, not an override of
// the ones they did: an explicit flag wins.
func TestApplyDecision_ExplicitFlagsWinOverARule(t *testing.T) {
	opts := Options{TierHint: "1", Enum: "EXPERT"}
	dec := signals.Decision{Rule: "r", Action: signals.Action{
		Type: signals.ActionRoute, Tier: "10", Enum: "SIMPLE",
	}}
	if err := applyDecision(dec, &opts); err != nil {
		t.Fatal(err)
	}
	if opts.TierHint != "1" || opts.Enum != "EXPERT" {
		t.Fatalf("a rule overrode an explicit flag: %+v", opts)
	}
}

// local_only is the exception, and deliberately so: it only ever tightens.
// A rule that keeps secrets on the machine must not be overridable by omission.
func TestApplyDecision_LocalOnlyOnlyTightens(t *testing.T) {
	opts := Options{LocalOnly: false}
	dec := signals.Decision{Rule: "r", Action: signals.Action{
		Type: signals.ActionRoute, LocalOnly: true,
	}}
	if err := applyDecision(dec, &opts); err != nil {
		t.Fatal(err)
	}
	if !opts.LocalOnly {
		t.Fatal("a local-only rule did not apply")
	}

	// And a rule that does not ask for it must not loosen one already set.
	opts = Options{LocalOnly: true}
	dec.Action.LocalOnly = false
	if err := applyDecision(dec, &opts); err != nil {
		t.Fatal(err)
	}
	if !opts.LocalOnly {
		t.Fatal("a rule loosened local-only")
	}
}

func TestApplyDecision_BlockRefuses(t *testing.T) {
	opts := Options{}
	dec := signals.Decision{Rule: "no prod", Action: signals.Action{
		Type: signals.ActionBlock, Reason: "a production migration needs a human",
	}}
	err := applyDecision(dec, &opts)
	if err == nil {
		t.Fatal("a block action did not refuse")
	}
	var blocked *ErrBlocked
	if !errors.As(err, &blocked) {
		t.Fatalf("want ErrBlocked, got %T", err)
	}
	if !strings.Contains(err.Error(), "no prod") || !strings.Contains(err.Error(), "needs a human") {
		t.Errorf("the refusal must name the rule and the reason: %q", err)
	}
}

// require_confidence is applied where the stopping rule is read, not here. It
// must be a no-op rather than silently dropped into one of the other branches.
func TestApplyDecision_RequireConfidenceIsNotDispatchsToApply(t *testing.T) {
	want := Options{TierHint: "8"}
	got := want
	dec := signals.Decision{Rule: "r", Action: signals.Action{
		Type: signals.ActionRequireConfidence, Value: 0.95,
	}}
	if err := applyDecision(dec, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mutated options: %+v want %+v", got, want)
	}
}

// A dispatcher with no engine still decides, and decides to change nothing.
func TestDecide_NilDispatcherAndNilEngineFallThrough(t *testing.T) {
	var d *Dispatcher
	if got := d.Decide("hello", "go", nil); got.Fired() {
		t.Fatalf("a nil dispatcher fired a rule: %+v", got)
	}
	d2 := &Dispatcher{}
	if got := d2.Decide("hello", "go", nil); got.Fired() {
		t.Fatalf("an engineless dispatcher fired a rule: %+v", got)
	}
}

// The embedded rules file ships empty, so loading it must succeed and add
// nothing. A rules file that will not load stops every dispatch.
func TestLoadRules_EmbeddedDefaultIsEmptyAndValid(t *testing.T) {
	e, err := loadRules("")
	if err != nil {
		t.Fatalf("the shipped signals.yaml does not load: %v", err)
	}
	if got := e.Rules(); len(got) != 0 {
		t.Fatalf("the shipped file declares rules: %v", got)
	}
}

// The validator is what turns a typo'd tier or enum into a load failure rather
// than a dispatch-time surprise, so it needs exercising on real routing.yaml.
func TestLoadRules_ValidatesTierAndEnumAgainstRouting(t *testing.T) {
	cases := map[string]struct {
		yaml    string
		wantErr string
	}{
		"unknown enum": {`
version: 1
rules:
  - name: r
    action: {type: route, enum: NOT_AN_ENUM}`, "routing.yaml"},

		"unknown tier": {`
version: 1
rules:
  - name: r
    action: {type: route, tier: nonsense}`, "tier"},

		"tier out of range": {`
version: 1
rules:
  - name: r
    action: {type: route, tier: "99"}`, "tier"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			writeSignals(t, home, c.yaml)
			_, err := loadRules(home)
			if err == nil {
				t.Fatal("loaded clean, want a refusal")
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("got %q, want it to mention %q", err, c.wantErr)
			}
		})
	}
}

func TestLoadRules_AcceptsARealTierAndEnum(t *testing.T) {
	home := t.TempDir()
	writeSignals(t, home, `
version: 1
rules:
  - name: by enum
    priority: 10
    when: pii.any
    action: {type: route, enum: SIMPLE}
  - name: by tier name
    priority: 5
    when: injection.matched
    action: {type: route, tier: simple}
  - name: by tier number
    priority: 1
    action: {type: route, tier: "8"}`)

	e, err := loadRules(home)
	if err != nil {
		t.Fatalf("a valid file was refused: %v", err)
	}
	if got := len(e.Rules()); got != 3 {
		t.Fatalf("want 3 rules, got %d", got)
	}
}

// A rules file that will not load must stop the dispatcher, not be skipped: a
// rule meant to keep secrets on the machine is not something to route without.
func TestLoadRules_MalformedFileIsFatal(t *testing.T) {
	home := t.TempDir()
	writeSignals(t, home, "rules: [: :")
	if _, err := loadRules(home); err == nil {
		t.Fatal("a malformed rules file loaded clean")
	}
}

func writeSignals(t *testing.T, home, body string) {
	t.Helper()
	dir := filepath.Join(home, "registry")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "signals.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// trust.calibrated must mean "there is a real observation for this domain".
// The prior alone is not evidence, so an untouched store reads false.
func TestDecide_CalibratedTracksRealObservations(t *testing.T) {
	cal, err := trust.New(filepath.Join(t.TempDir(), "calibration.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	d := &Dispatcher{cal: cal}

	if got := d.calibratedIn("go"); got {
		t.Error("an empty store reported evidence")
	}
	if err := cal.Update("ollama/qwen3:0.6b", "go", true, trust.OutcomeCorrect); err != nil {
		t.Fatal(err)
	}
	if got := d.calibratedIn("go"); !got {
		t.Error("a recorded observation was not reported as evidence")
	}
	if got := d.calibratedIn("sql"); got {
		t.Error("evidence in one domain was read as evidence in another")
	}

	// The signal reaches the decision, and an empty domain reads as the default.
	dec := d.Decide("hello", "go", nil)
	if v, ok := dec.Signals[signals.SigTrustCalibrated]; !ok || v != true {
		t.Fatalf("the calibrated signal did not reach the decision: %v %v", v, ok)
	}
	if got := d.calibratedIn(""); got != d.calibratedIn(trust.DefaultDomain) {
		t.Error("an empty domain must resolve to the default")
	}
}
