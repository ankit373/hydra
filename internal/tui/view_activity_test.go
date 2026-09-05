// SPDX-License-Identifier: MIT

package tui

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/testutil"
)

// ── run status folding ──────────────────────────────────────────────────────────

// Status is derived from the recorded events, never assumed: a live heartbeat
// means running, a successful completion (or a clean finish with no errors)
// means ok, anything else failed, the counts must never call running "ok".
func TestCkRunFromEvents_StatusDerivation(t *testing.T) {
	ok := []runlog.Event{
		{Kind: runlog.KindRunStarted, TS: "2026-09-04T10:00:00Z", Detail: "add pagination"},
		{Kind: runlog.KindHeadSelected, TS: "2026-09-04T10:00:01Z", Head: "h", Model: "m", Tier: 6, Detail: "candidate 1 of 2"},
		{Kind: runlog.KindDispatchFinished, TS: "2026-09-04T10:00:03Z", Head: "h", Model: "m", Status: "ok", CostUSD: 0.002, DurationMS: 2000},
		{Kind: runlog.KindRunFinished, TS: "2026-09-04T10:00:03.5Z"},
	}
	r := ckRunFromEvents("r1", ok, false)
	if r.status != "ok" || r.task != "add pagination" || r.costUSD != 0.002 {
		t.Errorf("ok run folded wrong: %+v", r)
	}
	if r.durMS <= 0 {
		t.Errorf("no duration derived from the event span: %d", r.durMS)
	}

	// A fallback that eventually succeeded is ok, not failed.
	fellBack := []runlog.Event{
		{Kind: runlog.KindHeadSelected, TS: "2026-09-04T10:00:00Z", Head: "a", Model: "A", Detail: "candidate 1 of 2"},
		{Kind: runlog.KindError, TS: "2026-09-04T10:00:01Z", Head: "a", Model: "A", Status: "failed", Detail: "timeout"},
		{Kind: runlog.KindHeadSelected, TS: "2026-09-04T10:00:02Z", Head: "b", Model: "B", Detail: "candidate 2 of 2"},
		{Kind: runlog.KindDispatchFinished, TS: "2026-09-04T10:00:04Z", Head: "b", Model: "B", Status: "ok"},
	}
	if r := ckRunFromEvents("r2", fellBack, false); r.status != "ok" || r.fails != 1 {
		t.Errorf("fallback-then-success folded wrong: %+v", r)
	}

	// Everything failed → failed.
	failed := []runlog.Event{
		{Kind: runlog.KindHeadSelected, TS: "2026-09-04T10:00:00Z", Head: "a", Model: "A"},
		{Kind: runlog.KindError, TS: "2026-09-04T10:00:01Z", Head: "a", Model: "A", Status: "failed", Detail: "boom"},
		{Kind: runlog.KindRunFinished, TS: "2026-09-04T10:00:02Z"},
	}
	if r := ckRunFromEvents("r3", failed, false); r.status != "failed" {
		t.Errorf("failed run read as %q", r.status)
	}

	// A live heartbeat wins over everything: running, not ok.
	if r := ckRunFromEvents("r4", ok, true); r.status != "running" {
		t.Errorf("a live run read as %q", r.status)
	}

	// Started, no finish, no heartbeat: interrupted, never ok.
	dead := []runlog.Event{{Kind: runlog.KindRunStarted, TS: "2026-09-04T10:00:00Z", Detail: "t"}}
	if r := ckRunFromEvents("r5", dead, false); r.status != "failed" {
		t.Errorf("an interrupted run read as %q", r.status)
	}

	// A clean finish with nothing recorded in between is ok, not failed.
	quiet := []runlog.Event{
		{Kind: runlog.KindRunStarted, TS: "2026-09-04T10:00:00Z", Detail: "hi"},
		{Kind: runlog.KindRunFinished, TS: "2026-09-04T10:00:01Z"},
	}
	if r := ckRunFromEvents("r6", quiet, false); r.status != "ok" {
		t.Errorf("a clean empty run read as %q", r.status)
	}

	// Swarm attempts and dispatch rows describe the same spend, never summed.
	swarm := []runlog.Event{
		{Kind: runlog.KindAttempt, TS: "2026-09-04T10:00:00Z", Head: "a", Model: "A", Status: "ok", CostUSD: 0.01},
		{Kind: runlog.KindDispatchFinished, TS: "2026-09-04T10:00:01Z", Head: "a", Model: "A", Status: "ok", CostUSD: 0.01},
	}
	if r := ckRunFromEvents("r7", swarm, false); r.costUSD != 0.01 {
		t.Errorf("swarm cost double-counted: %v", r.costUSD)
	}

	// Edits are collected for `o`.
	edits := []runlog.Event{
		{Kind: runlog.KindEdit, TS: "2026-09-04T10:00:00Z", File: "a.go", Detail: "+3/-1"},
		{Kind: runlog.KindEdit, TS: "2026-09-04T10:00:01Z", File: "b.go", Detail: "+1/-0"},
		{Kind: runlog.KindRunFinished, TS: "2026-09-04T10:00:02Z"},
	}
	if r := ckRunFromEvents("r8", edits, false); len(r.edited) != 2 {
		t.Errorf("edited files = %v", r.edited)
	}
}

// The header counts are the same buckets the glyphs use, consistent.
func TestCkRunCounts_Consistent(t *testing.T) {
	runs := []ckRun{
		{status: "ok"}, {status: "ok"}, {status: "failed"}, {status: "running"},
	}
	ok, failed, running := ckRunCounts(runs)
	if ok != 2 || failed != 1 || running != 1 {
		t.Errorf("counts = %d/%d/%d", ok, failed, running)
	}
	if ok+failed+running != len(runs) {
		t.Error("the buckets do not sum to the list")
	}
}

// With nothing recorded the loader returns nothing, an honest empty state,
// not an example (#191). With a run file written, it loads today's runs and
// skips other days by the id's date prefix.
func TestCkLoadRuns_EmptyThenRealMachine(t *testing.T) {
	testutil.NewSandbox(t)
	now := time.Now().UTC()
	if got := ckLoadRuns(now); len(got) != 0 {
		t.Errorf("ckLoadRuns on an empty machine = %d runs", len(got))
	}

	writeRunFile(t, now.Format("20060102T150405Z")+"-aaaa11112222", []string{
		`{"v":1,"seq":1,"ts":"` + now.Format(time.RFC3339) + `","kind":"run_started","detail":"today's task"}`,
		`{"v":1,"seq":2,"ts":"` + now.Format(time.RFC3339) + `","kind":"run_finished"}`,
	})
	// A run from another day must be skipped without even being read.
	writeRunFile(t, "20200101T000000Z-bbbb11112222", []string{
		`{"v":1,"seq":1,"ts":"2020-01-01T00:00:00Z","kind":"run_started","detail":"old"}`,
	})
	got := ckLoadRuns(now)
	if len(got) != 1 {
		t.Fatalf("ckLoadRuns = %d runs, want 1 (today's)", len(got))
	}
	if got[0].task != "today's task" || got[0].status != "ok" {
		t.Errorf("run folded wrong: %+v", got[0])
	}
}

func writeRunFile(t *testing.T, id string, lines []string) {
	t.Helper()
	if err := os.MkdirAll(runlog.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(runlog.Path(id), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// ── trace ───────────────────────────────────────────────────────────────────────

// The trace renders the run's real shape: routed (with the class and the why
// sub-line), the inferred guardrail outcome, request, stream, and done, and
// the fallbacks line only when the first candidate answered.
func TestCkTrace_SingleCleanRun(t *testing.T) {
	run := ckRunFromEvents("r1", []runlog.Event{
		{Kind: runlog.KindRunStarted, TS: "2026-09-04T10:00:00Z", Detail: "add pagination"},
		{Kind: runlog.KindHeadSelected, TS: "2026-09-04T10:00:01Z", Head: "ollama/qwen", Model: "qwen (Ollama)", Tier: 6, Detail: "candidate 1 of 3"},
		{Kind: runlog.KindDispatchFinished, TS: "2026-09-04T10:00:03Z", Head: "ollama/qwen", Model: "qwen", Status: "ok", CostUSD: 0.002},
		{Kind: runlog.KindRunFinished, TS: "2026-09-04T10:00:03.5Z"},
	}, false)
	rc := ckRunCost{enum: "STANDARD", prompt: 1200, resp: 400, actual: 1600, costUSD: 0.002}
	rows := ckTrace(run, rc, true, 0.002)

	joined := renderTrace(rows)
	for _, want := range []string{
		"routed", "STANDARD", "T6", "single",
		"why: candidate 1 of 3",
		"policy", "allowed", "recorded in the audit log",
		"request", "ollama/qwen → qwen",
		"stream", "1200 + 400 tokens", "provider-reported",
		"fallbacks", "none, first candidate answered",
		"done", "$0.0020 est",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("trace missing %q:\n%s", want, joined)
		}
	}
}

// A guardrail denial renders as the policy row with its reason, in the UI
// vocabulary (audit log, guardrails, not ledger), and is not duplicated as a
// separate error row.
func TestCkTrace_DeniedCandidate(t *testing.T) {
	run := ckRunFromEvents("r1", []runlog.Event{
		{Kind: runlog.KindHeadSelected, TS: "2026-09-04T10:00:00Z", Head: "gpt", Model: "gpt-4o", Tier: 3, Detail: "candidate 1 of 2"},
		{Kind: runlog.KindError, TS: "2026-09-04T10:00:01Z", Head: "gpt", Model: "gpt-4o", Status: "denied", Detail: "denied by ledger policy"},
		{Kind: runlog.KindHeadSelected, TS: "2026-09-04T10:00:02Z", Head: "q", Model: "qwen", Tier: 10, Detail: "candidate 2 of 2"},
		{Kind: runlog.KindDispatchFinished, TS: "2026-09-04T10:00:04Z", Head: "q", Model: "qwen", Status: "ok"},
	}, false)
	rows := ckTrace(run, ckRunCost{}, false, 0)
	joined := renderTrace(rows)

	if !strings.Contains(joined, "denied, denied by guardrails") {
		t.Errorf("the denial is not worded via guardrails:\n%s", joined)
	}
	if strings.Contains(joined, "ledger") {
		t.Errorf("internal jargon leaked into the trace:\n%s", joined)
	}
	if !strings.Contains(joined, "fallback") {
		t.Errorf("the second candidate is not labelled a fallback:\n%s", joined)
	}
	if strings.Contains(joined, "none, first candidate answered") {
		t.Errorf("the fallbacks-none line rendered on a run WITH fallbacks:\n%s", joined)
	}
	// The denied error must appear exactly once (as the policy row).
	if n := strings.Count(joined, "denied by guardrails"); n != 1 {
		t.Errorf("the denial rendered %d times:\n%s", n, joined)
	}
}

// Edits, consensus samples, and errors all render; tokens absent from the
// cost log are said to be absent, not invented.
func TestCkTrace_EditsSamplesAndHonestTokens(t *testing.T) {
	run := ckRunFromEvents("r1", []runlog.Event{
		{Kind: runlog.KindHeadSelected, TS: "2026-09-04T10:00:00Z", Head: "h", Model: "m", Tier: 3, Detail: "candidate 1 of 1"},
		{Kind: runlog.KindDispatchFinished, TS: "2026-09-04T10:00:01Z", Head: "h", Model: "m", Status: "ok"},
		{Kind: runlog.KindEdit, TS: "2026-09-04T10:00:02Z", File: "internal/auth/token.go", Detail: "+12/-3"},
		{Kind: runlog.KindSample, TS: "2026-09-04T10:00:03Z", Head: "s", Model: "m2", Confidence: 0.93, Detail: "SPRT ensemble · 3 samples"},
		{Kind: runlog.KindRunFinished, TS: "2026-09-04T10:00:04Z"},
	}, false)
	joined := renderTrace(ckTrace(run, ckRunCost{}, false, 0))

	for _, want := range []string{
		"edit", "internal/auth/token.go", "+12/-3",
		"consensus", "consensus check · 3 samples", "confidence 0.93",
		"stream", "tokens not recorded for this run",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("trace missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "SPRT") {
		t.Errorf("internal jargon leaked:\n%s", joined)
	}
}

// A running run ends its trace with live, a failed one with failed, never a
// fabricated done row.
func TestCkTrace_TerminalRow(t *testing.T) {
	live := ckRunFromEvents("r", []runlog.Event{
		{Kind: runlog.KindRunStarted, TS: "2026-09-04T10:00:00Z", Detail: "t"},
	}, true)
	if joined := renderTrace(ckTrace(live, ckRunCost{}, false, 0)); !strings.Contains(joined, "still running") {
		t.Errorf("a live run's trace does not say running:\n%s", joined)
	}
	failed := ckRunFromEvents("r", []runlog.Event{
		{Kind: runlog.KindHeadSelected, TS: "2026-09-04T10:00:00Z", Head: "a", Model: "A"},
		{Kind: runlog.KindError, TS: "2026-09-04T10:00:01Z", Head: "a", Model: "A", Status: "failed", Detail: "boom"},
	}, false)
	joined := renderTrace(ckTrace(failed, ckRunCost{}, false, 0))
	if !strings.Contains(joined, "no successful answer") {
		t.Errorf("a failed run's trace does not say so:\n%s", joined)
	}
	if strings.Contains(joined, "done") {
		t.Errorf("a failed run rendered a done row:\n%s", joined)
	}
}

func renderTrace(rows []ckTraceRow) string {
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(r.label + " " + r.text + " " + r.sub + "\n")
	}
	return b.String()
}

// ckPolicyOutcome infers the gate decision from what followed, and invents
// nothing when nothing followed.
func TestCkPolicyOutcome(t *testing.T) {
	events := []runlog.Event{
		{Kind: runlog.KindError, Head: "a", Status: "denied", Detail: "nope"},
		{Kind: runlog.KindDispatchFinished, Head: "b", Status: "ok"},
		{Kind: runlog.KindError, Head: "c", Status: "failed", Detail: "timeout"},
	}
	if dec, reason := ckPolicyOutcome(events, "a"); dec != "denied" || reason != "nope" {
		t.Errorf("a = %s/%s", dec, reason)
	}
	if dec, _ := ckPolicyOutcome(events, "b"); dec != "allowed" {
		t.Errorf("b = %s", dec)
	}
	// An execution failure means the gate allowed it.
	if dec, _ := ckPolicyOutcome(events, "c"); dec != "allowed" {
		t.Errorf("c = %s", dec)
	}
	if dec, _ := ckPolicyOutcome(events, "unknown"); dec != "" {
		t.Errorf("unknown = %q, want no invented outcome", dec)
	}
}

func TestCkTokenSource(t *testing.T) {
	if got := ckTokenSource(ckRunCost{actual: 10}); got != "provider-reported" {
		t.Errorf("actual-only = %q", got)
	}
	if got := ckTokenSource(ckRunCost{est: 10}); got != "estimated" {
		t.Errorf("est-only = %q", got)
	}
	if got := ckTokenSource(ckRunCost{actual: 5, est: 5}); got != "mixed actual/estimated" {
		t.Errorf("mixed = %q", got)
	}
}

// ── rendered view ───────────────────────────────────────────────────────────────

// The activity view: consistent header counts, run rows, the trace panel, and
// the failures filter.
func TestViewActivity_ListTraceAndFilter(t *testing.T) {
	m := testCockpit()
	m.view = ckViewActivity
	out := stripANSI(m.viewActivity(120, 26))
	for _, want := range []string{
		"RUNS · today, 3", "1 ok · 1 failed · 1 running",
		"add pagination", "rotate signing key", "write tests",
		"TRACE",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("activity view missing %q:\n%s", want, out)
		}
	}

	m.actFailOnly = true
	out = stripANSI(m.viewActivity(120, 26))
	if !strings.Contains(out, "RUNS · failures, 1") {
		t.Errorf("failures filter header wrong:\n%s", out)
	}
	if strings.Contains(out, "add pagination") {
		t.Errorf("a passing run survived the failures filter:\n%s", out)
	}
	// Counts stay consistent under the filter: 0 ok · 1 failed · 0 running.
	if !strings.Contains(out, "0 ok · 1 failed · 0 running") {
		t.Errorf("filtered counts are inconsistent:\n%s", out)
	}

	// Honest empty states.
	empty := testCockpit()
	empty.runsToday = nil
	out = stripANSI(empty.viewActivity(120, 26))
	for _, want := range []string{"no runs today", "start one from chat", "hyctl dispatch"} {
		if !strings.Contains(out, want) {
			t.Errorf("empty state missing %q:\n%s", want, out)
		}
	}
}

// Drilling in focuses the trace (narrow terminals show it alone), esc returns.
func TestViewActivity_DrillFocus(t *testing.T) {
	m := testCockpit()
	m.view = ckViewActivity
	m.w, m.h = 60, 20
	out := stripANSI(m.viewActivity(60, 17))
	if !strings.Contains(out, "RUNS · today") {
		t.Errorf("narrow activity lost the list:\n%s", out)
	}
	m.actDrill = true
	out = stripANSI(m.viewActivity(60, 17))
	if !strings.Contains(out, "TRACE") || strings.Contains(out, "RUNS · today") {
		t.Errorf("drilled narrow view should show the trace alone:\n%s", out)
	}
	if !strings.Contains(out, "esc back") {
		t.Errorf("the drilled trace does not say how to go back:\n%s", out)
	}
}

// `o` opens the first edited file, or says honestly why it cannot.
func TestActivity_OpenEditedFile(t *testing.T) {
	m := testCockpit()
	m.view = ckViewActivity
	m.actSel = 0

	t.Setenv("EDITOR", "")
	next, cmd := m.openEditedFile()
	m = next.(Cockpit)
	if cmd != nil {
		t.Error("a run with no edits spawned an editor")
	}
	if !strings.Contains(m.flash, "no edited files") {
		t.Errorf("flash = %q", m.flash)
	}

	m.runsToday[0].edited = []string{"a.go"}
	next, cmd = m.openEditedFile()
	m = next.(Cockpit)
	if cmd != nil {
		t.Error("no $EDITOR set but a command was spawned")
	}
	if !strings.Contains(m.flash, "$EDITOR") {
		t.Errorf("flash = %q", m.flash)
	}

	t.Setenv("EDITOR", "true")
	next, cmd = m.openEditedFile()
	if cmd == nil {
		t.Error("with $EDITOR set no editor was spawned")
	}
	_ = next
}

// `c` says honestly that run files hold no output yet (#597).
func TestActivity_CopyAnswerIsHonest(t *testing.T) {
	m := testCockpit()
	m.view = ckViewActivity
	m = typed(m, "c")
	if !strings.Contains(m.flash, "no answer stored") {
		t.Errorf("flash = %q", m.flash)
	}
}

// `l` jumps to the audit view and builds it on entry.
func TestActivity_JumpToAuditLog(t *testing.T) {
	testutil.NewSandbox(t)
	m := testCockpit()
	m.view = ckViewActivity
	m = typed(m, "l")
	if m.view != ckViewAudit {
		t.Errorf("l left view = %d", m.view)
	}
	if m.audit == nil {
		t.Error("jumping to audit did not build it")
	}
}

// ── agents view ─────────────────────────────────────────────────────────────────

func TestViewAgents_ListsLiveThenFinished(t *testing.T) {
	m := testCockpit()
	out := stripANSI(m.viewAgents(120, 24))
	for _, want := range []string{
		"AGENTS", "1 live · 2 finished today",
		"⠸", "✓", "✗",
		"write tests", "started",
		"$0.0000 est",
		"enter opens the run's trace",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("agents view missing %q:\n%s", want, out)
		}
	}
	// Live rows come first.
	liveIdx := strings.Index(out, "write tests")
	doneIdx := strings.Index(out, "add pagination")
	if liveIdx < 0 || doneIdx < 0 || liveIdx > doneIdx {
		t.Errorf("live run is not listed first:\n%s", out)
	}

	empty := testCockpit()
	empty.runsToday = nil
	out = stripANSI(empty.viewAgents(120, 24))
	for _, want := range []string{"no agents running", "Start one from chat"} {
		if !strings.Contains(out, want) {
			t.Errorf("agents empty state missing %q:\n%s", want, out)
		}
	}
	// The empty state promises nothing that does not exist.
	for _, ghost := range []string{"approval", "gradient", "fleet"} {
		if strings.Contains(strings.ToLower(out), ghost) {
			t.Errorf("the empty state fabricates %q:\n%s", ghost, out)
		}
	}
}

// focusRun lands on the right run even from the audit queue.
func TestFocusRun_SelectsTheRun(t *testing.T) {
	m := testCockpit()
	m = m.focusRun(m.runsToday[1].id)
	if m.view != ckViewActivity || !m.actDrill {
		t.Fatalf("focusRun left view=%d drill=%v", m.view, m.actDrill)
	}
	if got := m.activityRuns()[m.actSel].id; got != m.runsToday[1].id {
		t.Errorf("selected %s, want %s", got, m.runsToday[1].id)
	}
	// An unknown id falls back to the top rather than panicking.
	m = m.focusRun("nonexistent")
	if m.actSel != 0 {
		t.Errorf("unknown id selected %d", m.actSel)
	}
}
