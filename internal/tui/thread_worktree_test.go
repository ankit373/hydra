// SPDX-License-Identifier: MIT

package tui

// Worktree isolation lifecycle (#598) against real git: create, apply (clean /
// conflict / refusal / empty), discard, stale recovery. No git behavior is
// mocked — the merge semantics ARE the feature.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/editor"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/testutil"
)

// threadRepo installs a sandbox with the host's git allowed, builds a
// committed repo with pkg/{main,other,third}.go, and chdirs into it —
// mirroring where a user launches `hyctl tui`.
func threadRepo(t *testing.T) string {
	t.Helper()
	s := testutil.NewSandbox(t)
	if !s.AllowHostBinary(t, "git") {
		t.Skip("git is not installed on this machine")
	}
	repo := t.TempDir()
	mustGit := func(args ...string) {
		t.Helper()
		if out, err := ckGit(repo, "", args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	mustGit("init", "-q")
	mustGit("config", "user.email", "t@t")
	mustGit("config", "user.name", "t")
	if err := os.MkdirAll(filepath.Join(repo, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"main.go", "other.go", "third.go"} {
		if err := os.WriteFile(filepath.Join(repo, "pkg", f),
			[]byte("package pkg\n\nvar base = 1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustGit("add", "-A")
	mustGit("commit", "-qm", "base")

	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return cwd // symlink-resolved on macOS, matching what ckNamedFile computes
}

func TestWorktree_CreateApplyCleanLifecycle(t *testing.T) {
	repo := threadRepo(t)

	wt, err := ckCreateWorktree(repo, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(filepath.Base(wt.dir), "t3-") || !strings.HasPrefix(wt.branch, "hydra/task-t3-") {
		t.Errorf("naming: dir=%s branch=%s", wt.dir, wt.branch)
	}
	if !strings.HasPrefix(wt.dir, ckWorktreeBase()) {
		t.Errorf("worktree not under %s: %s", ckWorktreeBase(), wt.dir)
	}
	if wt.base == "" {
		t.Error("no base commit recorded")
	}

	edited := filepath.Join(wt.dir, "pkg", "main.go")
	if err := os.WriteFile(edited, []byte("package pkg\n\nvar base = 2 // thread\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := ckApplyWorktree(wt)
	if !out.applied || out.conflict || out.refused || out.err != nil {
		t.Fatalf("clean apply: %+v", out)
	}
	if len(out.files) != 1 || out.files[0] != "pkg/main.go" {
		t.Errorf("touched files = %v", out.files)
	}
	raw, _ := os.ReadFile(filepath.Join(repo, "pkg", "main.go"))
	if !strings.Contains(string(raw), "var base = 2") {
		t.Errorf("the user's tree did not receive the edit: %q", raw)
	}
	if _, err := os.Stat(wt.dir); !os.IsNotExist(err) {
		t.Error("the worktree dir survived a clean apply")
	}
	if bout, err := ckGit(repo, "", "branch", "--list", wt.branch); err != nil || strings.TrimSpace(bout) != "" {
		t.Errorf("the branch survived a clean apply: %q err=%v", bout, err)
	}
}

// The induced-conflict case: the user commits their own change to a file the
// thread also edited. Apply must land standard conflict markers, keep the
// branch, and say all of it — never a silent overwrite.
func TestWorktree_ApplyConflictSurfacesMarkers(t *testing.T) {
	repo := threadRepo(t)

	wt, err := ckCreateWorktree(repo, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.dir, "pkg", "main.go"),
		[]byte("package pkg\n\nvar base = 2 // thread\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The user commits a conflicting change on the same line.
	if err := os.WriteFile(filepath.Join(repo, "pkg", "main.go"),
		[]byte("package pkg\n\nvar base = 3 // user\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := ckGit(repo, "", "commit", "-aqm", "user change"); err != nil {
		t.Fatalf("user commit: %v\n%s", err, out)
	}

	out := ckApplyWorktree(wt)
	if !out.conflict || !out.branchKept {
		t.Fatalf("conflict apply: %+v", out)
	}
	raw, _ := os.ReadFile(filepath.Join(repo, "pkg", "main.go"))
	if !strings.Contains(string(raw), "<<<<<<<") || !strings.Contains(string(raw), ">>>>>>>") {
		t.Errorf("no conflict markers in the user's tree: %q", raw)
	}
	if st, _ := ckGit(repo, "", "status", "--porcelain"); !strings.Contains(st, "UU pkg/main.go") {
		t.Errorf("no unmerged index entry: %q", st)
	}
	if bout, _ := ckGit(repo, "", "branch", "--list", wt.branch); strings.TrimSpace(bout) == "" {
		t.Error("the branch was deleted on a conflicted apply — the thread's exact edit is gone")
	}
	if _, err := os.Stat(wt.dir); !os.IsNotExist(err) {
		t.Error("the worktree dir survived the apply")
	}
}

// Uncommitted user changes to an overlapping file. Which way git goes here is
// platform- and version-dependent — a Linux/macOS git refuses the patch
// atomically, the Windows runner's git 3-way-merges it into conflict markers —
// so this pins the invariant that holds either way: the user's uncommitted
// work is never silently replaced, and the thread's edits stay recoverable.
func TestWorktree_ApplyNeverDiscardsDirtyUserWork(t *testing.T) {
	repo := threadRepo(t)

	wt, err := ckCreateWorktree(repo, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.dir, "pkg", "main.go"),
		[]byte("package pkg\n\nvar base = 2 // thread\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.dir, "pkg", "other.go"),
		[]byte("package pkg\n\nvar base2 = 9 // thread\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const userMark = "var base = 7 // user, uncommitted"
	userEdit := "package pkg\n\n" + userMark + "\n"
	if err := os.WriteFile(filepath.Join(repo, "pkg", "main.go"), []byte(userEdit), 0o644); err != nil {
		t.Fatal(err)
	}

	out := ckApplyWorktree(wt)
	raw, _ := os.ReadFile(filepath.Join(repo, "pkg", "main.go"))
	got := string(raw)
	t.Logf("dirty-overlap apply: refused=%v conflict=%v applied=%v", out.refused, out.conflict, out.applied)

	switch {
	case out.refused:
		if got != userEdit {
			t.Errorf("the refusal was not atomic — the user's file changed: %q", got)
		}
		other, _ := os.ReadFile(filepath.Join(repo, "pkg", "other.go"))
		if strings.Contains(string(other), "thread") {
			t.Error("the refusal was not atomic — the clean half of the patch landed anyway")
		}
		if _, err := os.Stat(wt.dir); err != nil {
			t.Error("a refused apply lost the worktree — nothing is retryable")
		}
		// Retry once the user commits: it must now land or conflict, not refuse.
		if cout, cerr := ckGit(repo, "", "commit", "-aqm", "user keeps their edit"); cerr != nil {
			t.Fatalf("commit: %v\n%s", cerr, cout)
		}
		if again := ckApplyWorktree(wt); again.refused {
			t.Fatalf("retry still refused: %+v", again)
		}
	case out.conflict:
		// Merged instead of refused: the user's line must survive inside the
		// markers, and the UI must have been told it is a conflict.
		if !strings.Contains(got, userMark) {
			t.Errorf("a conflicted apply dropped the user's uncommitted line: %q", got)
		}
		if !strings.Contains(got, "<<<<<<<") {
			t.Errorf("conflict reported with no markers in the file: %q", got)
		}
		if !out.branchKept {
			t.Error("a conflicted apply deleted the branch holding the thread's edit")
		}
	default:
		t.Fatalf("a dirty overlap applied silently — the user's edit was overwritten: %+v\n%q", out, got)
	}
	if !out.branchKept {
		t.Error("the thread's work was not kept recoverable")
	}
}

// A merely STAGED overlap (what a previous thread's own clean apply leaves
// behind) must still merge into conflict markers, not refuse: that content is
// in the object store, so the markers are recoverable and more useful than
// "save your work first" for a change the user never made by hand.
func TestWorktree_StagedOverlapStillConflicts(t *testing.T) {
	repo := threadRepo(t)

	wt, err := ckCreateWorktree(repo, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.dir, "pkg", "main.go"),
		[]byte("package pkg\n\nvar base = 5 // thread\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Stand in for an earlier thread's apply: change staged, worktree matches.
	if err := os.WriteFile(filepath.Join(repo, "pkg", "main.go"),
		[]byte("package pkg\n\nvar base = 2 // earlier thread\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, aerr := ckGit(repo, "", "add", "pkg/main.go"); aerr != nil {
		t.Fatalf("stage: %v\n%s", aerr, out)
	}
	if got := ckUnsavedOverlap(repo, []string{"pkg/main.go"}); len(got) != 0 {
		t.Fatalf("a staged-and-saved file read as unsaved: %v", got)
	}

	out := ckApplyWorktree(wt)
	if out.refused {
		t.Fatalf("a staged overlap was refused instead of merged: %+v", out)
	}
	if !out.conflict {
		t.Fatalf("a staged overlap should surface as a conflict: %+v", out)
	}
	raw, _ := os.ReadFile(filepath.Join(repo, "pkg", "main.go"))
	if !strings.Contains(string(raw), "<<<<<<<") {
		t.Errorf("no markers left in the tree: %q", raw)
	}
}

// The conflict verdict must come from the index, not from git's prose: under a
// translated locale the message would not match, and a conflicted apply would
// then be reported as a failure while the markers were already in the tree.
func TestWorktree_ApplyConflictSurvivesATranslatedLocale(t *testing.T) {
	repo := threadRepo(t)
	// Ask git for French messages; ckGit pins LC_ALL=C, and the verdict is read
	// off the index either way.
	t.Setenv("LC_ALL", "fr_FR.UTF-8")
	t.Setenv("LANGUAGE", "fr")

	wt, err := ckCreateWorktree(repo, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.dir, "pkg", "main.go"),
		[]byte("package pkg\n\nvar base = 2 // thread\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "pkg", "main.go"),
		[]byte("package pkg\n\nvar base = 3 // user\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, cerr := ckGit(repo, "", "commit", "-aqm", "user change"); cerr != nil {
		t.Fatalf("user commit: %v\n%s", cerr, out)
	}

	out := ckApplyWorktree(wt)
	if !out.conflict || !out.branchKept {
		t.Fatalf("conflict verdict lost under a translated locale: %+v", out)
	}
	if got := ckUnmergedPaths(repo); len(got) != 1 || got[0] != "pkg/main.go" {
		t.Errorf("unmerged paths = %v", got)
	}
}

// A tree already holding someone else's unmerged entries must not make a
// blocked apply read as "applied with conflicts" — only paths this apply left
// unmerged count as evidence about this apply.
func TestWorktree_PreExistingConflictIsNotClaimedAsOurs(t *testing.T) {
	repo := threadRepo(t)

	// Leave pkg/other.go unmerged from an unrelated merge, before any thread runs.
	if out, err := ckGit(repo, "", "checkout", "-q", "-b", "sideline"); err != nil {
		t.Fatalf("branch: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(repo, "pkg", "other.go"), []byte("package pkg\n\nvar side = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := ckGit(repo, "", "commit", "-aqm", "sideline"); err != nil {
		t.Fatalf("commit: %v\n%s", err, out)
	}
	if out, err := ckGit(repo, "", "checkout", "-q", "-"); err != nil {
		t.Fatalf("back: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(repo, "pkg", "other.go"), []byte("package pkg\n\nvar side = 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := ckGit(repo, "", "commit", "-aqm", "main-side"); err != nil {
		t.Fatalf("commit: %v\n%s", err, out)
	}
	if out, _ := ckGit(repo, "", "merge", "sideline"); !strings.Contains(out, "CONFLICT") {
		t.Fatalf("could not induce a pre-existing conflict: %s", out)
	}
	if got := ckUnmergedPaths(repo); len(got) == 0 {
		t.Fatal("no pre-existing unmerged entry to test against")
	}

	// A thread edits a DIFFERENT file; the tree's own merge conflict is in the
	// way of nothing, but the dirty index blocks the apply.
	wt, err := ckCreateWorktree(repo, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.dir, "pkg", "main.go"),
		[]byte("package pkg\n\nvar base = 2 // thread\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := ckApplyWorktree(wt)
	if out.conflict {
		t.Errorf("the tree's own unmerged entry was claimed as this apply's conflict: %+v", out)
	}
	if !out.refused && !out.applied {
		t.Errorf("neither applied nor refused: %+v", out)
	}
}

func TestWorktree_EmptyApplyCleansUp(t *testing.T) {
	repo := threadRepo(t)
	wt, err := ckCreateWorktree(repo, 2)
	if err != nil {
		t.Fatal(err)
	}
	out := ckApplyWorktree(wt)
	if !out.empty || out.err != nil {
		t.Fatalf("empty apply: %+v", out)
	}
	if _, err := os.Stat(wt.dir); !os.IsNotExist(err) {
		t.Error("an empty worktree was not removed")
	}
}

func TestWorktree_DiscardRemovesDirAndBranch(t *testing.T) {
	repo := threadRepo(t)
	wt, err := ckCreateWorktree(repo, 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.dir, "pkg", "main.go"), []byte("package pkg // dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ckDiscardWorktree(wt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wt.dir); !os.IsNotExist(err) {
		t.Error("discard left the worktree dir")
	}
	if bout, _ := ckGit(repo, "", "branch", "--list", wt.branch); strings.TrimSpace(bout) != "" {
		t.Errorf("discard left the branch: %q", bout)
	}
	raw, _ := os.ReadFile(filepath.Join(repo, "pkg", "main.go"))
	if strings.Contains(string(raw), "dirty") {
		t.Error("discard touched the user's tree")
	}
}

// Stale worktrees from a crashed session are listed and reported, never
// auto-deleted — they may hold un-applied work.
func TestStaleWorktrees_ListedNeverDeleted(t *testing.T) {
	testutil.NewSandbox(t)
	stale := filepath.Join(ckWorktreeBase(), "t9-dead99")
	if err := os.MkdirAll(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	got := ckStaleWorktrees()
	if len(got) != 1 || got[0] != "t9-dead99" {
		t.Fatalf("stale scan = %v", got)
	}
	if _, err := os.Stat(stale); err != nil {
		t.Error("the scan deleted a stale worktree")
	}
}

// A failed worktree creation fails the task honestly and releases the hold —
// it must never fall back to editing the user's tree.
func TestWorktreeReady_ErrorFailsTaskAndReleasesQueue(t *testing.T) {
	testutil.NewSandbox(t)
	m := testCockpit()
	th := m.th()
	ex := &ckExecState{ctx: context.Background()}
	th.exec = ex
	th.files = []string{"pkg/main.go"}

	next, _ := m.Update(ckWtReadyMsg{exec: ex, task: ckTask{threadID: th.id, rel: "pkg/main.go", runID: "r1"},
		err: os.ErrPermission})
	m = next.(Cockpit)
	if m.th().exec != nil {
		t.Error("the failed creation left the thread running")
	}
	if m.th().wt != nil {
		t.Error("a worktree was attached despite the failure")
	}
	if len(m.th().files) != 0 {
		t.Errorf("the hold was not released: %v", m.th().files)
	}
	logs := stripANSI(strings.Join(m.th().log, "\n"))
	if !strings.Contains(logs, "worktree creation failed") {
		t.Errorf("the failure is not visible: %q", logs)
	}
}

// A superseded or cancelled creation discards the worktree it cut — nothing
// may litter ~/.hydra/worktrees.
func TestWorktreeReady_SupersededCreationIsDiscarded(t *testing.T) {
	repo := threadRepo(t)
	wt, err := ckCreateWorktree(repo, 1)
	if err != nil {
		t.Fatal(err)
	}
	m := testCockpit()
	// The thread's exec moved on: the arriving worktree belongs to nobody.
	m.th().exec = &ckExecState{ctx: context.Background()}
	next, _ := m.Update(ckWtReadyMsg{exec: &ckExecState{}, task: ckTask{threadID: 1}, wt: wt})
	m = next.(Cockpit)
	if m.th().wt != nil {
		t.Error("a superseded worktree was attached")
	}
	if _, err := os.Stat(wt.dir); !os.IsNotExist(err) {
		t.Error("the superseded worktree was left on disk")
	}
}

// The full UI path: an edit task lands in the worktree, the user's tree stays
// untouched until `a`, and apply stages it. Drives startTask → ckWtCreate →
// worker → settle → resultKey('a') with real git and a stubbed editor.
func TestThreadEdit_IsolatesThenAppliesThroughTheUI(t *testing.T) {
	repo := threadRepo(t)
	stubExec(t)
	ckEditStage = func(_ context.Context, tk *ckTask, _ string) (*editor.Result, error) {
		before, _ := os.ReadFile(tk.file)
		after := "package pkg\n\nvar base = 42 // thread edit\n"
		if err := os.WriteFile(tk.file, []byte(after), 0o644); err != nil {
			return nil, err
		}
		runlog.LogEdit(tk.runID, tk.taskID, tk.file, before, []byte(after), 1, 1)
		return &editor.Result{Status: "ok", File: tk.file, LinesAdded: 1, LinesRemoved: 1, Head: "stub-editor"}, nil
	}

	m := chatFixture("edit")
	m.repoRoot = repo
	m2, cmd := m.startTask("edit pkg/main.go please")
	m = runCmds(t, m2, cmd)

	th := m.th()
	if th.wt == nil {
		t.Fatal("the edit thread got no worktree")
	}
	if th.lastDone == nil || !th.lastDone.edited {
		t.Fatalf("the task did not settle as an edit: %+v", th.lastDone)
	}
	if !strings.HasPrefix(th.lastDone.file, th.wt.dir) {
		t.Errorf("the edit targeted %s, not the worktree", th.lastDone.file)
	}
	userCopy, _ := os.ReadFile(filepath.Join(repo, "pkg", "main.go"))
	if strings.Contains(string(userCopy), "thread edit") {
		t.Fatal("the user's tree changed before apply")
	}
	logs := stripANSI(strings.Join(th.log, "\n"))
	if !strings.Contains(logs, "worktree") || !strings.Contains(logs, "merges on apply") {
		t.Errorf("isolation is not disclosed: %q", logs)
	}

	m, acmd := keyRune(m, 'a')
	m = runCmds(t, m, acmd)
	if m.th().wt != nil {
		t.Error("apply left the worktree attached")
	}
	userCopy, _ = os.ReadFile(filepath.Join(repo, "pkg", "main.go"))
	if !strings.Contains(string(userCopy), "thread edit") {
		t.Errorf("apply did not land the edit: %q", userCopy)
	}
	logs = stripANSI(strings.Join(m.th().log, "\n"))
	if !strings.Contains(logs, "✓ applied") {
		t.Errorf("apply is not disclosed: %q", logs)
	}
}

// x x discards the worktree after a landed edit: tree untouched, worktree and
// branch gone, and the first x only arms.
func TestThreadEdit_DoubleXDiscards(t *testing.T) {
	repo := threadRepo(t)
	stubExec(t)
	ckEditStage = func(_ context.Context, tk *ckTask, _ string) (*editor.Result, error) {
		if err := os.WriteFile(tk.file, []byte("package pkg // discarded\n"), 0o644); err != nil {
			return nil, err
		}
		runlog.LogEdit(tk.runID, tk.taskID, tk.file, []byte("old"), []byte("new"), 1, 0)
		return &editor.Result{Status: "ok", File: tk.file, LinesAdded: 1, Head: "stub"}, nil
	}
	m := chatFixture("edit")
	m.repoRoot = repo
	m2, cmd := m.startTask("edit pkg/other.go now")
	m = runCmds(t, m2, cmd)
	wt := m.th().wt
	if wt == nil {
		t.Fatal("no worktree")
	}

	m, _ = keyRune(m, 'x')
	if m.th().wt == nil {
		t.Fatal("a single x already discarded — it must only arm")
	}
	if !strings.Contains(m.flash, "x again discards") {
		t.Errorf("the arm is silent: %q", m.flash)
	}
	m, dcmd := keyRune(m, 'x')
	m = runCmds(t, m, dcmd)
	if m.th().wt != nil {
		t.Fatal("the second x did not discard")
	}
	if _, err := os.Stat(wt.dir); !os.IsNotExist(err) {
		t.Error("discard left the worktree dir")
	}
	raw, _ := os.ReadFile(filepath.Join(repo, "pkg", "other.go"))
	if strings.Contains(string(raw), "discarded") {
		t.Error("discard let the edit reach the user's tree")
	}
}

// The induced-conflict path through the UI: the user's tree moves after the
// thread edited the same file; `a` surfaces the conflict, and the log says
// where the markers and the branch are.
func TestThreadEdit_ApplyConflictThroughTheUI(t *testing.T) {
	repo := threadRepo(t)
	stubExec(t)
	ckEditStage = func(_ context.Context, tk *ckTask, _ string) (*editor.Result, error) {
		if err := os.WriteFile(tk.file, []byte("package pkg\n\nvar base = 2 // thread\n"), 0o644); err != nil {
			return nil, err
		}
		runlog.LogEdit(tk.runID, tk.taskID, tk.file, []byte("a"), []byte("b"), 1, 1)
		return &editor.Result{Status: "ok", File: tk.file, LinesAdded: 1, LinesRemoved: 1, Head: "stub"}, nil
	}
	m := chatFixture("edit")
	m.repoRoot = repo
	m2, cmd := m.startTask("edit pkg/main.go here")
	m = runCmds(t, m2, cmd)
	branch := m.th().wt.branch

	// User commits a divergent change before pressing a.
	if err := os.WriteFile(filepath.Join(repo, "pkg", "main.go"),
		[]byte("package pkg\n\nvar base = 3 // user\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := ckGit(repo, "", "commit", "-aqm", "user"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}

	m, acmd := keyRune(m, 'a')
	m = runCmds(t, m, acmd)
	logs := stripANSI(strings.Join(m.th().log, "\n"))
	if !strings.Contains(logs, "applied with conflicts") || !strings.Contains(logs, branch) {
		t.Errorf("the conflict is not fully disclosed: %q", logs)
	}
	raw, _ := os.ReadFile(filepath.Join(repo, "pkg", "main.go"))
	if !strings.Contains(string(raw), "<<<<<<<") {
		t.Errorf("no markers in the user's tree: %q", raw)
	}
	if bout, _ := ckGit(repo, "", "branch", "--list", branch); strings.TrimSpace(bout) == "" {
		t.Error("the branch was deleted despite the conflict")
	}
}
