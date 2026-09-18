// SPDX-License-Identifier: MIT

// Package verify resolves the command that checks whether work is correct: the
// repo's own test suite, or the workspace validator for the file's language.
package verify

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/workspace"
)

// Command picks the verifier for work on file: `go test ./...` inside a Go
// repo, otherwise the workspace.yaml validator for the file's extension. Empty
// argv means nothing is configured to judge this, which callers must report
// rather than treat as a pass.
//
// label is the command as a human reads it, with {file} shown as a basename.
func Command(file string) (argv []string, label string) {
	if GoModDir() != "" {
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

// GoModDir walks up from the working directory to the go.mod that owns it, or
// "" when the repo root is reached first. Stopping at .git is what keeps a Go
// tool from claiming a non-Go repo that happens to vendor one.
func GoModDir() string {
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
