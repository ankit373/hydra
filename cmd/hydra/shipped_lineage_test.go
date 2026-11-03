// SPDX-License-Identifier: MIT

//go:build !windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// carryShapedRepo builds the history a carry release really has: a release
// branch holding the feature commits, and a main whose release commit takes
// that tree but main's parent, so the feature history is not an ancestor of the
// tag. Returns a clone with the fixture as its origin, which is where the
// script looks for release branches.
func carryShapedRepo(t *testing.T) string {
	t.Helper()
	origin := filepath.Join(t.TempDir(), "origin")
	run := func(dir string, args ...string) string {
		t.Helper()
		// The fixture must not inherit the developer's signing or template
		// config, or it builds a different history on every machine.
		cmd := exec.Command("git", append([]string{
			"-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false",
			"-c", "tag.forceSignAnnotated=false", "-c", "commit.template=",
		}, args...)...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(subject string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(origin, "f"), []byte(subject), 0o644); err != nil {
			t.Fatal(err)
		}
		run(origin, "add", "f")
		run(origin, "commit", "-m", subject)
	}

	if err := os.MkdirAll(origin, 0o755); err != nil {
		t.Fatal(err)
	}
	run(origin, "init", "-q", "-b", "main")
	commit("chore: root (#1)")

	// The previous release, and the branch that bounds this one.
	run(origin, "checkout", "-q", "-b", "release/v0.9.0")
	commit("fix: shipped last time (#10)")
	run(origin, "checkout", "-q", "main")

	// This release's features, on its own branch and nowhere else.
	run(origin, "checkout", "-q", "-b", "release/v1.0.0", "release/v0.9.0")
	commit("feat: carried feature (#20)")
	commit("fix: another carried one (#21)")

	// The carry: main takes the release branch's tree, keeping main's parent, so
	// the two commits above are not ancestors of the tag.
	run(origin, "checkout", "-q", "main")
	tree := run(origin, "rev-parse", "release/v1.0.0^{tree}")
	head := run(origin, "rev-parse", "HEAD")
	carry := run(origin, "commit-tree", tree, "-p", head, "-m", "chore(release): v1.0.0 (#99)")
	run(origin, "reset", "-q", "--hard", carry)
	// A lightweight tag through update-ref, so no local tag config applies.
	run(origin, "update-ref", "refs/tags/v1.0.0", carry)

	clone := filepath.Join(t.TempDir(), "clone")
	run(t.TempDir(), "clone", "-q", origin, clone)
	return clone
}

func shippedNumbers(t *testing.T, repo string) []string {
	t.Helper()
	script, err := filepath.Abs(filepath.Join("..", "..", ".github", "workflows", "shipped_lineage.sh"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(script, "v1.0.0")
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("shipped_lineage.sh: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(filepath.Join(repo, "shipped_prs.txt"))
	if err != nil {
		t.Fatalf("the script wrote no shipped_prs.txt:\n%s", out)
	}
	return strings.Fields(string(raw))
}

// A carry PR's tree is the release branch's and its parent is main's tip, so
// the feature history is not an ancestor of the tag and walking main finds none
// of it. v1.4.0 and v1.5.0 both shipped that way and closed zero issues between
// them, stranding 89 (#1140).
func TestShippedLineage_FindsWhatACarryReleaseDiscarded(t *testing.T) {
	repo := carryShapedRepo(t)

	// The premise: without the release branch there is nothing to find.
	cmd := exec.Command("git", "log", "--format=%s", "v1.0.0")
	cmd.Dir = repo
	out, _ := cmd.CombinedOutput()
	for _, gone := range []string{"(#20)", "(#21)"} {
		if strings.Contains(string(out), gone) {
			t.Fatalf("the fixture is not carry-shaped: %s is on the tag's own lineage", gone)
		}
	}

	got := strings.Join(shippedNumbers(t, repo), " ")
	for _, want := range []string{"20", "21"} {
		if !strings.Contains(" "+got+" ", " "+want+" ") {
			t.Errorf("#%s shipped in this release and the sweep cannot see it: got %q", want, got)
		}
	}
}

// Unbounded, the release branch reaches back through every earlier release, and
// their still-open issues would be closed as "Shipped in v1.0.0", which is a
// false statement about work that shipped months before. Measured on the real
// repository: 455 numbers unbounded against 159 since the previous release.
func TestShippedLineage_StopsAtThePreviousRelease(t *testing.T) {
	repo := carryShapedRepo(t)

	got := strings.Join(shippedNumbers(t, repo), " ")
	if strings.Contains(" "+got+" ", " 10 ") {
		t.Errorf("#10 shipped in the previous release and this sweep claims it: got %q", got)
	}
}
