// SPDX-License-Identifier: MIT

// Package parallel fans N independent tasks out to N Hydra Heads simultaneously.
// It is the Go port of dispatch/parallel.sh.
package parallel

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/policy"
	"github.com/ankit373/hydra/internal/runid"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/workspace"
)

const (
	markerStart = "<<<HYDRA_FILE_START>>>"
	markerEnd   = "<<<HYDRA_FILE_END>>>"
)

// Task is one item in the input batch.
// When File is non-empty the task is an edit task; otherwise a text dispatch.
type Task struct {
	Label    string `json:"label"`
	Enum     string `json:"enum"`
	Prompt   string `json:"prompt"`
	File     string `json:"file,omitempty"`
	Context  string `json:"context,omitempty"`  // context file path for text tasks
	Validate *bool  `json:"validate,omitempty"` // edit tasks only; nil = true
}

// TextResult is the result of a text dispatch task.
type TextResult struct {
	Label  string `json:"label"`
	Enum   string `json:"enum"`
	Mode   string `json:"mode"`
	Status string `json:"status"`
	Output string `json:"output"`
	Error  string `json:"error,omitempty"`
}

// EditResult is the result of an edit task (matches editor.Result + label/enum/mode).
type EditResult struct {
	Label           string `json:"label"`
	Enum            string `json:"enum"`
	Mode            string `json:"mode"`
	Status          string `json:"status"`
	File            string `json:"file"`
	Workspace       string `json:"workspace"`
	GitRoot         string `json:"git_root"`
	LinesAdded      int    `json:"lines_added"`
	LinesRemoved    int    `json:"lines_removed"`
	ValidatorPassed bool   `json:"validator_passed"`
	RolledBack      bool   `json:"rolled_back"`
	Error           string `json:"error,omitempty"`
}

// Result is a union type (either TextResult or EditResult) marshalled as JSON.
type Result struct {
	raw json.RawMessage
}

func (r Result) MarshalJSON() ([]byte, error) { return r.raw, nil }
func (r Result) Raw() json.RawMessage         { return r.raw }

// Options configures a batch run.
type Options struct {
	// RunID groups every log row the batch produces. All tasks in one batch
	// share it; each task still gets its own TaskID, so a reader can tell "one
	// batch of three tasks" from "three unrelated runs" (#181). Empty derives
	// one for the batch.
	RunID string
}

// Run fans all tasks out as goroutines and collects results.
// Returns non-nil error only for pre-flight failures; individual task errors
// are captured in each result's Status/Error fields.
func Run(ctx context.Context, tasks []Task, opts Options) ([]Result, error) {
	if len(tasks) == 0 {
		return nil, fmt.Errorf("no tasks provided")
	}

	// Resolve the batch's run identity once, here rather than per goroutine, so
	// every task in the batch genuinely shares it.
	runID := runid.ResolveRun(opts.RunID)

	// Pre-flight: detect duplicate file targets.
	seen := map[string]string{}
	for _, t := range tasks {
		if t.File == "" {
			continue
		}
		if first, ok := seen[t.File]; ok {
			return nil, fmt.Errorf("conflict: tasks %q and %q both target %s", first, t.Label, t.File)
		}
		seen[t.File] = t.Label
	}

	results := make([]Result, len(tasks))
	var mu sync.Mutex

	// A batch is a fan-out, and the tree that renders it needs the fan-out's
	// shape: one node per task, all under the batch. Before #204 nothing in this
	// package emitted, so an N-task batch had no structure at all in the run log.
	// Appends are best-effort throughout — a lost event must never fail a task.
	rl := runlog.New(runID)
	_ = rl.Append(runlog.Event{
		Kind:   runlog.KindTaskStarted,
		Agent:  batchAgent,
		Detail: fmt.Sprintf("parallel batch · %d tasks", len(tasks)),
	})

	g, gctx := errgroup.WithContext(ctx)

	for i, task := range tasks {
		i, task := i, task
		// Each task is its own logical unit of work inside the shared run.
		taskID := runid.New()
		g.Go(func() error {
			_ = rl.Append(runlog.Event{
				Kind: runlog.KindTaskStarted, TaskID: taskID,
				Agent: task.Label, Parent: batchAgent, Detail: task.Enum,
			})

			started := time.Now()
			var raw json.RawMessage
			if task.File != "" {
				raw = runEditTask(gctx, task, runID, taskID)
			} else {
				raw = runTextTask(gctx, task, runID, taskID)
			}
			mu.Lock()
			results[i] = Result{raw: raw}
			mu.Unlock()

			_ = rl.Append(runlog.Event{
				Kind: runlog.KindTaskFinished, TaskID: taskID,
				Agent: task.Label, Parent: batchAgent,
				Status:     statusOf(raw),
				DurationMS: time.Since(started).Milliseconds(),
			})
			return nil // always nil — errors are captured in raw
		})
	}

	_ = g.Wait()

	_ = rl.Append(runlog.Event{Kind: runlog.KindTaskFinished, Agent: batchAgent})

	_ = persistResults(results)
	return results, nil
}

// batchAgent is the batch's own node in the tree — the parent every task hangs
// off, so a fan-out renders as a fan-out rather than N unrelated roots.
const batchAgent = "parallel"

// statusOf recovers a task's outcome from the JSON each runner already
// produces, rather than threading a second return value through both paths.
func statusOf(raw json.RawMessage) string {
	var v struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &v); err != nil || v.Status == "" {
		return "unknown"
	}
	return v.Status
}

// runTextTask dispatches a prompt and returns the raw JSON result.
func runTextTask(ctx context.Context, task Task, runID, taskID string) json.RawMessage {
	d, err := dispatch.New(ctx)
	if err != nil {
		return failText(task, "dispatcher init: "+err.Error())
	}

	prompt := task.Prompt
	if task.Context != "" {
		if raw, err := os.ReadFile(task.Context); err == nil {
			prompt = fmt.Sprintf("CONTEXT:\n%s\n\nTASK:\n%s", string(raw), task.Prompt)
		}
	}

	result, err := d.Dispatch(ctx, prompt, dispatch.Options{
		TierHint: enumToTier(task.Enum),
		RunID:    runID,
		TaskID:   taskID,
	})
	if err != nil {
		return failText(task, err.Error())
	}

	return mustMarshal(TextResult{
		Label:  task.Label,
		Enum:   task.Enum,
		Mode:   "text",
		Status: "ok",
		Output: result.Output,
	})
}

// runEditTask performs an atomic file edit and returns the raw JSON result.
// Self-contained port of the edit.sh flow so this package compiles on develop
// before internal/editor merges. Once editor merges, this can delegate to editor.Edit.
func runEditTask(ctx context.Context, task Task, runID, taskID string) json.RawMessage {
	file := task.File
	if !filepath.IsAbs(file) {
		return failEdit(task, "file path must be absolute")
	}
	if task.Enum == "CORE" {
		return failEdit(task, "CORE tier: use Claude's native Edit/Write directly")
	}

	// Scope check
	reg, err := workspace.Load(config.ScriptHome())
	if err != nil {
		return failEdit(task, "workspace load: "+err.Error())
	}
	wsName, err := reg.Check(file)
	if err != nil {
		return failEdit(task, "scope_rejected: "+err.Error())
	}
	resolved, _ := reg.Resolve(file)

	// Policy (Phase 1)
	if eng, pErr := policy.LoadFilePolicy(config.ScriptHome()); pErr == nil {
		_ = eng.Decide(policy.Spec{
			File:          file,
			FileExtension: fileExt(file),
			Workspace:     wsName,
		})
	}

	// Snapshot
	origContent, origExisted := readFile(file)
	backup := file + ".hydra-bak"
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

	// Build prompt
	ctxNote := "The file currently exists. Modify it per the instruction below."
	currentBlock := origContent
	if !origExisted {
		ctxNote = "The file does NOT yet exist. Create it per the instruction below."
		currentBlock = "<empty — file does not exist yet>"
	}
	editPrompt := fmt.Sprintf(`You are editing a single file. Output ONLY the new file content between the
markers. No prose. No explanations. No code fences (no `+"`"+`).

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
		file, ctxNote, task.Prompt,
		markerStart, currentBlock, markerEnd,
		markerStart, markerEnd,
	)

	// Dispatch
	d, err := dispatch.New(ctx)
	if err != nil {
		cleanupBackup()
		return failEdit(task, "dispatcher init: "+err.Error())
	}
	dispResult, err := d.Dispatch(ctx, editPrompt, dispatch.Options{
		TierHint: enumToTier(task.Enum),
		RunID:    runID,
		TaskID:   taskID,
	})
	if err != nil {
		cleanupBackup()
		return failEdit(task, "route_failed: "+err.Error())
	}

	// Parse
	newContent := extractContent(dispResult.Output)
	newContent = stripOuterFence(newContent)
	if strings.Contains(newContent, markerStart) || strings.Contains(newContent, markerEnd) {
		cleanupBackup()
		return failEdit(task, "marker_leakage")
	}
	if newContent == "" {
		cleanupBackup()
		if origExisted {
			return failEdit(task, "empty_replacement")
		}
		return failEdit(task, "marker_parse_failed")
	}

	// Atomic write
	_ = os.MkdirAll(filepath.Dir(file), 0o755)
	tmpF, err := os.CreateTemp(filepath.Dir(file), ".hydra-tmp.*")
	if err != nil {
		cleanupBackup()
		return failEdit(task, "write_failed: "+err.Error())
	}
	tmpPath := tmpF.Name()
	if _, werr := fmt.Fprint(tmpF, newContent+"\n"); werr != nil {
		_ = tmpF.Close()
		_ = os.Remove(tmpPath)
		cleanupBackup()
		return failEdit(task, "write_failed: "+werr.Error())
	}
	_ = tmpF.Close()
	if err := os.Rename(tmpPath, file); err != nil {
		_ = os.Remove(tmpPath)
		cleanupBackup()
		return failEdit(task, "rename_failed: "+err.Error())
	}

	// Validate
	validate := true
	if task.Validate != nil {
		validate = *task.Validate
	}
	if validate {
		ext := fileExt(file)
		vtmpl := reg.ValidatorFor(ext)
		if vtmpl == "" && (ext == "ts" || ext == "tsx") && resolved.GitRoot != "" {
			vtmpl = tscTemplate(resolved.GitRoot)
		}
		if vtmpl != "" {
			if rc := runValidate(vtmpl, file); rc != 0 {
				rollback(file, origContent, origExisted, resolved.GitRoot, backup)
				return mustMarshal(EditResult{
					Label: task.Label, Enum: task.Enum, Mode: "edit",
					Status: "fail", File: file, Workspace: wsName, GitRoot: resolved.GitRoot,
					RolledBack: true, Error: "validation_failed",
				})
			}
		}
	}

	added, removed := diffStats(file, origContent, resolved.GitRoot, backup, origExisted)
	return mustMarshal(EditResult{
		Label: task.Label, Enum: task.Enum, Mode: "edit",
		Status: "ok", File: file, Workspace: wsName, GitRoot: resolved.GitRoot,
		LinesAdded: added, LinesRemoved: removed, ValidatorPassed: true,
	})
}

// persistResults writes results to logs/last_parallel.json.
func persistResults(results []Result) error {
	path := filepath.Join(config.Dir(), "logs", "last_parallel.json")
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	all := make([]json.RawMessage, len(results))
	for i, r := range results {
		all[i] = r.raw
	}
	raw, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

// ── helpers ───────────────────────────────────────────────────────────────────

func failText(task Task, errMsg string) json.RawMessage {
	return mustMarshal(TextResult{Label: task.Label, Enum: task.Enum, Mode: "text", Status: "fail", Error: errMsg})
}

func failEdit(task Task, errMsg string) json.RawMessage {
	return mustMarshal(EditResult{Label: task.Label, Enum: task.Enum, Mode: "edit", Status: "fail", File: task.File, Error: errMsg})
}

func mustMarshal(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
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
		return ext[1:]
	}
	return ""
}

func extractContent(raw string) string {
	hasStart := strings.Contains(raw, markerStart)
	hasEnd := strings.Contains(raw, markerEnd)
	switch {
	case hasStart && hasEnd:
		return extractBetween(raw)
	case hasEnd && !hasStart:
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
	inside, printed := false, false
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

func rollback(file, origContent string, origExisted bool, gitRoot, backup string) {
	if gitRoot != "" {
		if exec.Command("git", "-C", gitRoot, "ls-files", "--error-unmatch", file).Run() == nil {
			_ = exec.Command("git", "-C", gitRoot, "checkout", "--", file).Run()
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

// runValidate splits the validator template around {file} to prevent
// paths-with-spaces from being fragmented by strings.Fields.
func runValidate(vtmpl, file string) int {
	var parts []string
	if idx := strings.Index(vtmpl, "{file}"); idx >= 0 {
		parts = append(strings.Fields(vtmpl[:idx]), file)
		parts = append(parts, strings.Fields(vtmpl[idx+len("{file}"):])...)
	} else {
		parts = strings.Fields(vtmpl)
	}
	if len(parts) == 0 {
		return 0
	}
	c := exec.Command(parts[0], parts[1:]...)
	if err := c.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		return 1
	}
	return 0
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
		out, _ := exec.Command("diff", "-u", backup, file).CombinedOutput()
		for _, l := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++") {
				added++
			} else if strings.HasPrefix(l, "-") && !strings.HasPrefix(l, "---") {
				removed++
			}
		}
		return
	}
	newContent, _ := os.ReadFile(file)
	newLines := strings.Count(string(newContent), "\n")
	origLines := 0
	if origExisted {
		origLines = strings.Count(origContent, "\n")
	}
	if newLines > origLines {
		added = newLines - origLines
	} else {
		removed = origLines - newLines
	}
	return
}

func enumToTier(enum string) string { return dispatch.EnumToTier(enum) }
