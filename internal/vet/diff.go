// SPDX-License-Identifier: MIT

package vet

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/ankit373/hydra/internal/util"
)

// emptyTree is git's hash of the empty tree. A root commit has no "^" to
// resolve, and without this its diff would come back empty rather than whole.
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// nullPath is git's own spelling of "nothing on this side", special-cased by
// diff on every platform, unlike os.DevNull which is NUL on Windows.
const nullPath = "/dev/null"

// Diff returns one file's unified diff, in whichever mode the spec resolved.
func (s *Spec) Diff(ctx context.Context, path string) (string, error) {
	switch s.Mode {
	case "commit":
		base := s.parentOf(ctx, s.Commit)
		return s.git(ctx, false, "diff", base, s.Commit, "--", path)

	case "range":
		// The merge base, not From: a range review asks what this branch added,
		// not what has happened on the base branch since it forked.
		base := firstNonEmpty(s.MergeBase, s.From)
		return s.git(ctx, false, "diff", base, firstNonEmpty(s.To, "HEAD"), "--", path)

	default:
		return s.workspaceDiff(ctx, path)
	}
}

// workspaceDiff covers staged, unstaged and untracked together, which is what
// the workspace mode means.
func (s *Spec) workspaceDiff(ctx context.Context, path string) (string, error) {
	out, err := s.git(ctx, false, "diff", "HEAD", "--", path)
	if err == nil && strings.TrimSpace(out) != "" {
		return out, nil
	}
	// An untracked file has no HEAD side, so git reports nothing at all rather
	// than a new file. --no-index exits 1 when the two differ, which is the
	// expected answer here, not a failure.
	return s.git(ctx, true, "diff", "--no-index", "--", nullPath, path)
}

// parentOf resolves rev^, falling back to the empty tree for a root commit.
func (s *Spec) parentOf(ctx context.Context, rev string) string {
	out, err := s.git(ctx, true, "rev-parse", "--verify", "--quiet", rev+"^")
	if err != nil || strings.TrimSpace(out) == "" {
		return emptyTree
	}
	return strings.TrimSpace(out)
}

func (s *Spec) git(ctx context.Context, tolerateDiffFound bool, args ...string) (string, error) {
	repo := firstNonEmpty(s.Repository, ".")
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
	stdout := util.NewAccumulator(util.DefaultMaxBytes)
	stderr := util.NewAccumulator(64 << 10)
	cmd.Stdout, cmd.Stderr = stdout, stderr

	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !tolerateDiffFound || !errors.As(err, &ee) || ee.ExitCode() != 1 {
			return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
		}
	}
	// A truncated diff is a different diff, and a reviewer reading one would
	// report on code the author never wrote.
	if stdout.Truncated() {
		return "", fmt.Errorf("git %s: diff exceeded %d bytes", args[0], util.DefaultMaxBytes)
	}
	return stdout.String(), nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
