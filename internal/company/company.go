// Package company implements the Hydra playbook state machine.
// It is the Go port of dispatch/company.sh.
package company

import (
	"bufio"
	"compress/gzip"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ankit373/hydra/internal/config"
)

// ── Data types ────────────────────────────────────────────────────────────────

// Invocation describes how a stage is executed.
type Invocation struct {
	Type  string      `json:"type" yaml:"type"`   // brain | skill | tier_dispatch | parallel
	Skill string      `json:"skill,omitempty" yaml:"skill,omitempty"`
	Mode  string      `json:"mode,omitempty" yaml:"mode,omitempty"`
	Items []ParallelItem `json:"items,omitempty" yaml:"items,omitempty"`
}

// ParallelItem is one item in a parallel stage.
type ParallelItem struct {
	Label string `json:"label" yaml:"label"`
	Skill string `json:"skill,omitempty" yaml:"skill,omitempty"`
	When  string `json:"when,omitempty" yaml:"when,omitempty"`
}

// Stage is one step in a playbook.
type Stage struct {
	ID          int        `json:"id"`
	Name        string     `json:"name" yaml:"name"`
	Invocation  Invocation `json:"invocation" yaml:"invocation"`
	When        string     `json:"when,omitempty" yaml:"when,omitempty"`
	Gate        string     `json:"gate,omitempty" yaml:"gate,omitempty"`
	InputsFrom  []string   `json:"inputs_from,omitempty" yaml:"inputs_from,omitempty"`
	Outputs     []string   `json:"outputs,omitempty" yaml:"outputs,omitempty"`
	BlockOn     []string   `json:"block_on,omitempty" yaml:"block_on,omitempty"`
}

// TicketInfo tracks ticket linkage.
type TicketInfo struct {
	Mode     string `json:"mode" yaml:"mode"`
	Ref      string `json:"ref,omitempty" yaml:"ref,omitempty"`
	Platform string `json:"platform,omitempty" yaml:"platform,omitempty"`
}

// RunInputs are the user-supplied inputs to a run.
type RunInputs struct {
	Intent              string     `json:"intent"`
	HasUI               bool       `json:"has_ui"`
	DevFacing           bool       `json:"dev_facing"`
	NeedsDesignSystem   bool       `json:"needs_design_system"`
	Market              string     `json:"market,omitempty"`
	ParentRunID         string     `json:"parent_run_id,omitempty"`
	Ticket              TicketInfo `json:"ticket"`
}

// Manifest is the immutable plan written at run start.
type Manifest struct {
	RunID     string    `json:"run_id"`
	Playbook  string    `json:"playbook"`
	Workspace string    `json:"workspace"`
	CreatedAt string    `json:"created_at"`
	Inputs    RunInputs `json:"inputs"`
	Stages    []Stage   `json:"stages"`
}

// CompletedStage is one entry in state.completed_stages.
type CompletedStage struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// BlockingFindings is set when a stage hard-blocks the run.
type BlockingFindings struct {
	Stage    string   `json:"stage"`
	Findings []string `json:"findings"`
}

// State is the mutable run state.
type State struct {
	RunID            string           `json:"run_id"`
	CurrentStage     int              `json:"current_stage"`
	Status           string           `json:"status"`
	CompletedStages  []CompletedStage `json:"completed_stages"`
	BlockingFindings *BlockingFindings `json:"blocking_findings,omitempty"`
	UpdatedAt        string           `json:"updated_at"`
}

// StageMeta is written per-stage by Complete.
type StageMeta struct {
	Status     string   `json:"status"`
	Note       string   `json:"note,omitempty"`
	FinishedAt string   `json:"finished_at"`
	Findings   []string `json:"findings"`
}

// NextResult is the output of Next.
type NextResult struct {
	Done       bool     `json:"done,omitempty"`
	RunID      string   `json:"run_id,omitempty"`
	Stage      *Stage   `json:"stage,omitempty"`
	StageDir   string   `json:"stage_dir,omitempty"`
	OutputDir  string   `json:"output_dir,omitempty"`
	InputsPaths []string `json:"inputs_paths,omitempty"`
	RunInputs  *RunInputs `json:"run_inputs,omitempty"`
}

// ── Playbook registry ─────────────────────────────────────────────────────────

type playbookStage struct {
	Name        string     `yaml:"name"`
	Invocation  Invocation `yaml:"invocation"`
	When        string     `yaml:"when"`
	Gate        string     `yaml:"gate"`
	InputsFrom  []string   `yaml:"inputs_from"`
	Outputs     []string   `yaml:"outputs"`
	BlockOn     []string   `yaml:"block_on"`
}

type playbookDef struct {
	RequiresTicketChoice bool            `yaml:"requires_ticket_choice"`
	Stages               []playbookStage `yaml:"stages"`
}

type playbooksFile struct {
	Playbooks map[string]playbookDef `yaml:"playbooks"`
}

func loadPlaybooks() (*playbooksFile, error) {
	path := filepath.Join(config.ScriptHome(), "registry", "playbooks.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("playbooks.yaml not found at %s", path)
	}
	var pf playbooksFile
	if err := yaml.Unmarshal(raw, &pf); err != nil {
		return nil, fmt.Errorf("parsing playbooks.yaml: %w", err)
	}
	return &pf, nil
}

// ── Start ─────────────────────────────────────────────────────────────────────

// StartOptions are the flags for Start.
type StartOptions struct {
	Intent             string
	Workspace          string
	HasUI              bool
	DevFacing          bool
	TicketMode         string
	TicketRef          string
	TicketPlatform     string
	NeedsDesignSystem  bool
	Market             string
	ParentRun          string
}

// Start creates a new run for the given playbook and returns the run_id.
func Start(playbook string, opts StartOptions) (string, error) {
	if opts.Intent == "" {
		return "", fmt.Errorf("--intent is required")
	}

	pf, err := loadPlaybooks()
	if err != nil {
		return "", err
	}
	pb, ok := pf.Playbooks[playbook]
	if !ok {
		return "", fmt.Errorf("playbook %q not found in playbooks.yaml", playbook)
	}
	if pb.RequiresTicketChoice && opts.TicketMode == "" && opts.TicketRef == "" {
		return "", fmt.Errorf("playbook %q requires an explicit ticket choice (--ticket-ref or --ticket-mode)", playbook)
	}
	if opts.TicketMode == "" {
		opts.TicketMode = "none"
	}
	if opts.TicketRef != "" && opts.TicketMode == "" {
		opts.TicketMode = "existing"
	}
	if opts.TicketPlatform == "" && opts.TicketRef != "" {
		opts.TicketPlatform = inferPlatform(opts.TicketRef)
	}

	if opts.Workspace == "" {
		opts.Workspace, _ = os.Getwd()
	}
	if _, err := os.Stat(opts.Workspace); err != nil {
		return "", fmt.Errorf("workspace dir does not exist: %s", opts.Workspace)
	}

	runID := genRunID(playbook)
	rd := runDir(opts.Workspace, runID)
	if err := os.MkdirAll(filepath.Join(rd, "stages"), 0o755); err != nil {
		return "", err
	}

	inputs := RunInputs{
		Intent:            opts.Intent,
		HasUI:             opts.HasUI,
		DevFacing:         opts.DevFacing,
		NeedsDesignSystem: opts.NeedsDesignSystem,
		Market:            opts.Market,
		ParentRunID:       opts.ParentRun,
		Ticket:            TicketInfo{Mode: opts.TicketMode, Ref: opts.TicketRef, Platform: opts.TicketPlatform},
	}

	stages := pruneStages(pb.Stages, inputs)

	manifest := Manifest{
		RunID:     runID,
		Playbook:  playbook,
		Workspace: opts.Workspace,
		CreatedAt: nowISO(),
		Inputs:    inputs,
		Stages:    stages,
	}
	if err := writeJSON(filepath.Join(rd, "manifest.json"), manifest); err != nil {
		return "", err
	}

	state := State{
		RunID:           runID,
		CurrentStage:    1,
		Status:          "pending",
		CompletedStages: []CompletedStage{},
		UpdatedAt:       nowISO(),
	}
	if err := writeJSON(filepath.Join(rd, "state.json"), state); err != nil {
		return "", err
	}

	_ = appendRunIndex(runID, opts.Workspace)
	_ = Ledger(opts.Workspace)

	return runID, nil
}

// ── Next ──────────────────────────────────────────────────────────────────────

// Next returns the next stage descriptor and marks it in_progress.
func Next(runID string) (*NextResult, error) {
	rd, err := FindRunDir(runID)
	if err != nil {
		return nil, err
	}

	state, err := readState(rd)
	if err != nil {
		return nil, err
	}
	if state.Status == "blocked" {
		return nil, fmt.Errorf("run %s is blocked (findings: %v)", runID, state.BlockingFindings)
	}

	manifest, err := readManifest(rd)
	if err != nil {
		return nil, err
	}

	if state.CurrentStage > len(manifest.Stages) {
		return &NextResult{Done: true}, nil
	}

	stage := manifest.Stages[state.CurrentStage-1]
	stageID := fmt.Sprintf("%02d", stage.ID)
	stageDir := filepath.Join(rd, "stages", stageID+"_"+stage.Name)
	_ = os.MkdirAll(filepath.Join(stageDir, "output"), 0o755)

	// Resolve inputs_paths
	var inputsPaths []string
	for _, ref := range stage.InputsFrom {
		if ref == "ALL" {
			inputsPaths = append(inputsPaths, filepath.Join(rd, "stages"))
		} else {
			// Find stage dir matching the name
			entries, _ := os.ReadDir(filepath.Join(rd, "stages"))
			for _, e := range entries {
				if e.IsDir() && strings.HasSuffix(e.Name(), "_"+ref) {
					inputsPaths = append(inputsPaths, filepath.Join(rd, "stages", e.Name()))
					break
				}
			}
		}
	}

	// Mark in_progress
	now := nowISO()
	_ = writeJSON(filepath.Join(stageDir, "meta.json"), map[string]string{
		"status": "in_progress", "started_at": now,
	})
	state.Status = "in_progress"
	state.UpdatedAt = now
	_ = writeJSON(filepath.Join(rd, "state.json"), state)

	return &NextResult{
		RunID:       runID,
		Stage:       &stage,
		StageDir:    stageDir,
		OutputDir:   filepath.Join(stageDir, "output"),
		InputsPaths: inputsPaths,
		RunInputs:   &manifest.Inputs,
	}, nil
}

// ── Complete ──────────────────────────────────────────────────────────────────

// CompleteOptions are the flags for Complete.
type CompleteOptions struct {
	Status   string   // ok | fail | skipped
	Findings []string // severity strings
	Note     string
}

// Complete marks a stage done and advances state.
func Complete(runID, stageName string, opts CompleteOptions) (*State, error) {
	if opts.Status == "" {
		opts.Status = "ok"
	}
	rd, err := FindRunDir(runID)
	if err != nil {
		return nil, err
	}
	manifest, err := readManifest(rd)
	if err != nil {
		return nil, err
	}
	state, err := readState(rd)
	if err != nil {
		return nil, err
	}

	stage, stageIdx := findStageByName(manifest.Stages, stageName)
	if stageIdx < 0 {
		return nil, fmt.Errorf("no stage %q in run %s", stageName, runID)
	}

	stageID := fmt.Sprintf("%02d", stage.ID)
	stageDir := filepath.Join(rd, "stages", stageID+"_"+stageName)
	_ = os.MkdirAll(stageDir, 0o755)

	now := nowISO()
	meta := StageMeta{
		Status:     opts.Status,
		Note:       opts.Note,
		FinishedAt: now,
		Findings:   opts.Findings,
	}
	if meta.Findings == nil {
		meta.Findings = []string{}
	}
	_ = writeJSON(filepath.Join(stageDir, "meta.json"), meta)

	// Check block_on
	if len(stage.BlockOn) > 0 && len(opts.Findings) > 0 {
		intersection := intersect(stage.BlockOn, opts.Findings)
		if len(intersection) > 0 {
			state.Status = "blocked"
			state.BlockingFindings = &BlockingFindings{Stage: stageName, Findings: intersection}
			state.UpdatedAt = now
			_ = writeJSON(filepath.Join(rd, "state.json"), state)
			_ = Ledger(manifest.Workspace)
			return state, nil
		}
	}

	// Advance
	state.CompletedStages = append(state.CompletedStages, CompletedStage{Name: stageName, Status: opts.Status})
	state.CurrentStage++
	state.Status = "pending"
	state.UpdatedAt = now
	_ = writeJSON(filepath.Join(rd, "state.json"), state)
	_ = Ledger(manifest.Workspace)

	return state, nil
}

// ── Status ────────────────────────────────────────────────────────────────────

// PrintStatus prints a human-readable progress table.
func PrintStatus(runID string) error {
	rd, err := FindRunDir(runID)
	if err != nil {
		return err
	}
	manifest, err := readManifest(rd)
	if err != nil {
		return err
	}
	state, err := readState(rd)
	if err != nil {
		return err
	}

	fmt.Printf("Run:       %s\n", manifest.RunID)
	fmt.Printf("Playbook:  %s\n", manifest.Playbook)
	fmt.Printf("Workspace: %s\n", manifest.Workspace)
	fmt.Printf("Status:    %s\n", state.Status)
	fmt.Printf("Progress:  %d/%d\n\n", state.CurrentStage, len(manifest.Stages))
	fmt.Println("Stages:")

	for i, s := range manifest.Stages {
		mark := "·"
		completed := completedStatus(state, s.Name)
		if completed != "" {
			switch completed {
			case "ok":
				mark = "✓"
			case "skipped":
				mark = "∅"
			case "fail":
				mark = "✗"
			default:
				mark = completed
			}
		} else if i+1 == state.CurrentStage {
			mark = "▶"
		}
		gate := s.Gate
		if gate == "" {
			gate = "auto"
		}
		fmt.Printf("  %s %2d. %-22s (%s, %s)\n", mark, i+1, s.Name, s.Invocation.Type, gate)
	}

	if state.BlockingFindings != nil {
		fmt.Printf("\n🚫 BLOCKED: stage=%s findings=%v\n", state.BlockingFindings.Stage, state.BlockingFindings.Findings)
	}
	return nil
}

// ── List ──────────────────────────────────────────────────────────────────────

// ListRun is one entry in List output.
type ListRun struct {
	RunID    string `json:"run_id"`
	Playbook string `json:"playbook"`
	Status   string `json:"status"`
}

// List returns runs in a workspace.
func List(workspace string) ([]ListRun, error) {
	if workspace == "" {
		workspace, _ = os.Getwd()
	}
	runsDir := filepath.Join(workspace, ".hydra", "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		return nil, nil // no runs
	}
	var result []ListRun
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		rd := filepath.Join(runsDir, e.Name())
		state, err := readState(rd)
		if err != nil {
			continue
		}
		manifest, err := readManifest(rd)
		if err != nil {
			continue
		}
		result = append(result, ListRun{
			RunID:    e.Name(),
			Playbook: manifest.Playbook,
			Status:   state.Status,
		})
	}
	return result, nil
}

// ── Show ──────────────────────────────────────────────────────────────────────

// ShowResult is the output of Show.
type ShowResult struct {
	Manifest Manifest `json:"manifest"`
	State    State    `json:"state"`
}

// Show returns manifest + state for a run.
func Show(runID string) (*ShowResult, error) {
	rd, err := FindRunDir(runID)
	if err != nil {
		return nil, err
	}
	manifest, err := readManifest(rd)
	if err != nil {
		return nil, err
	}
	state, err := readState(rd)
	if err != nil {
		return nil, err
	}
	return &ShowResult{Manifest: *manifest, State: *state}, nil
}

// ── Worklog ───────────────────────────────────────────────────────────────────

// Worklog returns a markdown section for one run.
func Worklog(runID string) (string, error) {
	rd, err := FindRunDir(runID)
	if err != nil {
		return "", err
	}
	manifest, err := readManifest(rd)
	if err != nil {
		return "", err
	}
	state, err := readState(rd)
	if err != nil {
		return "", err
	}

	dateShort := manifest.CreatedAt
	if len(dateShort) >= 10 {
		dateShort = dateShort[:10]
	}

	runStatus := state.Status
	statusEmoji := "·"
	switch runStatus {
	case "pending", "in_progress":
		statusEmoji = "🟢"
	case "blocked":
		statusEmoji = "🚫"
	case "completed":
		statusEmoji = "✅"
	case "failed":
		statusEmoji = "✗"
	}
	if state.CurrentStage > len(manifest.Stages) {
		runStatus = "completed"
		statusEmoji = "✅"
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "### %s — %s\n\n", dateShort, manifest.Playbook)
	fmt.Fprintf(&sb, "**Intent:** %s  \n", manifest.Inputs.Intent)
	fmt.Fprintf(&sb, "**Run:** `%s`  \n", runID)
	fmt.Fprintf(&sb, "**Status:** %s %s (%d/%d)  \n", statusEmoji, runStatus, state.CurrentStage, len(manifest.Stages))
	if manifest.Inputs.ParentRunID != "" {
		fmt.Fprintf(&sb, "**Parent run:** `%s`  \n", manifest.Inputs.ParentRunID)
	}
	if manifest.Inputs.Ticket.Ref != "" {
		fmt.Fprintf(&sb, "**Ticket:** %s _(%s)_  \n", manifest.Inputs.Ticket.Ref, manifest.Inputs.Ticket.Platform)
	} else if manifest.Inputs.Ticket.Mode == "create_after_plan" {
		fmt.Fprintf(&sb, "**Ticket:** _(will draft after finalize_plan)_  \n")
	}
	fmt.Fprintf(&sb, "**Artifacts:** `.hydra/runs/%s/`\n\n", runID)

	if state.BlockingFindings != nil {
		fmt.Fprintf(&sb, "> 🚫 **Blocked at `%s`** — findings: %s\n\n",
			state.BlockingFindings.Stage, strings.Join(state.BlockingFindings.Findings, ", "))
	}

	fmt.Fprintln(&sb, "**Stages:**")
	for i, s := range manifest.Stages {
		mark := "[ ]"
		note := ""
		completed := completedStatus(state, s.Name)
		if completed != "" {
			switch completed {
			case "ok":
				mark = "[x]"
			case "skipped":
				mark = "[~]"
				note = " _(skipped)_"
			case "fail":
				mark = "[!]"
				note = " _(failed)_"
			default:
				mark = "[?]"
			}
			// Read meta note/findings
			stageID := fmt.Sprintf("%02d", i+1)
			metaPath := filepath.Join(rd, "stages", stageID+"_"+s.Name, "meta.json")
			if raw, err := os.ReadFile(metaPath); err == nil {
				var meta StageMeta
				if json.Unmarshal(raw, &meta) == nil {
					if meta.Note != "" {
						note += " — " + meta.Note
					}
					if len(meta.Findings) > 0 {
						note += " · findings: " + strings.Join(meta.Findings, ", ")
					}
				}
			}
		} else if i+1 == state.CurrentStage && runStatus == "in_progress" {
			mark = "[▶]"
			note = " _(in progress)_"
		}

		if s.Invocation.Type == "parallel" {
			labels := make([]string, 0, len(s.Invocation.Items))
			for _, it := range s.Invocation.Items {
				labels = append(labels, it.Label)
			}
			fmt.Fprintf(&sb, "- %s **%s** _(%s)_%s\n", mark, s.Name, strings.Join(labels, ", "), note)
		} else {
			fmt.Fprintf(&sb, "- %s **%s**%s\n", mark, s.Name, note)
		}
	}
	fmt.Fprintln(&sb)
	return sb.String(), nil
}

// ── Ledger ────────────────────────────────────────────────────────────────────

// Ledger regenerates <workspace>/HYDRA.md from all runs.
func Ledger(workspace string) error {
	if workspace == "" {
		workspace, _ = os.Getwd()
	}
	runsDir := filepath.Join(workspace, ".hydra", "runs")

	var open, blocked, completed, failed []string
	if entries, err := os.ReadDir(runsDir); err == nil {
		// Sort descending (run_id is YYYYMMDD-... so lexicographic = chronological)
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].Name() > entries[j].Name()
		})
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			rd := filepath.Join(runsDir, e.Name())
			state, err := readState(rd)
			if err != nil {
				continue
			}
			manifest, err := readManifest(rd)
			if err != nil {
				continue
			}
			id := e.Name()
			switch {
			case state.Status == "blocked":
				blocked = append(blocked, id)
			case state.CurrentStage > len(manifest.Stages):
				completed = append(completed, id)
			case state.Status == "failed":
				failed = append(failed, id)
			default:
				open = append(open, id)
			}
		}
	}

	out := filepath.Join(workspace, "HYDRA.md")
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()

	fmt.Fprintf(f, "# Hydra worklog — %s\n\n", filepath.Base(workspace))
	fmt.Fprintln(f, "_Auto-maintained by Hydra. Each run gets a section below._")
	fmt.Fprintln(f, "_Do not hand-edit between `<!-- HYDRA-AUTO -->` markers — they get overwritten._")
	fmt.Fprintln(f)
	fmt.Fprintln(f, "<!-- HYDRA-AUTO -->")
	fmt.Fprintln(f)

	writeSection := func(emoji, title string, ids []string) {
		if len(ids) == 0 {
			return
		}
		fmt.Fprintf(f, "## %s %s\n\n", emoji, title)
		for _, id := range ids {
			wl, err := Worklog(id)
			if err == nil {
				fmt.Fprintln(f, wl)
			}
		}
	}

	writeSection("🟢", "Open", open)
	writeSection("🚫", "Blocked", blocked)
	writeSection("✅", "Completed", completed)
	writeSection("✗", "Failed", failed)

	if len(open)+len(blocked)+len(completed)+len(failed) == 0 {
		fmt.Fprintln(f, "_(no runs yet)_")
	}

	fmt.Fprintln(f, "<!-- /HYDRA-AUTO -->")
	return nil
}

// ── Ticket ────────────────────────────────────────────────────────────────────

// SetTicket sets the ticket ref on an existing run.
func SetTicket(runID, ref, platform string) error {
	rd, err := FindRunDir(runID)
	if err != nil {
		return err
	}
	manifest, err := readManifest(rd)
	if err != nil {
		return err
	}
	if platform == "" {
		platform = inferPlatform(ref)
	}
	manifest.Inputs.Ticket = TicketInfo{Mode: "existing", Ref: ref, Platform: platform}
	if err := writeJSON(filepath.Join(rd, "manifest.json"), manifest); err != nil {
		return err
	}
	return Ledger(manifest.Workspace)
}

// TicketCommentResult is the return value of TicketComment.
type TicketCommentResult struct {
	Action   string `json:"action,omitempty"`
	Ticket   string `json:"ticket,omitempty"`
	Body     string `json:"body,omitempty"`
	RunID    string `json:"run_id,omitempty"`
	MCPTool  string `json:"mcp_tool,omitempty"`
}

// TicketComment posts a comment to the linked ticket.
// For GitHub, shells out to `gh`. For Jira, returns a directive for the brain.
func TicketComment(runID, body string, dryRun bool) (*TicketCommentResult, int, error) {
	rd, err := FindRunDir(runID)
	if err != nil {
		return nil, 1, err
	}
	manifest, err := readManifest(rd)
	if err != nil {
		return nil, 1, err
	}
	ref := manifest.Inputs.Ticket.Ref
	platform := manifest.Inputs.Ticket.Platform
	if ref == "" {
		return nil, 1, fmt.Errorf("run %s has no linked ticket", runID)
	}

	switch platform {
	case "github":
		if dryRun {
			fmt.Printf("[dry-run] gh issue comment %s --body ...\n", ref)
			return nil, 0, nil
		}
		out, err := exec.Command("gh", "issue", "comment", ref, "--body", body).CombinedOutput()
		if err != nil {
			return nil, 1, fmt.Errorf("gh failed: %s", string(out))
		}
		return nil, 0, nil
	case "jira":
		result := &TicketCommentResult{
			Action:  "jira_comment",
			Ticket:  ref,
			Body:    body,
			RunID:   runID,
			MCPTool: "addCommentToJiraIssue",
		}
		return result, 2, nil // exit 2 = brain directive
	default:
		return nil, 1, fmt.Errorf("unsupported platform: %s", platform)
	}
}

// ── Prune ─────────────────────────────────────────────────────────────────────

// PruneOptions are the flags for Prune.
type PruneOptions struct {
	OlderThan          string // e.g. "30d"
	IncludeCompleted   bool
	DryRun             bool
}

// Prune deletes finished runs older than a threshold.
func Prune(workspace string, opts PruneOptions) (pruned, kept int, err error) {
	if workspace == "" {
		workspace, _ = os.Getwd()
	}
	if opts.OlderThan == "" {
		opts.OlderThan = "30d"
	}
	cutoffSecs := parseDuration(opts.OlderThan)
	cutoff := time.Now().Add(-time.Duration(cutoffSecs) * time.Second)

	runsDir := filepath.Join(workspace, ".hydra", "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		return 0, 0, nil
	}

	indexPath := filepath.Join(config.Dir(), "logs", "runs.index")

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		rd := filepath.Join(runsDir, e.Name())
		state, err := readState(rd)
		if err != nil {
			continue
		}
		manifest, err := readManifest(rd)
		if err != nil {
			continue
		}

		// Never prune open runs
		isOpen := (state.Status == "pending" || state.Status == "in_progress") && state.CurrentStage <= len(manifest.Stages)
		if isOpen {
			kept++
			continue
		}

		isCompleted := state.CurrentStage > len(manifest.Stages)
		if isCompleted && !opts.IncludeCompleted {
			kept++
			continue
		}

		// Age check
		updatedAt, err := time.Parse(time.RFC3339, state.UpdatedAt)
		if err != nil {
			kept++
			continue
		}
		if updatedAt.After(cutoff) {
			kept++
			continue
		}

		if opts.DryRun {
			fmt.Printf("[dry-run] would delete %s\n", rd)
		} else {
			_ = os.RemoveAll(rd)
			_ = removeFromIndex(indexPath, e.Name())
		}
		pruned++
	}

	if !opts.DryRun && pruned > 0 {
		_ = Ledger(workspace)
	}
	return pruned, kept, nil
}

// ── Children ──────────────────────────────────────────────────────────────────

// ChildRun is one entry in Children output.
type ChildRun struct {
	RunID    string `json:"run_id"`
	Playbook string `json:"playbook"`
	Status   string `json:"status"`
	Current  int    `json:"current_stage"`
	Total    int    `json:"total_stages"`
	Intent   string `json:"intent"`
}

// Children returns all runs whose parent_run_id == parentRunID.
func Children(parentRunID string) ([]ChildRun, error) {
	rd, err := FindRunDir(parentRunID)
	if err != nil {
		return nil, err
	}
	manifest, err := readManifest(rd)
	if err != nil {
		return nil, err
	}
	runsDir := filepath.Join(manifest.Workspace, ".hydra", "runs")
	entries, _ := os.ReadDir(runsDir)

	var result []ChildRun
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		crd := filepath.Join(runsDir, e.Name())
		cm, err := readManifest(crd)
		if err != nil {
			continue
		}
		if cm.Inputs.ParentRunID != parentRunID {
			continue
		}
		cs, _ := readState(crd)
		cur, total := 0, len(cm.Stages)
		if cs != nil {
			cur = cs.CurrentStage
		}
		result = append(result, ChildRun{
			RunID:    e.Name(),
			Playbook: cm.Playbook,
			Status:   cs.Status,
			Current:  cur,
			Total:    total,
			Intent:   cm.Inputs.Intent,
		})
	}
	return result, nil
}

// ── RotateLogs ────────────────────────────────────────────────────────────────

// RotateLogs rotates log files that exceed maxSize, keeping keep generations.
func RotateLogs(maxSizeStr string, keep int) (int, error) {
	if maxSizeStr == "" {
		maxSizeStr = "5M"
	}
	if keep <= 0 {
		keep = 5
	}
	maxBytes := parseSize(maxSizeStr)
	logDir := filepath.Join(config.Dir(), "logs")
	entries, _ := os.ReadDir(logDir)

	rotated := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		logPath := filepath.Join(logDir, e.Name())
		info, err := os.Stat(logPath)
		if err != nil || info.Size() < maxBytes {
			continue
		}

		// Shift existing rotations
		for i := keep; i >= 1; i-- {
			cur := fmt.Sprintf("%s.%d.gz", logPath, i)
			nxt := fmt.Sprintf("%s.%d.gz", logPath, i+1)
			if _, err := os.Stat(cur); err == nil {
				if i >= keep {
					_ = os.Remove(cur)
				} else {
					_ = os.Rename(cur, nxt)
				}
			}
		}

		// Compress current → .1.gz
		if err := gzipFile(logPath, logPath+".1.gz"); err == nil {
			_ = os.Truncate(logPath, 0)
			rotated++
		}
	}
	return rotated, nil
}

// ── Internal helpers ──────────────────────────────────────────────────────────

func genRunID(playbook string) string {
	ts := time.Now().UTC().Format("20060102-1504")
	slug := slugify(playbook)
	b := make([]byte, 2)
	_, _ = rand.Read(b)
	rand4 := hex.EncodeToString(b)[:4]
	return ts + "-" + slug + "-" + rand4
}

func slugify(s string) string {
	s = strings.ToLower(strings.ReplaceAll(s, "_", "-"))
	if len(s) > 12 {
		s = s[:12]
	}
	return s
}

func runDir(workspace, runID string) string {
	return filepath.Join(workspace, ".hydra", "runs", runID)
}

// FindRunDir resolves the run directory for a run_id.
func FindRunDir(runID string) (string, error) {
	// 1. Try global runs index
	indexPath := filepath.Join(config.Dir(), "logs", "runs.index")
	if raw, err := os.ReadFile(indexPath); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			parts := strings.SplitN(strings.TrimSpace(line), "\t", 2)
			if len(parts) == 2 && parts[0] == runID {
				candidate := filepath.Join(parts[1], ".hydra", "runs", runID)
				if _, err := os.Stat(candidate); err == nil {
					return candidate, nil
				}
			}
		}
	}

	// 2. Try workspace registry
	wsFile := filepath.Join(config.ScriptHome(), "registry", "workspace.yaml")
	if raw, err := os.ReadFile(wsFile); err == nil {
		var ws struct {
			Workspaces map[string]struct {
				Root string `yaml:"root"`
			} `yaml:"workspaces"`
		}
		if yaml.Unmarshal(raw, &ws) == nil {
			for _, w := range ws.Workspaces {
				candidate := filepath.Join(w.Root, ".hydra", "runs", runID)
				if _, err := os.Stat(candidate); err == nil {
					return candidate, nil
				}
			}
		}
	}

	// 3. Try PWD
	pwd, _ := os.Getwd()
	candidate := filepath.Join(pwd, ".hydra", "runs", runID)
	if _, err := os.Stat(candidate); err == nil {
		return candidate, nil
	}

	return "", fmt.Errorf("run %s not found", runID)
}

func readManifest(rd string) (*Manifest, error) {
	var m Manifest
	if err := readJSON(filepath.Join(rd, "manifest.json"), &m); err != nil {
		return nil, fmt.Errorf("reading manifest: %w", err)
	}
	return &m, nil
}

func readState(rd string) (*State, error) {
	var s State
	if err := readJSON(filepath.Join(rd, "state.json"), &s); err != nil {
		return nil, fmt.Errorf("reading state: %w", err)
	}
	return &s, nil
}

func readJSON(path string, v any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

func writeJSON(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func nowISO() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func pruneStages(pbStages []playbookStage, inputs RunInputs) []Stage {
	var result []Stage
	id := 1
	for _, ps := range pbStages {
		if !evalWhen(ps.When, inputs) {
			continue
		}
		// Filter parallel items by their own when:
		items := ps.Invocation.Items
		if ps.Invocation.Type == "parallel" && len(items) > 0 {
			var filtered []ParallelItem
			for _, item := range items {
				if evalWhen(item.When, inputs) {
					filtered = append(filtered, item)
				}
			}
			ps.Invocation.Items = filtered
		}
		s := Stage{
			ID:   id,
			Name: ps.Name,
			Invocation: Invocation{
				Type:  ps.Invocation.Type,
				Skill: ps.Invocation.Skill,
				Mode:  ps.Invocation.Mode,
				Items: ps.Invocation.Items,
			},
			When:       ps.When,
			Gate:       ps.Gate,
			InputsFrom: ps.InputsFrom,
			Outputs:    ps.Outputs,
			BlockOn:    ps.BlockOn,
		}
		result = append(result, s)
		id++
	}
	return result
}

// evalWhen evaluates a when: clause against RunInputs.
// Supports bare boolean keys (has_ui, dev_facing), "always", or "" → true.
func evalWhen(condition string, inputs RunInputs) bool {
	if condition == "" || condition == "always" || condition == "null" {
		return true
	}
	switch condition {
	case "has_ui":
		return inputs.HasUI
	case "dev_facing":
		return inputs.DevFacing
	case "needs_design_system":
		return inputs.NeedsDesignSystem
	}
	// Unknown → let brain handle (don't auto-skip)
	return true
}

func completedStatus(state *State, stageName string) string {
	for _, cs := range state.CompletedStages {
		if cs.Name == stageName {
			return cs.Status
		}
	}
	return ""
}

func findStageByName(stages []Stage, name string) (Stage, int) {
	for i, s := range stages {
		if s.Name == name {
			return s, i
		}
	}
	return Stage{}, -1
}

func intersect(a, b []string) []string {
	set := map[string]bool{}
	for _, v := range a {
		set[v] = true
	}
	var result []string
	for _, v := range b {
		if set[v] {
			result = append(result, v)
		}
	}
	return result
}

func inferPlatform(ref string) string {
	switch {
	case strings.Contains(ref, "github.com"):
		return "github"
	case strings.Contains(ref, "atlassian.net"):
		return "jira"
	case strings.Contains(ref, "-"):
		return "jira" // Jira-style keys like PROJ-123
	default:
		return "unknown"
	}
}

func appendRunIndex(runID, workspace string) error {
	path := filepath.Join(config.Dir(), "logs", "runs.index")
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s\t%s\n", runID, workspace)
	return err
}

func removeFromIndex(indexPath, runID string) error {
	raw, err := os.ReadFile(indexPath)
	if err != nil {
		return err
	}
	var lines []string
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, runID+"\t") {
			lines = append(lines, line)
		}
	}
	return os.WriteFile(indexPath, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}

func parseDuration(s string) int64 {
	if len(s) < 2 {
		v, _ := strconv.ParseInt(s, 10, 64)
		return v
	}
	n, _ := strconv.ParseInt(s[:len(s)-1], 10, 64)
	switch s[len(s)-1] {
	case 'd', 'D':
		return n * 86400
	case 'h', 'H':
		return n * 3600
	case 'm', 'M':
		return n * 60
	default:
		v, _ := strconv.ParseInt(s, 10, 64)
		return v
	}
}

func parseSize(s string) int64 {
	if len(s) < 2 {
		v, _ := strconv.ParseInt(s, 10, 64)
		return v
	}
	last := s[len(s)-1]
	n, _ := strconv.ParseInt(s[:len(s)-1], 10, 64)
	switch last {
	case 'K', 'k':
		return n * 1024
	case 'M', 'm':
		return n * 1024 * 1024
	case 'G', 'g':
		return n * 1024 * 1024 * 1024
	default:
		v, _ := strconv.ParseInt(s, 10, 64)
		return v
	}
}

func gzipFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	gz := gzip.NewWriter(out)
	defer gz.Close()
	_, err = io.Copy(gz, in)
	return err
}
