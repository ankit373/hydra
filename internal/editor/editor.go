// SPDX-License-Identifier: MIT

// Package editor provides atomic, validated, rollback-safe file editing via
// Hydra Heads. It is the Go port of dispatch/edit.sh.
package editor

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/workspace"
)

const (
	markerStart = "<<<HYDRA_FILE_START>>>"
	markerEnd   = "<<<HYDRA_FILE_END>>>"
)

// Request is the input to Edit.
type Request struct {
	File     string // absolute path
	Enum     string // routing enum key (e.g. "SIMPLE")
	Prompt   string // edit instruction
	Validate bool   // run extension validator after write (default true)
}

// Result is the JSON output emitted by Edit.
type Result struct {
	Status          string `json:"status"`
	File            string `json:"file"`
	Workspace       string `json:"workspace"`
	GitRoot         string `json:"git_root"`
	Enum            string `json:"enum"`
	LinesAdded      int    `json:"lines_added"`
	LinesRemoved    int    `json:"lines_removed"`
	ValidatorPassed bool   `json:"validator_passed"`
	RolledBack      bool   `json:"rolled_back"`
	Error           string `json:"error,omitempty"`
}

// Edit performs a scoped, validated, rollback-safe file edit.
// It uses dispatch.Dispatcher internally so it participates in Hydra's
// fallback chain and cost logging.
func Edit(ctx context.Context, req Request) (*Result, error) {
	if !filepath.IsAbs(req.File) {
		return failResult(req, "file path must be absolute"), nil
	}
	if req.Enum == "CORE" {
		return failResult(req, "CORE tier: use Claude's native Edit/Write directly"), nil
	}

	// ── Scope check ──────────────────────────────────────────────────────────
	reg, err := workspace.Load(config.ScriptHome())
	if err != nil {
		return failResult(req, "workspace registry load failed: "+err.Error()), nil
	}
	wsName, err := reg.Check(req.File)
	if err != nil {
		return failResult(req, "scope_rejected: "+err.Error()), nil
	}
	resolved, _ := reg.Resolve(req.File)

	// ── Snapshot ──────────────────────────────────────────────────────────────
	origContent, origExisted := readFile(req.File)

	// Non-git workspaces: create .hydra-bak on FIRST edit only so rollback has baseline.
	backup := req.File + ".hydra-bak"
	createdBackup := false
	if resolved.GitRoot == "" && origExisted && !fileExists(backup) {
		_ = os.WriteFile(backup, []byte(origContent), 0o644)
		createdBackup = true
	}
	cleanupBackup := func() {
		if createdBackup {
			_ = os.Remove(backup)
		}
	}

	// ── Build prompt ──────────────────────────────────────────────────────────
	var ctxNote string
	if origExisted {
		ctxNote = "The file currently exists. Modify it per the instruction below."
	} else {
		ctxNote = "The file does NOT yet exist. Create it per the instruction below."
	}
	currentBlock := origContent
	if !origExisted {
		currentBlock = "<empty — file does not exist yet>"
	}

	editPrompt := fmt.Sprintf(`You are editing a single file. Output ONLY the new file content between the
markers. No prose. No explanations. No code fences (no ` + "```" + `).

File path: %s
%s

Instruction:
%s

Current file content:
%s
%s
%s

Now output the COMPLETE new file content (every line, not a diff, not a
snippet) between these exact markers and nothing else:
%s
(new content here)
%s`,
		req.File,
		ctxNote,
		req.Prompt,
		markerStart,
		currentBlock,
		markerEnd,
		markerStart,
		markerEnd,
	)

	// ── Dispatch ──────────────────────────────────────────────────────────────
	d, err := dispatch.New(ctx)
	if err != nil {
		cleanupBackup()
		return failResult(req, "dispatcher init failed: "+err.Error()), nil
	}
	tierHint := enumToTier(req.Enum)
	dispResult, err := d.Dispatch(ctx, editPrompt, dispatch.Options{
		TierHint: tierHint,
	})
	if err != nil {
		cleanupBackup()
		return failResult(req, "route_failed: "+err.Error()), nil
	}

	// ── Parse response ────────────────────────────────────────────────────────
	newContent := extractContent(dispResult.Output)
	newContent = stripOuterFence(newContent)

	if strings.Contains(newContent, markerStart) || strings.Contains(newContent, markerEnd) {
		cleanupBackup()
		return failResult(req, "marker_leakage"), nil
	}
	if newContent == "" {
		cleanupBackup()
		if origExisted {
			return failResult(req, "empty_replacement"), nil
		}
		return failResult(req, "marker_parse_failed"), nil
	}

	// ── Atomic write ──────────────────────────────────────────────────────────
	_ = os.MkdirAll(filepath.Dir(req.File), 0o755)
	tmpF, err := os.CreateTemp(filepath.Dir(req.File), ".hydra-tmp.*")
	if err != nil {
		cleanupBackup()
		return failResult(req, "write_failed: "+err.Error()), nil
	}
	tmpPath := tmpF.Name()
	if _, werr := fmt.Fprint(tmpF, newContent+"\n"); werr != nil {
		_ = tmpF.Close()
		_ = os.Remove(tmpPath)
		cleanupBackup()
		return failResult(req, "write_failed: "+werr.Error()), nil
	}
	_ = tmpF.Close()
	if err := os.Rename(tmpPath, req.File); err != nil {
		_ = os.Remove(tmpPath)
		cleanupBackup()
		return failResult(req, "rename_failed: "+err.Error()), nil
	}

	// ── Validate ──────────────────────────────────────────────────────────────
	validatorPassed := true
	if req.Validate {
		ext := fileExt(req.File)
		vtmpl := reg.ValidatorFor(ext)

		// TypeScript fallback: use workspace-local tsc
		if vtmpl == "" && (ext == "ts" || ext == "tsx") && resolved.GitRoot != "" {
			vtmpl = tscTemplate(resolved.GitRoot)
		}

		if vtmpl != "" {
			vout, vrc := runValidatorCmd(vtmpl, req.File)
			if vrc != 0 {
				validatorPassed = false
				rollback(req.File, origContent, origExisted, resolved.GitRoot, backup)
				return &Result{
					Status:      "fail",
					File:        req.File,
					Workspace:   wsName,
					GitRoot:     resolved.GitRoot,
					Enum:        req.Enum,
					ValidatorPassed: false,
					RolledBack:  true,
					Error:       "validation_failed: " + firstLine(vout),
				}, nil
			}
		}
	}

	// ── Diff stats ────────────────────────────────────────────────────────────
	added, removed := diffStats(req.File, origContent, resolved.GitRoot, backup, origExisted)

	// ── A2A handoff ───────────────────────────────────────────────────────────
	_ = writeLastEdit(req.File, req.Enum, wsName, added, removed)

	return &Result{
		Status:          "ok",
		File:            req.File,
		Workspace:       wsName,
		GitRoot:         resolved.GitRoot,
		Enum:            req.Enum,
		LinesAdded:      added,
		LinesRemoved:    removed,
		ValidatorPassed: validatorPassed,
		RolledBack:      false,
	}, nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func failResult(req Request, errMsg string) *Result {
	return &Result{
		Status: "fail",
		File:   req.File,
		Enum:   req.Enum,
		Error:  errMsg,
	}
}

func readFile(path string) (content string, existed bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(raw), true
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func fileExt(path string) string {
	ext := filepath.Ext(path)
	if len(ext) > 0 {
		return ext[1:] // strip leading dot
	}
	return ""
}

// extractContent gets text between HYDRA_FILE_START and HYDRA_FILE_END.
// Falls back to lenient extraction when a marker is missing.
func extractContent(raw string) string {
	hasStart := strings.Contains(raw, markerStart)
	hasEnd := strings.Contains(raw, markerEnd)

	switch {
	case hasStart && hasEnd:
		return extractBetween(raw)
	case hasEnd && !hasStart:
		// Everything before END
		lines := strings.Split(raw, "\n")
		var out []string
		for _, l := range lines {
			if strings.Contains(l, markerEnd) {
				break
			}
			out = append(out, l)
		}
		return strings.Join(out, "\n")
	case hasStart && !hasEnd:
		// Everything after START
		parts := strings.SplitN(raw, markerStart, 2)
		if len(parts) == 2 {
			return strings.TrimPrefix(parts[1], "\n")
		}
	}
	return ""
}

func extractBetween(raw string) string {
	scanner := bufio.NewScanner(strings.NewReader(raw))
	var out []string
	inside := false
	printed := false
	for scanner.Scan() {
		line := scanner.Text()
		if !printed && strings.Contains(line, markerStart) {
			inside = true
			continue
		}
		if inside && strings.Contains(line, markerEnd) {
			printed = true
			break
		}
		if inside {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// stripOuterFence removes a leading/trailing code fence line if the model disobeyed.
func stripOuterFence(s string) string {
	lines := strings.Split(s, "\n")
	if len(lines) == 0 {
		return s
	}
	start, end := 0, len(lines)-1
	if strings.HasPrefix(lines[0], "```") {
		start = 1
	}
	if end > start && lines[end] == "```" {
		end--
	}
	return strings.Join(lines[start:end+1], "\n")
}

// rollback restores the file to its original state via git, backup, or in-memory.
func rollback(file, origContent string, origExisted bool, gitRoot, backup string) {
	if gitRoot != "" {
		if out, err := exec.Command("git", "-C", gitRoot, "ls-files", "--error-unmatch", file).CombinedOutput(); err == nil {
			_ = out
			_, _ = exec.Command("git", "-C", gitRoot, "checkout", "--", file).Output()
			return
		}
	}
	if fileExists(backup) {
		_ = os.Rename(backup, file)
		return
	}
	if !origExisted {
		_ = os.Remove(file)
		return
	}
	_ = os.WriteFile(file, []byte(origContent), 0o644)
}

// runValidatorCmd executes a validator template safely.
// Splits around {file} so paths containing spaces are never fragmented by Fields.
func runValidatorCmd(vtmpl, file string) (output string, exitCode int) {
	var parts []string
	if idx := strings.Index(vtmpl, "{file}"); idx >= 0 {
		parts = append(strings.Fields(vtmpl[:idx]), file)
		parts = append(parts, strings.Fields(vtmpl[idx+len("{file}"):])...)
	} else {
		parts = strings.Fields(vtmpl)
	}
	if len(parts) == 0 {
		return "", 0
	}
	c := exec.Command(parts[0], parts[1:]...)
	out, err := c.CombinedOutput()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return string(out), exitErr.ExitCode()
		}
		return string(out), 1
	}
	return string(out), 0
}

func tscTemplate(gitRoot string) string {
	tsc := filepath.Join(gitRoot, "node_modules", ".bin", "tsc")
	if !fileExists(tsc) {
		return ""
	}
	if fileExists(filepath.Join(gitRoot, "tsconfig.json")) {
		return tsc + " --noEmit -p " + gitRoot + "/tsconfig.json"
	}
	return tsc + " --noEmit --allowJs --skipLibCheck --target es2022 --lib es2022,dom {file}"
}

func diffStats(file, origContent, gitRoot, backup string, origExisted bool) (added, removed int) {
	if gitRoot != "" {
		out, err := exec.Command("git", "-C", gitRoot, "diff", "--numstat", "--", file).Output()
		if err == nil {
			line := strings.TrimSpace(string(out))
			fmt.Sscanf(line, "%d\t%d", &added, &removed)
			return
		}
	}
	if fileExists(backup) {
		out, err := exec.Command("diff", "-u", backup, file).CombinedOutput()
		_ = err
		for _, l := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++") {
				added++
			} else if strings.HasPrefix(l, "-") && !strings.HasPrefix(l, "---") {
				removed++
			}
		}
		return
	}
	newLines := strings.Count(readFileLines(file), "\n")
	origLines := 0
	if origExisted {
		origLines = strings.Count(origContent, "\n")
	}
	added = max(0, newLines-origLines)   //nolint:builtin — uses Go 1.21+ built-in
	removed = max(0, origLines-newLines) //nolint:builtin
	return
}

func readFileLines(path string) string {
	raw, _ := os.ReadFile(path)
	return string(raw)
}


func firstLine(s string) string {
	lines := strings.SplitN(strings.TrimSpace(s), "\n", 2)
	if len(lines) > 0 {
		return lines[0]
	}
	return s
}

func writeLastEdit(file, enum, ws string, added, removed int) error {
	h := map[string]any{
		"from":         "hydra-edit-" + enum,
		"file":         file,
		"enum":         enum,
		"workspace":    ws,
		"lines_added":  added,
		"lines_removed": removed,
	}
	raw, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(config.Dir(), "logs", "last_edit.json")
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	return os.WriteFile(path, raw, 0o600)
}

func enumToTier(enum string) string { return dispatch.EnumToTier(enum) }
