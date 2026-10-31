// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/policy"
	"github.com/ankit373/hydra/internal/rank"
	"github.com/ankit373/hydra/internal/runid"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/workflow"
)

// dispatchRouter adapts the real router to workflow.Router. The workflow
// package deliberately does not depend on dispatch, so its runner is testable
// without a model; this is the one place the two meet.
type dispatchRouter struct {
	d          *dispatch.Dispatcher
	runID      string
	system     string
	localOnly  bool
	maxCostSet bool
	maxCost    float64

	// Injected so a test asserts the options a step really dispatches with.
	// Asserting on the builder alone leaves the call site free to rebuild them
	// by hand, which is the defect and not a variant of it (#996).
	dispatchFn func(context.Context, string, dispatch.Options) (*dispatch.Result, error)
}

// routerFor is the one reading of what a run dispatches under: its stored
// settings, which is what makes a resume continue the workflow it loaded
// rather than start a differently-configured one (#1168).
func routerFor(d *dispatch.Dispatcher, w workflow.Workflow) dispatchRouter {
	r := dispatchRouter{d: d, runID: w.RunID, system: w.System, localOnly: w.LocalOnly}
	if w.MaxCostUSD != nil {
		r.maxCostSet, r.maxCost = true, *w.MaxCostUSD
	}
	return r
}

// options is the dispatch one step runs as, with the policy that bounds it.
//
// A step is a dispatch and carries a dispatch's caps: #838 gave hyctl dispatch
// a ceiling for being "the command that actually spends the money", and a
// workflow spends it once per step with no guard at all (#1161).
func (r dispatchRouter) options(prompt, enum string) (dispatch.Options, policy.FilePolicy, error) {
	tier := ""
	if enum != "" {
		// A garbage enum must not fall through to unrestricted auto-routing,
		// the #501 rule, which applies to every caller of the map.
		if !dispatch.IsKnownEnum(enum) {
			return dispatch.Options{}, policy.FilePolicy{}, fmt.Errorf("unknown enum %q for this step", enum)
		}
		tier = dispatch.EnumToTier(enum)
	}
	enumTier, _ := dispatch.ResolveTier(tier)
	fp := policy.ForFile(config.ScriptHome(), policy.Spec{
		Prompt: prompt, PromptLength: len(prompt), EnumTier: enumTier,
	})
	ceiling, ceilingFrom := costCeiling(fp, r.maxCostSet, r.maxCost)
	return dispatch.Options{
		TierHint:      tier,
		Enum:          enum,
		System:        r.system,
		LocalOnly:     r.localOnly,
		RunID:         r.runID,
		TaskID:        runid.New(),
		MaxCostUSD:    ceiling,
		MaxCostSource: ceilingFrom,
		MaxMemoryMB:   fp.MaxMemoryMB,
		MaxCPUSeconds: fp.MaxCPUSeconds,
	}, fp, nil
}

func (r dispatchRouter) Route(ctx context.Context, prompt, enum string) (workflow.StepResult, error) {
	opts, fp, err := r.options(prompt, enum)
	if err != nil {
		return workflow.StepResult{}, err
	}
	ctx, cancel := fp.Deadline(ctx)
	defer cancel()

	run := r.dispatchFn
	if run == nil {
		run = r.d.Dispatch
	}
	res, err := run(ctx, prompt, opts)
	if err != nil {
		// A deadline is the policy refusing rather than the head failing, and
		// the two want different answers from whoever reads the step.
		if fp.Bounded(ctx) {
			return workflow.StepResult{}, errors.New(fp.WallExceeded())
		}
		return workflow.StepResult{}, err
	}
	out := workflow.StepResult{
		Output: res.Output,
		Head:   res.Head.ID,
		Model:  res.Head.Name,
		Tier:   rank.UITier(res.Head),
	}
	if res.Response != nil {
		out.CostUSD = r.d.EstimateCost(out.Tier, res.Response.InputTokens, res.Response.OutputTokens)
	}
	return out, nil
}

func cmdWorkflow() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workflow",
		Short: "Run a multi-step task, each step routed on its own",
		Long: "A workflow is an ordered list of steps. Each is dispatched separately, so\n" +
			"triage can run on a cheap head and the fix on a strong one, and state is\n" +
			"written before every step so a killed run resumes where it stopped.\n\n" +
			"Every step carries a dispatch's own caps, policy.yaml's max_cost_usd and\n" +
			"max_wall_seconds, and every prior step's output, each fenced.",
	}
	cmd.AddCommand(cmdWorkflowRun(), cmdWorkflowList(), cmdWorkflowShow(), cmdWorkflowResume(), cmdWorkflowRemove())
	return cmd
}

func cmdWorkflowRun() *cobra.Command {
	var steps []string
	var enums []string
	var task, id, system string
	var localOnly, dryRun bool
	var maxCost float64

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run a workflow from explicit steps",
		Example: "  hyctl workflow run --task \"fix the flaky test\" \\\n" +
			"    --step \"list the failing tests\" --enum SIMPLE \\\n" +
			"    --step \"explain why the first one fails\" --enum MODERATE \\\n" +
			"    --step \"write the fix\" --enum EXPERT",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(steps) == 0 {
				return errors.New("a workflow needs at least one --step")
			}
			// Positional pairing, so --enum N applies to --step N. Refused rather
			// than zipped short: a mismatch would silently route a step at a tier
			// the user meant for a different one.
			if len(enums) > 0 && len(enums) != len(steps) {
				return fmt.Errorf("%d --step and %d --enum given; pass one --enum per --step, or none",
					len(steps), len(enums))
			}
			built := make([]workflow.Step, 0, len(steps))
			for i, p := range steps {
				s := workflow.Step{Prompt: p}
				if i < len(enums) {
					s.Enum = strings.ToUpper(strings.TrimSpace(enums[i]))
					if s.Enum != "" && !dispatch.IsKnownEnum(s.Enum) {
						return fmt.Errorf("step %d: unknown --enum %q", i+1, s.Enum)
					}
				}
				built = append(built, s)
			}
			if id == "" {
				id = runid.New()
			} else if err := refuseStoredID(id); err != nil {
				return err
			}
			if task == "" {
				task = built[0].Prompt
			}
			w, err := workflow.New(id, task, built)
			if err != nil {
				return err
			}
			w.RunID = w.ID
			// Stored on the record, so a resume continues the workflow under
			// what it started with rather than under nothing (#1168).
			w.System, w.LocalOnly = system, localOnly
			if cmd.Flags().Changed("max-cost") {
				w.MaxCostUSD = &maxCost
			}
			if dryRun {
				return dryRunWorkflow(cmd.Context(), w)
			}
			return runWorkflow(cmd.Context(), w)
		},
	}
	cmd.Flags().StringArrayVar(&steps, "step", nil, "a step's prompt; repeat, in order")
	cmd.Flags().StringArrayVar(&enums, "enum", nil, "routing key for the step at the same position")
	cmd.Flags().StringVar(&task, "task", "", "what the whole workflow is for (default: the first step)")
	cmd.Flags().StringVar(&id, "id", "", "workflow id (default: generated)")
	cmd.Flags().StringVar(&system, "system", "", "system prompt applied to every step")
	cmd.Flags().BoolVar(&localOnly, "local", false, "force local-only heads for every step")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print each step's routing chain and run nothing")
	cmd.Flags().Float64Var(&maxCost, "max-cost", 0,
		"per-step cost ceiling in USD; overrides policy.yaml max_cost_usd, 0 lifts it")
	return cmd
}

// refuseStoredID is what `run --id` says about an id already stored: it
// overwrote the record and corrupted the trace of whatever was using it
// (#1168), and on a live one it started a second runner (#1162).
func refuseStoredID(id string) error {
	stored, err := workflow.Exists(id)
	if err != nil || !stored {
		return err
	}
	if w, lerr := workflow.Load(id); lerr == nil && w.Observed() == workflow.Running {
		return fmt.Errorf("workflow %s is already running; `hyctl workflow resume %s` continues it", id, id)
	}
	return fmt.Errorf("workflow %s already exists; continue it with `hyctl workflow resume %s`, or replace it with `hyctl workflow rm %s`",
		id, id, id)
}

func cmdWorkflowResume() *cobra.Command {
	var system string
	var localOnly bool
	var maxCost float64
	cmd := &cobra.Command{
		Use:   "resume <id>",
		Short: "Continue a workflow from its first incomplete step",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := workflow.Load(args[0])
			if err != nil {
				return err
			}
			// The stored settings are what the workflow started under; a flag
			// given here overrides one. Resume read neither, so a run started
			// with --system carried none from the resume on (#1168).
			if cmd.Flags().Changed("system") {
				w.System = system
			}
			if cmd.Flags().Changed("local") {
				w.LocalOnly = localOnly
			}
			if cmd.Flags().Changed("max-cost") {
				w.MaxCostUSD = &maxCost
			}
			// First, so nothing below writes to a record another process owns,
			// and so a refused resume opens no dispatcher and declares no run.
			// workflow.Run claims the heartbeat exclusively as the backstop,
			// which closes the gap between this read and acting on it (#1162).
			if w.Observed() == workflow.Running {
				return fmt.Errorf("%w: %s is being run by another process; wait for it to finish, or resume once its heartbeat has stopped",
					workflow.ErrAlreadyRunning, w.ID)
			}
			if _, ok := w.Next(); !ok {
				// Nothing is left, so the record is finished whatever status
				// the last writer stored. Left alone it read `interrupted` for
				// ever and only `rm` cleared it (#1168).
				if w.Status != workflow.Done {
					w.Status = workflow.Done
					if err := workflow.Save(w); err != nil {
						return err
					}
				}
				fmt.Printf("  %s\n", dimStyle.Render("nothing left to run; every step is done"))
				return nil
			}
			return runWorkflow(cmd.Context(), w)
		},
	}
	cmd.Flags().StringVar(&system, "system", "", "system prompt applied to every step (default: what it started with)")
	cmd.Flags().BoolVar(&localOnly, "local", false, "force local-only heads (default: what it started with)")
	cmd.Flags().Float64Var(&maxCost, "max-cost", 0, "per-step cost ceiling in USD (default: what it started with)")
	return cmd
}

// dryRunWorkflow prints the chain each step would route to and runs nothing.
// Nothing is stored either: a preview that saved a record would leave behind a
// workflow nobody started.
//
// A later step is previewed on its own prompt, since the prior output it would
// carry does not exist yet.
func dryRunWorkflow(ctx context.Context, w workflow.Workflow) error {
	d, err := dispatch.New(ctx)
	if err != nil {
		return err
	}
	defer d.Close()
	fmt.Printf("\n  %s %s\n", cortexStyle.Render("▶ WORKFLOW · dry run"), w.ID)
	fmt.Printf("  %s\n\n", dimStyle.Render(w.Task))

	r := routerFor(d, w)
	for _, s := range w.Steps {
		hint := s.Enum
		if hint == "" {
			hint = "auto"
		}
		fmt.Printf("  %s %s %s\n",
			dimStyle.Render(fmt.Sprintf("[%d/%d]", s.N, len(w.Steps))), s.Title, dimStyle.Render("· "+hint))
		opts, _, err := r.options(s.Prompt, s.Enum)
		if err != nil {
			return err
		}
		opts.DryRun = true
		res, err := d.Dispatch(ctx, s.Prompt, opts)
		if err != nil {
			return err
		}
		if opts.MaxCostUSD > 0 {
			fmt.Printf("      %s\n", dimStyle.Render(fmt.Sprintf("ceiling $%.4f (%s)", opts.MaxCostUSD, opts.MaxCostSource)))
		}
		fmt.Printf("      %s %s (score %d, %s)\n", cortexStyle.Render("→"), res.Head.Name, res.Head.CapScore, res.Head.Source)
		for j, f := range res.Fallbacks {
			fmt.Println(dimStyle.Render(fmt.Sprintf("        %d. %-28s score %d  %s", j+1, f.Name, f.CapScore, f.Source)))
		}
	}
	fmt.Println()
	return nil
}

// runWorkflow executes and renders one workflow.
func runWorkflow(ctx context.Context, w workflow.Workflow) error {
	d, err := dispatch.New(ctx)
	if err != nil {
		return err
	}
	defer d.Close()
	fmt.Printf("\n  %s %s\n", cortexStyle.Render("▶ WORKFLOW"), w.ID)
	fmt.Printf("  %s\n\n", dimStyle.Render(w.Task))

	// The workflow's task is what this run is about. Without it the cockpit
	// fell back to the first step's routing enum and listed the run as
	// "GRUNT" (#910).
	taskID := runid.New()
	runlog.DeclareRun(w.RunID, taskID, w.Task)
	defer runlog.FinishRun(w.RunID, taskID)

	r := routerFor(d, w)
	// Progress is printed from the saver rather than a separate hook: Run
	// already saves the moment a step goes Running, which is exactly the
	// transition worth announcing, so a long workflow does not go quiet.
	total := len(w.Steps)
	announced := map[int]bool{}
	save := func(x workflow.Workflow) error {
		for _, s := range x.Steps {
			if s.Status == workflow.Running && !announced[s.N] {
				announced[s.N] = true
				hint := s.Enum
				if hint == "" {
					hint = "auto"
				}
				fmt.Printf("  %s %s %s\n",
					dimStyle.Render(fmt.Sprintf("[%d/%d]", s.N, total)),
					s.Title, dimStyle.Render("· "+hint))
			}
		}
		return workflow.Save(x)
	}
	done, err := workflow.Run(ctx, w, r, save)
	if errors.Is(err, workflow.ErrAlreadyRunning) {
		// Nothing ran, so there is no table to print and no progress to claim.
		return err
	}
	printWorkflow(done)
	if err != nil {
		return err
	}
	return nil
}

// padCell pads s to n display cells, and rpadCell right-aligns in them.
//
// fmt's %-10s counts an ANSI escape as width, so every styled cell pushed the
// columns after it left on a colour terminal (#1168). lipgloss.Width is the
// same measurement internal/tui's own pad uses.
func padCell(s string, n int) string {
	if w := lipgloss.Width(s); w < n {
		return s + strings.Repeat(" ", n-w)
	}
	return s
}

func rpadCell(s string, n int) string {
	if w := lipgloss.Width(s); w < n {
		return strings.Repeat(" ", n-w) + s
	}
	return s
}

// workflowHeader and workflowRow are one definition of the step table's
// columns, so a row cannot drift from the header it sits under.
func workflowHeader() string {
	return "  " + padCell("#", 3) + " " + padCell("STEP", 28) + " " + padCell("STATUS", 10) +
		" " + padCell("TIER", 5) + " " + padCell("HEAD", 18) + " " + rpadCell("TIME", 8)
}

// Cells arrive already styled and already truncated: measuring is ANSI-aware,
// cutting is not, and a half-cut escape corrupts the frame.
func workflowRow(n int, title, status, tier, head, took string) string {
	return "  " + padCell(fmt.Sprintf("%d", n), 3) + " " + padCell(title, 28) + " " + padCell(status, 10) +
		" " + padCell(tier, 5) + " " + padCell(head, 18) + " " + rpadCell(took, 8)
}

// The same pairing for `hyctl workflow list`, whose STATUS column is styled too.
func workflowListHeader() string {
	return "  " + padCell("ID", 22) + " " + padCell("STATUS", 10) + " " + padCell("STEPS", 8) +
		" " + rpadCell("COST", 9) + "  TASK"
}

func workflowListRow(id, status, steps, cost, task string) string {
	return "  " + padCell(id, 22) + " " + padCell(status, 10) + " " + padCell(steps, 8) +
		" " + rpadCell(cost, 9) + "  " + task
}

func printWorkflow(w workflow.Workflow) {
	// Resolved once: a run recorded as running whose process is gone is
	// interrupted, and its in-flight step is not running either (#898).
	observed := w.Observed()
	sep := dimStyle.Render("  " + strings.Repeat("─", 66))
	fmt.Println(sep)
	// TIER has its own column: appended to HEAD it was the first thing the
	// 18-char truncation cut, and which tier answered is the most informative
	// part of the row.
	fmt.Println(workflowHeader())
	fmt.Println(sep)
	for _, s := range w.Steps {
		head := dimStyle.Render("—")
		if s.Model != "" {
			head = truncLabel(s.Model, 18)
		}
		took := dimStyle.Render("—")
		if s.DurationMS > 0 {
			took = fmt.Sprintf("%.1fs", float64(s.DurationMS)/1000)
		}
		tier := dimStyle.Render("—")
		if s.Tier > 0 {
			tier = fmt.Sprintf("T%d", s.Tier)
		}
		fmt.Println(workflowRow(s.N, truncLabel(s.Title, 28),
			statusLabel(workflow.ObservedStep(s.Status, observed)), tier, head, took))
		if s.Err != "" {
			fmt.Printf("      %s\n", warnStyle.Render("↳ "+oneLine(s.Err)))
		}
	}
	fmt.Println(sep)
	done, total := w.Progress()
	fmt.Printf("  %s %s  ·  %d/%d steps  ·  $%.4f\n\n",
		dimStyle.Render("Workflow →"), cortexStyle.Render(string(observed)), done, total, w.CostUSD())
	if observed == workflow.Interrupted {
		fmt.Printf("  %s\n\n", warnStyle.Render(
			"the process running this is gone; continue it with `hyctl workflow resume "+w.ID+"`"))
	}

	if last := lastOutput(w); last != "" {
		fmt.Println(last)
		fmt.Println()
	}
}

// lastOutput is the final completed step's output, which is the workflow's
// answer. Printing every step's output would bury it.
func lastOutput(w workflow.Workflow) string {
	for i := len(w.Steps) - 1; i >= 0; i-- {
		if w.Steps[i].Status == workflow.Done && w.Steps[i].Output != "" {
			return w.Steps[i].Output
		}
	}
	return ""
}

func statusLabel(s workflow.Status) string {
	switch s {
	case workflow.Done:
		return okStyle.Render("done")
	case workflow.Failed:
		return warnStyle.Render("failed")
	case workflow.Running:
		return cortexStyle.Render("running")
	case workflow.Interrupted:
		return warnStyle.Render("interrupted")
	default:
		return dimStyle.Render("pending")
	}
}

func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return truncLabel(s, 60)
}

func cmdWorkflowList() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List stored workflows, newest first",
		RunE: func(_ *cobra.Command, _ []string) error {
			list, err := workflow.List()
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(list)
			}
			if len(list) == 0 {
				fmt.Printf("\n  %s\n\n", dimStyle.Render("no workflows yet; start one with `hyctl workflow run --step ...`"))
				return nil
			}
			sep := dimStyle.Render("  " + strings.Repeat("─", 66))
			fmt.Println()
			fmt.Println(workflowListHeader())
			fmt.Println(sep)
			for _, w := range list {
				done, total := w.Progress()
				fmt.Println(workflowListRow(truncLabel(w.ID, 22), statusLabel(w.Observed()),
					fmt.Sprintf("%d/%d", done, total),
					fmt.Sprintf("$%.4f", w.CostUSD()),
					truncLabel(oneLine(w.Task), 30)))
			}
			fmt.Println()
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable JSON output")
	return cmd
}

func cmdWorkflowShow() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show one workflow's steps, heads and cost",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			w, err := workflow.Load(args[0])
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(w)
			}
			fmt.Printf("\n  %s %s  ·  %s\n", cortexStyle.Render("▶ WORKFLOW"), w.ID,
				dimStyle.Render(createdAge(w.Created)))
			fmt.Printf("  %s\n\n", dimStyle.Render(w.Task))
			printWorkflow(w)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable JSON output")
	return cmd
}

func cmdWorkflowRemove() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <id>",
		Short: "Delete a stored workflow",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := workflow.Delete(args[0]); err != nil {
				return err
			}
			fmt.Printf("  %s %s\n", dimStyle.Render("removed"), args[0])
			return nil
		},
	}
}

// createdAge renders a stored RFC3339Nano timestamp as an age, falling back to
// the raw value rather than hiding a timestamp it cannot parse. The rendering
// itself is humanAge, already in main.go.
func createdAge(ts string) string {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return ts
	}
	return humanAge(time.Since(t))
}
