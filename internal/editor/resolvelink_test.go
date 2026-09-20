// SPDX-License-Identifier: MIT

package editor

import (
	"os"
	"path/filepath"
	"testing"
)

// ResolveLink must move a path only when the path is a symlink. Resolving
// unconditionally canonicalises every edit's path instead: Windows expands an
// 8.3 temp directory, macOS turns /var into /private/var, and the recorded path
// stops matching the one the caller passed, which is the key the run log's edit
// event and the agent-tree node are built on. Untagged on purpose, this is the
// case that only goes wrong on the platforms with a spelling to expand (#1023).
func TestResolveLink_LeavesAPathThatIsNotALinkAlone(t *testing.T) {
	dir := t.TempDir()

	file := filepath.Join(dir, "main.go")
	if err := os.WriteFile(file, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ResolveLink(file); got != file {
		t.Errorf("ResolveLink respelled an ordinary file: %q → %q", file, got)
	}

	// The path hyctl edit is about to create, in a directory that does not
	// exist yet. Nothing to follow, so nothing to change.
	missing := filepath.Join(dir, "pkg", "new.go")
	if got := ResolveLink(missing); got != missing {
		t.Errorf("ResolveLink respelled a file not created yet: %q → %q", missing, got)
	}
}
