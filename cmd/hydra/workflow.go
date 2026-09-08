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

	"github.com/spf13/cobra"

	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/rank"
	"github.com/ankit373/hydra/internal/runid"
	"github.com/ankit373/hydra/internal/workflow"
)

// dispatchRouter adapts the real router to workflow.Router. The workflow
// package deliberately does not depend on dispatch, so its runner is testable
// without a model; this is the one place the two meet.
type dispatchRouter struct {
	d      *dispatch.Dispatcher
	runID  string
	system string
}

func (r dispatchRouter) Route(ctx context.Context, prompt, enum string) (workflow.StepResult, error) {
	tier := ""
	if enum != "" {
		// A garbage enum must not fall through to unrestricted auto-routing,
		// the #501 rule, which applies to every caller of the map.
		if !dispatch.IsKnownEnum(enum) {
			return workflow.StepResult{}, fmt.Errorf("unknown enum %q for this step", enum)
		}
		tier = dispatch.EnumToTier(enum)
	}
	res, err := r.d.Dispatch(ctx, prompt, dispatch.Options{
		TierHint: tier,
		Enum:     enum,
		System:   r.system,
		RunID:    r.runID,
		TaskID:   runid.New(),
	})
	if err != nil {
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
			"written before every step so a killed run resumes where it stopped.",
	}
	cmd.AddCommand(cmdWorkflowRun(), cmdWorkflowList(), cmdWorkflowShow(), cmdWorkflowResume(), cmdWorkflowRemove())
	return cmd
}

func cmdWorkflowRun() *cobra.Command {
	var steps []string
	var enums []string
	var task, id, system string

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
			}
			if task == "" {
				task = built[0].Prompt
			}
			w, err := workflow.New(id, task, built)
			if err != nil {
				return err
			}
			w.RunID = w.ID
			return runWorkflow(cmd.Context(), w, system)
		},
	}
	cmd.Flags().StringArrayVar(&steps, "step", nil, "a step's prompt; repeat, in order")
	cmd.Flags().StringArrayVar(&enums, "enum", nil, "routing key for the step at the same position")
	cmd.Flags().StringVar(&task, "task", "", "what the whole workflow is for (default: the first step)")
	cmd.Flags().StringVar(&id, "id", "", "workflow id (default: generated)")
	cmd.Flags().StringVar(&system, "system", "", "system prompt applied to every step")
	return cmd
}

func cmdWorkflowResume() *cobra.Command {
	var system string
	cmd := &cobra.Command{
		Use:   "resume <id>",
		Short: "Continue a workflow from its first incomplete step",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := workflow.Load(args[0])
			if err != nil {
				return err
			}
			if _, ok := w.Next(); !ok {
				fmt.Printf("  %s\n", dimStyle.Render("nothing left to run; every step is done"))
				return nil
			}
			return runWorkflow(cmd.Context(), w, system)
		},
	}
	cmd.Flags().StringVar(&system, "system", "", "system prompt applied to every step")
	return cmd
}

// runWorkflow executes and renders one workflow.
func runWorkflow(ctx context.Context, w workflow.Workflow, system string) error {
	d, err := dispatch.New(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("\n  %s %s\n", cortexStyle.Render("▶ WORKFLOW"), w.ID)
	fmt.Printf("  %s\n\n", dimStyle.Render(w.Task))

	r := dispatchRouter{d: d, runID: w.RunID, system: system}
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
	printWorkflow(done)
	if err != nil {
		return err
	}
	return nil
}

func printWorkflow(w workflow.Workflow) {
	sep := dimStyle.Render("  " + strings.Repeat("─", 66))
	fmt.Println(sep)
	fmt.Printf("  %-3s %-28s %-10s %-18s %8s\n", "#", "STEP", "STATUS", "HEAD", "TIME")
	fmt.Println(sep)
	for _, s := range w.Steps {
		head := s.Model
		if head == "" {
			head = dimStyle.Render("—")
		}
		took := dimStyle.Render("—")
		if s.DurationMS > 0 {
			took = fmt.Sprintf("%.1fs", float64(s.DurationMS)/1000)
		}
		tier := ""
		if s.Tier > 0 {
			tier = fmt.Sprintf(" T%d", s.Tier)
		}
		fmt.Printf("  %-3d %-28.28s %-10s %-18.18s %8s\n",
			s.N, s.Title, statusLabel(s.Status), truncLabel(head+tier, 18), took)
		if s.Err != "" {
			fmt.Printf("      %s\n", warnStyle.Render("↳ "+oneLine(s.Err)))
		}
	}
	fmt.Println(sep)
	done, total := w.Progress()
	fmt.Printf("  %s %s  ·  %d/%d steps  ·  $%.4f\n\n",
		dimStyle.Render("Workflow →"), cortexStyle.Render(string(w.Status)), done, total, w.CostUSD())

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
			fmt.Printf("  %-22s %-10s %-8s %9s  %s\n", "ID", "STATUS", "STEPS", "COST", "TASK")
			fmt.Println(sep)
			for _, w := range list {
				done, total := w.Progress()
				fmt.Printf("  %-22.22s %-10s %-8s %9s  %-.30s\n",
					w.ID, statusLabel(w.Status),
					fmt.Sprintf("%d/%d", done, total),
					fmt.Sprintf("$%.4f", w.CostUSD()),
					oneLine(w.Task))
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
