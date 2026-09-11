// SPDX-License-Identifier: MIT

package swarm

import (
	"testing"

	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/provider"
)

// An enum is a routing instruction. It reached swarm.Options and stopped there,
// so `--enum SIMPLE --swarm` fanned out over CapScoreSelector's top-N, which is
// the *strongest* heads available, precisely the opposite of what SIMPLE asks
// for. The same flag on a plain dispatch resolved to a tier (#832).
func TestResolveSelector_AnEnumPicksTheTierSelector(t *testing.T) {
	if _, ok := resolveSelector(Options{Enum: "SIMPLE"}).(*TierSelector); !ok {
		t.Error("--enum SIMPLE did not reach the TierSelector, so the fan-out " +
			"ignores the routing key it was given")
	}
	// An unknown key resolves to no tier. cmdDispatch rejects a typo before it
	// gets here, so the open question is only whether an unroutable value can
	// pin selection to something arbitrary; it must not.
	if _, ok := resolveSelector(Options{Enum: "NOT_A_ROUTING_KEY"}).(*CapScoreSelector); !ok {
		t.Error("an unrecognized enum resolved to a tier")
	}
	if _, ok := resolveSelector(Options{}).(*CapScoreSelector); !ok {
		t.Error("no tier and no enum did not resolve to CapScoreSelector")
	}
}

// The behavioural half: the heads an enum fans out to are the heads its tier
// fans out to. Asserted by selecting through resolveSelector, the path both
// bugs lived on, rather than by calling TierSelector directly, which would
// assume the very dispatch the fix had to make happen.
func TestSelect_AnEnumFansOutOverTheHeadsItsTierSelects(t *testing.T) {
	all := []provider.Head{
		registryHead("strongest", "Strongest", 100), // UITier 1
		registryHead("mid", "Mid", 70),              // UITier 7
		registryHead("weak", "Weak", 40),            // UITier 10
	}
	const enum = "SIMPLE"

	tier := dispatch.EnumToTier(enum)
	if tier == "" {
		t.Fatalf("%s resolves to no tier in routing.yaml", enum)
	}

	byTier, err := resolveSelector(Options{TierHint: tier}).Select(all, Options{TierHint: tier})
	if err != nil {
		t.Fatal(err)
	}
	unrouted, err := resolveSelector(Options{}).Select(all, Options{})
	if err != nil {
		t.Fatal(err)
	}
	// Without this the guard can pass under its own bug: if the enum's tier
	// admitted every head, the ignored-enum fan-out would select the same set
	// and the comparison below would hold for the wrong reason.
	if equalIDs(byTier, unrouted) {
		t.Fatalf("fixture no longer distinguishes: %s selects the same heads as "+
			"no routing key at all (%v)", enum, ids(byTier))
	}

	byEnum, err := resolveSelector(Options{Enum: enum}).Select(all, Options{Enum: enum})
	if err != nil {
		t.Fatal(err)
	}
	if !equalIDs(byEnum, byTier) {
		t.Errorf("--enum %s selected %v, --tier %s selected %v; one flag, two routings",
			enum, ids(byEnum), tier, ids(byTier))
	}
}

// --tier wins over --enum, the rule the single-dispatch path already applies.
func TestSelect_AnExplicitTierWinsOverTheEnum(t *testing.T) {
	all := []provider.Head{
		registryHead("strongest", "Strongest", 100), // UITier 1
		registryHead("weak", "Weak", 40),            // UITier 10
	}
	opts := Options{TierHint: "1", Enum: "GRUNT"} // GRUNT is tier 10

	got, err := resolveSelector(opts).Select(all, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0].ID != "strongest" {
		t.Errorf("--tier 1 --enum GRUNT selected %v; the explicit tier must win", ids(got))
	}
}

// The enum that chose the tier belongs on the spend it caused, or a reader
// cannot tell which routing key produced the row. internal/cost's writer guard
// asserts the field is written at all; this asserts the value is the one that
// routed.
func TestLogAttempts_CostRowsCarryTheEnumThatRouted(t *testing.T) {
	attempts := []Attempt{idAttempt("alpha")}
	opts := Options{RunID: "run-enum", TaskID: "task-enum", Enum: "SIMPLE"}

	rows := costRows(t, func() { logAttempts(attempts, ModeBest, opts, "p") })
	if len(rows) != 1 {
		t.Fatalf("wrote %d cost rows, want 1", len(rows))
	}
	if got := rows[0]["enum"]; got != "SIMPLE" {
		t.Errorf("cost row enum = %v, want %q", got, "SIMPLE")
	}
}

func equalIDs(a, b []provider.Head) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID {
			return false
		}
	}
	return true
}
