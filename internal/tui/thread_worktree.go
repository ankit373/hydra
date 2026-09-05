// SPDX-License-Identifier: MIT

package tui

// thread_worktree.go, edit-thread isolation (#598): each edit-capable thread
// gets its own git worktree of the user's repo, and the a / x x keys merge it
// back with conflicts surfaced (git apply --3way) or discard it.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/util"
)

// ckWorktree is one thread's isolated checkout.
type ckWorktree struct {
	tag    string // "t3-9f2c1a", dir basename, branch suffix, and the UI label
	dir    string // <hydra home>/worktrees/<tag>
	branch string // hydra/task-<tag>
	repo   string // the user's repo root it was cut from
	base   string // commit the worktree was created at, the diff's left side
}

// ckWorktreeBase is where thread worktrees live: $HYDRA_HOME/worktrees, i.e.
// ~/.hydra/worktrees on a default install.
func ckWorktreeBase() string { return filepath.Join(config.Dir(), "worktrees") }

// ckGitTimeout bounds every worktree git command so a wedged lock cannot hang
// the UI (apply/discard run synchronously, they are index-local and fast).
const ckGitTimeout = 30 * time.Second

// ckGit runs one git command and returns its combined output, bounded. The
// messages below are read back, so the locale is pinned: a translated git
// would otherwise turn a conflicted apply into an unrecognized failure.
func ckGit(dir, stdin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), ckGitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANGUAGE=")
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out := util.NewAccumulator(1 << 22)
	cmd.Stdout, cmd.Stderr = out, out
	err := cmd.Run()
	return out.String(), err
}

// ckNewWorktreeTag is unique across sessions so a stale worktree from a crash
// can never collide with a new thread's.
func ckNewWorktreeTag(threadID int) string {
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("t%d-%d", threadID, time.Now().UnixNano()%0xffffff)
	}
	return fmt.Sprintf("t%d-%s", threadID, hex.EncodeToString(b[:]))
}

// ckCreateWorktree cuts a worktree of repo at HEAD on its own branch.
func ckCreateWorktree(repo string, threadID int) (*ckWorktree, error) {
	if err := os.MkdirAll(ckWorktreeBase(), 0o700); err != nil {
		return nil, err
	}
	tag := ckNewWorktreeTag(threadID)
	wt := &ckWorktree{
		tag: tag, dir: filepath.Join(ckWorktreeBase(), tag),
		branch: "hydra/task-" + tag, repo: repo,
	}
	if out, err := ckGit(repo, "", "worktree", "add", wt.dir, "-b", wt.branch); err != nil {
		return nil, fmt.Errorf("git worktree add: %s", ckFirstLine(out))
	}
	base, err := ckGit(wt.dir, "", "rev-parse", "HEAD")
	if err != nil {
		_ = ckDiscardWorktree(wt)
		return nil, fmt.Errorf("git rev-parse: %s", ckFirstLine(base))
	}
	wt.base = strings.TrimSpace(base)
	return wt, nil
}

// ckApplyOut is one apply attempt's honest outcome.
type ckApplyOut struct {
	applied    bool     // the diff landed in the user's tree (staged)
	conflict   bool     // landed with conflict markers (committed divergence)
	refused    bool     // something blocked it (usually a dirty overlap); nothing applied
	empty      bool     // the worktree held no changes
	files      []string // repo-relative paths the diff touches
	detail     string   // first git line explaining a refusal or failure
	branchKept bool     // the branch survives (conflict/refusal recovery)
	err        error    // git itself failed, not a merge outcome
}

// ckApplyWorktree commits the worktree and 3-way-applies its diff onto the
// user's tree. Atomic on refusal (dirty overlap: nothing lands), standard
// conflict markers on committed divergence, never a silent overwrite.
func ckApplyWorktree(wt *ckWorktree) ckApplyOut {
	if out, err := ckGit(wt.dir, "", "add", "-A"); err != nil {
		return ckApplyOut{err: fmt.Errorf("git add: %s", ckFirstLine(out))}
	}
	if st, err := ckGit(wt.dir, "", "status", "--porcelain"); err != nil {
		return ckApplyOut{err: fmt.Errorf("git status: %s", ckFirstLine(st))}
	} else if strings.TrimSpace(st) != "" {
		if out, cerr := ckGit(wt.dir, "", "commit", "-m", "hydra: "+wt.tag); cerr != nil {
			return ckApplyOut{err: fmt.Errorf("git commit: %s", ckFirstLine(out))}
		}
	}
	names, err := ckGit(wt.repo, "", "diff", "--name-only", wt.base, wt.branch)
	if err != nil {
		return ckApplyOut{err: fmt.Errorf("git diff: %s", ckFirstLine(names))}
	}
	res := ckApplyOut{files: ckSplitLines(names)}
	if len(res.files) == 0 {
		res.empty = true
		res.err = ckCleanupWorktree(wt, false)
		return res
	}
	// Refuse before touching anything when the user has UNSAVED edits to a path
	// this patch touches. Whether git itself refuses or 3-way-merges there is
	// platform- and version-dependent, and merging into work that exists in no
	// object is not a gamble to take on the user's behalf.
	if dirty := ckUnsavedOverlap(wt.repo, res.files); len(dirty) > 0 {
		res.refused, res.branchKept = true, true
		res.detail = "unsaved changes in " + truncate(strings.Join(dirty, ", "), 60)
		return res
	}
	patch, err := ckGit(wt.repo, "", "diff", "--binary", "--full-index", wt.base, wt.branch)
	if err != nil {
		return ckApplyOut{err: fmt.Errorf("git diff: %s", ckFirstLine(patch))}
	}
	// A tree already mid-merge has unmerged entries of its own; only paths THIS
	// apply left unmerged are evidence about THIS apply.
	before := ckUnmergedPaths(wt.repo)
	out, aerr := ckGit(wt.repo, patch, "apply", "--3way")
	switch {
	case aerr == nil:
		res.applied = true
		res.err = ckCleanupWorktree(wt, false)
	case len(ckNewPaths(before, ckUnmergedPaths(wt.repo))) > 0:
		// Standard-git conflict state, read off the index rather than trusted
		// from a message: markers in the files, unmerged entries staged.
		res.applied, res.conflict, res.branchKept = true, true, true
		res.err = ckCleanupWorktree(wt, true)
	default:
		// --3way is atomic, so with no new unmerged entries nothing landed at
		// all. Everything is kept for a retry once the blocker is cleared.
		res.refused, res.branchKept = true, true
		res.detail = ckFirstLine(out)
	}
	return res
}

// ckUnsavedOverlap lists which of files the tree has unsaved (worktree-vs-index)
// changes to. A merely staged change is safe to merge over, its content is in
// the object store, but an unsaved edit exists nowhere else.
func ckUnsavedOverlap(repo string, files []string) []string {
	out, err := ckGit(repo, "", append([]string{"status", "--porcelain", "-z", "--"}, files...)...)
	if err != nil {
		return nil // status failed: leave the decision to git's own apply
	}
	var dirty []string
	for _, rec := range strings.Split(out, "\x00") {
		// "XY path": Y is the worktree-vs-index column; ' ' means saved.
		if len(rec) > 3 && rec[1] != ' ' && rec[1] != '?' {
			dirty = append(dirty, rec[3:])
		}
	}
	return dirty
}

// ckUnmergedPaths lists paths git left in a conflicted (unmerged) index state.
func ckUnmergedPaths(repo string) []string {
	out, err := ckGit(repo, "", "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil
	}
	return ckSplitLines(out)
}

// ckNewPaths returns the entries of after that were not already in before.
func ckNewPaths(before, after []string) []string {
	had := make(map[string]bool, len(before))
	for _, p := range before {
		had[p] = true
	}
	var out []string
	for _, p := range after {
		if !had[p] {
			out = append(out, p)
		}
	}
	return out
}

// ckCleanupWorktree removes the worktree checkout, and its branch too unless
// keepBranch (a conflicted apply keeps it so nothing is lost while resolving).
func ckCleanupWorktree(wt *ckWorktree, keepBranch bool) error {
	if out, err := ckGit(wt.repo, "", "worktree", "remove", "--force", wt.dir); err != nil {
		return fmt.Errorf("git worktree remove: %s", ckFirstLine(out))
	}
	if keepBranch {
		return nil
	}
	if out, err := ckGit(wt.repo, "", "branch", "-D", wt.branch); err != nil {
		return fmt.Errorf("git branch -D: %s", ckFirstLine(out))
	}
	return nil
}

// ckDiscardWorktree drops the worktree and its branch, the thread's un-applied
// edits are gone, which is exactly what discard means.
func ckDiscardWorktree(wt *ckWorktree) error { return ckCleanupWorktree(wt, false) }

// ckStaleWorktrees lists leftover worktree dirs from a previous session.
// Listed and reported, never auto-deleted: they may hold un-applied work.
func ckStaleWorktrees() []string {
	entries, err := os.ReadDir(ckWorktreeBase())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// ckSplitLines splits command output into trimmed non-empty lines.
func ckSplitLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// ── apply / discard (the a and x x keys) ─────────────────────────────────────

// discardKey is the two-press discard: destructive, so the first x only arms.
func (m Cockpit) discardKey(th *ckThread) (Cockpit, tea.Cmd, bool) {
	if !th.discardArm {
		th.discardArm = true
		m.flash = fmt.Sprintf("x again discards worktree %s, its edits are lost", th.wt.tag)
		return m, nil, true
	}
	th.discardArm = false
	tag := th.wt.tag
	if err := ckDiscardWorktree(th.wt); err != nil {
		th.log = append(th.log, ckExpS.Render("  ✗ discard failed ")+ckDimS.Render(err.Error()))
		return m, nil, true
	}
	th.wt = nil
	th.files = nil
	th.log = append(th.log, ckDimS.Render(fmt.Sprintf("  ⎇ worktree %s discarded, your tree was never touched", tag)))
	nm, cmd := m.releaseThreads(th)
	return nm, cmd, true
}

// applyThread merges the thread's worktree back into the user's tree with
// conflicts surfaced (thread_worktree.go has the mechanism), then releases
// whatever queued behind it.
func (m Cockpit) applyThread(th *ckThread) (Cockpit, tea.Cmd, bool) {
	th.log = append(th.log, m.concurrentWarnings(th)...)
	wt := th.wt
	out := ckApplyWorktree(wt)
	switch {
	case out.err != nil && !out.applied && !out.empty:
		th.log = append(th.log, ckExpS.Render("  ✗ apply failed ")+ckDimS.Render(ckSafe(out.err.Error())))
		return m, nil, true
	case out.refused:
		// Either this code refused up front (unsaved overlap) or git's atomic
		// apply declined; both mean nothing in the user's tree changed.
		th.log = append(th.log,
			ckExpS.Render("  ✗ apply blocked ")+ckDimS.Render("· nothing in your tree was changed"),
			ckDimS.Render("    "+truncate(ckSafe(out.detail), 80)),
			ckFaintS.Render("    save or stash the overlapping change, then press a again · x x discards"))
		return m, nil, true
	case out.empty:
		th.log = append(th.log, ckDimS.Render("  ⎇ nothing to apply, worktree removed"))
	case out.conflict:
		th.log = append(th.log,
			ckMidS.Render("  ⚠ applied with conflicts ")+ckDimS.Render("· markers left in "+truncate(strings.Join(out.files, ", "), 60)),
			ckDimS.Render("    resolve the <<<<<<< markers, then `git add` · `git diff` shows the conflict"),
			ckDimS.Render("    branch "+wt.branch+" kept, `git branch -D` it once resolved"))
	default:
		th.log = append(th.log, ckCheapS.Render("  ✓ applied ")+
			ckDimS.Render(fmt.Sprintf("%d file%s staged onto %s · worktree and branch removed",
				len(out.files), plural(len(out.files)), truncate(wt.repo, 40))))
	}
	if out.err != nil { // cleanup hiccup after a landed apply, disclose it
		th.log = append(th.log, ckMidS.Render("  ⚠ cleanup: ")+ckDimS.Render(ckSafe(out.err.Error())))
	}
	th.wt = nil
	th.files = nil
	th.clock = th.clock.Tick(ckThreadAgent(th.id))
	nm, cmd := m.releaseThreads(th)
	return nm, cmd, true
}

// ── async creation ────────────────────────────────────────────────────────────

// ckWtReadyMsg lands when a thread's worktree finished being cut; the pending
// task rides along so Update can launch the pipeline against it.
type ckWtReadyMsg struct {
	exec *ckExecState
	task ckTask
	wt   *ckWorktree
	err  error
}

// ckWtCreate cuts the worktree off the UI loop, `git worktree add` checks out
// the whole tree, which is slow on big repos.
func ckWtCreate(ex *ckExecState, t ckTask, repo string) tea.Cmd {
	return func() tea.Msg {
		wt, err := ckCreateWorktree(repo, t.threadID)
		return ckWtReadyMsg{exec: ex, task: t, wt: wt, err: err}
	}
}
