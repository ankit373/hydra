// Package workspace validates file paths against the workspace registry
// (registry/workspace.yaml) and provides git root detection and per-extension
// validator lookup. Go port of dispatch/scope.sh.
package workspace

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
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

// Registry holds the loaded workspace configuration.
type Registry struct {
	workspaces []Workspace
	validators map[string]string // ext → command template
}

// Load reads registry/workspace.yaml relative to hydraHome.
func Load(hydraHome string) (*Registry, error) {
	path := filepath.Join(hydraHome, "registry", "workspace.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("workspace.yaml not found at %s", path)
	}

	var rf registryFile
	if err := yaml.Unmarshal(raw, &rf); err != nil {
		return nil, fmt.Errorf("parse workspace.yaml: %w", err)
	}

	r := &Registry{validators: make(map[string]string)}
	for name, e := range rf.Workspaces {
		r.workspaces = append(r.workspaces, Workspace{
			Name:         name,
			Root:         e.Root,
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

	rel, _ := filepath.Rel(ws.Root, path)

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
func GitRoot(path string) string {
	dir := path
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		dir = filepath.Dir(dir)
	}
	for dir != "/" && dir != "" {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	return ""
}

// GitRootCmd uses git rev-parse for accuracy when git is available.
func GitRootCmd(path string) string {
	dir := path
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		dir = filepath.Dir(dir)
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return GitRoot(path)
	}
	return strings.TrimSpace(string(out))
}

// find returns the workspace whose root contains path.
// Respects HYDRA_WORKSPACE override.
func (r *Registry) find(path string) (Workspace, error) {
	if override := os.Getenv("HYDRA_WORKSPACE"); override != "" {
		for _, ws := range r.workspaces {
			if ws.Name == override {
				if !strings.HasPrefix(path, ws.Root) {
					return Workspace{}, fmt.Errorf("HYDRA_WORKSPACE=%s root (%s) does not contain %s", override, ws.Root, path)
				}
				return ws, nil
			}
		}
		return Workspace{}, fmt.Errorf("HYDRA_WORKSPACE=%s not defined in registry", override)
	}

	for _, ws := range r.workspaces {
		if strings.HasPrefix(path, ws.Root+"/") || path == ws.Root {
			return ws, nil
		}
	}
	return Workspace{}, fmt.Errorf("no workspace contains %s", path)
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
