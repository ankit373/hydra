// SPDX-License-Identifier: MIT

package tui

// chat_task.go — the UI side of a chat task's life: launch with its route
// line, the plan/confirm gates, the finished rendering (answer block, proof
// strip, footer), and the d/x/o result actions over runlog edit snapshots.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ankit373/hydra/internal/diff"
	"github.com/ankit373/hydra/internal/policy"
	"github.com/ankit373/hydra/internal/runid"
	"github.com/ankit373/hydra/internal/runlog"
)

// ckTierEnum inverts dispatch's enum→tier table for the editor path, which
// takes an enum, not a tier number.
var ckTierEnum = map[int]string{
	1: "CORE", 2: "EXPERT", 3: "VERY_HARD", 4: "HARD", 5: "COMPLEX",
	6: "MODERATE", 7: "STANDARD", 8: "SIMPLE", 9: "TRIVIAL", 10: "GRUNT",
}

// ── launch ────────────────────────────────────────────────────────────────────

// startTask classifies the task, prints the route line, and spawns the
// pipeline worker. The ctrl+o override is consumed here — next task only.
func (m Cockpit) startTask(task string) (Cockpit, tea.Cmd) {
	m.flash = "" // an override's "next task" note is consumed by this task
	m.log = append(m.log, ckYouS.Render("❯ "+ckSafe(task)))

	md := ckModeByName(m.mode)
	enum, wantTier := classifyTask(task)
	ov := m.override
	m.override = ckOverride{}

	pinned := m.pinnedTier > 0 && ov.kind != 'T'
	switch {
	case ov.kind == 'T':
		wantTier = ov.tier
	case pinned:
		wantTier = m.pinnedTier
	}

	class := policy.Classify(task)
	localOnly := ov.kind == 'L' || (class.PII && m.piiLocal)

	idx := m.pickHead(wantTier, localOnly)
	// A real scan can legitimately find nothing; inventing a route is exactly
	// what #189 removed, and dispatching would only fail later and worse.
	if idx < 0 {
		m.log = append(m.log, ckDimS.Render("  no routable model — run `hyctl probe` to see why"))
		return m, nil
	}
	h := m.heads[idx]

	file := ckNamedFile(task)
	t := ckTask{
		prompt: task, mode: md, file: file,
		runID: runid.New(), taskID: runid.New(),
		answerTier: strconv.Itoa(wantTier),
		planTier:   md.planTier,
		editEnum:   ckEditEnum(enum, md, ov),
		localOnly:  localOnly,
		pii:        class.PII,
		startedAt:  time.Now(),
	}
	if t.planTier == "" {
		t.planTier = ckPlanTierDefault
	}
	// Fan-out strategies produce one answer, not a file write, so an edit task
	// stays single — visibly, on the route line, not silently.
	if ov.kind == 'B' || ov.kind == 'C' {
		if file == "" || md.name == "ask" {
			t.strategy, t.confidence = ov.kind, ov.conf
		}
	}
	if md.verify {
		t.verifyArgv, t.verifyLabel = ckVerifyArgs(file)
	}

	m.log = append(m.log, m.routeLines(t, h, enum, ov, pinned)...)
	if f := ckFileRe.FindString(task); f != "" {
		if radius, deps, kappa, ok := m.metrics.ckBlastFor(f); ok {
			risk := ckCheapS
			if kappa >= 2 {
				risk = ckExpS
			}
			m.log = append(m.log, ckDimS.Render("  impact ")+
				risk.Render(fmt.Sprintf("κ=%.1f", kappa))+
				ckDimS.Render(fmt.Sprintf("  %d dependent%s · radius %.2f×  → %s",
					deps, plural(deps), radius, truncate(f, 40))))
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), ckTaskTimeout)
	ex := &ckExecState{ctx: ctx, cancel: cancel, started: t.startedAt, stage: "routing"}
	m.exec = ex
	m.lastDone = nil
	m.chatScroll = 0

	phase := ckPhaseFull
	if md.name == "plan" || (md.confirm && file != "") {
		phase = ckPhaseHead
	}
	return m, tea.Batch(ckWorker(ex, t, phase), ckSpinTick(ex))
}

// ckEditEnum picks the editor path's routing enum: the classification, the
// cheap tier for architect's implement half, or a forced tier's equivalent.
// CORE maps to EXPERT — the editor refuses CORE by design.
func ckEditEnum(classified string, md ckModeDef, ov ckOverride) string {
	enum := classified
	if md.cheapImpl {
		enum = "SIMPLE"
	}
	if ov.kind == 'T' {
		enum = ckTierEnum[ov.tier]
	}
	if enum == "CORE" {
		enum = "EXPERT"
	}
	return enum
}

// ckNamedFile returns the absolute path of a file the prompt names — only when
// it exists on disk. Edit runs against real files, never guesses.
func ckNamedFile(task string) string {
	f := ckFileRe.FindString(task)
	if f == "" {
		return ""
	}
	abs, err := filepath.Abs(f)
	if err != nil {
		return ""
	}
	if st, err := os.Stat(abs); err != nil || st.IsDir() {
		return ""
	}
	return abs
}

// ── route line ────────────────────────────────────────────────────────────────

// routeLines is the pre-execution routing disclosure: how it was routed, the
// class in plain words, the tier/model, the strategy, and a why clause.
func (m Cockpit) routeLines(t ckTask, h ckHead, enum string, ov ckOverride, pinned bool) []string {
	lead := "auto-routed"
	switch {
	case ov.kind == 'T':
		lead = "forced"
	case ov.kind == 'L':
		lead = "local only"
	case pinned:
		lead = "pinned"
	}
	strategy := ov.strategy()
	if (ov.kind == 'B' || ov.kind == 'C') && t.strategy == 0 {
		strategy = "single (edits can't fan out)"
	}
	name := lipgloss.NewStyle().Foreground(ckTierColor(h.tier))
	first := ckDimS.Render("  "+lead+" · ") + ckInkS.Render(ckClassWords(enum)) +
		ckAquaS.Render(" → ") + ckDimS.Render(fmt.Sprintf("T%d ", h.tier)) + name.Render(h.name) +
		ckDimS.Render(" · "+strategy)
	second := ckFaintS.Render("  why ") + ckDimS.Render(ckWhyWords(t, enum))
	if t.pii && t.localOnly {
		second += ckMidS.Render(" · local-only (pii)")
	}
	return []string{first, second}
}

// ckClassWords renders a routing enum as plain language.
func ckClassWords(enum string) string {
	switch enum {
	case "CORE":
		return "critical work"
	case "COMPLEX":
		return "complex work"
	case "MODERATE":
		return "moderate work"
	case "STANDARD":
		return "standard work"
	default:
		return "simple task"
	}
}

// ckWhyWords is the route line's plain-words reasoning clause.
func ckWhyWords(t ckTask, enum string) string {
	kind := "question"
	if t.file != "" && t.mode.name != "ask" {
		kind = "code edit"
	}
	scope := map[string]string{
		"CORE": "critical", "COMPLEX": "complex", "MODERATE": "moderate",
		"STANDARD": "standard",
	}[enum]
	if scope == "" {
		scope = "small"
	}
	pii := "no PII"
	if t.pii {
		pii = "PII detected"
	}
	return kind + ", " + scope + " scope, " + pii
}

// ── gates ─────────────────────────────────────────────────────────────────────

// gateTask handles a pipeline paused for the user: render what is waiting and
// arm the matching wait state.
func (m Cockpit) gateTask(msg ckGateMsg) (Cockpit, tea.Cmd) {
	if msg.exec != m.exec || m.exec == nil {
		return m, nil
	}
	m.exec = nil
	t := msg.task
	switch msg.gate {
	case 'p':
		m.log = append(m.log, ckPlanLines(t)...)
		m.log = append(m.log, ckFaintS.Render("  enter/y runs it · esc discards"))
		m.planWait = &ckWait{task: t, phase: ckPhaseTail}
	case 'w':
		m.log = append(m.log, ckPlanLines(t)...)
		m.confirm = &ckWait{task: t, phase: ckPhaseTail,
			question: "write " + filepath.Base(t.file) + "? y writes · n stops"}
	case 'f':
		m.log = append(m.log, ckExpS.Render("  ✗ verify failed ")+
			ckDimS.Render(truncate(ckSafe(t.fixDetail), 70)))
		m.confirm = &ckWait{task: t, phase: ckPhaseFix,
			question: fmt.Sprintf("write fix %d/%d to %s? y/n", t.fixRound, ckMaxFixes, filepath.Base(t.file))}
	}
	return m, nil
}

// ckPlanLines renders a drafted plan into the log, capped so a rambling plan
// cannot flood the pane (scrollback still holds what is shown).
func ckPlanLines(t ckTask) []string {
	out := []string{ckLabelS.Render("  PLAN") + ckDimS.Render(
		fmt.Sprintf(" — %d step%s · drafted by %s", t.planSteps, plural(t.planSteps), t.headName))}
	lines := strings.Split(strings.TrimRight(t.plan, "\n"), "\n")
	const keep = 12
	over := 0
	if len(lines) > keep {
		over = len(lines) - keep
		lines = lines[:keep]
	}
	for _, l := range lines {
		out = append(out, ckDimS.Render("   "+ckSafe(l)))
	}
	if over > 0 {
		out = append(out, ckFaintS.Render(fmt.Sprintf("   … %d more line%s", over, plural(over))))
	}
	return out
}

// resumeTask restarts a gated pipeline at its stored phase with a fresh
// context; elapsed time keeps counting from the original start.
func (m Cockpit) resumeTask(w ckWait) (Cockpit, tea.Cmd) {
	ctx, cancel := context.WithTimeout(context.Background(), ckTaskTimeout)
	ex := &ckExecState{ctx: ctx, cancel: cancel, started: w.task.startedAt, stage: "resuming"}
	m.exec = ex
	m.planWait, m.confirm = nil, nil
	return m, tea.Batch(ckWorker(ex, w.task, w.phase), ckSpinTick(ex))
}

// stopWait ends a gated task without resuming it: the run closes, and what
// already happened (plan cost, a landed edit) is settled honestly.
func (m Cockpit) stopWait(w ckWait, note string) (Cockpit, tea.Cmd) {
	m.planWait, m.confirm = nil, nil
	t := w.task
	_ = runlog.New(t.runID).Append(runlog.Event{Kind: runlog.KindRunFinished, TaskID: t.taskID})
	t.stopped = true
	t.note = note
	ckFinalize(&t, context.Background())
	return m.settleTask(t)
}

// ── completion ────────────────────────────────────────────────────────────────

// finishTask lands a worker's final message.
func (m Cockpit) finishTask(msg ckExecDoneMsg) (Cockpit, tea.Cmd) {
	if msg.exec != m.exec || m.exec == nil {
		return m, nil
	}
	m.exec = nil
	return m.settleTask(msg.task)
}

// settleTask renders a finished task and updates everything that watched it:
// session cost, the run list, the last-result actions, and the code panel.
func (m Cockpit) settleTask(t ckTask) (Cockpit, tea.Cmd) {
	m.sessionUSD += t.costUSD
	m.runsToday = ckLoadRuns(time.Now().UTC())
	m.lastDone = &t
	m.log = append(m.log, ckResultLines(t)...)
	if t.edited && t.editRefLast != "" {
		if _, after, err := runlog.LoadEdit(t.runID, t.editRefLast); err == nil {
			m = m.showCode(t.file, string(after))
			return m, ckCodeTick(m.codeGen)
		}
	}
	return m, nil
}

// showCode streams content into the code panel (real file content — the fake
// snippets died with the preview-only chat).
func (m Cockpit) showCode(file, content string) Cockpit {
	m.codeLang = strings.TrimPrefix(filepath.Ext(file), ".")
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	if len(lines) > ckCodeMaxLines {
		lines = append(lines[:ckCodeMaxLines], "… (truncated)")
	}
	m.codeLines = lines
	m.codeShown = 0
	m.codeDiff = false
	m.codeGen++
	return m
}

// ckResultLines renders a finished task: answer block, proof strip, footer.
// A failure always carries its trace link — never a dead end.
func ckResultLines(t ckTask) []string {
	var out []string
	trace := ckDimS.Render(" · trace "+ckShortID(t.runID)) + ckFaintS.Render(" — enter opens the trace")
	secs := fmt.Sprintf("%.1fs", t.elapsed.Seconds())
	switch {
	case t.canceled:
		return append(out, ckMidS.Render("  ✗ cancelled ")+ckDimS.Render(secs)+trace)
	case t.errText != "":
		out = append(out, ckExpS.Render("  ✗ failed ")+ckDimS.Render(secs+" · ")+
			ckExpS.Render(truncate(ckSafe(ckFirstLine(t.errText)), 90)))
		return append(out, ckFaintS.Render("  trace "+ckShortID(t.runID)+" — enter opens the trace"))
	}
	if t.answer != "" {
		lines := strings.Split(strings.TrimRight(t.answer, "\n"), "\n")
		over := 0
		if len(lines) > ckAnswerCapLines {
			over = len(lines) - ckAnswerCapLines
			lines = lines[:ckAnswerCapLines]
		}
		for _, l := range lines {
			out = append(out, ckAquaS.Render("  ▎ ")+ckInkS.Render(ckSafe(l)))
		}
		if over > 0 {
			out = append(out, ckFaintS.Render(fmt.Sprintf("  ▎ … %d more line%s truncated", over, plural(over))))
		}
	}
	if t.note != "" {
		out = append(out, ckDimS.Render("  "+t.note))
	}
	if t.edited {
		out = append(out, ckProofStrip(t))
		out = append(out, ckFaintS.Render("  d diff · x undo · o open — on an empty input"))
	}
	glyph, tone := "✓ done ", ckCheapS
	if t.stopped {
		glyph, tone = "◼ stopped ", ckMidS
	}
	foot := tone.Render("  "+glyph) + ckDimS.Render(fmt.Sprintf("%s · $%.4f est", secs, t.costUSD))
	if t.conf > 0 {
		foot += ckVioletS.Render(" · confidence " + ckPct(t.conf))
	}
	return append(out, foot+trace)
}

// ckProofStrip is the design's per-edit proof line: plan ✓ N steps · edit ✓
// file +A/−R · tests ✓/✗ cmd summary.
func ckProofStrip(t ckTask) string {
	var parts []string
	if t.mode.plan {
		parts = append(parts, ckCheapS.Render("plan ✓ ")+
			ckDimS.Render(fmt.Sprintf("%d step%s", t.planSteps, plural(t.planSteps))))
	}
	parts = append(parts, ckCheapS.Render("edit ✓ ")+
		ckDimS.Render(fmt.Sprintf("%s +%d/−%d", filepath.Base(t.file), t.added, t.removed)))
	parts = append(parts, ckTestsCell(t))
	return "  " + strings.Join(parts, ckFaintS.Render(" · "))
}

// ckTestsCell is the strip's verification verdict, honest about every state:
// passed (and after how many fixes), still failing, skipped, unconfigured, or
// the verifier itself failing to run.
func ckTestsCell(t ckTask) string {
	switch {
	case !t.mode.verify:
		return ckFaintS.Render("tests — skipped (" + t.mode.name + " mode)")
	case t.verifySkipped:
		return ckFaintS.Render("tests — no verifier configured")
	case t.verifyErr != "":
		return ckMidS.Render("tests ? ") + ckDimS.Render(
			t.verifyLabel+" — verifier failed: "+truncate(ckSafe(t.verifyErr), 40))
	case len(t.rounds) == 0:
		return ckFaintS.Render("tests — not run")
	}
	last := t.rounds[len(t.rounds)-1]
	fixes := len(t.rounds) - 1
	if last.passed {
		s := ckCheapS.Render("tests ✓ ") + ckDimS.Render(t.verifyLabel)
		if fixes > 0 {
			s += ckDimS.Render(fmt.Sprintf(" (after %d %s)", fixes, ckFixWord(fixes)))
		}
		return s
	}
	summary := fmt.Sprintf("%s — still failing", t.verifyLabel)
	if fixes > 0 {
		summary += fmt.Sprintf(" after %d %s", fixes, ckFixWord(fixes))
	}
	return ckExpS.Render("tests ✗ ") + ckDimS.Render(summary+": "+truncate(ckSafe(last.detail), 40))
}

func ckFixWord(n int) string {
	if n == 1 {
		return "fix"
	}
	return "fixes"
}

func ckFirstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// ── result actions (d/x/o) ────────────────────────────────────────────────────

// resultKey acts on the last finished edit from an empty input: d toggles the
// diff in the code panel, x restores the pre-task snapshot, o opens $EDITOR.
// Returns handled=false when there is nothing to act on, so the rune types.
func (m Cockpit) resultKey(r rune) (Cockpit, tea.Cmd, bool) {
	t := m.lastDone
	if t == nil || !t.edited {
		return m, nil, false
	}
	switch r {
	case 'd':
		return m.toggleDiff(t), nil, true
	case 'x':
		return m.undoEdit(t), nil, true
	case 'o':
		ed := os.Getenv("EDITOR")
		if ed == "" {
			m.flash = "set $EDITOR to open " + truncate(t.file, 30)
			return m, nil, true
		}
		return m, tea.ExecProcess(exec.Command(ed, t.file), func(error) tea.Msg { return nil }), true
	}
	return m, nil, false
}

// toggleDiff swaps the code panel between the task's full diff (first before →
// last after, so the fix loop's intermediate writes collapse) and the file.
func (m Cockpit) toggleDiff(t *ckTask) Cockpit {
	if t.editRef == "" || t.editRefLast == "" {
		m.flash = "no snapshot stored for this edit"
		return m
	}
	if m.codeDiff {
		if _, after, err := runlog.LoadEdit(t.runID, t.editRefLast); err == nil {
			m = m.showCode(t.file, string(after))
			m.codeShown = len(m.codeLines)
		}
		return m
	}
	before, _, err := runlog.LoadEdit(t.runID, t.editRef)
	if err != nil {
		m.flash = "diff unavailable — " + err.Error()
		return m
	}
	_, after, err := runlog.LoadEdit(t.runID, t.editRefLast)
	if err != nil {
		m.flash = "diff unavailable — " + err.Error()
		return m
	}
	base := filepath.Base(t.file)
	lines := strings.Split(strings.TrimRight(diff.Unified("a/"+base, "b/"+base, before, after), "\n"), "\n")
	if len(lines) > ckCodeMaxLines {
		lines = append(lines[:ckCodeMaxLines], "… (truncated)")
	}
	m.codeLang = "diff"
	m.codeLines = lines
	m.codeShown = len(lines)
	m.codeDiff = true
	m.codeGen++ // cancel any stream in flight
	return m
}

// undoEdit restores the file to its exact pre-task bytes from the first edit
// snapshot, preserving the file's permissions. One-shot per task.
func (m Cockpit) undoEdit(t *ckTask) Cockpit {
	if t.undone {
		m.flash = "already restored"
		return m
	}
	if t.editRef == "" {
		m.flash = "no snapshot stored for this edit"
		return m
	}
	before, _, err := runlog.LoadEdit(t.runID, t.editRef)
	if err != nil {
		m.flash = "undo unavailable — " + err.Error()
		return m
	}
	mode := os.FileMode(0o644)
	if st, serr := os.Stat(t.file); serr == nil {
		mode = st.Mode().Perm()
	}
	if werr := os.WriteFile(t.file, before, mode); werr != nil {
		m.flash = "undo failed — " + werr.Error()
		return m
	}
	t.undone = true // through the pointer: the second x says "already restored"
	m.flash = "restored " + filepath.Base(t.file) + " to its pre-task state"
	m = m.showCode(t.file, string(before))
	m.codeShown = len(m.codeLines)
	return m
}
