// SPDX-License-Identifier: MIT

// Package workspace validates file paths against the workspace registry
// (registry/workspace.yaml) and provides git root detection and per-extension
// validator lookup. Go port of dispatch/scope.sh.
package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ankit373/hydra/registry"
)

// Workspace is a single entry from registry/workspace.yaml.
type Workspace struct {
	Name         string
	Root         string
	Git          string // "auto" | "true" | "false"
	AllowedGlobs []string
	DeniedGlobs  []string
}

// Resolved is the output of Resolve — full context for a validated path.
type Resolved struct {
	Workspace string
	Root      string
	Git       string
	GitRoot   string
}

type registryFile struct {
	Version    string                    `yaml:"version"`
	Workspaces map[string]workspaceEntry `yaml:"workspaces"`
	Validators map[string]interface{}    `yaml:"validators"`
}

type workspaceEntry struct {
	Root         string   `yaml:"root"`
	Git          string   `yaml:"git"`
	AllowedGlobs []string `yaml:"allowed_globs"`
	DeniedGlobs  []string `yaml:"denied_globs"`
}

// SkippedWorkspace is an entry Load could not use. It is kept so callers can
// distinguish "no workspaces are configured" from "your configuration was read
// and then ignored" — the second is a problem the user needs told about.
type SkippedWorkspace struct {
	Name   string
	Root   string
	Reason string
}

// Registry holds the loaded workspace configuration.
type Registry struct {
	workspaces []Workspace
	validators map[string]string // ext → command template
	skipped    []SkippedWorkspace
}

// Skipped returns the workspace entries Load read but could not use.
func (r *Registry) Skipped() []SkippedWorkspace {
	if r == nil {
		return nil
	}
	return r.skipped
}

// Load reads registry/workspace.yaml — an on-disk copy under hydraHome if one
// exists, otherwise the copy embedded in the binary (#238).
func Load(hydraHome string) (*Registry, error) {
	raw, err := registry.Read(hydraHome, "workspace.yaml")
	if err != nil {
		return nil, fmt.Errorf("workspace.yaml unreadable: %w", err)
	}

	var rf registryFile
	if err := yaml.Unmarshal(raw, &rf); err != nil {
		return nil, fmt.Errorf("parse workspace.yaml: %w", err)
	}

	r := &Registry{validators: make(map[string]string)}
	for name, e := range rf.Workspaces {
		root := filepath.Clean(e.Root)
		if !filepath.IsAbs(root) {
			// Skip this entry rather than failing the whole load.
			//
			// A root that is not absolute *for this platform* must not make the
			// entire scope layer unusable. The embedded registry ships POSIX
			// roots, and filepath.IsAbs("/Users/x") is false on Windows — so
			// erroring here made workspace.Load fail outright there, and every
			// hyctl edit/parallel/review died before it looked at a path (#297).
			//
			// Skipping fails closed: paths that would have resolved to this
			// workspace are now refused by find() as "no workspace contains",
			// which is the safe direction. Skipped is recorded so callers can
			// tell "no workspaces configured" from "your config was ignored".
			r.skipped = append(r.skipped, SkippedWorkspace{
				Name:   name,
				Root:   e.Root,
				Reason: "root is not an absolute path on " + runtime.GOOS,
			})
			continue
		}
		r.workspaces = append(r.workspaces, Workspace{
			Name:         name,
			Root:         root,
			Git:          e.Git,
			AllowedGlobs: e.AllowedGlobs,
			DeniedGlobs:  e.DeniedGlobs,
		})
	}

	for ext, val := range rf.Validators {
		if val == nil {
			r.validators[ext] = ""
			continue
		}
		if s, ok := val.(string); ok {
			r.validators[ext] = s
		}
	}

	return r, nil
}

// Check validates that path is inside a workspace and matches its glob rules.
// Returns the workspace name or an error.
// Honours HYDRA_WORKSPACE env var override.
func (r *Registry) Check(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("path must be absolute: %s", path)
	}

	ws, err := r.find(path)
	if err != nil {
		return "", err
	}

	// filepath.Rel cleans both sides, so rel is already free of "." and ".."
	// segments — and find has established containment, so it cannot start with
	// "..". ToSlash because the glob patterns are written with forward slashes
	// and matchGlob splits on "/"; without it no pattern matches on Windows.
	rel, err := filepath.Rel(ws.Root, path)
	if err != nil {
		return "", fmt.Errorf("path %s is not relative to workspace %q root %s", path, ws.Name, ws.Root)
	}
	rel = filepath.ToSlash(rel)

	// Denied globs win.
	for _, pat := range ws.DeniedGlobs {
		if matchGlob(rel, pat) {
			return "", fmt.Errorf("denied by glob %q in workspace %q: %s", pat, ws.Name, path)
		}
	}

	// Must match at least one allowed glob.
	for _, pat := range ws.AllowedGlobs {
		if matchGlob(rel, pat) {
			return ws.Name, nil
		}
	}

	return "", fmt.Errorf("not in any allowed_glob for workspace %q: %s", ws.Name, rel)
}

// Resolve returns full workspace metadata for a validated path.
func (r *Registry) Resolve(path string) (Resolved, error) {
	if !filepath.IsAbs(path) {
		return Resolved{}, fmt.Errorf("path must be absolute: %s", path)
	}

	ws, err := r.find(path)
	if err != nil {
		return Resolved{}, err
	}

	gitRoot := ""
	switch ws.Git {
	case "auto":
		gitRoot = GitRoot(path)
	case "true":
		gitRoot = ws.Root
	}

	return Resolved{
		Workspace: ws.Name,
		Root:      ws.Root,
		Git:       ws.Git,
		GitRoot:   gitRoot,
	}, nil
}

// ValidatorFor returns the validator command template for a file extension.
// Returns empty string if none defined or if explicitly set to null.
// The caller replaces {file} with the actual path.
func (r *Registry) ValidatorFor(ext string) string {
	return r.validators[strings.TrimPrefix(ext, ".")]
}

// GitRoot walks up from path to find the nearest .git directory.
// Returns empty string if not inside a git repo.
// Termination is by fixed point, not by comparing against "/". filepath.Dir
// returns its argument unchanged once it reaches the filesystem root, and on
// Windows that root is "C:\", never "/" — so the old `for dir != "/"` condition
// never became false there and the walk spun forever.
func GitRoot(path string) string {
	dir := path
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		dir = filepath.Dir(dir)
	}
	for {
		if dir == "" {
			return ""
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "" // reached the root on any platform
		}
		dir = parent
	}
}

// find returns the workspace whose root contains path.
// Respects HYDRA_WORKSPACE override.
func (r *Registry) find(path string) (Workspace, error) {
	if override := os.Getenv("HYDRA_WORKSPACE"); override != "" {
		for _, ws := range r.workspaces {
			if ws.Name == override {
				if !contains(ws.Root, path) {
					return Workspace{}, fmt.Errorf("HYDRA_WORKSPACE=%s root (%s) does not contain %s", override, ws.Root, path)
				}
				return ws, nil
			}
		}
		return Workspace{}, fmt.Errorf("HYDRA_WORKSPACE=%s not defined in registry", override)
	}

	for _, ws := range r.workspaces {
		if contains(ws.Root, path) {
			return ws, nil
		}
	}
	return Workspace{}, fmt.Errorf("no workspace contains %s", path)
}

// contains reports whether path is root itself or lives beneath it.
//
// This used to be `strings.HasPrefix(path, root+"/")`, which was wrong three
// ways. It let "/ws/../etc/passwd" through, because the literal prefix matches
// even though the path resolves outside the root — a workspace with a "**"
// allowed_glob would then have permitted writing anywhere on the filesystem.
// It hardcoded "/" as the separator, so on Windows no path ever matched any
// workspace and every edit was rejected. And a sibling directory named
// "<root>-evil" only failed to match by luck of the trailing slash.
//
// filepath.Rel cleans both operands and is separator-correct, so all three fall
// out of using it: a path that escapes yields a rel beginning with "..".
func contains(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false // different volumes on Windows, or otherwise unrelatable
	}
	if rel == "." {
		return true // the root itself
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// matchGlob matches a relative path against a glob pattern.
// Supports ** for matching zero or more path segments, * for single-segment wildcard.
func matchGlob(rel, pattern string) bool {
	rel = strings.TrimPrefix(rel, "./")
	return matchSegments(strings.Split(rel, "/"), strings.Split(pattern, "/"))
}

// matchSegments recursively matches path segments against pattern segments.
// "**" matches zero or more path segments; everything else uses filepath.Match.
func matchSegments(path, pat []string) bool {
	if len(pat) == 0 {
		return len(path) == 0
	}
	if pat[0] == "**" {
		// Try consuming 0, 1, 2, ... path segments with this **.
		for i := 0; i <= len(path); i++ {
			if matchSegments(path[i:], pat[1:]) {
				return true
			}
		}
		return false
	}
	if len(path) == 0 {
		return false
	}
	matched, _ := filepath.Match(pat[0], path[0])
	if !matched {
		return false
	}
	return matchSegments(path[1:], pat[1:])
}
