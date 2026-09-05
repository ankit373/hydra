// SPDX-License-Identifier: MIT

package tui

// chat_exec.go, the chat's execution pipeline (plan → edit → verify), run as
// tea.Cmds through internal/dispatch, internal/editor and internal/oracle.
// The stage funcs are seams: tests fake the providers, never the pipeline.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/editor"
	"github.com/ankit373/hydra/internal/oracle"
	"github.com/ankit373/hydra/internal/policy"
	"github.com/ankit373/hydra/internal/rank"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/swarm"
	"github.com/ankit373/hydra/internal/trust"
	"github.com/ankit373/hydra/internal/workspace"
)

// ckMaxFixes caps Auto's fix-and-reverify loop: a change that is still failing
// after two model-written fixes needs a human, not a third attempt.
const ckMaxFixes = 2

// ckTaskTimeout bounds one pipeline phase, same reasoning as the desktop's
// ChatTimeout, wider because a phase can hold several dispatches plus a test run.
const ckTaskTimeout = 10 * time.Minute

// ckAnswerCapLines bounds how many answer lines are appended to the chat log.
const ckAnswerCapLines = 400

// ckCodeMaxLines bounds what the code panel streams after an edit.
const ckCodeMaxLines = 200

// ── task ──────────────────────────────────────────────────────────────────────

type ckVerifyRound struct {
	passed bool
	detail string
}

// ckTask is one chat task: the routing decision plus everything the pipeline
// accumulates while running it. It moves by value between the UI and the
// worker, exactly one owner at a time, so no locking.
type ckTask struct {
	prompt      string
	mode        ckModeDef
	file        string // absolute path being edited, inside the worktree when isolated
	rel         string // repo-relative display path; "" when outside the repo
	root        string // editor scope-root override: the thread's worktree dir
	dir         string // verify working directory; "" = the process CWD
	threadID    int    // the owning thread, tea.Cmd results route back by this
	runID       string
	taskID      string
	answerTier  string
	planTier    string
	editEnum    string
	strategy    byte // 0 single · 'B' best of 3 · 'C' consensus (answer stage only)
	confidence  float64
	localOnly   bool
	pii         bool
	verifyArgv  []string
	verifyLabel string
	startedAt   time.Time

	// accumulated across phases
	plan           string
	planSteps      int
	answer         string
	note           string
	edited         bool
	added, removed int
	rounds         []ckVerifyRound
	verifySkipped  bool
	verifyErr      string
	headName       string
	tier           int
	conf           float64 // achieved consensus confidence, when strategy was 'C'
	fixRound       int
	fixDetail      string

	// finalized
	editRef     string // first edit snapshot: undo target, diff's "before"
	editRefLast string // last edit snapshot: diff's "after"
	costUSD     float64
	elapsed     time.Duration
	errText     string
	canceled    bool
	stopped     bool // ended at a gate (plan discarded, write declined)
	undone      bool
}

// ckExecState is the live-task handle the UI and the worker share: the stage
// line under a mutex, and the cancel esc pulls. Everything else is immutable
// after creation.
type ckExecState struct {
	mu      sync.Mutex
	stage   string
	ctx     context.Context
	cancel  context.CancelFunc
	started time.Time
}

func (e *ckExecState) setStage(s string) {
	e.mu.Lock()
	e.stage = s
	e.mu.Unlock()
}

func (e *ckExecState) Stage() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.stage
}

// ckWait is a pipeline paused for the user: a plan awaiting approval, or a
// careful-mode write/fix confirm. Approval resumes the task at phase.
type ckWait struct {
	task     ckTask
	phase    int
	question string
}

// worker phases.
const (
	ckPhaseFull = iota // the whole pipeline in one worker
	ckPhaseHead        // plan only, then gate (plan approval / careful confirm)
	ckPhaseTail        // post-gate: edit → verify (or answer)
	ckPhaseFix         // careful-mode: one confirmed fix, then re-verify
)

// messages
type ckExecDoneMsg struct {
	exec *ckExecState
	task ckTask
}

type ckGateMsg struct {
	exec *ckExecState
	task ckTask
	gate byte // 'p' plan approval · 'w' write confirm · 'f' fix confirm
}

type ckSpinTickMsg struct{ exec *ckExecState }

func ckSpinTick(ex *ckExecState) tea.Cmd {
	return tea.Tick(time.Second/5, func(time.Time) tea.Msg { return ckSpinTickMsg{ex} })
}

// ── stage seams ───────────────────────────────────────────────────────────────

// ckStageOut is one dispatch-shaped stage's outcome.
type ckStageOut struct {
	output     string
	head       string
	tier       int
	confidence float64 // consensus runs only

	// attempts is every head that was tried and did not answer. Carried so the
	// task can say it fell back rather than reporting only who replied (#676).
	attempts []dispatch.Attempt
}

// The three provider seams. Tests substitute these; the pipeline around them
// is always the real one.
var (
	ckDispatchStage = ckRealDispatchStage
	ckEditStage     = ckRealEditStage
	ckVerifyStage   = ckRealVerifyStage
)

// ckRealDispatchStage routes one prompt through the real router, honoring the
// task's strategy for this stage: plain dispatch, best-of-3 swarm, or the SPRT
// consensus ensemble, the same code paths cmd/hydra's dispatch uses.
func ckRealDispatchStage(ctx context.Context, t *ckTask, prompt, tierHint string, strat byte) (ckStageOut, error) {
	d, err := dispatch.New(ctx)
	if err != nil {
		return ckStageOut{}, err
	}
	class := policy.Classify(prompt)
	localOnly := t.localOnly || (d.PIILocalOnly() && class.PII)
	switch strat {
	case 'B':
		sw := swarm.New(d, d.Heads(), d)
		res, err := sw.Run(ctx, prompt, swarm.Options{
			Mode: swarm.ModeBest, TierHint: tierHint, MaxHeads: 3,
			LocalOnly: localOnly, RunID: t.runID, TaskID: t.taskID,
			Classification: &class,
		})
		if err != nil {
			return ckStageOut{}, err
		}
		if res.Winner == nil {
			return ckStageOut{}, errors.New("best of 3: no head produced an answer")
		}
		return ckStageOut{output: res.Winner.Output, head: res.Winner.Head.Name, tier: rank.UITier(res.Winner.Head)}, nil
	case 'C':
		sw := swarm.New(d, d.Heads(), d)
		res, err := sw.RunSPRT(ctx, prompt, swarm.Options{
			Confidence: t.confidence, TierHint: tierHint,
			LocalOnly: localOnly, RunID: t.runID, TaskID: t.taskID,
			Classification: &class,
		})
		if err != nil {
			return ckStageOut{}, err
		}
		out := ckStageOut{output: res.Trust.Candidate, confidence: res.Trust.Confidence,
			head: fmt.Sprintf("consensus · %d samples", res.Trust.Samples)}
		for _, a := range res.Attempts {
			if a.Status == swarm.StatusOK && a.Output == res.Trust.Candidate {
				out.head = a.Head.Name
				out.tier = rank.UITier(a.Head)
				break
			}
		}
		return out, nil
	default:
		res, err := d.Dispatch(ctx, prompt, dispatch.Options{
			TierHint: tierHint, LocalOnly: localOnly,
			MaxCostUSD: t.mode.capUSD, RunID: t.runID, TaskID: t.taskID,
			Classification: &class,
		})
		if err != nil {
			return ckStageOut{}, err
		}
		return ckStageOut{output: res.Output, head: res.Head.Name, tier: rank.UITier(res.Head), attempts: res.Attempts}, nil
	}
}

// ckRealEditStage runs the editor path: scoped, validated, rollback-safe, with
// the runlog KindEdit snapshot the d/x keys read back. Root carries a worktree
// thread's scope override, its files live outside every registered workspace.
func ckRealEditStage(ctx context.Context, t *ckTask, prompt string) (*editor.Result, error) {
	return editor.Edit(ctx, editor.Request{
		File: t.file, Enum: t.editEnum, Prompt: prompt, Validate: true,
		RunID: t.runID, TaskID: t.taskID, LocalOnly: t.localOnly, Root: t.root,
	})
}

// ckRealVerifyStage runs the workspace's verify command through the oracle.
// argv carries no placeholders, the file argument was substituted with the
// real on-disk path, which is what is being verified. dir is the worktree for
// isolated threads, so the verifier checks the thread's copy, not the user's.
func ckRealVerifyStage(ctx context.Context, argv []string, dir string) (oracle.Verdict, error) {
	o := &oracle.CommandOracle{Args: argv, Source: "verifier:tui", Dir: dir}
	return o.Verify(ctx, "", trust.Task{})
}

// ── verify command resolution ─────────────────────────────────────────────────

// ckVerifyArgs picks the verify command: `go test ./...` when the CWD repo is
// Go, else the workspace.yaml validator for the edited file's extension.
// Empty argv means no verifier is configured, the proof strip says so.
func ckVerifyArgs(file string) (argv []string, label string) {
	if ckGoModDir() != "" {
		return []string{"go", "test", "./..."}, "go test ./..."
	}
	if file == "" {
		return nil, ""
	}
	reg, err := workspace.Load(config.ScriptHome())
	if err != nil {
		return nil, ""
	}
	tmpl := reg.ValidatorFor(strings.TrimPrefix(filepath.Ext(file), "."))
	if tmpl == "" {
		return nil, ""
	}
	// {file} substitutes the real path as one argv element (paths with spaces
	// survive), the verifier must check the file on disk, not a temp copy.
	if idx := strings.Index(tmpl, "{file}"); idx >= 0 {
		argv = append(strings.Fields(tmpl[:idx]), file)
		argv = append(argv, strings.Fields(tmpl[idx+len("{file}"):])...)
	} else {
		argv = strings.Fields(tmpl)
	}
	if len(argv) == 0 {
		return nil, ""
	}
	return argv, strings.ReplaceAll(tmpl, "{file}", filepath.Base(file))
}

// ckGoModDir walks up from the CWD to the nearest go.mod, stopping at the
// first .git boundary so a Go directory above an unrelated repo doesn't claim it.
func ckGoModDir() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// ── the worker ────────────────────────────────────────────────────────────────

// ckWorker runs one pipeline phase off the UI loop. A gate (plan approval,
// careful confirm) returns a ckGateMsg with the run left open; everything else
// closes the run and returns a ckExecDoneMsg.
func ckWorker(ex *ckExecState, t ckTask, phase int) tea.Cmd {
	return func() tea.Msg {
		hb := runlog.StartHeartbeat(ex.ctx, t.runID, runlog.HeartbeatInterval)
		defer hb.Stop()
		rl := runlog.New(t.runID)
		if phase == ckPhaseFull || phase == ckPhaseHead {
			_ = rl.Append(runlog.Event{Kind: runlog.KindRunStarted, TaskID: t.taskID, Detail: truncate(t.prompt, 80)})
		}
		gate := ckRunStages(ex.ctx, ex, &t, phase)
		if gate != 0 && t.errText == "" {
			return ckGateMsg{exec: ex, task: t, gate: gate}
		}
		_ = rl.Append(runlog.Event{Kind: runlog.KindRunFinished, TaskID: t.taskID})
		ckFinalize(&t, ex.ctx)
		return ckExecDoneMsg{exec: ex, task: t}
	}
}

// ckRunStages advances the pipeline for one phase, mutating t as it goes.
// A non-zero return is the gate that paused it.
func ckRunStages(ctx context.Context, ex *ckExecState, t *ckTask, phase int) byte {
	if phase == ckPhaseFull || phase == ckPhaseHead {
		if t.mode.plan {
			ex.setStage("planning")
			out, err := ckDispatchStage(ctx, t, ckPlanPrompt(t), t.planTier, 0)
			if err != nil {
				t.errText = "plan: " + err.Error()
				return 0
			}
			t.plan, t.planSteps = out.output, ckCountSteps(out.output)
			t.headName, t.tier = out.head, out.tier
			if err := ckOverCap(t); err != nil {
				t.errText = err.Error()
				return 0
			}
		}
		if phase == ckPhaseHead {
			if t.mode.name == "plan" {
				return 'p'
			}
			return 'w' // careful: confirm before the write lands
		}
	}
	if t.file != "" && t.mode.name != "ask" {
		return ckEditAndVerify(ctx, ex, t)
	}
	ex.setStage("answering")
	out, err := ckDispatchStage(ctx, t, ckAnswerPrompt(t), t.answerTier, t.strategy)
	if err != nil {
		t.errText = err.Error()
		return 0
	}
	t.answer, t.conf = out.output, out.confidence
	t.headName, t.tier = out.head, out.tier
	if n := ckFellBackNote(out); n != "" {
		t.note = n
	}
	if t.mode.name == "edit" && t.file == "" {
		t.note = "no file named, answered instead"
	}
	return 0
}

// ckFellBackNote says which head was tried and could not answer. Silence here
// is what #676 reported: a reply from a weak head reads the same whether the
// router chose it or fell back to it.
func ckFellBackNote(out ckStageOut) string {
	if len(out.attempts) == 0 {
		return ""
	}
	a := out.attempts[0]
	name := a.Model
	if name == "" {
		name = a.Head
	}
	more := ""
	if len(out.attempts) > 1 {
		more = fmt.Sprintf(" (+%d more)", len(out.attempts)-1)
	}
	return fmt.Sprintf("%s could not answer%s: %s", name, more, truncate(a.Reason, 60))
}

// ckEditAndVerify is the edit → verify → fix loop. In careful mode each fix
// write pauses at a 'f' gate instead of landing unconfirmed.
func ckEditAndVerify(ctx context.Context, ex *ckExecState, t *ckTask) byte {
	base := filepath.Base(t.file)
	editOnce := func(stage, prompt string) bool {
		ex.setStage(stage)
		er, err := ckEditStage(ctx, t, prompt)
		if err != nil {
			t.errText = "edit " + base + ": " + err.Error()
			return false
		}
		if er.Status != "ok" {
			t.errText = "edit " + base + ": " + er.Error
			return false
		}
		t.edited = true
		t.added += er.LinesAdded
		t.removed += er.LinesRemoved
		if t.headName == "" {
			t.headName = er.Head
		}
		return true
	}

	if t.fixRound == 0 {
		if !editOnce("editing "+base, ckEditPrompt(t)) {
			return 0
		}
		if !t.mode.verify {
			return 0
		}
		if len(t.verifyArgv) == 0 {
			t.verifySkipped = true
			return 0
		}
	} else { // resumed at a confirmed fix
		if !editOnce(fmt.Sprintf("fixing (%d/%d), %s", t.fixRound, ckMaxFixes, base), ckFixPrompt(t)) {
			return 0
		}
	}

	for {
		ex.setStage("verifying, " + t.verifyLabel)
		v, verr := ckVerifyStage(ctx, t.verifyArgv, t.dir)
		if verr != nil {
			t.verifyErr = verr.Error()
			return 0
		}
		t.rounds = append(t.rounds, ckVerifyRound{passed: v.Passed, detail: v.Detail})
		if v.Passed || t.fixRound >= ckMaxFixes {
			return 0
		}
		if err := ckOverCap(t); err != nil {
			t.errText = err.Error()
			return 0
		}
		t.fixRound++
		t.fixDetail = v.Detail
		if t.mode.confirm {
			return 'f' // careful: this write needs a y first
		}
		if !editOnce(fmt.Sprintf("fixing (%d/%d), %s", t.fixRound, ckMaxFixes, base), ckFixPrompt(t)) {
			return 0
		}
	}
}

// ckOverCap enforces unattended's hard per-task cap between stages: the next
// stage is refused once the run's recorded spend reaches it. In-stage spend is
// bounded separately by dispatch's own MaxCostUSD preflight.
func ckOverCap(t *ckTask) error {
	if t.mode.capUSD <= 0 {
		return nil
	}
	if spent := ckRunSpend(t.runID); spent >= t.mode.capUSD {
		return fmt.Errorf("cost cap $%.2f reached ($%.4f spent), stopped before the next stage", t.mode.capUSD, spent)
	}
	return nil
}

// ckRunSpend folds the run's recorded cost the same way the activity list does,
// so the footer, the cap, and the trace can never disagree.
func ckRunSpend(runID string) float64 {
	events, err := runlog.Load(runID)
	if err != nil {
		return 0
	}
	return ckRunFromEvents(runID, events, false).costUSD
}

// ckFinalize stamps the outcome fields every renderer reads: elapsed, the
// folded cost, the last edit snapshot ref, and whether esc ended it.
func ckFinalize(t *ckTask, ctx context.Context) {
	t.elapsed = time.Since(t.startedAt)
	if events, err := runlog.Load(t.runID); err == nil {
		t.costUSD = ckRunFromEvents(t.runID, events, false).costUSD
		for _, e := range events {
			if e.Kind == runlog.KindEdit && e.Ref != "" {
				if t.editRef == "" {
					t.editRef = e.Ref // first edit: the pre-task state
				}
				t.editRefLast = e.Ref
			}
		}
	}
	// esc is context.Canceled; a deadline is a timeout and stays an error.
	if t.errText != "" && (errors.Is(ctx.Err(), context.Canceled) ||
		strings.Contains(t.errText, context.Canceled.Error())) {
		t.canceled = true
	}
}

// ── prompts ───────────────────────────────────────────────────────────────────

func ckPlanPrompt(t *ckTask) string {
	p := "Plan this task as a short numbered list of concrete steps (at most 8). Steps only, no code, no preamble.\n\nTask: " + t.prompt
	if t.file != "" {
		p += "\nTarget file: " + t.file
	}
	return p
}

func ckAnswerPrompt(t *ckTask) string {
	if t.plan == "" {
		return t.prompt
	}
	return t.prompt + "\n\nA plan was drafted:\n" + t.plan + "\n\nAnswer the task, following the plan where it helps."
}

func ckEditPrompt(t *ckTask) string {
	if t.plan == "" {
		return t.prompt
	}
	return t.prompt + "\n\nFollow this plan:\n" + t.plan
}

func ckFixPrompt(t *ckTask) string {
	return fmt.Sprintf("The previous change to this file failed verification.\nCommand: %s\nFirst failure line: %s\n\nFix the file so the verification passes. Original task: %s",
		t.verifyLabel, t.fixDetail, t.prompt)
}

var ckStepRe = regexp.MustCompile(`^\s*\d+[.)]\s`)

// ckCountSteps counts numbered lines in a plan; an unnumbered non-empty plan
// still counts as one step rather than zero.
func ckCountSteps(plan string) int {
	n := 0
	for _, l := range strings.Split(plan, "\n") {
		if ckStepRe.MatchString(l) {
			n++
		}
	}
	if n == 0 && strings.TrimSpace(plan) != "" {
		return 1
	}
	return n
}
