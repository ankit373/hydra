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
	if r.IntegrityIntact {
		t.Error("Report.IntegrityIntact = true after tampering, want false — the hard override must fire")
	}
}

func TestBuild_IntegrityIntactWhenChainUntampered(t *testing.T) {
	testutil.NewSandbox(t)
	if err := ledger.Record(ledger.DefaultPath(), ledger.Event{Agent: "a", Tool: "h1", Decision: ledger.Allow}); err != nil {
		t.Fatal(err)
	}
	r, err := Build(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !r.IntegrityIntact {
		t.Error("Report.IntegrityIntact = false with an untampered chain, want true")
	}
}

// Actions is the feedback loop: exactly the coverage Gaps plus above-
// threshold risky heads, nothing else.
func TestBuild_ActionsListsGapsAndRiskyHeads(t *testing.T) {
	testutil.NewSandbox(t)
	for i := 0; i < 2; i++ {
		if err := ledger.Record(ledger.DefaultPath(), ledger.Event{Agent: "a", Tool: "sketchy", Decision: ledger.Deny}); err != nil {
			t.Fatal(err)
		}
	}

	r, err := Build(nil)
	if err != nil {
		t.Fatal(err)
	}
	joined := actionsText(r.Actions)
	for _, want := range []string{"LLM03", "LLM07", "sketchy"} {
		if !strings.Contains(joined, want) {
			t.Errorf("Actions missing %q:\n%s", want, joined)
		}
	}
	// A head with only one denial is below the threshold and must not appear.
	if err := ledger.Record(ledger.DefaultPath(), ledger.Event{Agent: "a", Tool: "barely-risky", Decision: ledger.Deny}); err != nil {
		t.Fatal(err)
	}
	r, err = Build(nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(actionsText(r.Actions), "barely-risky") {
		t.Error("a head with a single denial (below threshold) should not generate an action")
	}
}

func actionsText(actions []Action) string {
	parts := make([]string, len(actions))
	for i, a := range actions {
		parts[i] = a.ID + " " + a.Title + " " + a.Detail
	}
	return strings.Join(parts, "\n")
}

// A risky head is live, ongoing exposure — it must always rank PriorityNow,
// never downgraded by age the way a gap would be.
func TestBuildActions_RiskyHeadIsAlwaysPriorityNow(t *testing.T) {
	byHead := []ledger.HeadRisk{{Head: "sketchy", Denied: 2, Flagged: 1}}
	actions := buildActions(Coverage{}, byHead)
	if len(actions) != 1 || actions[0].Priority != PriorityNow {
		t.Errorf("actions = %+v, want exactly one PriorityNow action", actions)
	}
}

// gapPriority buckets a gap's age the way vulnerability-management dashboards
// bucket finding age: fresh (<7d) / aging (7-29d) / stale (>=30d).
func TestGapPriority_Thresholds(t *testing.T) {
	cases := []struct {
		age  int
		want ActionPriority
	}{
		{0, PriorityWatch}, {6, PriorityWatch},
		{7, PrioritySoon}, {29, PrioritySoon},
		{30, PriorityNow}, {90, PriorityNow},
	}
	for _, tc := range cases {
		if got := gapPriority(tc.age); got != tc.want {
			t.Errorf("gapPriority(%d) = %q, want %q", tc.age, got, tc.want)
		}
	}
}

// The queue must read top-to-bottom as a work order: most urgent priority
// first, and within the same priority the oldest (most overdue) item first.
func TestBuildActions_SortedMostUrgentFirst(t *testing.T) {
	cov := Coverage{Categories: []Category{
		{ID: "LLM07", Status: Gap, GapAgeDays: 3},  // watch
		{ID: "LLM03", Status: Gap, GapAgeDays: 45}, // now (stale)
		{ID: "LLM06", Status: Gap, GapAgeDays: 10}, // soon
	}}
	actions := buildActions(cov, nil)
	if len(actions) != 3 {
		t.Fatalf("actions = %+v, want 3", actions)
	}
	got := []string{actions[0].ID, actions[1].ID, actions[2].ID}
	want := []string{"LLM03", "LLM06", "LLM07"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("order = %v, want %v (now > soon > watch, then oldest first)", got, want)
		}
	}
}

// Build must persist a score-history entry every call, so the second call in
// the same test sees a trend against the first.
func TestBuild_PersistsScoreHistoryAcrossCalls(t *testing.T) {
	testutil.NewSandbox(t)

	first, err := Build(nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Trend.Available {
		t.Error("first-ever Build() call reported a trend, want none")
	}

	second, err := Build(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Trend.Available {
		t.Fatal("second Build() call reported no trend — the first call's history was not persisted")
	}
	if second.Trend.FirstPct != first.Coverage.PercentCovered {
		t.Errorf("Trend.FirstPct = %v, want %v (the first call's own coverage)", second.Trend.FirstPct, first.Coverage.PercentCovered)
	}
}

// History is the raw series a chart draws from — it must always end with the
// run that just computed it, and grow by exactly one point per Build call.
func TestBuild_HistoryEndsWithTheCurrentRun(t *testing.T) {
	testutil.NewSandbox(t)

	first, err := Build(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.History) != 1 {
		t.Fatalf("first Build(): History = %+v, want exactly 1 point", first.History)
	}
	if first.History[0].PercentCovered != first.Coverage.PercentCovered {
		t.Errorf("History's only point = %v, want it to match this run's own coverage %v",
			first.History[0].PercentCovered, first.Coverage.PercentCovered)
	}

	second, err := Build(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.History) != 2 {
		t.Fatalf("second Build(): History = %+v, want 2 points (the first run's, plus this one)", second.History)
	}
	if second.History[len(second.History)-1].PercentCovered != second.Coverage.PercentCovered {
		t.Error("History's last point must be the current run's own coverage, not a stale persisted one")
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
