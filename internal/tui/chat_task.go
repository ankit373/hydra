// SPDX-License-Identifier: MIT

package tui

// chat_task.go, the UI side of a chat task's life on its thread: launch with
// the route line (through the overlap gate and worktree isolation), the
// plan/confirm gates, the finished rendering (answer block, proof strip,
// footer), and the a/d/x/o result actions.

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

// startTask echoes the prompt on the active thread and begins it.
func (m Cockpit) startTask(task string) (Cockpit, tea.Cmd) {
	th := m.th()
	if th.name == "" {
		th.name = ckThreadName(task)
	}
	th.log = append(th.log, ckYouS.Render("❯ "+ckSafe(task)))
	return m.beginTask(th, task, m.mode)
}

// beginTask classifies the task, gates it on file overlap, prints the route
// line, and spawns the pipeline, behind worktree creation when the thread
// needs isolation. The ctrl+o override is consumed here, next task only.
// modeName is captured at submit time, so a queued task keeps its mode.
func (m Cockpit) beginTask(th *ckThread, task, modeName string) (Cockpit, tea.Cmd) {
	m.flash = "" // an override's "next task" note is consumed by this task
	md := ckModeByName(modeName)
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
		th.log = append(th.log, ckDimS.Render("  no routable model, run `hyctl probe` to see why"))
		return m, nil
	}
	h := m.heads[idx]

	file := ckNamedFile(task)
	editCapable := file != "" && md.name != "ask"
	rel := ckRepoRel(m.repoRoot, file)

	// Overlap gate (#598): the same duplicate-target pre-flight
	// internal/parallel runs, against every other thread's holds. A thread
	// re-checked after a release keeps its original place in the queue.
	if editCapable {
		key := rel
		if key == "" {
			key = file
		}
		if blocker, overlap := m.overlapBlocker(th, key); blocker != nil {
			if th.queued != nil {
				th.requeueBehind(blocker, overlap)
				return m, nil
			}
			th.queueTask(task, key, blocker, overlap)
			th.queued.mode, th.queued.seq = modeName, m.queueSeq
			m.queueSeq++
			return m, nil
		}
		if th.queued != nil {
			th.queued = nil
			th.log = append(th.log, ckDimS.Render("  ▶ unblocked, starting"))
		}
		th.files = ckAppendUnique(th.files, key)
	}

	t := ckTask{
		prompt: task, mode: md, file: file, rel: rel,
		threadID: th.id,
		runID:    runid.New(), taskID: runid.New(),
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
	// stays single, visibly, on the route line, not silently.
	if ov.kind == 'B' || ov.kind == 'C' {
		if file == "" || md.name == "ask" {
			t.strategy, t.confidence = ov.kind, ov.conf
		}
	}
	th.lastRunID = t.runID
	th.clock = th.clock.Tick(ckThreadAgent(th.id))

	th.log = append(th.log, m.routeLines(t, h, enum, ov, pinned)...)
	if f := ckFileRe.FindString(task); f != "" {
		if radius, deps, kappa, ok := m.metrics.ckBlastFor(f); ok {
			risk := ckCheapS
			if kappa >= 2 {
				risk = ckExpS
			}
			th.log = append(th.log, ckDimS.Render("  impact ")+
				risk.Render(fmt.Sprintf("κ=%.1f", kappa))+
				ckDimS.Render(fmt.Sprintf("  %d dependent%s · radius %.2f×  → %s",
					deps, plural(deps), radius, truncate(f, 40))))
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), ckTaskTimeout)
	ex := &ckExecState{ctx: ctx, cancel: cancel, started: t.startedAt, stage: "routing"}
	th.exec = ex
	th.lastDone = nil
	th.scroll = 0

	// Isolation (#598): an edit thread inside a repo works in its own worktree.
	if editCapable && rel != "" {
		if th.wt == nil {
			ex.setStage("isolating, creating worktree")
			th.log = append(th.log, ckDimS.Render("  ⎇ isolating, cutting a worktree of ")+
				ckDimS.Render(truncate(m.repoRoot, 40)))
			return m, tea.Batch(ckWtCreate(ex, t, m.repoRoot), ckSpinTick(ex))
		}
		th.log = append(th.log, ckDimS.Render("  ⎇ worktree ")+ckVioletS.Render(th.wt.tag)+
			ckDimS.Render(" · merges on apply"))
		return m.launchTask(th, t, ex)
	}
	if editCapable {
		// No repo (or the file lives outside it): edits land in place, one at a
		// time, said out loud, never pretended isolation (#598).
		th.inEdit = true
		th.log = append(th.log, ckMidS.Render("  ⚠ no isolation ")+
			ckDimS.Render("· editing your files directly (no git repo here); edits run one at a time"))
	}
	return m.launchTask(th, t, ex)
}

// launchTask finalizes the task against its working tree (worktree or CWD) and
// spawns the right pipeline phase.
func (m Cockpit) launchTask(th *ckThread, t ckTask, ex *ckExecState) (Cockpit, tea.Cmd) {
	if th.wt != nil && t.rel != "" {
		t.file = filepath.Join(th.wt.dir, filepath.FromSlash(t.rel))
		t.root = th.wt.dir
		t.dir = th.wt.dir
	}
	if t.mode.verify {
		t.verifyArgv, t.verifyLabel = ckVerifyArgs(t.file)
	}
	phase := ckPhaseFull
	if t.mode.name == "plan" || (t.mode.confirm && t.file != "") {
		phase = ckPhaseHead
	}
	return m, tea.Batch(ckWorker(ex, t, phase), ckSpinTick(ex))
}

// worktreeReady lands the async `git worktree add`: attach it and launch the
// pending task, or fail the task honestly, never fall back to editing the
// user's tree when isolation was promised.
func (m Cockpit) worktreeReady(msg ckWtReadyMsg) (tea.Model, tea.Cmd) {
	th := m.threadByID(msg.task.threadID)
	if th == nil || th.exec != msg.exec {
		if msg.wt != nil { // superseded, do not litter the worktree dir
			_ = ckDiscardWorktree(msg.wt)
		}
		return m, nil
	}
	t := msg.task
	if err := msg.exec.ctx.Err(); err != nil {
		if msg.wt != nil {
			_ = ckDiscardWorktree(msg.wt)
		}
		t.errText = "worktree creation cancelled"
		t.canceled = true
		return m.failBeforeRun(th, t)
	}
	if msg.err != nil {
		t.errText = "worktree creation failed: " + msg.err.Error()
		return m.failBeforeRun(th, t)
	}
	th.wt = msg.wt
	th.log = append(th.log, ckDimS.Render("  ⎇ worktree ")+ckVioletS.Render(th.wt.tag)+
		ckDimS.Render(" · branch "+th.wt.branch+" · merges on apply"))
	return m.launchTask(th, t, msg.exec)
}

// failBeforeRun settles a task that never reached the pipeline, releasing the
// thread's fresh hold so queued threads are not blocked by a task that never ran.
func (m Cockpit) failBeforeRun(th *ckThread, t ckTask) (Cockpit, tea.Cmd) {
	th.exec = nil
	key := t.rel
	if key == "" {
		key = t.file
	}
	th.files = ckRemove(th.files, key)
	ckFinalize(&t, context.Background())
	nm, cmd := m.settleTask(th, t)
	nm, rcmd := nm.releaseThreads(th)
	return nm, tea.Batch(cmd, rcmd)
}

// ckRepoRel is file's repo-relative path, or "" when it is not under root.
func ckRepoRel(root, file string) string {
	if root == "" || file == "" {
		return ""
	}
	rel, err := filepath.Rel(root, file)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return filepath.ToSlash(rel)
}

func ckAppendUnique(files []string, f string) []string {
	for _, x := range files {
		if x == f {
			return files
		}
	}
	return append(files, f)
}

func ckRemove(files []string, f string) []string {
	out := files[:0]
	for _, x := range files {
		if x != f {
			out = append(out, x)
		}
	}
	return out
}

// ckEditEnum picks the editor path's routing enum: the classification, the
// cheap tier for architect's implement half, or a forced tier's equivalent.
// CORE maps to EXPERT, the editor refuses CORE by design.
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

// ckNamedFile returns the absolute path of a file the prompt names, only when
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

// gateTask handles a pipeline paused for the user: render what is waiting on
// its own thread, arm the matching wait state, and ping chat when that thread
// is not the one in front of the user.
func (m Cockpit) gateTask(msg ckGateMsg) (Cockpit, tea.Cmd) {
	th := m.threadByID(msg.task.threadID)
	if th == nil || th.exec != msg.exec {
		return m, nil
	}
	th.exec = nil
	t := msg.task
	switch msg.gate {
	case 'p':
		th.log = append(th.log, ckPlanLines(t)...)
		th.log = append(th.log, ckFaintS.Render("  enter/y runs it · esc discards"))
		th.planWait = &ckWait{task: t, phase: ckPhaseTail}
	case 'w':
		th.log = append(th.log, ckPlanLines(t)...)
		th.confirm = &ckWait{task: t, phase: ckPhaseTail,
			question: "write " + filepath.Base(t.file) + "? y writes · n stops"}
	case 'f':
		th.log = append(th.log, ckExpS.Render("  ✗ verify failed ")+
			ckDimS.Render(truncate(ckSafe(t.fixDetail), 70)))
		th.confirm = &ckWait{task: t, phase: ckPhaseFix,
			question: fmt.Sprintf("write fix %d/%d to %s? y/n", t.fixRound, ckMaxFixes, filepath.Base(t.file))}
	}
	m.pingIfElsewhere(th, "needs you, "+ckGateWord(msg.gate))
	return m, nil
}

func ckGateWord(gate byte) string {
	switch gate {
	case 'p':
		return "a plan waits for approval"
	case 'f':
		return "a fix wants a y/n"
	default:
		return "a write wants a y/n"
	}
}

// pingIfElsewhere drops a hydra ▸ line into the ACTIVE thread when th is
// backgrounded or simply not the one on screen, the spec's completion ping.
func (m *Cockpit) pingIfElsewhere(th *ckThread, what string) {
	active := m.th()
	if th.id == active.id {
		return
	}
	th.attn = th.planWait != nil || th.confirm != nil || th.attn
	active.log = append(active.log, ckAquaS.Render("hydra ▸ ")+
		ckDimS.Render(fmt.Sprintf("thread %d %s · alt+%d opens it · ctrl+g next", th.id, what, th.id)))
	m.flash = fmt.Sprintf("thread %d %s", th.id, what)
}

// ckPlanLines renders a drafted plan into the log, capped so a rambling plan
// cannot flood the pane (scrollback still holds what is shown).
func ckPlanLines(t ckTask) []string {
	out := []string{ckLabelS.Render("  PLAN") + ckDimS.Render(
		fmt.Sprintf(", %d step%s · drafted by %s", t.planSteps, plural(t.planSteps), t.headName))}
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
func (m Cockpit) resumeTask(th *ckThread, w ckWait) (Cockpit, tea.Cmd) {
	ctx, cancel := context.WithTimeout(context.Background(), ckTaskTimeout)
	ex := &ckExecState{ctx: ctx, cancel: cancel, started: w.task.startedAt, stage: "resuming"}
	th.exec = ex
	th.planWait, th.confirm = nil, nil
	return m, tea.Batch(ckWorker(ex, w.task, w.phase), ckSpinTick(ex))
}

// stopWait ends a gated task without resuming it: the run closes, and what
// already happened (plan cost, a landed edit) is settled honestly.
func (m Cockpit) stopWait(th *ckThread, w ckWait, note string) (Cockpit, tea.Cmd) {
	th.planWait, th.confirm = nil, nil
	t := w.task
	_ = runlog.New(t.runID).Append(runlog.Event{Kind: runlog.KindRunFinished, TaskID: t.taskID})
	t.stopped = true
	t.note = note
	ckFinalize(&t, context.Background())
	return m.settleTask(th, t)
}

// ── completion ────────────────────────────────────────────────────────────────

// finishTask lands a worker's final message on its thread, routed by the
// task's thread id, never by whichever thread is current.
func (m Cockpit) finishTask(msg ckExecDoneMsg) (Cockpit, tea.Cmd) {
	th := m.threadByID(msg.task.threadID)
	if th == nil || th.exec != msg.exec {
		return m, nil
	}
	th.exec = nil
	return m.settleTask(th, msg.task)
}

// settleTask renders a finished task on its thread and updates everything that
// watched it: session cost, the run list, the result actions, the code panel,
// held-file release, and the elsewhere ping.
func (m Cockpit) settleTask(th *ckThread, t ckTask) (Cockpit, tea.Cmd) {
	m.sessionUSD += t.costUSD
	m.runsToday = ckLoadRuns(time.Now().UTC())
	th.lastDone = &t
	th.inEdit = false
	th.log = append(th.log, ckResultLines(t)...)
	if t.edited && th.wt != nil {
		th.log = append(th.log, ckFaintS.Render("  a applies it to your tree · x x discards the worktree"))
	}

	// An untouched worktree from a task that never edited (failed plan, declined
	// write, cancel) is litter, not work, remove it and say so.
	if th.wt != nil && !t.edited && len(ckWorktreeChanges(th.wt)) == 0 {
		if err := ckDiscardWorktree(th.wt); err == nil {
			th.log = append(th.log, ckDimS.Render("  ⎇ worktree removed, nothing landed in it"))
			th.wt = nil
			th.files = nil
		} else {
			th.log = append(th.log, ckMidS.Render("  ⚠ empty worktree cleanup failed: "+err.Error()))
		}
	}
	if th.wt == nil {
		th.files = nil // in-place holds end with the task; worktrees hold to apply
	}

	what := "done"
	switch {
	case t.canceled:
		what = "cancelled"
	case t.errText != "":
		what = "failed, " + truncate(ckFirstLine(t.errText), 40)
	case t.stopped:
		what = "stopped"
	}
	m.pingIfElsewhere(th, what)
	if th.id != m.th().id && (t.errText != "" && !t.canceled) {
		th.attn = true // a failure elsewhere needs eyes; ctrl+g finds it
	}

	nm, rcmd := m.releaseThreads(th)
	m = nm
	if t.edited && t.editRefLast != "" {
		if _, after, err := runlog.LoadEdit(t.runID, t.editRefLast); err == nil {
			m = m.showCode(th, t.file, string(after))
			return m, tea.Batch(rcmd, ckCodeTick(th.id, th.codeGen))
		}
	}
	return m, rcmd
}

// ckWorktreeChanges lists what the worktree holds beyond its base: dirty files
// plus anything already committed on its branch.
func ckWorktreeChanges(wt *ckWorktree) []string {
	st, err := ckGit(wt.dir, "", "status", "--porcelain")
	if err != nil {
		return []string{"unknown: " + ckFirstLine(st)}
	}
	changes := ckSplitLines(st)
	if names, err := ckGit(wt.repo, "", "diff", "--name-only", wt.base, wt.branch); err == nil {
		changes = append(changes, ckSplitLines(names)...)
	}
	return changes
}

// showCode streams content into the thread's code panel (real file content,
// the fake snippets died with the preview-only chat).
func (m Cockpit) showCode(th *ckThread, file, content string) Cockpit {
	th.codeLang = strings.TrimPrefix(filepath.Ext(file), ".")
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	if len(lines) > ckCodeMaxLines {
		lines = append(lines[:ckCodeMaxLines], "… (truncated)")
	}
	th.codeLines = lines
	th.codeShown = 0
	th.codeDiff = false
	th.codeGen++
	return m
}

// ── background (ctrl+b) ───────────────────────────────────────────────────────

// backgroundThread re-parents the active thread to the agents view. Its
// pipeline keeps running on its own context; completion pings chat.
func (m Cockpit) backgroundThread() Cockpit {
	th := m.th()
	if len(m.threads) >= ckMaxThreads && len(m.visibleThreads()) == 1 {
		m.flash = "cannot background the last thread, the thread limit is reached"
		return m
	}
	th.bg = true
	m.runsToday = ckLoadRuns(time.Now().UTC()) // its live run shows up now
	if m.split && m.splitID == th.id {
		m.split = false
	}
	// The input needs a foreground owner: the next visible thread, or a new one.
	if vis := m.visibleThreads(); len(vis) > 0 {
		m = m.focusThread(vis[0].id)
	} else {
		m = m.addThread()
	}
	m.flash = fmt.Sprintf("thread %d backgrounded, it lives in agents (2) · alt+%d brings it back", th.id, th.id)
	return m
}

// threadForRun maps a run id back to its thread (the agents-view join).
func (m Cockpit) threadForRun(runID string) *ckThread {
	for _, t := range m.threads {
		if t.lastRunID == runID {
			return t
		}
	}
	return nil
}

// ── result rendering ──────────────────────────────────────────────────────────

// ckResultLines renders a finished task: answer block, proof strip, footer.
// A failure always carries its trace link, never a dead end.
func ckResultLines(t ckTask) []string {
	var out []string
	trace := ckDimS.Render(" · trace "+ckShortID(t.runID)) + ckFaintS.Render(", enter opens the trace")
	secs := fmt.Sprintf("%.1fs", t.elapsed.Seconds())
	switch {
	case t.canceled:
		return append(out, ckMidS.Render("  ✗ cancelled ")+ckDimS.Render(secs)+trace)
	case t.errText != "":
		out = append(out, ckExpS.Render("  ✗ failed ")+ckDimS.Render(secs+" · ")+
			ckExpS.Render(truncate(ckSafe(ckFirstLine(t.errText)), 90)))
		return append(out, ckFaintS.Render("  trace "+ckShortID(t.runID)+", enter opens the trace"))
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
		out = append(out, ckFaintS.Render("  d diff · x undo · o open, on an empty input"))
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
	name := t.rel
	if name == "" {
		name = filepath.Base(t.file)
	}
	parts = append(parts, ckCheapS.Render("edit ✓ ")+
		ckDimS.Render(fmt.Sprintf("%s +%d/−%d", truncate(name, 30), t.added, t.removed)))
	parts = append(parts, ckTestsCell(t))
	return "  " + strings.Join(parts, ckFaintS.Render(" · "))
}

// ckTestsCell is the strip's verification verdict, honest about every state:
// passed (and after how many fixes), still failing, skipped, unconfigured, or
// the verifier itself failing to run.
func ckTestsCell(t ckTask) string {
	switch {
	case !t.mode.verify:
		return ckFaintS.Render("tests, skipped (" + t.mode.name + " mode)")
	case t.verifySkipped:
		return ckFaintS.Render("tests, no verifier configured")
	case t.verifyErr != "":
		return ckMidS.Render("tests ? ") + ckDimS.Render(
			t.verifyLabel+", verifier failed: "+truncate(ckSafe(t.verifyErr), 40))
	case len(t.rounds) == 0:
		return ckFaintS.Render("tests, not run")
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
	summary := fmt.Sprintf("%s, still failing", t.verifyLabel)
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

// ── result actions (a/d/x/o) ──────────────────────────────────────────────────

// resultKey acts on the active thread's last finished edit from an empty
// input: a applies an isolated thread's worktree, d toggles the diff, x undoes
// (or, on a worktree thread, discards, pressed twice), o opens $EDITOR.
// Returns handled=false when there is nothing to act on, so the rune types.
func (m Cockpit) resultKey(r rune) (Cockpit, tea.Cmd, bool) {
	th := m.th()
	t := th.lastDone
	if t == nil || !t.edited {
		if r == 'a' || r == 'x' {
			// A held worktree can outlive lastDone (e.g. after a declined fix);
			// apply/discard must still be reachable.
			if th.wt != nil && th.exec == nil {
				if r == 'a' {
					return m.applyThread(th)
				}
				return m.discardKey(th)
			}
		}
		return m, nil, false
	}
	switch r {
	case 'a':
		if th.wt == nil || th.exec != nil {
			return m, nil, false
		}
		return m.applyThread(th)
	case 'd':
		return m.toggleDiff(th, t), nil, true
	case 'x':
		if th.wt != nil {
			return m.discardKey(th)
		}
		return m.undoEdit(th, t), nil, true
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

// toggleDiff swaps the thread's code panel between the task's full diff (first
// before → last after, so the fix loop's intermediate writes collapse) and the
// file.
func (m Cockpit) toggleDiff(th *ckThread, t *ckTask) Cockpit {
	if t.editRef == "" || t.editRefLast == "" {
		m.flash = "no snapshot stored for this edit"
		return m
	}
	if th.codeDiff {
		if _, after, err := runlog.LoadEdit(t.runID, t.editRefLast); err == nil {
			m = m.showCode(th, t.file, string(after))
			th.codeShown = len(th.codeLines)
		}
		return m
	}
	before, _, err := runlog.LoadEdit(t.runID, t.editRef)
	if err != nil {
		m.flash = "diff unavailable, " + err.Error()
		return m
	}
	_, after, err := runlog.LoadEdit(t.runID, t.editRefLast)
	if err != nil {
		m.flash = "diff unavailable, " + err.Error()
		return m
	}
	base := filepath.Base(t.file)
	lines := strings.Split(strings.TrimRight(diff.Unified("a/"+base, "b/"+base, before, after), "\n"), "\n")
	if len(lines) > ckCodeMaxLines {
		lines = append(lines[:ckCodeMaxLines], "… (truncated)")
	}
	th.codeLang = "diff"
	th.codeLines = lines
	th.codeShown = len(lines)
	th.codeDiff = true
	th.codeGen++ // cancel any stream in flight
	return m
}

// undoEdit restores the file to its exact pre-task bytes from the first edit
// snapshot, preserving the file's permissions. One-shot per task.
func (m Cockpit) undoEdit(th *ckThread, t *ckTask) Cockpit {
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
		m.flash = "undo unavailable, " + err.Error()
		return m
	}
	mode := os.FileMode(0o644)
	if st, serr := os.Stat(t.file); serr == nil {
		mode = st.Mode().Perm()
	}
	if werr := os.WriteFile(t.file, before, mode); werr != nil {
		m.flash = "undo failed, " + werr.Error()
		return m
	}
	t.undone = true // through the pointer: the second x says "already restored"
	m.flash = "restored " + filepath.Base(t.file) + " to its pre-task state"
	m = m.showCode(th, t.file, string(before))
	th.codeShown = len(th.codeLines)
	return m
}
