// SPDX-License-Identifier: MIT

//go:build !windows

package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/testutil"
)

// Scope is judged on where a path points, not how it is spelled. Tagged rather
// than skipped: creating a symlink on Windows needs a privilege a test runner
// does not normally hold, and a skip costs the suite's budget (#1019).

// linkRegistry builds a one-workspace registry whose root is reached through a
// symlink, which is how an operator with ~/code -> /Volumes/… writes one.
func linkRegistry(t *testing.T, denied []string) (r *Registry, link, real string) {
	t.Helper()
	real = filepath.Join(t.TempDir(), "real")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link = filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	return &Registry{
		workspaces: []Workspace{{
			Name: "ws", Root: link, realRoot: resolveScopePath(link), Git: "false",
			AllowedGlobs: []string{"**"}, DeniedGlobs: denied,
		}},
		validators: map[string]string{},
	}, link, real
}

func TestCheck_ASymlinkedDirectoryCannotWriteOutsideTheWorkspace(t *testing.T) {
	testutil.NewSandbox(t)

	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	r := &Registry{workspaces: []Workspace{{
		Name: "ws", Root: root, realRoot: resolveScopePath(root), Git: "false",
		AllowedGlobs: []string{"**"},
	}}}

	// filepath.Rel reads this as "escape/secret.go", which is not "..", so a
	// lexical check admitted it and the edit landed outside every workspace.
	_, err := r.Check(filepath.Join(root, "escape", "secret.go"))
	if err == nil {
		t.Fatal("a symlink inside the workspace wrote outside it")
	}
	if !strings.Contains(err.Error(), "resolves to") {
		t.Errorf("the refusal does not say where the path pointed: %v", err)
	}
}

func TestCheck_ASymlinkToADeniedFileIsDeniedByTheSameGlob(t *testing.T) {
	testutil.NewSandbox(t)

	root := t.TempDir()
	env := filepath.Join(root, ".env")
	if err := os.WriteFile(env, []byte("SECRET_TOKEN=abc123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(env, filepath.Join(root, "cfg")); err != nil {
		t.Fatal(err)
	}
	r := &Registry{workspaces: []Workspace{{
		Name: "ws", Root: root, realRoot: resolveScopePath(root), Git: "false",
		AllowedGlobs: []string{"**"}, DeniedGlobs: defaultDeniedGlobs,
	}}}

	if _, err := r.Check(env); err == nil {
		t.Fatal("the denied file was writable under its own name")
	}
	_, err := r.Check(filepath.Join(root, "cfg"))
	if err == nil {
		t.Fatal("a symlink walked past the deny list the file it points at is on")
	}
	// The refusal must be the deny rule, not "no workspace contains": a second
	// mechanism reaching the same outcome would let this pass under the bug.
	if !strings.Contains(err.Error(), "**/.env*") {
		t.Errorf("refused for the wrong reason, want the .env deny glob: %v", err)
	}
}

func TestCheck_ARootAndAFileSpelledDifferentlyAreTheSameWorkspace(t *testing.T) {
	testutil.NewSandbox(t)

	r, link, real := linkRegistry(t, nil)

	// The spelling an operator wrote, and the one an editor or realpath hands
	// over. Same file either way.
	for _, p := range []string{
		filepath.Join(link, "main.go"),
		filepath.Join(real, "main.go"),
	} {
		if ws, err := r.Check(p); err != nil || ws != "ws" {
			t.Errorf("%s was refused by its own workspace: ws=%q err=%v", p, ws, err)
		}
	}
}

func TestCheck_AFileNotCreatedYetIsScopedByTheDirectoryItLandsIn(t *testing.T) {
	testutil.NewSandbox(t)

	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	r := &Registry{workspaces: []Workspace{{
		Name: "ws", Root: root, realRoot: resolveScopePath(root), Git: "false",
		AllowedGlobs: []string{"**"},
	}}}

	// hyctl edit creates files, so resolution has to work on a path with no
	// file at the end of it, and still refuse one landing outside.
	if _, err := r.Check(filepath.Join(root, "pkg", "new.go")); err != nil {
		t.Errorf("a file about to be created inside the workspace was refused: %v", err)
	}
	if _, err := r.Check(filepath.Join(root, "escape", "new.go")); err == nil {
		t.Error("a file about to be created outside the workspace was allowed")
	}
}

func TestCheckRooted_ASymlinkDoesNotEscapeTheWorktreeRoot(t *testing.T) {
	testutil.NewSandbox(t)

	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckRooted(root, filepath.Join(root, "escape", "x.go")); err == nil {
		t.Error("the worktree escape hatch let a symlink out of its root")
	}
	if ws, err := CheckRooted(root, filepath.Join(root, "pkg", "x.go")); err != nil || ws == "" {
		t.Errorf("a real file under the worktree root was refused: ws=%q err=%v", ws, err)
	}
}

func TestResolve_ReportsTheGitRootThePathActuallyLandsIn(t *testing.T) {
	testutil.NewSandbox(t)

	r, link, real := linkRegistry(t, nil)
	if err := os.MkdirAll(filepath.Join(real, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	r.workspaces[0].Git = "auto"

	got, err := r.Resolve(filepath.Join(link, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	if got.GitRoot != want {
		t.Errorf("git root %q, want the resolved repository %q", got.GitRoot, want)
	}
}
