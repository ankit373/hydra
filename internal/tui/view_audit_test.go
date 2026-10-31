// SPDX-License-Identifier: MIT

package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ankit373/hydra/internal/ledger"
	"github.com/ankit373/hydra/internal/mcpregistry"
	"github.com/ankit373/hydra/internal/security"
	"github.com/ankit373/hydra/internal/testutil"
	"github.com/ankit373/hydra/internal/trust"
)

// testAuditNow anchors the "today" tallies.
var testAuditNow = time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)

// testAudit assembles a deterministic audit fixture through the same pure
// builder loadAudit uses.
func testAudit(report *security.Report, runs []ckRun) *ckAudit {
	events := []ledger.Event{
		{TS: "2026-09-04T10:00:00Z", Decision: ledger.Allow},
		{TS: "2026-09-04T10:01:00Z", Decision: ledger.Allow},
		{TS: "2026-09-04T10:02:00Z", Decision: ledger.Deny, Reason: "pii must stay local"},
		{TS: "2026-09-04T10:03:00Z", Decision: ledger.Allow, Flagged: true, FlagReason: "ignore previous instructions"},
		{TS: "2026-09-01T10:00:00Z", Decision: ledger.Deny, Reason: "old, not today"},
	}
	scorecard := []trust.Stat{
		{Source: "model:qwen", Domain: "go", N: 12, Se: 0.91, Sp: 0.84, D: 1.32},
		{Source: "model:claude", Domain: "go", N: 30, Se: 0.95, Sp: 0.92, D: 2.10},
	}
	families := map[string]trust.FamilyCouplingResult{
		"gemini": {J: 0.62, OK: true, Warn: true},
		"qwen":   {J: 0.10, OK: true},
		"novel":  {OK: false},
	}
	states := map[string]mcpregistry.ServerState{
		"github":   {State: mcpregistry.StateTrusted},
		"postmark": {State: mcpregistry.StateProvisional},
		"filesys":  {State: mcpregistry.StateTrusted},
		"sketchy":  {State: mcpregistry.StateQuarantined},
	}
	return ckAuditFrom(report, events, scorecard, families, states, runs, testAuditNow)
}

func testSecurityReport() *security.Report {
	return &security.Report{
		HasData:         true,
		IntegrityIntact: true,
		Ledger:          security.LedgerPanel{Total: 5, Allowed: 3, Denied: 2},
		PolicyAudit: security.PolicyAudit{
			Default: "allow", FailOpen: true,
			Rules: []security.RuleStat{{Index: 0, Hits: 3}, {Index: 1, Hits: 0, Dead: true}},
		},
		Exposures: []security.Exposure{
			{Head: "ollama/qwen", Remote: false, Known: true},
			{Head: "gpt-4o", Remote: true, Known: true},
		},
		Attestation: security.Attestation{
			Evidence: security.AttestedEvidence{Events: 5, ChainedEvents: 5, ChainIntact: true},
		},
	}
}

// ckAuditFrom: today's tallies count only today, deny reasons surface, MCP
// states are bucketed, and needs-a-human items are real signals only.
func TestCkAuditFrom_Aggregation(t *testing.T) {
	runs := []ckRun{
		testRun("r-ok", "ok", "fine"),
		testRun("r-f1", "failed", "broke once"),
		testRun("r-f2", "failed", "broke twice"),
	}
	a := testAudit(testSecurityReport(), runs)

	if a.allowedToday != 3 || a.deniedToday != 1 || a.flaggedToday != 1 {
		t.Errorf("today tallies = %d/%d/%d, want 3/1/1", a.allowedToday, a.deniedToday, a.flaggedToday)
	}
	if a.denyReason != "pii must stay local" {
		t.Errorf("denyReason = %q", a.denyReason)
	}
	if a.mcpCounts[mcpregistry.StateTrusted] != 2 ||
		a.mcpCounts[mcpregistry.StateProvisional] != 1 ||
		a.mcpCounts[mcpregistry.StateQuarantined] != 1 {
		t.Errorf("mcp counts = %+v", a.mcpCounts)
	}
	if len(a.provisional) != 1 || a.provisional[0] != "postmark" {
		t.Errorf("provisional = %v", a.provisional)
	}

	// Two failed runs today + one provisional server = two real items.
	if len(a.items) != 2 {
		t.Fatalf("items = %+v, want 2", a.items)
	}
	if !strings.Contains(a.items[0].text, "2 runs failed today") || a.items[0].runID != "r-f1" {
		t.Errorf("failed-runs item = %+v", a.items[0])
	}
	if !strings.Contains(a.items[1].text, "postmark") || !strings.Contains(a.items[1].text, "provisional") {
		t.Errorf("provisional item = %+v", a.items[1])
	}

	// One failed run is not a pattern, no item.
	one := testAudit(testSecurityReport(), []ckRun{testRun("r", "failed", "once")})
	for _, it := range one.items {
		if strings.Contains(it.text, "failed today") {
			t.Errorf("a single failure minted an item: %+v", one.items)
		}
	}
}

// The scorecard is sorted most-observed first with a stable tiebreak.
func TestCkAuditFrom_ScorecardOrder(t *testing.T) {
	a := testAudit(nil, nil)
	if a.scorecard[0].Source != "model:claude" {
		t.Errorf("scorecard order = %+v, want most-observed first", a.scorecard)
	}
}

// The rendered audit view: scorecard columns, footer facts, chain state,
// today's approvals/denials with the reason, guardrails, and the queue,
// and no trend arrows, since no history exists to compute one from.
func TestViewAudit_RendersRealDataOnly(t *testing.T) {
	m := testCockpit()
	m.metrics.trustStats = &trust.Stats{Runs: 4, MeanFinalConf: 0.94, AutoClearedPct: 78, MeanSamples: 2.5}
	m.runsToday = []ckRun{
		testRun("r-f1", "failed", "broke once"),
		testRun("r-f2", "failed", "broke twice"),
	}
	m.audit = testAudit(testSecurityReport(), m.runsToday)
	m.view = ckViewAudit
	out := stripANSI(m.viewAudit(120, 34))

	for _, want := range []string{
		"MODEL SCORECARD", "source", "domain", "D nats",
		"model:claude", "30", "0.95", "2.10",
		"consensus checks", "0.94", "mean confidence over 4",
		"cleared without review", "78%",
		"same-family agreement", "gemini ×0.38 (J=0.62)", "echo, not evidence",
		"AUDIT LOG", "intact", "5 events", "3 allowed", "1 denied", "pii must stay local",
		"GUARDRAILS", "pii → local-only", "enforced", "fired 2×", "1 REACHED REMOTE",
		"injection markers", "1 seen today",
		"rules", "fail-open · 1 dead",
		"mcp servers", "2 trusted", "1 provisional", "1 quarantined",
		"NEEDS A HUMAN", "2 runs failed today", "postmark",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("audit view missing %q:\n%s", want, out)
		}
	}
	// No fabricated trend arrows anywhere on the scorecard.
	for _, arrow := range []string{"↗", "↘"} {
		if strings.Contains(out, arrow) {
			t.Errorf("a trend arrow rendered with no history to compute it:\n%s", out)
		}
	}
}

// A nil report renders the tiles as unavailable while the trust-side data
// still shows; a broken chain is called out.
func TestViewAudit_DegradedStates(t *testing.T) {
	m := testCockpit()
	m.audit = testAudit(nil, nil)
	out := stripANSI(m.viewAudit(120, 30))
	if !strings.Contains(out, "unavailable, the audit log could not be read") {
		t.Errorf("nil report not disclosed:\n%s", out)
	}
	if !strings.Contains(out, "MODEL SCORECARD") || !strings.Contains(out, "model:claude") {
		t.Errorf("the scorecard vanished with the report:\n%s", out)
	}

	broken := testSecurityReport()
	broken.Attestation.Evidence.ChainIntact = false
	m.audit = testAudit(broken, nil)
	if out := stripANSI(m.viewAudit(120, 30)); !strings.Contains(out, "BROKEN") {
		t.Errorf("a broken chain is not called out:\n%s", out)
	}

	// Not built at all (never entered): honest placeholder.
	m.audit = nil
	if out := stripANSI(m.viewAudit(120, 30)); !strings.Contains(out, "not built yet") {
		t.Errorf("nil audit not honest:\n%s", out)
	}
}

func TestCkChainWord(t *testing.T) {
	for _, tt := range []struct {
		ev   security.AttestedEvidence
		want string
	}{
		{security.AttestedEvidence{Events: 5, ChainedEvents: 5, ChainIntact: true}, "intact"},
		{security.AttestedEvidence{Events: 5, ChainedEvents: 5, ChainIntact: false}, "BROKEN"},
		{security.AttestedEvidence{Events: 5, ChainedEvents: 5, ChainIntact: true, Truncated: true}, "TRUNCATED"},
		{security.AttestedEvidence{Events: 5, ChainedEvents: 0, ChainIntact: true}, "unverifiable"},
		{security.AttestedEvidence{Events: 5, ChainedEvents: 5, ChainIntact: true, AnchorMissing: true}, "unanchored"},
	} {
		if got := ckChainWord(tt.ev); got != tt.want {
			t.Errorf("ckChainWord(%+v) = %q, want %q", tt.ev, got, tt.want)
		}
	}
}

func TestCkPolicyPosture(t *testing.T) {
	if got := ckPolicyPosture(security.PolicyAudit{}); got != "no rules defined" {
		t.Errorf("empty policy = %q", got)
	}
	zero := 0
	a := security.PolicyAudit{
		FailOpen: true,
		Rules:    []security.RuleStat{{Hits: 1}, {Dead: true}, {ShadowedBy: &zero}},
	}
	got := ckPolicyPosture(a)
	for _, want := range []string{"fail-open", "1 dead", "1 unreachable"} {
		if !strings.Contains(got, want) {
			t.Errorf("posture %q missing %q", got, want)
		}
	}
}

// The empty queue is the answer "nothing", and items can be resolved, jumped,
// or ignored for the session.
func TestAuditQueue_ResolveJumpIgnore(t *testing.T) {
	m := testCockpit()
	m.runsToday = []ckRun{
		testRun("r-f1", "failed", "broke once"),
		testRun("r-f2", "failed", "broke twice"),
	}
	m.audit = testAudit(testSecurityReport(), m.runsToday)
	m.view = ckViewAudit

	// Enter on the failed-runs item jumps to the trace.
	m.auditSel = 0
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	jumped := next.(Cockpit)
	if jumped.view != ckViewActivity || !jumped.actDrill {
		t.Fatalf("enter did not jump to the trace: view=%d", jumped.view)
	}

	// Enter on the provisional item (no destination) resolves it in place.
	m.auditSel = 1
	m = m.resolveAuditItem()
	if len(m.auditItems()) != 1 {
		t.Errorf("resolve did not remove the item: %+v", m.auditItems())
	}
	if !strings.Contains(m.flash, "resolved") {
		t.Errorf("flash = %q", m.flash)
	}

	// i ignores the remaining item; the empty state then says nothing needs
	// review.
	m.auditSel = 0
	m = m.ignoreAuditItem()
	if len(m.auditItems()) != 0 {
		t.Errorf("ignore did not remove the item: %+v", m.auditItems())
	}
	out := stripANSI(m.auditQueue())
	if !strings.Contains(out, "nothing needs review") {
		t.Errorf("empty queue not honest:\n%s", out)
	}

	// Ignoring with an empty queue is a no-op, not a panic.
	m = m.ignoreAuditItem()
	m = m.resolveAuditItem()
}

// v rebuilds the audit (verify now); entry always refreshes (#524's lazy
// build, plus this phase's refresh-on-entry).
func TestAudit_LazyBuildAndRefresh(t *testing.T) {
	testutil.NewSandbox(t)
	m := testCockpit()
	if m.audit != nil {
		t.Fatal("the audit was built at startup, it must stay lazy (#524)")
	}
	m = m.jump(ckViewAudit)
	if m.audit == nil {
		t.Fatal("entering the view did not build the audit")
	}
	first := m.audit
	m = m.jump(ckViewChat)
	m = m.jump(ckViewAudit)
	if m.audit == first {
		t.Error("re-entering did not refresh the audit data")
	}

	m.flash = ""
	m = typed(m, "v")
	if !strings.Contains(m.flash, "re-verified") {
		t.Errorf("v did not verify: flash = %q", m.flash)
	}
}

// The status-bar fact reflects the chain and the check time.
func TestAuditFact(t *testing.T) {
	m := testCockpit()
	if got := m.auditFact(); got != "not checked yet" {
		t.Errorf("auditFact with no audit = %q", got)
	}
	m.audit = testAudit(testSecurityReport(), nil)
	got := m.auditFact()
	if !strings.Contains(got, "chain intact") || !strings.Contains(got, "12:00:00") {
		t.Errorf("auditFact = %q", got)
	}
}

// The family fact renders only measured coupling, worst family first.
func TestAuditFamilyFact(t *testing.T) {
	m := testCockpit()
	m.audit = testAudit(nil, nil)
	got := stripANSI(m.auditFamilyFact())
	if !strings.Contains(got, "gemini") {
		t.Errorf("family fact = %q, want the worst-coupled family", got)
	}
	m.audit.families = map[string]trust.FamilyCouplingResult{}
	if got := stripANSI(m.auditFamilyFact()); !strings.Contains(got, "not measured yet") {
		t.Errorf("unmeasured families = %q", got)
	}
}

// The audit view scrolls as one document: content past the fold is reachable,
// never hidden behind "enlarge the terminal" (#630).
func TestAuditView_ScrollsToEverything(t *testing.T) {
	m := testCockpit()
	m.w, m.h = 80, 24
	a := testAudit(nil, nil)
	for i := 0; i < 20; i++ {
		a.scorecard = append(a.scorecard, trust.Stat{
			Source: "src-" + string(rune('a'+i)), Domain: "go", N: float64(i),
		})
	}
	m.audit = a
	m.view = ckViewAudit

	out := stripANSI(m.viewAudit(80, 21))
	if !strings.Contains(out, "↓") {
		t.Errorf("an overflowing audit view shows no scroll cue:\n%s", out)
	}
	if strings.Contains(out, "enlarge the terminal") {
		t.Errorf("audit told the user to resize instead of scrolling:\n%s", out)
	}

	m = m.scrollBy(ckScrollAll)
	out = stripANSI(m.viewAudit(80, 21))
	if !strings.Contains(out, "NEEDS A HUMAN") {
		t.Errorf("scrolling to the end never reaches the last tile:\n%s", out)
	}
	if !strings.Contains(out, "↑") {
		t.Errorf("a scrolled audit view shows no top cue:\n%s", out)
	}
}

// j/k are the keys the status bar advertises: with no queue to pick through
// they scroll the view rather than doing nothing at all (#630).
func TestAuditView_JKScrollsWhenTheQueueIsEmpty(t *testing.T) {
	m := testCockpit()
	m.w, m.h = 80, 24
	m.view = ckViewAudit
	a := testAudit(nil, nil)
	a.items = nil
	for i := 0; i < 20; i++ {
		a.scorecard = append(a.scorecard, trust.Stat{
			Source: "src-" + string(rune('a'+i)), Domain: "go", N: float64(i),
		})
	}
	m.audit = a

	moved := m.move(4)
	if moved.auditOff != 4 {
		t.Errorf("j did not scroll an audit view with no queue: off=%d", moved.auditOff)
	}
	if stripANSI(moved.viewAudit(80, 21)) == stripANSI(m.viewAudit(80, 21)) {
		t.Error("j left the audit view unchanged")
	}

	// With a queue, j/k keep picking items, that is what enter/i act on.
	withQueue := m
	withQueue.audit.items = []ckAuditItem{{text: "one"}, {text: "two"}}
	if got := withQueue.move(1); got.auditSel != 1 || got.auditOff != 0 {
		t.Errorf("j with a queue moved the wrong thing: sel=%d off=%d", got.auditSel, got.auditOff)
	}
}
