// SPDX-License-Identifier: MIT

// Package review provides the review surface for files edited by Hydra.
// It is the Go port of dispatch/review.sh.
package review

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/workspace"
)

// FileEntry is one file's review state.
type FileEntry struct {
	File      string `json:"file"`
	Workspace string `json:"workspace"`
	GitRoot   string `json:"git_root"`
	Added     int    `json:"lines_added"`
	Removed   int    `json:"lines_removed"`
	Status    string `json:"status"` // modified | new | unchanged | no_baseline | missing
}

// SummaryResult is the JSON output of Summary.
type SummaryResult struct {
	Files  []FileEntry   `json:"files"`
	Totals SummaryTotals `json:"totals"`
}

// SummaryTotals aggregates counts across all files.
type SummaryTotals struct {
	Count   int `json:"count"`
	Added   int `json:"added"`
	Removed int `json:"removed"`
}

// ApproveResult is the JSON output of Approve.
type ApproveResult struct {
	Status string `json:"status"`
	File   string `json:"file"`
}

// RejectResult is the JSON output of Reject.
type RejectResult struct {
	Status string `json:"status"`
	File   string `json:"file"`
	Method string `json:"method"` // git_checkout | rm_untracked | backup_restore
}

// QAResult is the JSON output of QA.
type QAResult struct {
	Status       string `json:"status"`
	File         string `json:"file"`
	ReviewerTier int    `json:"reviewer_tier"`
	Verdict      string `json:"verdict"`
}

// Summary returns diff stats for one or more files.
// If files is empty it reads file paths from logs/last_parallel.json and logs/last_edit.json.
func Summary(files []string) (*SummaryResult, error) {
	if len(files) == 0 {
		files = filesFromLogs()
	}

	reg, _ := workspace.Load(config.ScriptHome())

	var entries []FileEntry
	for _, f := range files {
		if !filepath.IsAbs(f) {
			continue
		}
		ws := ""
		if reg != nil {
			ws, _ = reg.Check(f)
		}
		resolved := workspace.Resolved{}
		if reg != nil {
			resolved, _ = reg.Resolve(f)
		}

		added, removed, status := numstat(f, resolved.GitRoot)
		entries = append(entries, FileEntry{
			File:      f,
			Workspace: ws,
			GitRoot:   resolved.GitRoot,
			Added:     added,
			Removed:   removed,
			Status:    status,
		})
	}

	totals := SummaryTotals{Count: len(entries)}
	for _, e := range entries {
		totals.Added += e.Added
		totals.Removed += e.Removed
	}

	return &SummaryResult{Files: entries, Totals: totals}, nil
}

// Diff prints a unified diff for a file to stdout.
// Returns non-nil error if no diff is available.
func Diff(file string) (string, error) {
	if !filepath.IsAbs(file) {
		return "", fmt.Errorf("path must be absolute: %s", file)
	}
	reg, _ := workspace.Load(config.ScriptHome())
	resolved := workspace.Resolved{}
	if reg != nil {
		resolved, _ = reg.Resolve(file)
	}

	if resolved.GitRoot != "" {
		out, err := exec.Command("git", "-C", resolved.GitRoot, "diff", "--", file).Output()
		if err != nil {
			return "", fmt.Errorf("git diff failed: %w", err)
		}
		return string(out), nil
	}

	backup := file + ".hydra-bak"
	if fileExists(backup) {
		out, _ := exec.Command("diff", "-u", backup, file).CombinedOutput()
		return string(out), nil
	}

	return "", fmt.Errorf("no diff available for %s (no git root, no backup)", file)
}

// Approve accepts changes for a file.
// For git workspaces: no-op (leaves diff in working tree).
// For non-git workspaces: removes .hydra-bak.
func Approve(file string) (*ApproveResult, error) {
	if !filepath.IsAbs(file) {
		return nil, fmt.Errorf("path must be absolute: %s", file)
	}
	if err := scopeCheck(file); err != nil {
		return nil, err
	}

	reg, _ := workspace.Load(config.ScriptHome())
	resolved := workspace.Resolved{}
	if reg != nil {
		resolved, _ = reg.Resolve(file)
	}

	if resolved.GitRoot == "" {
		backup := file + ".hydra-bak"
		if fileExists(backup) {
			_ = os.Remove(backup)
		}
	}

	return &ApproveResult{Status: "approved", File: file}, nil
}

// Reject rolls back a file to its pre-edit state.
func Reject(file string) (*RejectResult, error) {
	if !filepath.IsAbs(file) {
		return nil, fmt.Errorf("path must be absolute: %s", file)
	}
	if err := scopeCheck(file); err != nil {
		return nil, err
	}

	reg, _ := workspace.Load(config.ScriptHome())
	resolved := workspace.Resolved{}
	if reg != nil {
		resolved, _ = reg.Resolve(file)
	}

	gitRoot := resolved.GitRoot
	if gitRoot != "" {
		if err := exec.Command("git", "-C", gitRoot, "ls-files", "--error-unmatch", file).Run(); err == nil {
			if err := exec.Command("git", "-C", gitRoot, "checkout", "--", file).Run(); err != nil {
				return nil, fmt.Errorf("git checkout failed: %w", err)
			}
			return &RejectResult{Status: "rejected", File: file, Method: "git_checkout"}, nil
		}
		// Untracked new file
		if fileExists(file) {
			_ = os.Remove(file)
			return &RejectResult{Status: "rejected", File: file, Method: "rm_untracked"}, nil
		}
	}

	backup := file + ".hydra-bak"
	if fileExists(backup) {
		if err := os.Rename(backup, file); err != nil {
			return nil, fmt.Errorf("backup restore failed: %w", err)
		}
		return &RejectResult{Status: "rejected", File: file, Method: "backup_restore"}, nil
	}

	return nil, fmt.Errorf("nothing to roll back for %s", file)
}

// QA sends a file's diff to a Hydra Head for LLM review.
func QA(ctx context.Context, file string, tier int) (*QAResult, error) {
	if !filepath.IsAbs(file) {
		return nil, fmt.Errorf("path must be absolute: %s", file)
	}
	if err := scopeCheck(file); err != nil {
		return nil, err
	}

	diffText, err := Diff(file)
	if err != nil || strings.TrimSpace(diffText) == "" {
		return nil, fmt.Errorf("no diff to review for %s", file)
	}

	qaPrompt := fmt.Sprintf(`You are a code reviewer. Review the following diff for: bugs, security issues,
broken invariants, style violations, and missing edge cases. Be concise.

File: %s

Diff:
%s

Output exactly one of:
APPROVED <one-line reason>
CONCERNS <bullet list of issues>`, file, diffText)

	d, err := dispatch.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("dispatcher init: %w", err)
	}
	result, err := d.Dispatch(ctx, qaPrompt, dispatch.Options{
		TierHint: strconv.Itoa(tier),
	})
	if err != nil {
		return nil, fmt.Errorf("dispatch failed: %w", err)
	}

	return &QAResult{
		Status:       "reviewed",
		File:         file,
		ReviewerTier: tier,
		Verdict:      result.Output,
	}, nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func scopeCheck(file string) error {
	reg, err := workspace.Load(config.ScriptHome())
	if err != nil {
		return fmt.Errorf("workspace load failed: %w", err)
	}
	if _, err := reg.Check(file); err != nil {
		return fmt.Errorf("scope_rejected: %w", err)
	}
	return nil
}

// numstat returns (added, removed, statusString) for a file.
func numstat(file, gitRoot string) (added, removed int, status string) {
	if gitRoot != "" {
		if err := exec.Command("git", "-C", gitRoot, "ls-files", "--error-unmatch", file).Run(); err == nil {
			out, _ := exec.Command("git", "-C", gitRoot, "diff", "--numstat", "--", file).Output()
			line := strings.TrimSpace(string(out))
			if line == "" {
				return 0, 0, "unchanged"
			}
			fmt.Sscanf(line, "%d\t%d", &added, &removed)
			return added, removed, "modified"
		}
		// Untracked / new
		if fileExists(file) {
			data, _ := os.ReadFile(file)
			return strings.Count(string(data), "\n"), 0, "new"
		}
		return 0, 0, "missing"
	}

	backup := file + ".hydra-bak"
	if fileExists(backup) {
		out, _ := exec.Command("diff", "-u", backup, file).CombinedOutput()
		for _, l := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++") {
				added++
			} else if strings.HasPrefix(l, "-") && !strings.HasPrefix(l, "---") {
				removed++
			}
		}
		return added, removed, "modified"
	}

	if fileExists(file) {
		return 0, 0, "no_baseline"
	}
	return 0, 0, "missing"
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// filesFromLogs reads file paths from logs/last_parallel.json and logs/last_edit.json.
func filesFromLogs() []string {
	logDir := filepath.Join(config.Dir(), "logs")
	var files []string

	// last_parallel.json: array of { mode: "edit", file: "..." }
	if raw, err := os.ReadFile(filepath.Join(logDir, "last_parallel.json")); err == nil {
		var rows []struct {
			Mode string `json:"mode"`
			File string `json:"file"`
		}
		if json.Unmarshal(raw, &rows) == nil {
			for _, r := range rows {
				if r.Mode == "edit" && r.File != "" {
					files = append(files, r.File)
				}
			}
		}
	}

	// Fallback to last_edit.json
	if len(files) == 0 {
		if raw, err := os.ReadFile(filepath.Join(logDir, "last_edit.json")); err == nil {
			var e struct {
				File string `json:"file"`
			}
			if json.Unmarshal(raw, &e) == nil && e.File != "" {
				files = append(files, e.File)
			}
		}
	}

	return files
}
