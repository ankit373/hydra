// SPDX-License-Identifier: MIT

package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/editor"
	"github.com/ankit373/hydra/internal/oracle"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/testutil"
)

// ── harness ───────────────────────────────────────────────────────────────────

// stubExec snapshots the three provider seams and restores them after t. Every
// pipeline test starts here so a forgotten assignment cannot leak.
func stubExec(t *testing.T) {
	t.Helper()
	d, e, v := ckDispatchStage, ckEditStage, ckVerifyStage
	// The pipeline around the seams is real; the providers never are in a test.
	ckDispatchStage = func(context.Context, *ckTask, string, string, byte) (ckStageOut, error) {
		t.Fatal("ckDispatchStage used without a stub")
		return ckStageOut{}, nil
	}
	ckEditStage = func(context.Context, *ckTask, string) (*editor.Result, error) {
		t.Fatal("ckEditStage used without a stub")
		return nil, nil
	}
	ckVerifyStage = func(context.Context, []string, string) (oracle.Verdict, error) {
		t.Fatal("ckVerifyStage used without a stub")
		return oracle.Verdict{}, nil
	}
	t.Cleanup(func() { ckDispatchStage, ckEditStage, ckVerifyStage = d, e, v })
}

// stubAnswer is a dispatch stage returning out; cost > 0 also records the
// spend the way real dispatch does, so the cost fold has something to fold.
func stubAnswer(out string, cost float64) func(context.Context, *ckTask, string, string, byte) (ckStageOut, error) {
	return func(_ context.Context, tk *ckTask, _ string, _ string, _ byte) (ckStageOut, error) {
		if cost > 0 {
			_ = runlog.New(tk.runID).Append(runlog.Event{
				Kind: runlog.KindDispatchFinished, TaskID: tk.taskID, Status: "ok", CostUSD: cost})
		}
		return ckStageOut{output: out, head: "stub-head", tier: 8}, nil
	}
}

// stubEdit writes after to the task's file and snapshots before/after via the
// real runlog.LogEdit — the same record the real editor path leaves.
func stubEdit(before, after string) func(context.Context, *ckTask, string) (*editor.Result, error) {
	return func(_ context.Context, tk *ckTask, _ string) (*editor.Result, error) {
		if err := os.WriteFile(tk.file, []byte(after), 0o644); err != nil {
			return nil, err
		}
		runlog.LogEdit(tk.runID, tk.taskID, tk.file, []byte(before), []byte(after), 1, 1)
		return &editor.Result{Status: "ok", File: tk.file, LinesAdded: 1, LinesRemoved: 1, Head: "stub-editor"}, nil
	}
}

// runCmds executes a command tree synchronously, feeding every message back
// into Update. Clock ticks are dropped — re-feeding them would loop forever.
func runCmds(t *testing.T, m Cockpit, cmd tea.Cmd) Cockpit {
	t.Helper()
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		switch msg := c().(type) {
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case ckSpinTickMsg, ckCodeTickMsg:
		default:
			next, nc := m.Update(msg)
			m = next.(Cockpit)
			queue = append(queue, nc)
		}
	}
	return m
}

func keyRune(m Cockpit, r rune) (Cockpit, tea.Cmd) {
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	return next.(Cockpit), cmd
}

// chatFixture is a chat-focused cockpit in the given mode.
func chatFixture(mode string) Cockpit {
	m := testCockpit()
	m.mode = mode
	return m
}

// namedFile creates dir/pkg/main.go with content and chdirs into dir so the
// prompt "… pkg/main.go" names a real file (ckFileRe takes word chars and
// slashes only, which a temp dir's absolute path would not match).
func namedFile(t *testing.T, content string) (rel, abs string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	abs = filepath.Join(dir, "pkg", "main.go")
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	// The verify-args probe walks up for go.mod; give the loop a module so
	// auto-family modes get a real argv (the verify stage itself is stubbed).
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	return "pkg/main.go", abs
}

// ── ask ───────────────────────────────────────────────────────────────────────

// A typed task actually executes: route line first, then the answer block, the
// footer with cost and trace, session cost accrual — and an empty enter
// afterwards opens the trace in activity.
func TestExec_AskAnswersAccruesCostAndLinksTrace(t *testing.T) {
	testutil.NewSandbox(t)
	stubExec(t)
	ckDispatchStage = stubAnswer("goroutines are cheap\nuse them", 0.01)

	m := chatFixture("ask")
	m, cmd := enter(typed(m, "explain the users endpoint"))
	if m.th().exec == nil || cmd == nil {
		t.Fatal("submitting did not start a task")
	}
	joined := stripANSI(strings.Join(m.th().log, "\n"))
	if !strings.Contains(joined, "auto-routed") || !strings.Contains(joined, "single") {
		t.Errorf("no route line before execution:\n%s", joined)
	}
	if !strings.Contains(joined, "why question, standard scope, no PII") {
		t.Errorf("no plain-words why clause:\n%s", joined)
	}

	m = runCmds(t, m, cmd)
	if m.th().exec != nil {
		t.Fatal("exec still set after completion")
	}
	joined = stripANSI(strings.Join(m.th().log, "\n"))
	for _, want := range []string{"goroutines are cheap", "use them", "✓ done", "$0.0100", "trace"} {
		if !strings.Contains(joined, want) {
			t.Errorf("result is missing %q:\n%s", want, joined)
		}
	}
	if m.sessionUSD != 0.01 {
		t.Errorf("sessionUSD = %v, want 0.01", m.sessionUSD)
	}
	if m.th().lastDone == nil || m.th().lastDone.answer == "" {
		t.Fatal("lastDone not recorded")
	}

	// The footer's promise: an empty enter opens the trace.
	m, _ = enter(m)
	if m.view != ckViewActivity || !m.actDrill {
		t.Fatalf("empty enter did not open the trace (view=%d drill=%v)", m.view, m.actDrill)
	}
	if got := m.activityRuns()[m.actSel].id; got != m.th().lastDone.runID {
		t.Errorf("trace focused run %s, want %s", got, m.th().lastDone.runID)
	}
}

// A dispatch failure renders the error and its trace link — never a dead end.
func TestExec_FailureRendersErrorAndTrace(t *testing.T) {
	testutil.NewSandbox(t)
	stubExec(t)
	ckDispatchStage = func(context.Context, *ckTask, string, string, byte) (ckStageOut, error) {
		return ckStageOut{}, errors.New("no heads answered")
	}
	m := chatFixture("ask")
	m, cmd := enter(typed(m, "explain this"))
	m = runCmds(t, m, cmd)
	joined := stripANSI(strings.Join(m.th().log, "\n"))
	if !strings.Contains(joined, "✗ failed") || !strings.Contains(joined, "no heads answered") {
		t.Errorf("failure not rendered:\n%s", joined)
	}
	if !strings.Contains(joined, "trace") {
		t.Errorf("failure carries no trace link:\n%s", joined)
	}
}

// esc cancels the running task through context cancellation, rendered as
// cancelled — not as a generic failure.
func TestExec_EscCancelsRunningTask(t *testing.T) {
	testutil.NewSandbox(t)
	stubExec(t)
	ckDispatchStage = func(ctx context.Context, _ *ckTask, _ string, _ string, _ byte) (ckStageOut, error) {
		<-ctx.Done()
		return ckStageOut{}, ctx.Err()
	}
	m := chatFixture("ask")
	m, cmd := enter(typed(m, "explain this"))
	if m.th().exec == nil {
		t.Fatal("no task started")
	}
	m = press(m, tea.KeyEsc) // cancel — exec stays until the worker reports back
	if m.th().exec == nil {
		t.Fatal("esc cleared exec before the worker returned")
	}
	if got := m.th().exec.Stage(); !strings.Contains(got, "cancelling") {
		t.Errorf("stage = %q after esc", got)
	}
	m = runCmds(t, m, cmd)
	if m.th().exec != nil {
		t.Fatal("exec still set after the cancelled worker returned")
	}
	joined := stripANSI(strings.Join(m.th().log, "\n"))
	if !strings.Contains(joined, "✗ cancelled") {
		t.Errorf("cancellation not rendered:\n%s", joined)
	}
}

// While a task runs, a second enter refuses without losing the typed text, and
// esc clears typed input before it cancels anything.
func TestExec_SubmitWhileRunningRefusesAndKeepsInput(t *testing.T) {
	testutil.NewSandbox(t)
	stubExec(t)
	ckDispatchStage = func(ctx context.Context, _ *ckTask, _ string, _ string, _ byte) (ckStageOut, error) {
		<-ctx.Done()
		return ckStageOut{}, ctx.Err()
	}
	m := chatFixture("ask")
	m, _ = enter(typed(m, "first task"))
	m = typed(m, "second task")
	m, _ = enter(m)
	if m.th().input != "second task" {
		t.Errorf("the queued text was lost: %q", m.th().input)
	}
	if m.flash == "" {
		t.Error("no explanation for the refusal")
	}
	// esc with text clears the text; the task keeps running.
	m = press(m, tea.KeyEsc)
	if m.th().input != "" {
		t.Errorf("esc did not clear the input: %q", m.th().input)
	}
	if got := m.th().exec.Stage(); strings.Contains(got, "cancelling") {
		t.Error("esc cancelled the task while clearing typed input")
	}
}

// The PII badge shows when the config forces local, and the dispatch options
// carry LocalOnly — the enforcement is dispatch's, the disclosure is the TUI's.
func TestExec_PIIForcesLocalBadgeAndOption(t *testing.T) {
	testutil.NewSandbox(t)
	stubExec(t)
	var got *ckTask
	ckDispatchStage = func(_ context.Context, tk *ckTask, _ string, _ string, _ byte) (ckStageOut, error) {
		got = tk
		return ckStageOut{output: "ok", head: "local-stub", tier: 10}, nil
	}
	m := chatFixture("ask")
	m.piiLocal = true
	m, cmd := enter(typed(m, "email bob@example.com the users report"))
	joined := stripANSI(strings.Join(m.th().log, "\n"))
	if !strings.Contains(joined, "local-only (pii)") {
		t.Errorf("no local-only badge on a PII prompt:\n%s", joined)
	}
	if !strings.Contains(joined, "PII detected") {
		t.Errorf("the why clause hides the PII:\n%s", joined)
	}
	m = runCmds(t, m, cmd)
	if got == nil || !got.localOnly {
		t.Error("the dispatch options do not carry LocalOnly")
	}
	// Without the config policy, the same prompt routes normally: no badge.
	m2 := chatFixture("ask")
	m2, _ = enter(typed(m2, "email bob@example.com the users report"))
	if strings.Contains(stripANSI(strings.Join(m2.th().log, "\n")), "local-only (pii)") {
		t.Error("the badge showed with no local-only pii policy configured")
	}
}

// ── edit mode + d/x/o ─────────────────────────────────────────────────────────

func TestExec_EditModeEditsAndDXOWorkOffSnapshots(t *testing.T) {
	testutil.NewSandbox(t)
	stubExec(t)
	const before, after = "package main\n\nvar old = 1\n", "package main\n\nvar new = 2\n"
	rel, abs := namedFile(t, before)
	edits := 0
	ckEditStage = func(ctx context.Context, tk *ckTask, prompt string) (*editor.Result, error) {
		edits++
		return stubEdit(before, after)(ctx, tk, prompt)
	}

	m := chatFixture("edit")
	m, cmd := enter(typed(m, "swap old for new in "+rel))
	m = runCmds(t, m, cmd)
	if edits != 1 {
		t.Fatalf("edit mode ran %d edits, want 1 (no plan step)", edits)
	}
	joined := stripANSI(strings.Join(m.th().log, "\n"))
	for _, want := range []string{"edit ✓ main.go +1/−1", "tests — skipped (edit mode)", "d diff · x undo · o open"} {
		if !strings.Contains(joined, want) {
			t.Errorf("proof strip missing %q:\n%s", want, joined)
		}
	}
	if raw, _ := os.ReadFile(abs); string(raw) != after {
		t.Fatalf("the file was not written: %q", raw)
	}
	// The code panel streams the real new content — the fake snippets are gone.
	if !strings.Contains(strings.Join(m.th().codeLines, "\n"), "var new = 2") {
		t.Errorf("the code panel does not hold the edited content: %v", m.th().codeLines)
	}

	// d: diff on, from the snapshots; d again: back to the file.
	m, _ = keyRune(m, 'd')
	if !m.th().codeDiff {
		t.Fatal("d did not open the diff")
	}
	diffText := strings.Join(m.th().codeLines, "\n")
	if !strings.Contains(diffText, "-var old = 1") || !strings.Contains(diffText, "+var new = 2") {
		t.Errorf("the diff does not show the change:\n%s", diffText)
	}
	m, _ = keyRune(m, 'd')
	if m.th().codeDiff {
		t.Fatal("d did not toggle the diff off")
	}

	// o with no $EDITOR: an explanation, not a dead key.
	t.Setenv("EDITOR", "")
	m, cmd = keyRune(m, 'o')
	if cmd != nil || !strings.Contains(m.flash, "EDITOR") {
		t.Errorf("o without $EDITOR: cmd=%v flash=%q", cmd, m.flash)
	}
	t.Setenv("EDITOR", "true")
	if _, cmd = keyRune(m, 'o'); cmd == nil {
		t.Error("o with $EDITOR did not open the file")
	}

	// x: restore the exact pre-task bytes; a second x refuses.
	m, _ = keyRune(m, 'x')
	if raw, _ := os.ReadFile(abs); string(raw) != before {
		t.Fatalf("undo did not restore exactly:\n got %q\nwant %q", raw, before)
	}
	if !strings.Contains(m.flash, "restored") {
		t.Errorf("undo flash = %q", m.flash)
	}
	m, _ = keyRune(m, 'x')
	if !strings.Contains(m.flash, "already restored") {
		t.Errorf("second undo flash = %q", m.flash)
	}
}

// With no completed edit, d/x/o type like any other letter — the hijack exists
// only while there is something to act on.
func TestExec_DXOTypeWhenNothingToActOn(t *testing.T) {
	m := testCockpit()
	m = typed(m, "d")
	if m.th().input != "d" || m.th().codeDiff {
		t.Errorf("d with no last edit did not type: input=%q", m.th().input)
	}
	m.th().input = ""
	ans := chatFixture("ask")
	ans.th().lastDone = &ckTask{answer: "text only"} // finished, but nothing edited
	ans = typed(ans, "x")
	if ans.th().input != "x" {
		t.Errorf("x with an answer-only result did not type: %q", ans.th().input)
	}
}

func TestExec_EditModeWithoutFileAnswersWithNote(t *testing.T) {
	testutil.NewSandbox(t)
	stubExec(t)
	ckDispatchStage = stubAnswer("here is how", 0)
	m := chatFixture("edit")
	m, cmd := enter(typed(m, "rename the variable"))
	m = runCmds(t, m, cmd)
	joined := stripANSI(strings.Join(m.th().log, "\n"))
	if !strings.Contains(joined, "no file named — answered instead") {
		t.Errorf("the no-file note is missing:\n%s", joined)
	}
	if !strings.Contains(joined, "here is how") {
		t.Errorf("the answer is missing:\n%s", joined)
	}
}

// ── plan mode ─────────────────────────────────────────────────────────────────

func TestExec_PlanApproveRunsTheRest(t *testing.T) {
	testutil.NewSandbox(t)
	stubExec(t)
	rel, _ := namedFile(t, "package main\n")
	ckDispatchStage = stubAnswer("1. read the file\n2. change it", 0)
	ckEditStage = stubEdit("package main\n", "package main // edited\n")
	verifies := 0
	ckVerifyStage = func(context.Context, []string, string) (oracle.Verdict, error) {
		verifies++
		return oracle.Verdict{Passed: true}, nil
	}

	m := chatFixture("plan")
	m, cmd := enter(typed(m, "add a comment to "+rel))
	m = runCmds(t, m, cmd)
	if m.th().planWait == nil {
		t.Fatal("no plan approval gate")
	}
	if m.th().exec != nil {
		t.Fatal("exec still set while waiting on approval")
	}
	joined := stripANSI(strings.Join(m.th().log, "\n"))
	if !strings.Contains(joined, "PLAN — 2 steps") || !strings.Contains(joined, "1. read the file") {
		t.Errorf("the plan is not rendered:\n%s", joined)
	}

	m, cmd = enter(m) // approve
	if m.th().planWait != nil || m.th().exec == nil {
		t.Fatal("approval did not resume the pipeline")
	}
	m = runCmds(t, m, cmd)
	joined = stripANSI(strings.Join(m.th().log, "\n"))
	for _, want := range []string{"plan ✓ 2 steps", "edit ✓ main.go", "tests ✓ go test ./..."} {
		if !strings.Contains(joined, want) {
			t.Errorf("proof strip missing %q:\n%s", want, joined)
		}
	}
	if verifies != 1 {
		t.Errorf("verify ran %d times, want 1", verifies)
	}
}

// 'a' (apply) approves a pending plan exactly like enter/y.
func TestExec_PlanApplyKeyApproves(t *testing.T) {
	testutil.NewSandbox(t)
	stubExec(t)
	ckDispatchStage = stubAnswer("1. think\n2. answer", 0)
	m := chatFixture("plan")
	m, cmd := enter(typed(m, "outline a users endpoint"))
	m = runCmds(t, m, cmd)
	if m.th().planWait == nil {
		t.Fatal("no plan gate")
	}
	m, cmd = keyRune(m, 'a')
	if m.th().planWait != nil || m.th().exec == nil {
		t.Fatal("a did not approve the plan")
	}
	m = runCmds(t, m, cmd)
	if !strings.Contains(stripANSI(strings.Join(m.th().log, "\n")), "✓ done") {
		t.Error("the approved plan did not finish")
	}
}

func TestExec_PlanDiscardRunsNothing(t *testing.T) {
	testutil.NewSandbox(t)
	stubExec(t)
	rel, abs := namedFile(t, "package main\n")
	ckDispatchStage = stubAnswer("1. do it", 0)
	// The edit stub stays the harness trap: discarding must never reach it.

	m := chatFixture("plan")
	m, cmd := enter(typed(m, "rewrite "+rel))
	m = runCmds(t, m, cmd)
	if m.th().planWait == nil {
		t.Fatal("no plan gate")
	}
	m = press(m, tea.KeyEsc)
	if m.th().planWait != nil {
		t.Fatal("esc did not discard the plan")
	}
	joined := stripANSI(strings.Join(m.th().log, "\n"))
	if !strings.Contains(joined, "plan discarded — nothing ran") || !strings.Contains(joined, "◼ stopped") {
		t.Errorf("the discard is not disclosed:\n%s", joined)
	}
	if raw, _ := os.ReadFile(abs); string(raw) != "package main\n" {
		t.Error("discarding a plan changed the file")
	}
	if m.th().lastDone == nil || !m.th().lastDone.stopped {
		t.Error("the discarded task did not settle as stopped")
	}
}

// ── auto: the verify loop ─────────────────────────────────────────────────────

func TestExec_AutoVerifyLoopFixesThenPasses(t *testing.T) {
	testutil.NewSandbox(t)
	stubExec(t)
	rel, _ := namedFile(t, "package main\n")
	ckDispatchStage = stubAnswer("1. edit\n2. test", 0)
	var edits int
	var fixPrompt string
	ckEditStage = func(ctx context.Context, tk *ckTask, prompt string) (*editor.Result, error) {
		edits++
		if edits > 1 {
			fixPrompt = prompt
		}
		return stubEdit("package main\n", "package main // v"+fmt.Sprint(edits)+"\n")(ctx, tk, prompt)
	}
	verifies := 0
	ckVerifyStage = func(context.Context, []string, string) (oracle.Verdict, error) {
		verifies++
		if verifies == 1 {
			return oracle.Verdict{Passed: false, Detail: "TestBoom failed"}, nil
		}
		return oracle.Verdict{Passed: true}, nil
	}

	m := chatFixture("auto")
	m, cmd := enter(typed(m, "make the test pass in "+rel))
	m = runCmds(t, m, cmd)
	if edits != 2 || verifies != 2 {
		t.Fatalf("loop ran %d edits / %d verifies, want 2/2", edits, verifies)
	}
	if !strings.Contains(fixPrompt, "TestBoom failed") {
		t.Errorf("the fix re-dispatch does not carry the failure output:\n%s", fixPrompt)
	}
	joined := stripANSI(strings.Join(m.th().log, "\n"))
	if !strings.Contains(joined, "tests ✓ go test ./... (after 1 fix)") {
		t.Errorf("the strip does not credit the fix loop:\n%s", joined)
	}
	// The task's diff spans first-before → last-after, collapsing the fix hops.
	m, _ = keyRune(m, 'd')
	diffText := strings.Join(m.th().codeLines, "\n")
	if !strings.Contains(diffText, "+package main // v2") {
		t.Errorf("the diff does not end at the final content:\n%s", diffText)
	}
}

func TestExec_AutoVerifyLoopCapsAtTwoFixes(t *testing.T) {
	testutil.NewSandbox(t)
	stubExec(t)
	rel, _ := namedFile(t, "package main\n")
	ckDispatchStage = stubAnswer("1. try", 0)
	edits := 0
	ckEditStage = func(ctx context.Context, tk *ckTask, prompt string) (*editor.Result, error) {
		edits++
		return stubEdit("package main\n", "package main // still wrong\n")(ctx, tk, prompt)
	}
	verifies := 0
	ckVerifyStage = func(context.Context, []string, string) (oracle.Verdict, error) {
		verifies++
		return oracle.Verdict{Passed: false, Detail: "still broken"}, nil
	}

	m := chatFixture("auto")
	m, cmd := enter(typed(m, "fix "+rel))
	m = runCmds(t, m, cmd)
	if edits != 1+ckMaxFixes || verifies != 1+ckMaxFixes {
		t.Fatalf("loop ran %d edits / %d verifies, want %d/%d", edits, verifies, 1+ckMaxFixes, 1+ckMaxFixes)
	}
	joined := stripANSI(strings.Join(m.th().log, "\n"))
	if !strings.Contains(joined, "tests ✗") || !strings.Contains(joined, "after 2 fixes") {
		t.Errorf("the capped failure is not disclosed:\n%s", joined)
	}
	if !strings.Contains(joined, "still broken") {
		t.Errorf("the last failure detail is missing:\n%s", joined)
	}
}

// Auto with no verifier configured says so instead of pretending it verified.
func TestExec_AutoWithoutVerifierSaysSo(t *testing.T) {
	testutil.NewSandbox(t)
	stubExec(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	// .txt has no workspace validator and there is no go.mod: argv resolves empty.
	ckDispatchStage = stubAnswer("1. edit", 0)
	ckEditStage = stubEdit("x", "y")

	m := chatFixture("auto")
	// ckFileRe does not match .txt, so no file is named: auto degrades to
	// plan → answer, with the answer rendered.
	m, cmd := enter(typed(m, "tidy the notes file"))
	m = runCmds(t, m, cmd)
	if !strings.Contains(stripANSI(strings.Join(m.th().log, "\n")), "✓ done") {
		t.Error("auto without a file did not settle as an answer")
	}
	// And with a named .go file but no verifier anywhere: the strip says so.
	if args, label := ckVerifyArgs(filepath.Join(dir, "main.txt")); args != nil || label != "" {
		t.Errorf("ckVerifyArgs invented a verifier: %v %q", args, label)
	}
}

// ── careful ───────────────────────────────────────────────────────────────────

func TestExec_CarefulConfirmsEveryWrite(t *testing.T) {
	testutil.NewSandbox(t)
	stubExec(t)
	rel, abs := namedFile(t, "package main\n")
	ckDispatchStage = stubAnswer("1. edit it", 0)
	edits := 0
	ckEditStage = func(ctx context.Context, tk *ckTask, prompt string) (*editor.Result, error) {
		edits++
		return stubEdit("package main\n", "package main // careful\n")(ctx, tk, prompt)
	}
	verifies := 0
	ckVerifyStage = func(context.Context, []string, string) (oracle.Verdict, error) {
		verifies++
		if verifies == 1 {
			return oracle.Verdict{Passed: false, Detail: "not yet"}, nil
		}
		return oracle.Verdict{Passed: true}, nil
	}

	// n at the write gate: nothing lands.
	m := chatFixture("careful")
	m, cmd := enter(typed(m, "change "+rel))
	m = runCmds(t, m, cmd)
	if m.th().confirm == nil || !strings.Contains(m.th().confirm.question, "write main.go?") {
		t.Fatalf("no write confirm: %+v", m.th().confirm)
	}
	m, cmd = keyRune(m, 'n')
	m = runCmds(t, m, cmd)
	if edits != 0 {
		t.Fatal("n still wrote the file")
	}
	if !strings.Contains(stripANSI(strings.Join(m.th().log, "\n")), "stopped before writing") {
		t.Error("the refusal is not disclosed")
	}

	// y at the write gate, then y at the fix gate: both writes were confirmed.
	m = chatFixture("careful")
	m, cmd = enter(typed(m, "change "+rel))
	m = runCmds(t, m, cmd)
	m, cmd = keyRune(m, 'y')
	m = runCmds(t, m, cmd)
	if m.th().confirm == nil || !strings.Contains(m.th().confirm.question, "fix 1/2") {
		t.Fatalf("no fix confirm after a failing verify: %+v", m.th().confirm)
	}
	m, cmd = keyRune(m, 'y')
	m = runCmds(t, m, cmd)
	if edits != 2 || verifies != 2 {
		t.Fatalf("confirmed flow ran %d edits / %d verifies, want 2/2", edits, verifies)
	}
	if !strings.Contains(stripANSI(strings.Join(m.th().log, "\n")), "tests ✓") {
		t.Error("the confirmed fix did not pass")
	}

	// esc at a fix gate: the landed edit stays, disclosed as declined.
	m = chatFixture("careful")
	verifies, edits = 0, 0
	ckVerifyStage = func(context.Context, []string, string) (oracle.Verdict, error) {
		verifies++
		return oracle.Verdict{Passed: false, Detail: "always red"}, nil
	}
	m, cmd = enter(typed(m, "change "+rel))
	m = runCmds(t, m, cmd)
	m, cmd = keyRune(m, 'y')
	m = runCmds(t, m, cmd)
	if m.th().confirm == nil {
		t.Fatal("no fix gate")
	}
	m = press(m, tea.KeyEsc)
	joined := stripANSI(strings.Join(m.th().log, "\n"))
	if !strings.Contains(joined, "fix declined") || !strings.Contains(joined, "tests ✗") {
		t.Errorf("the declined fix is not honest:\n%s", joined)
	}
	if raw, _ := os.ReadFile(abs); !strings.Contains(string(raw), "careful") {
		t.Error("the confirmed first write was rolled back by declining the fix")
	}
}

// ── unattended ────────────────────────────────────────────────────────────────

func TestExec_UnattendedStopsVisiblyAtTheCostCap(t *testing.T) {
	testutil.NewSandbox(t)
	stubExec(t)
	rel, _ := namedFile(t, "package main\n")
	// The plan alone books more than the cap; the next stage must refuse.
	ckDispatchStage = stubAnswer("1. spend", 1.00)

	m := chatFixture("unattended")
	m, cmd := enter(typed(m, "refactor "+rel))
	m = runCmds(t, m, cmd)
	joined := stripANSI(strings.Join(m.th().log, "\n"))
	if !strings.Contains(joined, "✗ failed") || !strings.Contains(joined, "cost cap $0.50 reached") {
		t.Errorf("the cap stop is not visible:\n%s", joined)
	}
	// The stage-level guard also rides along on plain dispatches.
	var seen *ckTask
	ckDispatchStage = func(_ context.Context, tk *ckTask, _ string, _ string, _ byte) (ckStageOut, error) {
		seen = tk
		return ckStageOut{output: "1. ok", head: "h", tier: 8}, nil
	}
	m2 := chatFixture("unattended")
	m2, cmd = enter(typed(m2, "explain the plan"))
	_ = runCmds(t, m2, cmd)
	if seen == nil || seen.mode.capUSD != ckUnattendedCapUSD {
		t.Error("the per-dispatch MaxCostUSD knob is not threaded")
	}
}

// ── ctrl+o override wiring ────────────────────────────────────────────────────

func TestExec_OverrideWiresIntoTheNextDispatchOnly(t *testing.T) {
	testutil.NewSandbox(t)
	stubExec(t)
	var strat byte
	var seen *ckTask
	capture := func(_ context.Context, tk *ckTask, _ string, _ string, s byte) (ckStageOut, error) {
		seen, strat = tk, s
		return ckStageOut{output: "ok", head: "h", tier: 8, confidence: 0.97}, nil
	}
	ckDispatchStage = capture

	// local only
	m := chatFixture("ask")
	m = press(m, tea.KeyCtrlO)
	m = typed(m, "jj")
	m, _ = enter(m)
	m, cmd := enter(typed(m, "explain the endpoint"))
	if !strings.Contains(stripANSI(strings.Join(m.th().log, "\n")), "local only") {
		t.Error("the route line does not show the local-only override")
	}
	m = runCmds(t, m, cmd)
	if seen == nil || !seen.localOnly {
		t.Error("local only did not reach the dispatch options")
	}
	if m.override.kind != 0 {
		t.Error("the override survived the task — it is next-task-only")
	}

	// force tier 3
	m = chatFixture("ask")
	m = press(m, tea.KeyCtrlO)
	m = typed(m, "j")
	m, _ = enter(m)
	m = typed(m, "3")
	m, cmd = enter(typed(m, "explain the endpoint"))
	if !strings.Contains(stripANSI(strings.Join(m.th().log, "\n")), "forced") {
		t.Error("the route line does not show the forced tier")
	}
	m = runCmds(t, m, cmd)
	if seen.answerTier != "3" {
		t.Errorf("answerTier = %q, want 3", seen.answerTier)
	}

	// best of 3 reaches the answer stage as its strategy
	m = chatFixture("ask")
	m = press(m, tea.KeyCtrlO)
	m = typed(m, "jjj")
	m, _ = enter(m)
	m, cmd = enter(typed(m, "is this migration safe?"))
	m = runCmds(t, m, cmd)
	if strat != 'B' {
		t.Errorf("strategy = %q, want best of 3", strat)
	}

	// consensus carries its target and renders the achieved confidence
	m = chatFixture("ask")
	m = press(m, tea.KeyCtrlO)
	m = typed(m, "jjjj")
	m, _ = enter(m)
	m = typed(m, "2") // ≥95%
	m, cmd = enter(typed(m, "is this migration safe?"))
	if !strings.Contains(stripANSI(strings.Join(m.th().log, "\n")), "consensus ≥95%") {
		t.Error("the route line does not show the consensus strategy")
	}
	m = runCmds(t, m, cmd)
	if strat != 'C' || seen.confidence != 0.95 {
		t.Errorf("consensus not wired: strat=%q conf=%v", strat, seen.confidence)
	}
	if !strings.Contains(stripANSI(strings.Join(m.th().log, "\n")), "confidence 97%") {
		t.Error("the achieved confidence is not in the footer")
	}
}

// A fan-out override on a file edit stays single — visibly.
func TestExec_OverrideFanoutFallsBackToSingleForEdits(t *testing.T) {
	testutil.NewSandbox(t)
	stubExec(t)
	rel, _ := namedFile(t, "package main\n")
	var strat byte = 0xFF
	ckDispatchStage = func(_ context.Context, _ *ckTask, _ string, _ string, s byte) (ckStageOut, error) {
		strat = s
		return ckStageOut{output: "1. plan", head: "h", tier: 8}, nil
	}
	ckEditStage = stubEdit("package main\n", "package main // x\n")
	ckVerifyStage = func(context.Context, []string, string) (oracle.Verdict, error) {
		return oracle.Verdict{Passed: true}, nil
	}

	m := chatFixture("auto")
	m = press(m, tea.KeyCtrlO)
	m = typed(m, "jjj") // best of 3
	m, _ = enter(m)
	m, cmd := enter(typed(m, "improve "+rel))
	if !strings.Contains(stripANSI(strings.Join(m.th().log, "\n")), "edits can't fan out") {
		t.Error("the single fallback is not disclosed on the route line")
	}
	m = runCmds(t, m, cmd)
	if strat != 0 {
		t.Errorf("the plan stage ran with strategy %q, want single", strat)
	}
	_ = m
}

// ── architect ─────────────────────────────────────────────────────────────────

func TestExec_ArchitectSplitsPlanAndImplementTiers(t *testing.T) {
	testutil.NewSandbox(t)
	stubExec(t)
	rel, _ := namedFile(t, "package main\n")
	var planTier string
	ckDispatchStage = func(_ context.Context, tk *ckTask, _ string, tier string, _ byte) (ckStageOut, error) {
		planTier = tier
		return ckStageOut{output: "1. design\n2. build", head: "opus", tier: 2}, nil
	}
	var editTask *ckTask
	ckEditStage = func(ctx context.Context, tk *ckTask, prompt string) (*editor.Result, error) {
		editTask = tk
		return stubEdit("package main\n", "package main // built\n")(ctx, tk, prompt)
	}
	ckVerifyStage = func(context.Context, []string, string) (oracle.Verdict, error) {
		return oracle.Verdict{Passed: true}, nil
	}

	m := chatFixture("architect")
	m, cmd := enter(typed(m, "restructure "+rel))
	m = runCmds(t, m, cmd)
	if planTier != "2" {
		t.Errorf("the plan ran on tier %q, want the strong tier 2", planTier)
	}
	if editTask == nil || editTask.editEnum != "SIMPLE" {
		t.Errorf("the implement half is not on the cheap tier: %+v", editTask)
	}
	if !strings.Contains(stripANSI(strings.Join(m.th().log, "\n")), "tests ✓") {
		t.Error("architect did not verify")
	}
}

// ── stale/foreign messages ────────────────────────────────────────────────────

func TestExec_StaleWorkerMessagesAreIgnored(t *testing.T) {
	m := testCockpit()
	ghost := &ckExecState{}
	before := len(m.th().log)
	next, _ := m.Update(ckExecDoneMsg{exec: ghost, task: ckTask{answer: "ghost"}})
	m = next.(Cockpit)
	if len(m.th().log) != before || m.th().lastDone != nil {
		t.Error("a stale done message landed")
	}
	next, _ = m.Update(ckGateMsg{exec: ghost, task: ckTask{}, gate: 'p'})
	m = next.(Cockpit)
	if m.th().planWait != nil {
		t.Error("a stale gate message landed")
	}
	// A stale spin tick schedules nothing.
	if _, cmd := m.Update(ckSpinTickMsg{exec: ghost}); cmd != nil {
		t.Error("a stale spin tick rescheduled itself")
	}
}

// ── verify command resolution ─────────────────────────────────────────────────

func TestCkVerifyArgs_GoRepoThenWorkspaceValidator(t *testing.T) {
	testutil.NewSandbox(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	argv, label := ckVerifyArgs("")
	if len(argv) != 3 || argv[0] != "go" || label != "go test ./..." {
		t.Errorf("go repo: argv=%v label=%q", argv, label)
	}

	// Outside a Go repo, the workspace validator for the extension applies,
	// with {file} substituted by the real path (embedded workspace.yaml has a
	// py validator and none for go).
	plain := t.TempDir()
	t.Chdir(plain)
	target := filepath.Join(plain, "script.py")
	argv, label = ckVerifyArgs(target)
	if len(argv) == 0 || argv[0] != "python3" {
		t.Fatalf("py validator not resolved: %v", argv)
	}
	found := false
	for _, a := range argv {
		if a == target {
			found = true
		}
	}
	if !found {
		t.Errorf("{file} was not substituted with the real path: %v", argv)
	}
	if !strings.Contains(label, "script.py") || strings.Contains(label, "{file}") {
		t.Errorf("label = %q", label)
	}

	// No verifier anywhere: empty, never invented.
	if argv, label = ckVerifyArgs(filepath.Join(plain, "notes.txt")); argv != nil || label != "" {
		t.Errorf("invented a verifier: %v %q", argv, label)
	}
	if argv, _ = ckVerifyArgs(""); argv != nil {
		t.Errorf("invented a verifier with no file: %v", argv)
	}
}

// A .git boundary stops the go.mod walk: an unrelated repo nested under a Go
// directory is not "a Go repo".
func TestCkGoModDir_StopsAtGitBoundary(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module up\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "sub")
	if err := os.MkdirAll(filepath.Join(nested, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(nested)
	if got := ckGoModDir(); got != "" {
		t.Errorf("the walk crossed a .git boundary to %q", got)
	}
	t.Chdir(root)
	if got := ckGoModDir(); got != root {
		t.Errorf("go.mod in the cwd not found: %q", got)
	}
}

// ── small pieces ──────────────────────────────────────────────────────────────

func TestCkCountSteps(t *testing.T) {
	if got := ckCountSteps("1. a\n2. b\n3) c\nnot a step"); got != 3 {
		t.Errorf("counted %d steps", got)
	}
	if got := ckCountSteps("just prose"); got != 1 {
		t.Errorf("unnumbered plan counted %d", got)
	}
	if got := ckCountSteps("  \n"); got != 0 {
		t.Errorf("empty plan counted %d", got)
	}
}

func TestCkEditEnum(t *testing.T) {
	auto := ckModeByName("auto")
	if got := ckEditEnum("STANDARD", auto, ckOverride{}); got != "STANDARD" {
		t.Errorf("classified enum not kept: %q", got)
	}
	if got := ckEditEnum("CORE", auto, ckOverride{}); got != "EXPERT" {
		t.Errorf("CORE must cap to EXPERT for edits: %q", got)
	}
	if got := ckEditEnum("STANDARD", ckModeByName("architect"), ckOverride{}); got != "SIMPLE" {
		t.Errorf("architect implements cheap: %q", got)
	}
	if got := ckEditEnum("STANDARD", auto, ckOverride{kind: 'T', tier: 5}); got != "COMPLEX" {
		t.Errorf("forced tier not mapped: %q", got)
	}
	if got := ckEditEnum("STANDARD", auto, ckOverride{kind: 'T', tier: 1}); got != "EXPERT" {
		t.Errorf("forced CORE must cap to EXPERT: %q", got)
	}
}

func TestCkNamedFile_OnlyRealFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pkg", "real.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	if got := ckNamedFile("edit pkg/real.go now"); got != filepath.Join(dir, "pkg", "real.go") {
		t.Errorf("real file not resolved: %q", got)
	}
	if got := ckNamedFile("edit pkg/ghost.go now"); got != "" {
		t.Errorf("a missing file was named: %q", got)
	}
	if got := ckNamedFile("no file here"); got != "" {
		t.Errorf("a file was invented: %q", got)
	}
	if got := ckNamedFile("look at pkg"); got != "" {
		t.Errorf("matched without an extension: %q", got)
	}
}

func TestCkTailTruncate_KeepsTheTail(t *testing.T) {
	long := strings.Repeat("head ", 40) + "TAIL"
	got := stripANSI(ckTailTruncate(long, 20))
	if !strings.Contains(got, "TAIL") {
		t.Errorf("the tail was cut: %q", got)
	}
	if lipgloss.Width(got) > 21 { // "…" + up to 20 cells
		t.Errorf("still %d cells wide", lipgloss.Width(got))
	}
	if s := ckTailTruncate("short", 20); s != "short" {
		t.Errorf("a fitting line was changed: %q", s)
	}
}

func TestCkTestsCellAndProofStrip_EveryState(t *testing.T) {
	base := ckTask{mode: ckModeByName("auto"), file: "a/b.go", added: 3, removed: 1, planSteps: 2, verifyLabel: "go test ./..."}
	cases := []struct {
		name string
		mut  func(*ckTask)
		want string
	}{
		{"skipped mode", func(t *ckTask) { t.mode = ckModeByName("edit") }, "skipped (edit mode)"},
		{"unconfigured", func(t *ckTask) { t.verifySkipped = true }, "no verifier configured"},
		{"verifier failed", func(t *ckTask) { t.verifyErr = "exec: not found" }, "verifier failed"},
		{"not run", func(*ckTask) {}, "tests — not run"},
		{"passed", func(t *ckTask) { t.rounds = []ckVerifyRound{{passed: true}} }, "tests ✓ go test ./..."},
		{"passed after fixes", func(t *ckTask) {
			t.rounds = []ckVerifyRound{{}, {}, {passed: true}}
		}, "after 2 fixes"},
		{"failing", func(t *ckTask) {
			t.rounds = []ckVerifyRound{{detail: "boom"}}
		}, "still failing: boom"},
	}
	for _, tc := range cases {
		tk := base
		tc.mut(&tk)
		if got := stripANSI(ckTestsCell(tk)); !strings.Contains(got, tc.want) {
			t.Errorf("%s: %q does not contain %q", tc.name, got, tc.want)
		}
	}
	strip := stripANSI(ckProofStrip(base))
	if !strings.Contains(strip, "plan ✓ 2 steps") || !strings.Contains(strip, "edit ✓ b.go +3/−1") {
		t.Errorf("strip = %q", strip)
	}
}

// ── layout: the new chat states hold the frame ────────────────────────────────

func TestChatStates_LayoutInvariantsAtEverySize(t *testing.T) {
	testutil.NewSandbox(t)
	long := strings.Repeat("x", 3000)
	longAnswer := ckTask{
		runID: "20260904T111111Z-aaaa1111", mode: ckModeByName("auto"),
		answer: strings.Repeat("answer line — reasonably long so it wraps at narrow widths\n", 60) + long,
		edited: true, file: "internal/api/users.go", added: 12, removed: 4, planSteps: 3,
		rounds: []ckVerifyRound{{passed: true}}, verifyLabel: "go test ./...",
		costUSD: 0.0123, elapsed: 3 * time.Second,
	}

	states := map[string]func(Cockpit) Cockpit{
		"running": func(m Cockpit) Cockpit {
			m.th().exec = &ckExecState{stage: "verifying — go test ./... with a very long stage line " + long[:200], started: time.Now()}
			return m
		},
		"plan_pending": func(m Cockpit) Cockpit {
			t := ckTask{plan: "1. first\n2. second\n3. " + long, planSteps: 3, headName: "stub"}
			m.th().log = append(m.th().log, ckPlanLines(t)...)
			m.th().planWait = &ckWait{task: t, phase: ckPhaseTail}
			return m
		},
		"confirm": func(m Cockpit) Cockpit {
			m.th().confirm = &ckWait{question: "write " + long[:120] + "? y/n"}
			return m
		},
		"proof_strip_long_answer": func(m Cockpit) Cockpit {
			m.th().log = append(m.th().log, ckResultLines(longAnswer)...)
			m.th().lastDone = &longAnswer
			return m
		},
		"mode_picker":    func(m Cockpit) Cockpit { m.modePick = true; return m },
		"override_modal": func(m Cockpit) Cockpit { m.ovOpen = true; return m },
		"long_input":     func(m Cockpit) Cockpit { m.th().input = long; return m },
		"diff_panel": func(m Cockpit) Cockpit {
			m.th().codeDiff, m.th().codeLang = true, "diff"
			for i := 0; i < 80; i++ {
				m.th().codeLines = append(m.th().codeLines, "+"+long[:80])
			}
			m.th().codeShown = len(m.th().codeLines)
			return m
		},
	}

	sizes := []struct{ w, h int }{{60, 15}, {80, 24}, {100, 30}, {120, 40}}
	for name, build := range states {
		for _, sz := range sizes {
			t.Run(fmt.Sprintf("%s_%dx%d", name, sz.w, sz.h), func(t *testing.T) {
				m := build(testCockpit())
				m.w, m.h, m.ready = sz.w, sz.h, true
				out := m.View()
				lines := strings.Split(out, "\n")
				if len(lines) > sz.h {
					t.Errorf("%s at %dx%d renders %d lines, want <= %d", name, sz.w, sz.h, len(lines), sz.h)
				}
				for i, l := range lines {
					if got := lipgloss.Width(l); got > sz.w {
						t.Errorf("%s at %dx%d: line %d is %d cells wide", name, sz.w, sz.h, i, got)
					}
				}
				last := stripANSI(lines[len(lines)-1])
				if !strings.Contains(last, "shortcuts") {
					t.Errorf("%s at %dx%d: the status bar is not the final line: %q", name, sz.w, sz.h, last)
				}
			})
		}
	}
}

// Long real outputs keep append-no-yank: a reader anchored in scrollback stays
// anchored when a 200-line answer lands.
func TestChatScrollback_SurvivesLongAnswers(t *testing.T) {
	testutil.NewSandbox(t)
	m := testCockpit()
	m.w, m.h, m.ready = 100, 24, true
	for i := 0; i < 60; i++ {
		m.th().log = append(m.th().log, fmt.Sprintf("history %d", i))
	}
	m = press(m, tea.KeyPgUp)
	anchor := m.th().scroll
	if anchor == 0 {
		t.Fatal("pgup did not anchor")
	}
	big := ckTask{runID: "r", answer: strings.Repeat("new answer line\n", 200), elapsed: time.Second}
	m.th().log = append(m.th().log, ckResultLines(big)...)
	if m.th().scroll != anchor {
		t.Error("a long answer yanked the anchored reader")
	}
	if out := stripANSI(m.View()); strings.Contains(out, "new answer line") {
		t.Error("anchored view was dragged to the new output")
	}
	m = press(m, tea.KeyEnd)
	if out := stripANSI(m.View()); !strings.Contains(out, "✓ done") {
		t.Error("end did not return to the live tail")
	}
}

// ── the real stages (error paths — no provider is ever driven in a unit test) ──

// The real dispatch stage reaches the real router for every strategy; with a
// config but no heads each strategy fails at its own entry point rather than
// silently collapsing into one path.
func TestCkRealDispatchStage_StrategiesReachTheirEntryPoints(t *testing.T) {
	testutil.NewSandbox(t)
	tk := &ckTask{runID: "r", taskID: "t", confidence: 0.95}

	// No config at all: dispatch.New itself refuses.
	if _, err := ckRealDispatchStage(context.Background(), tk, "p", "8", 0); err == nil ||
		!strings.Contains(err.Error(), "hyctl init") {
		t.Fatalf("no-config dispatch did not fail loudly: %v", err)
	}

	if err := config.Save(&config.Config{Cortex: "none"}); err != nil {
		t.Fatal(err)
	}
	for _, strat := range []byte{0, 'B', 'C'} {
		_, err := ckRealDispatchStage(context.Background(), tk, "p", "", strat)
		if err == nil {
			t.Fatalf("strategy %q dispatched with zero heads", strat)
		}
		if !strings.Contains(err.Error(), "head") {
			t.Errorf("strategy %q error does not blame the head pool: %v", strat, err)
		}
	}
}

// The real edit stage is the editor path: outside any workspace it fails as a
// scope rejection, not a write.
func TestCkRealEditStage_OutsideWorkspaceIsRejected(t *testing.T) {
	testutil.NewSandbox(t)
	dir := t.TempDir()
	tk := &ckTask{runID: "r", taskID: "t", file: filepath.Join(dir, "x.go"), editEnum: "SIMPLE"}
	res, err := ckRealEditStage(context.Background(), tk, "change it")
	if err != nil {
		t.Fatalf("editor errors travel inside the Result: %v", err)
	}
	if res.Status != "fail" || res.Error == "" {
		t.Fatalf("an out-of-scope edit did not fail: %+v", res)
	}
}

// The real verify stage maps exit codes to verdicts and launch failures to errors.
func TestCkRealVerifyStage_ExitCodesBecomeVerdicts(t *testing.T) {
	v, err := ckRealVerifyStage(context.Background(), []string{"go", "version"}, "")
	if err != nil || !v.Passed {
		t.Fatalf("`go version` did not pass: v=%+v err=%v", v, err)
	}
	v, err = ckRealVerifyStage(context.Background(), []string{"go", "tool", "definitely-not-a-tool"}, "")
	if err != nil || v.Passed {
		t.Fatalf("a failing command did not fail: v=%+v err=%v", v, err)
	}
	if _, err = ckRealVerifyStage(context.Background(), []string{"/nonexistent-hydra-verifier"}, ""); err == nil {
		t.Fatal("an unlaunchable verifier must be an error, not a verdict")
	}
}

// ── pipeline error branches ───────────────────────────────────────────────────

// A verifier that cannot run is disclosed as such — distinct from tests failing.
func TestExec_VerifierErrorIsHonest(t *testing.T) {
	testutil.NewSandbox(t)
	stubExec(t)
	rel, _ := namedFile(t, "package main\n")
	ckDispatchStage = stubAnswer("1. edit", 0)
	ckEditStage = stubEdit("package main\n", "package main // v\n")
	ckVerifyStage = func(context.Context, []string, string) (oracle.Verdict, error) {
		return oracle.Verdict{}, errors.New("exec: go not found")
	}
	m := chatFixture("auto")
	m, cmd := enter(typed(m, "touch "+rel))
	m = runCmds(t, m, cmd)
	joined := stripANSI(strings.Join(m.th().log, "\n"))
	if !strings.Contains(joined, "tests ? ") || !strings.Contains(joined, "verifier failed") {
		t.Errorf("a broken verifier is not disclosed:\n%s", joined)
	}
}

// An editor failure (its Result, not a Go error) fails the task with the reason.
func TestExec_EditFailureRendersReason(t *testing.T) {
	testutil.NewSandbox(t)
	stubExec(t)
	rel, _ := namedFile(t, "package main\n")
	calls := 0
	ckEditStage = func(context.Context, *ckTask, string) (*editor.Result, error) {
		calls++
		if calls == 1 {
			return &editor.Result{Status: "fail", Error: "validation_failed: gofmt"}, nil
		}
		return nil, errors.New("dispatcher exploded")
	}
	m := chatFixture("edit")
	m, cmd := enter(typed(m, "break "+rel))
	m = runCmds(t, m, cmd)
	if joined := stripANSI(strings.Join(m.th().log, "\n")); !strings.Contains(joined, "validation_failed") {
		t.Errorf("the editor's reason is missing:\n%s", joined)
	}
	m2 := chatFixture("edit")
	m2, cmd = enter(typed(m2, "break "+rel))
	m2 = runCmds(t, m2, cmd)
	if joined := stripANSI(strings.Join(m2.th().log, "\n")); !strings.Contains(joined, "dispatcher exploded") {
		t.Errorf("the edit error is missing:\n%s", joined)
	}
}

// ckRunSpend and the result actions degrade cleanly when the runlog has nothing.
func TestResultActions_DegradeWithoutSnapshots(t *testing.T) {
	testutil.NewSandbox(t)
	if got := ckRunSpend("never-ran"); got != 0 {
		t.Errorf("spend for an unknown run = %v", got)
	}
	m := testCockpit()
	tk := &ckTask{edited: true, file: "x.go", runID: "never-ran"}
	m.th().lastDone = tk
	m, _, ok := m.resultKey('d')
	if !ok || m.th().codeDiff {
		t.Errorf("d with no snapshot: ok=%v diff=%v", ok, m.th().codeDiff)
	}
	if !strings.Contains(m.flash, "no snapshot") {
		t.Errorf("flash = %q", m.flash)
	}
	m, _, _ = m.resultKey('x')
	if !strings.Contains(m.flash, "no snapshot") {
		t.Errorf("undo flash = %q", m.flash)
	}
	// Refs that point at pruned snapshots surface the loader's reason.
	tk.editRef, tk.editRefLast = "000001", "000001"
	m, _, _ = m.resultKey('d')
	if !strings.Contains(m.flash, "diff unavailable") {
		t.Errorf("pruned-snapshot diff flash = %q", m.flash)
	}
	m, _, _ = m.resultKey('x')
	if !strings.Contains(m.flash, "undo unavailable") {
		t.Errorf("pruned-snapshot undo flash = %q", m.flash)
	}
}

// ckPlanLines caps a rambling plan and counts what it hid.
func TestCkPlanLines_CapsLongPlans(t *testing.T) {
	var steps []string
	for i := 1; i <= 20; i++ {
		steps = append(steps, fmt.Sprintf("%d. step", i))
	}
	tk := ckTask{plan: strings.Join(steps, "\n"), planSteps: 20, headName: "h"}
	lines := stripANSI(strings.Join(ckPlanLines(tk), "\n"))
	if !strings.Contains(lines, "PLAN — 20 steps") || !strings.Contains(lines, "… 8 more lines") {
		t.Errorf("the cap is not disclosed:\n%s", lines)
	}
}

func TestCkClassWordsAndWhy(t *testing.T) {
	for enum, want := range map[string]string{
		"CORE": "critical work", "COMPLEX": "complex work", "MODERATE": "moderate work",
		"STANDARD": "standard work", "SIMPLE": "simple task",
	} {
		if got := ckClassWords(enum); got != want {
			t.Errorf("ckClassWords(%s) = %q", enum, got)
		}
	}
	tk := ckTask{file: "x.go", mode: ckModeByName("auto"), pii: true}
	if got := ckWhyWords(tk, "COMPLEX"); got != "code edit, complex scope, PII detected" {
		t.Errorf("why = %q", got)
	}
	if got := ckWhyWords(ckTask{mode: ckModeByName("ask"), file: "x.go"}, "SIMPLE"); got != "question, small scope, no PII" {
		t.Errorf("ask-mode why = %q", got)
	}
}

func TestCkFirstLine(t *testing.T) {
	if got := ckFirstLine("  first\nsecond"); got != "first" {
		t.Errorf("got %q", got)
	}
	if got := ckFirstLine("only"); got != "only" {
		t.Errorf("got %q", got)
	}
}

// The chat scroll geometry mirrors the renderer with the taller input bar, and
// scrollback still clamps at both ends.
func TestChatScrollGeom_MirrorsInputBarHeight(t *testing.T) {
	m := testCockpit()
	m.w, m.h = 100, 24
	_, logH := m.chatLogGeom()
	m.th().input = strings.Repeat("wrap me ", 40)
	_, logHWrapped := m.chatLogGeom()
	if logHWrapped >= logH {
		t.Errorf("a wrapped input did not shrink the log window: %d → %d", logH, logHWrapped)
	}
	for i := 0; i < 50; i++ {
		m.th().log = append(m.th().log, "line")
	}
	m = m.chatScrollBy(-1000)
	if m.th().scroll != 1 {
		t.Errorf("scrollback did not clamp at the top: %d", m.th().scroll)
	}
	m = m.chatScrollBy(1000)
	if m.th().scroll != 0 {
		t.Errorf("scrollback did not return to live: %d", m.th().scroll)
	}
	m.th().log = []string{"one"}
	if m = m.chatScrollBy(-3); m.th().scroll != 0 {
		t.Error("a fitting log entered scrollback")
	}
}
