// SPDX-License-Identifier: MIT

package tui

// view_audit.go — view 5: calibration scorecard, audit-log integrity,
// guardrails, and the needs-a-human queue. Like #524's security view it is
// NOT built at startup — reading the audit log and scoring history is a cost
// the other five views must not pay — and it refreshes on every entry.

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/ankit373/hydra/internal/ledger"
	"github.com/ankit373/hydra/internal/mcpregistry"
	"github.com/ankit373/hydra/internal/security"
	"github.com/ankit373/hydra/internal/trust"
)

type ckAuditItem struct {
	text  string
	runID string // jump target; "" resolves in place
}

type ckAudit struct {
	report    *security.Report // nil when the audit log could not be read
	scorecard []trust.Stat
	families  map[string]trust.FamilyCouplingResult

	mcpCounts   map[mcpregistry.LifecycleState]int
	provisional []string

	allowedToday, deniedToday, flaggedToday int
	denyReason                              string

	items   []ckAuditItem
	builtAt time.Time
}

// loadAudit gathers everything the audit view shows. Called on view entry and
// on `v`, never at startup.
func (m Cockpit) loadAudit() Cockpit {
	report, _ := security.Build(m.probedHeads)
	events, _ := ledger.Load(ledger.DefaultPath())
	states, _ := mcpregistry.LoadStates()
	var scorecard []trust.Stat
	if m.metrics.calibrator != nil {
		scorecard = m.metrics.calibrator.Report()
	}
	families := trust.AllFamilyCoupling(trust.DefaultCoAgreementPath())
	m.audit = ckAuditFrom(report, events, scorecard, families, states, m.runsToday, time.Now().UTC())
	if m.auditSel >= len(m.auditItems()) {
		m.auditSel = 0
	}
	return m
}

// ckAuditFrom assembles the view's data from already-loaded inputs — pure, so
// the aggregation is testable without a machine's real logs.
func ckAuditFrom(report *security.Report, events []ledger.Event, scorecard []trust.Stat,
	families map[string]trust.FamilyCouplingResult, states map[string]mcpregistry.ServerState,
	runs []ckRun, now time.Time) *ckAudit {

	a := &ckAudit{
		report:    report,
		scorecard: append([]trust.Stat(nil), scorecard...),
		families:  families,
		mcpCounts: map[mcpregistry.LifecycleState]int{},
		builtAt:   now,
	}
	sort.Slice(a.scorecard, func(i, j int) bool {
		if a.scorecard[i].N != a.scorecard[j].N {
			return a.scorecard[i].N > a.scorecard[j].N
		}
		if a.scorecard[i].Source != a.scorecard[j].Source {
			return a.scorecard[i].Source < a.scorecard[j].Source
		}
		return a.scorecard[i].Domain < a.scorecard[j].Domain
	})

	day := now.Format("2006-01-02")
	for _, e := range events {
		if !strings.HasPrefix(e.TS, day) {
			continue
		}
		switch e.Decision {
		case ledger.Allow:
			a.allowedToday++
		case ledger.Deny:
			a.deniedToday++
			if a.denyReason == "" && e.Reason != "" {
				a.denyReason = e.Reason
			}
		}
		if e.Flagged {
			a.flaggedToday++
		}
	}

	for name, st := range states {
		a.mcpCounts[st.State]++
		if st.State == mcpregistry.StateProvisional {
			a.provisional = append(a.provisional, name)
		}
	}
	sort.Strings(a.provisional)

	// Needs-a-human items are real signals only; an empty list is the answer
	// "nothing", not a placeholder.
	var failed []ckRun
	for _, r := range runs {
		if r.status == "failed" {
			failed = append(failed, r)
		}
	}
	if len(failed) >= 2 {
		a.items = append(a.items, ckAuditItem{
			text:  fmt.Sprintf("%d runs failed today — enter opens the latest failed trace", len(failed)),
			runID: failed[0].id,
		})
	}
	for _, name := range a.provisional {
		a.items = append(a.items, ckAuditItem{
			text: "MCP server " + name + " is provisional — version changed, awaiting re-trust",
		})
	}
	return a
}

// auditItems is the needs-a-human queue minus what was ignored this session.
func (m Cockpit) auditItems() []ckAuditItem {
	if m.audit == nil {
		return nil
	}
	var out []ckAuditItem
	for _, it := range m.audit.items {
		if !m.auditIgnored[it.text] {
			out = append(out, it)
		}
	}
	return out
}

// resolveAuditItem is enter on a queue row: jump to the run's trace when there
// is one, otherwise resolve the item for this session.
func (m Cockpit) resolveAuditItem() Cockpit {
	items := m.auditItems()
	if len(items) == 0 {
		return m
	}
	sel := m.auditSel
	if sel < 0 || sel >= len(items) {
		sel = 0
	}
	it := items[sel]
	if it.runID != "" {
		return m.focusRun(it.runID)
	}
	m.auditIgnored[it.text] = true
	m.flash = "resolved for this session"
	return m
}

// ignoreAuditItem hides the selected item until the cockpit restarts.
func (m Cockpit) ignoreAuditItem() Cockpit {
	items := m.auditItems()
	if len(items) == 0 {
		return m
	}
	sel := m.auditSel
	if sel < 0 || sel >= len(items) {
		sel = 0
	}
	m.auditIgnored[items[sel].text] = true
	m.flash = "ignored for this session"
	if m.auditSel >= len(m.auditItems()) {
		m.auditSel = 0
	}
	return m
}

// auditFact is the status bar's right-hand fact for this view.
func (m Cockpit) auditFact() string {
	if m.audit == nil {
		return "not checked yet"
	}
	chain := "chain —"
	if m.audit.report != nil {
		chain = "chain " + ckChainWord(m.audit.report.Attestation.Evidence)
	}
	return fmt.Sprintf("%s · checked %s", chain, m.audit.builtAt.Format("15:04:05"))
}

// ── render ────────────────────────────────────────────────────────────────────

func (m Cockpit) viewAudit(w, h int) string {
	if m.audit == nil {
		return lipgloss.NewStyle().Width(w).Height(h).Render(
			ckFaintS.Render(" audit not built yet — press v to verify now"))
	}
	// Row caps adapt to the height so the four tiles share a 24-row terminal
	// instead of pushing the status bar off-frame.
	scoreRows := h - 14
	if scoreRows > 8 {
		scoreRows = 8
	}
	if scoreRows < 3 {
		scoreRows = 3
	}
	left := ckBoxS.Render(m.auditScorecard(scoreRows))
	right := lipgloss.JoinVertical(lipgloss.Left,
		ckBoxS.Render(m.auditLogTile()), ckBoxS.Render(m.auditGuardrails()))
	top := ckSplit(w, left, right, false)
	if lipgloss.Width(left)+1+lipgloss.Width(right) > w {
		top = lipgloss.JoinVertical(lipgloss.Left, left, right)
	}
	queue := ckBoxS.Render(m.auditQueue())
	return lipgloss.NewStyle().Width(w).Height(h).
		Render(lipgloss.JoinVertical(lipgloss.Left, top, queue))
}

// ckScorecardCols are the table's fixed column budgets: source, domain, and
// the four numeric columns (right-aligned).
const (
	ckScoreSourceW = 20
	ckScoreDomainW = 8
)

// auditScorecard is the per-source calibration table. No history is persisted
// for these figures yet, so no trend arrows are drawn — a fabricated trend is
// worse than none.
func (m Cockpit) auditScorecard(rowCap int) string {
	a := m.audit
	var b strings.Builder
	b.WriteString(ckLabelS.Render("MODEL SCORECARD") + ckDimS.Render(" · calibration") + "\n\n")
	if len(a.scorecard) == 0 {
		b.WriteString(ckFaintS.Render(" no calibration records yet") + "\n" +
			ckDimS.Render(" score verdicts with `hyctl trust record`") + "\n")
	} else {
		b.WriteString(ckFaintS.Render(" "+ckCell("source", ckScoreSourceW)+" "+ckCell("domain", ckScoreDomainW)+
			" "+ckRCell("n", 4)+" "+ckRCell("sens", 5)+" "+ckRCell("spec", 5)+" "+ckRCell("D nats", 7)) + "\n")
		lines := make([]string, len(a.scorecard))
		for i, s := range a.scorecard {
			lines[i] = " " + ckCell(ckSafe(s.Source), ckScoreSourceW) + " " +
				ckDimS.Render(ckCell(s.Domain, ckScoreDomainW)) + " " +
				ckRCell(fmt.Sprintf("%.0f", s.N), 4) + " " +
				ckRCell(fmt.Sprintf("%.2f", s.Se), 5) + " " +
				ckRCell(fmt.Sprintf("%.2f", s.Sp), 5) + " " +
				ckCyanS.Render(ckRCell(fmt.Sprintf("%.2f", s.D), 7))
		}
		window, _ := ckScrollLines(lines, m.scoreOff, rowCap)
		b.WriteString(strings.Join(window, "\n") + "\n")
	}
	b.WriteString("\n")
	if st := m.metrics.trustStats; st != nil {
		b.WriteString(" " + ckDimS.Render("consensus checks       ") +
			ckCyanS.Render(fmt.Sprintf("%.2f", st.MeanFinalConf)) +
			ckDimS.Render(fmt.Sprintf(" mean confidence over %d", st.Runs)) + "\n")
		b.WriteString(" " + ckDimS.Render("cleared without review ") +
			ckCyanS.Render(fmt.Sprintf("%.0f%%", st.AutoClearedPct)) + "\n")
	} else {
		b.WriteString(" " + ckFaintS.Render("no consensus checks recorded yet") + "\n")
	}
	b.WriteString(" " + ckDimS.Render("same-family agreement  ") + m.auditFamilyFact())
	return b.String()
}

// auditFamilyFact renders the measured same-family agreement discount, or says
// none was measured. The discount is 1−J — a family whose members nearly
// always agree contributes almost nothing on a repeat vote.
func (m Cockpit) auditFamilyFact() string {
	names := make([]string, 0, len(m.audit.families))
	for name, f := range m.audit.families {
		if f.OK {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return ckFaintS.Render("not measured yet")
	}
	sort.Slice(names, func(i, j int) bool {
		if m.audit.families[names[i]].J != m.audit.families[names[j]].J {
			return m.audit.families[names[i]].J > m.audit.families[names[j]].J
		}
		return names[i] < names[j]
	})
	f := m.audit.families[names[0]]
	s := fmt.Sprintf("%s ×%.2f (J=%.2f)", names[0], 1-f.J, f.J)
	if f.Warn {
		return ckMidS.Render(s + " — echo, not evidence")
	}
	return ckCyanS.Render(s)
}

// ckChainWord maps the attested evidence state onto one word, the same
// mapping the security view used.
func ckChainWord(ev security.AttestedEvidence) string {
	switch {
	case ev.Truncated:
		return "TRUNCATED"
	case !ev.ChainIntact:
		return "BROKEN"
	case ev.Events > 0 && ev.ChainedEvents == 0:
		return "unverifiable"
	case ev.AnchorMissing:
		return "unanchored"
	default:
		return "intact"
	}
}

func (m Cockpit) auditLogTile() string {
	a := m.audit
	var b strings.Builder
	b.WriteString(ckLabelS.Render("AUDIT LOG") + "\n\n")
	if a.report == nil {
		b.WriteString(ckFaintS.Render(" unavailable — the audit log could not be read"))
		return b.String()
	}
	ev := a.report.Attestation.Evidence
	word := ckChainWord(ev)
	ws := ckCheapS
	switch word {
	case "TRUNCATED", "BROKEN", "unverifiable":
		ws = ckExpS
	case "unanchored":
		ws = ckMidS
	}
	b.WriteString(" " + ckDimS.Render(ckCell("chain", 9)) + ws.Render(word) +
		ckDimS.Render(fmt.Sprintf(" · %d event%s, %d chained", ev.Events, plural(ev.Events), ev.ChainedEvents)) + "\n")
	b.WriteString(" " + ckDimS.Render(ckCell("today", 9)) +
		ckCheapS.Render(fmt.Sprintf("%d allowed", a.allowedToday)) + ckDimS.Render(" · ") +
		ckExpS.Render(fmt.Sprintf("%d denied", a.deniedToday)) + "\n")
	if a.deniedToday > 0 && a.denyReason != "" {
		b.WriteString("          " + ckFaintS.Render(truncate(ckSafe(a.denyReason), 36)) + "\n")
	}
	if l := a.report.Ledger; l.Total > 0 {
		b.WriteString(" " + ckDimS.Render(ckCell("all-time", 9)) +
			ckSegmentedBar(20, []int{l.Allowed, l.Denied}, []lipgloss.Style{ckCheapS, ckExpS}) +
			ckDimS.Render(fmt.Sprintf(" %d", l.Total)))
	}
	return b.String()
}

func (m Cockpit) auditGuardrails() string {
	a := m.audit
	var b strings.Builder
	b.WriteString(ckLabelS.Render("GUARDRAILS") + "\n\n")
	if a.report == nil {
		b.WriteString(ckFaintS.Render(" unavailable — the audit log could not be read") + "\n")
	} else {
		r := a.report
		fired := len(r.Exposures)
		note := fmt.Sprintf(" · fired %d×", fired)
		var tail string
		if fired > 0 {
			confirmed := security.ConfirmedRemote(r.Exposures)
			unknown := security.RemoteCount(r.Exposures) - confirmed
			switch {
			case confirmed > 0:
				tail = ckExpS.Render(fmt.Sprintf(" · %d REACHED REMOTE", confirmed))
			case unknown > 0:
				tail = ckMidS.Render(fmt.Sprintf(" · %d unidentified", unknown))
			default:
				tail = ckDimS.Render(" · all stayed local")
			}
		}
		b.WriteString(" " + ckDimS.Render(ckCell("pii → local-only", 19)) +
			ckCheapS.Render("enforced") + ckDimS.Render(note) + tail + "\n")
		b.WriteString(" " + ckDimS.Render(ckCell("injection markers", 19)) +
			ckInkS.Render(fmt.Sprintf("%d seen today", a.flaggedToday)) + "\n")
		b.WriteString(" " + ckDimS.Render(ckCell("rules", 19)) +
			ckCyanS.Render(truncate(ckPolicyPosture(r.PolicyAudit), 28)) + "\n")
	}
	t, p, q := a.mcpCounts[mcpregistry.StateTrusted], a.mcpCounts[mcpregistry.StateProvisional], a.mcpCounts[mcpregistry.StateQuarantined]
	if len(a.mcpCounts) == 0 {
		b.WriteString(" " + ckDimS.Render(ckCell("mcp servers", 19)) +
			ckFaintS.Render("none tracked — `hyctl mcp registry scan`"))
		return b.String()
	}
	ps := ckDimS
	if p > 0 {
		ps = ckMidS
	}
	qs := ckDimS
	if q > 0 {
		qs = ckExpS
	}
	b.WriteString(" " + ckDimS.Render(ckCell("mcp servers", 19)) +
		ckCheapS.Render(fmt.Sprintf("%d trusted", t)) + ckDimS.Render(" · ") +
		ps.Render(fmt.Sprintf("%d provisional", p)) + ckDimS.Render(" · ") +
		qs.Render(fmt.Sprintf("%d quarantined", q)))
	return b.String()
}

// ckPolicyPosture is the one-line guardrail-rules readout: fail-open/closed
// plus the two real defects the audit can prove.
func ckPolicyPosture(a security.PolicyAudit) string {
	if len(a.Rules) == 0 {
		return "no rules defined"
	}
	s := "fail-closed"
	if a.FailOpen {
		s = "fail-open"
	}
	if n := a.DeadCount(); n > 0 {
		s += fmt.Sprintf(" · %d dead", n)
	}
	if n := a.ShadowedCount(); n > 0 {
		s += fmt.Sprintf(" · %d unreachable", n)
	}
	return s
}

func (m Cockpit) auditQueue() string {
	items := m.auditItems()
	var b strings.Builder
	b.WriteString(ckLabelS.Render("NEEDS A HUMAN") + "\n\n")
	if len(items) == 0 {
		b.WriteString(ckCheapS.Render(" nothing needs review") +
			ckDimS.Render(" — no repeated failures, no provisional MCP servers"))
		return b.String()
	}
	sel := m.auditSel
	if sel < 0 || sel >= len(items) {
		sel = 0
	}
	for i, it := range items {
		marker := "  "
		if i == sel {
			marker = ckAquaS.Render("▸ ")
		}
		b.WriteString(marker + ckMidS.Bold(i == sel).Render(truncate(ckSafe(it.text), 70)) + "\n")
	}
	b.WriteString(ckFaintS.Render(" enter resolve/jump · i ignore for this session"))
	return b.String()
}
