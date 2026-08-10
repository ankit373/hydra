// SPDX-License-Identifier: MIT

package security

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/ledger"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/testutil"
)

func TestBuild_NoDataOnAnEmptyMachine(t *testing.T) {
	testutil.NewSandbox(t)

	r, err := Build(nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.HasData {
		t.Error("HasData = true with no ledger on disk")
	}
	if r.Ledger.Total != 0 {
		t.Errorf("Ledger.Total = %d, want 0", r.Ledger.Total)
	}
	// Checks must still run and say something concrete even with no data.
	for _, c := range r.Checks {
		if c.Name == "" || c.Status == "" {
			t.Errorf("check %+v is missing a Name/Status", c)
		}
	}
}

// Panel numbers must come straight from ledger.Summarize/ByHeadRisk — no
// reimplemented math to drift from the ledger package's own truth.
func TestBuild_PanelNumbersMatchLedgerSummarize(t *testing.T) {
	testutil.NewSandbox(t)

	if err := ledger.Record(ledger.DefaultPath(), ledger.Event{Agent: "a", Tool: "h1", Decision: ledger.Allow}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Record(ledger.DefaultPath(), ledger.Event{Agent: "a", Tool: "h1", Decision: ledger.Deny}); err != nil {
		t.Fatal(err)
	}

	r, err := Build(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !r.HasData {
		t.Error("HasData = false with events on disk")
	}
	if r.Ledger.Total != 2 || r.Ledger.Allowed != 1 || r.Ledger.Denied != 1 {
		t.Errorf("Ledger = %+v, want Total=2 Allowed=1 Denied=1", r.Ledger)
	}
	if len(r.ByHead) != 1 || r.ByHead[0].Head != "h1" || r.ByHead[0].Denied != 1 {
		t.Errorf("ByHead = %+v, want one entry for h1 with 1 denied", r.ByHead)
	}
}

func TestBuild_ChainCheckReflectsRealVerifyChain(t *testing.T) {
	testutil.NewSandbox(t)

	if err := ledger.Record(ledger.DefaultPath(), ledger.Event{Agent: "a", Tool: "h1", Decision: ledger.Allow}); err != nil {
		t.Fatal(err)
	}

	r, err := Build(nil)
	if err != nil {
		t.Fatal(err)
	}
	chain := findCheck(t, r, "Ledger chain integrity")
	if chain.Status != "intact" {
		t.Errorf("chain check Status = %q, want intact", chain.Status)
	}

	// Tamper with the one line on disk.
	raw, err := os.ReadFile(ledger.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(raw), `"allow"`, `"deny"`, 1)
	if err := os.WriteFile(ledger.DefaultPath(), []byte(tampered), 0o600); err != nil {
		t.Fatal(err)
	}

	r, err = Build(nil)
	if err != nil {
		t.Fatal(err)
	}
	chain = findCheck(t, r, "Ledger chain integrity")
	if chain.Status != "BROKEN" {
		t.Errorf("chain check Status = %q, want BROKEN after tampering", chain.Status)
	}
}

func TestBuild_CostCeilingCheckCountsCostDenials(t *testing.T) {
	testutil.NewSandbox(t)

	if err := ledger.Record(ledger.DefaultPath(), ledger.Event{
		Agent: "hydra-dispatch", Tool: "h1", Decision: ledger.Deny,
		Reason: "exceeds cost ceiling: estimated $1.00 > limit $0.50",
	}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Record(ledger.DefaultPath(), ledger.Event{
		Agent: "hydra-dispatch", Tool: "h2", Decision: ledger.Deny, Reason: "denied by ledger policy",
	}); err != nil {
		t.Fatal(err)
	}

	r, err := Build(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := findCheck(t, r, "Denial-of-wallet guard").Status; got != "1 refusal(s)" {
		t.Errorf("cost-ceiling check Status = %q, want exactly 1 counted (not the unrelated policy denial)", got)
	}
}

func TestBuild_ProvenanceCheckCountsHeadsBySource(t *testing.T) {
	testutil.NewSandbox(t)

	heads := []provider.Head{
		{ID: "a", Meta: map[string]string{"model_source": "builtin"}},
		{ID: "b", Meta: map[string]string{"model_source": "user"}},
		{ID: "c", Meta: map[string]string{}},
	}
	r, err := Build(heads)
	if err != nil {
		t.Fatal(err)
	}
	if got := findCheck(t, r, "Model provenance").Status; got != "1 builtin, 1 user-added, 1 unclassified" {
		t.Errorf("provenance check Status = %q", got)
	}
}

func TestBuild_FrameworkCheckReflectsPolicyTags(t *testing.T) {
	testutil.NewSandbox(t)

	raw := `{"rules":[{"tool":"a","framework":"owasp:llm06","decision":"deny"}],"default":"allow"}`
	if err := os.MkdirAll(filepath.Dir(ledger.DefaultPolicyPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ledger.DefaultPolicyPath(), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}

	r, err := Build(nil)
	if err != nil {
		t.Fatal(err)
	}
	got := findCheck(t, r, "Framework tag coverage")
	if got.Status != "1 tagged" || !strings.Contains(got.Detail, "owasp:llm06") {
		t.Errorf("framework check = %+v", got)
	}
}

func findCheck(t *testing.T, r *Report, name string) Check {
	t.Helper()
	for _, c := range r.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no check named %q in %+v", name, r.Checks)
	return Check{}
}
