// SPDX-License-Identifier: MIT

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ankit373/hydra/internal/policy"
)

// policyHome writes a registry/policy.yaml a test controls and returns the
// HYDRA_HOME registry.Read will prefer it from.
func policyHome(t *testing.T, body string) string {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, "registry")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "policy.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

const ceilingPolicy = `version: "1.0"
defaults:
  max_cost_usd: 0.25
rules:
  - name: expensive-tiers-get-more-room
    when:
      enum_tier_lte: 2
    apply:
      max_cost_usd: 5.00
`

// The denial-of-wallet guard had never fired on any machine: --max-cost
// defaults to 0 and policy.yaml's max_cost_usd reached hyctl edit and hyctl
// parallel only, never hyctl dispatch (#838).
func TestResolveCostCeiling_PolicyAppliesWithNoFlag(t *testing.T) {
	home := policyHome(t, ceilingPolicy)
	got, src := resolveCostCeiling(home, false, 0, policy.Spec{EnumTier: 8})
	if got != 0.25 {
		t.Errorf("ceiling = %v, want 0.25 from the policy defaults", got)
	}
	if src != "policy.yaml max_cost_usd" {
		t.Errorf("source = %q, want the policy file", src)
	}
}

// The spec has to reach the engine, or every dispatch gets the defaults block
// and a rule that names a tier is dead. A ceiling equal to the default would
// pass whether or not the rule matched, so this asserts the rule's own number.
func TestResolveCostCeiling_ThreadsTheSpecSoRulesMatch(t *testing.T) {
	home := policyHome(t, ceilingPolicy)
	got, _ := resolveCostCeiling(home, false, 0, policy.Spec{EnumTier: 1})
	if got != 5.00 {
		t.Errorf("ceiling = %v at tier 1, want 5.00 from the matching rule", got)
	}
}

func TestResolveCostCeiling_FlagWins(t *testing.T) {
	home := policyHome(t, ceilingPolicy)
	got, src := resolveCostCeiling(home, true, 1.50, policy.Spec{EnumTier: 8})
	if got != 1.50 {
		t.Errorf("ceiling = %v, want the flag's 1.50", got)
	}
	if src != "--max-cost" {
		t.Errorf("source = %q, want the flag", src)
	}
}

// --max-cost 0 means "no ceiling for this run", and it is the case a zero
// check gets wrong: reading the value rather than whether it was given would
// silently keep the policy's ceiling the user just lifted.
func TestResolveCostCeiling_ExplicitZeroLiftsThePolicyCeiling(t *testing.T) {
	home := policyHome(t, ceilingPolicy)
	got, src := resolveCostCeiling(home, true, 0, policy.Spec{EnumTier: 8})
	if got != 0 {
		t.Errorf("ceiling = %v, want 0: --max-cost 0 lifts the policy ceiling", got)
	}
	if src != "--max-cost" {
		t.Errorf("source = %q, want the flag", src)
	}
}

// The shipped policy.yaml sets max_cost_usd: 0.0 in its defaults, so wiring
// this in refuses nothing that was not already refused. A regression here is
// every existing install suddenly capped.
func TestResolveCostCeiling_ShippedDefaultStillRefusesNothing(t *testing.T) {
	got, _ := resolveCostCeiling(t.TempDir(), false, 0, policy.Spec{EnumTier: 8})
	if got != 0 {
		t.Errorf("ceiling = %v under the embedded policy, want 0 (unlimited)", got)
	}
}
