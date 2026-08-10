// SPDX-License-Identifier: MIT

package security

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ankit373/hydra/internal/ledger"
	"github.com/ankit373/hydra/internal/testutil"
	"github.com/ankit373/hydra/internal/trust"
)

// LLM01/LLM02 are always Enforced (automatic, no config), LLM03/LLM07 are
// always Gap (no mechanism exists), LLM04/LLM08 are always N/A — these four
// pairs don't depend on install state, unlike LLM05/06/09/10.
func TestComputeCoverage_StaticCategoriesAreFixed(t *testing.T) {
	testutil.NewSandbox(t)

	cov := computeCoverage(ledger.Policy{}, nil)
	want := map[string]CoverageStatus{
		"LLM01": Enforced, "LLM02": Enforced,
		"LLM03": Gap, "LLM07": Gap,
		"LLM04": NotApplicable, "LLM08": NotApplicable,
	}
	got := map[string]CoverageStatus{}
	for _, c := range cov.Categories {
		got[c.ID] = c.Status
	}
	for id, status := range want {
		if got[id] != status {
			t.Errorf("%s = %q, want %q", id, got[id], status)
		}
	}
}

func TestComputeCoverage_NAExcludedFromBothNumeratorAndDenominator(t *testing.T) {
	testutil.NewSandbox(t)

	cov := computeCoverage(ledger.Policy{}, nil)
	if cov.Applicable != 8 {
		t.Errorf("Applicable = %d, want 8 (10 categories minus LLM04 and LLM08)", cov.Applicable)
	}
	for _, c := range cov.Categories {
		if c.ID == "LLM04" || c.ID == "LLM08" {
			continue
		}
		if c.Status == NotApplicable {
			t.Errorf("%s unexpectedly marked N/A", c.ID)
		}
	}
}

func TestLLM06ExcessiveAgency_ConfiguredOnlyWithAResourceScopedRule(t *testing.T) {
	none := ledger.Policy{Rules: []ledger.Rule{{Tool: "a", Decision: ledger.Allow}}}
	if got := llm06ExcessiveAgency(none).Status; got != Gap {
		t.Errorf("no resource-scoped rule: Status = %q, want Gap", got)
	}

	scoped := ledger.Policy{Rules: []ledger.Rule{{Resource: "internal/auth/*", Decision: ledger.Deny}}}
	if got := llm06ExcessiveAgency(scoped).Status; got != Configured {
		t.Errorf("a resource-scoped rule exists: Status = %q, want Configured", got)
	}
}

func TestLLM09Misinformation_ConfiguredOnlyWithARecordedRun(t *testing.T) {
	testutil.NewSandbox(t)

	if got := llm09Misinformation().Status; got != Gap {
		t.Errorf("no trust.jsonl: Status = %q, want Gap", got)
	}

	if err := os.MkdirAll(filepath.Dir(trust.DefaultLogPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	row := `{"ts":"2026-01-01T00:00:00Z","task_hash":"abc","domain":"go","target_conf":0.9,"final_conf":0.9,"samples":1,"decision":"accept"}` + "\n"
	if err := os.WriteFile(trust.DefaultLogPath(), []byte(row), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := llm09Misinformation().Status; got != Configured {
		t.Errorf("a recorded run exists: Status = %q, want Configured", got)
	}
}

func TestLLM10UnboundedConsumption_ConfiguredOnlyWithACostCeilingDenial(t *testing.T) {
	none := []ledger.Event{{Tool: "a", Decision: ledger.Deny, Reason: "denied by ledger policy"}}
	if got := llm10UnboundedConsumption(none).Status; got != Gap {
		t.Errorf("no cost-ceiling denial: Status = %q, want Gap", got)
	}

	withCeiling := []ledger.Event{{Tool: "a", Decision: ledger.Deny, Reason: "exceeds cost ceiling: estimated $1 > limit $0.5"}}
	if got := llm10UnboundedConsumption(withCeiling).Status; got != Configured {
		t.Errorf("a cost-ceiling denial exists: Status = %q, want Configured", got)
	}
}

// A custom workspace.yaml with every validator explicitly nulled must report
// Gap — the only way LLM05 should ever be Gap, since the embedded default
// ships real validators.
func TestLLM05OutputHandling_GapWhenNoValidatorsConfigured(t *testing.T) {
	s := testutil.NewSandbox(t)
	regDir := filepath.Join(s.HydraHome, "registry")
	if err := os.MkdirAll(regDir, 0o700); err != nil {
		t.Fatal(err)
	}
	yaml := "workspaces: {}\nvalidators:\n  go: null\n  py: null\n"
	if err := os.WriteFile(filepath.Join(regDir, "workspace.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := llm05OutputHandling().Status; got != Gap {
		t.Errorf("every validator nulled out: Status = %q, want Gap", got)
	}
}

func TestLLM05OutputHandling_EnforcedByDefault(t *testing.T) {
	testutil.NewSandbox(t)
	if got := llm05OutputHandling().Status; got != Enforced {
		t.Errorf("embedded default registry: Status = %q, want Enforced", got)
	}
}
