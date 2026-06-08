package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/ankit373/hydra/internal/build"
	"github.com/ankit373/hydra/internal/company"
	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/cost"
	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/editor"
	"github.com/ankit373/hydra/internal/parallel"
	"github.com/ankit373/hydra/internal/probe"
	"github.com/ankit373/hydra/internal/review"
	"github.com/ankit373/hydra/internal/tui"
	"github.com/ankit373/hydra/internal/update"

	_ "github.com/ankit373/hydra/internal/provider/cli"
	_ "github.com/ankit373/hydra/internal/provider/env"
	_ "github.com/ankit373/hydra/internal/provider/port"
	_ "github.com/ankit373/hydra/internal/provider/agy"
)

func main() {
	// Fire update check in the background — never blocks startup.
	updateCh := update.CheckAsync()

	if err := rootCmd().Execute(); err != nil {
		os.Exit(1)
	}

	select {
	case latest := <-updateCh:
		if latest != "" {
			fmt.Fprintf(os.Stderr, "\n  %s  hydra %s is available → brew upgrade hydra\n",
				lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Render("✦"),
				latest,
			)
		}
	default:
		// Check still in flight — skip notification rather than block.
	}
}

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "hydra",
		Short: "Multi-model AI orchestration — one Cortex, many Heads",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !config.Exists() {
				return runInit()
			}
			return cmd.Help()
		},
	}
	root.AddCommand(
		cmdInit(), cmdProbe(), cmdStatus(), cmdDispatch(),
		cmdEdit(), cmdReview(), cmdParallel(), cmdCost(), cmdRun(),
		cmdVersion(),
	)
	return root
}

// ── version ───────────────────────────────────────────────────────────────────

func cmdVersion() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version, commit, and build info",
		Run: func(_ *cobra.Command, _ []string) {
			fmt.Printf("  hydra %s\n", build.Version)
			fmt.Printf("  commit:  %s\n", build.Commit)
			fmt.Printf("  built:   %s\n", build.Date)
			fmt.Printf("  by:      %s\n", build.BuiltBy)
			// Update notice is printed by main() after Execute returns.
		},
	}
}

// ── init ──────────────────────────────────────────────────────────────────────

func cmdInit() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "First-run wizard: discover Heads and choose your Cortex",
		RunE:  func(_ *cobra.Command, _ []string) error { return runInit() },
	}
}

func runInit() error {
	fmt.Println(dimStyle.Render("  Scanning your machine for AI models..."))
	result := probe.Run(context.Background())

	if len(result.Heads) == 0 {
		// Nothing found — guide the user through installing something.
		p := tea.NewProgram(tui.NewInstallModel(), tea.WithAltScreen())
		_, err := p.Run()
		return err
	}

	m := tui.NewInitModel(result)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

// ── probe ─────────────────────────────────────────────────────────────────────

func cmdProbe() *cobra.Command {
	return &cobra.Command{
		Use:   "probe",
		Short: "Scan machine for available AI Heads",
		RunE: func(_ *cobra.Command, _ []string) error {
			fmt.Println(dimStyle.Render("  Scanning..."))
			result := probe.Run(context.Background())
			cortexName := "none"
			if result.Cortex != nil {
				cortexName = result.Cortex.Name
			}
			fmt.Println(tui.Splash(cortexName))
			if len(result.Heads) == 0 {
				fmt.Println("  No models found.")
				return nil
			}
			fmt.Printf("  %-30s  %-5s  %-5s  %s\n", "Head", "Score", "Src", "Provider")
			fmt.Println("  " + strings.Repeat("─", 56))
			for _, h := range result.Heads {
				marker := "  "
				if result.Cortex != nil && h.ID == result.Cortex.ID {
					marker = cortexStyle.Render("→ ")
				}
				fmt.Printf("%s%-30s  %-5d  %-5s  %s\n", marker, h.Name, h.CapScore, h.Source, h.Provider)
			}
			return nil
		},
	}
}

// ── status ────────────────────────────────────────────────────────────────────

func cmdStatus() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show current Cortex and Head configuration",
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("no config found — run: hydra init")
			}

			fmt.Println()
			fmt.Printf("  %s  %s\n",
				dimStyle.Render("Cortex :"),
				cortexStyle.Render(cfg.Cortex),
			)
			fmt.Printf("  %s  %s\n",
				dimStyle.Render("Skills :"),
				strings.Join(cfg.Skills, ", "),
			)
			if len(cfg.Policies) > 0 {
				for name, p := range cfg.Policies {
					fmt.Printf("  %s  %s → %s\n",
						dimStyle.Render("Policy :"),
						name, p.Action,
					)
				}
			}
			fmt.Println()
			fmt.Println(dimStyle.Render("  " + strings.Repeat("─", 48)))
			fmt.Printf("  %-14s  %s\n", "Tier", "Heads")
			fmt.Println(dimStyle.Render("  " + strings.Repeat("─", 48)))
			for _, t := range cfg.Tiers {
				fmt.Printf("  %-14s  %s\n", t.Name, strings.Join(t.Heads, ", "))
			}
			fmt.Println()
			return nil
		},
	}
}

// ── dispatch ──────────────────────────────────────────────────────────────────

func cmdDispatch() *cobra.Command {
	var (
		tier      string
		localOnly bool
		dryRun    bool
		system    string
		a2aFile   string
		enumKey   string
	)

	cmd := &cobra.Command{
		Use:   "dispatch <prompt>",
		Short: "Route a prompt to the best available Head",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			prompt := strings.Join(args, " ")
			ctx := context.Background()

			d, err := dispatch.New(ctx)
			if err != nil {
				return err
			}

			opts := dispatch.Options{
				TierHint:  tier,
				LocalOnly: localOnly,
				DryRun:    dryRun,
				System:    system,
				A2AFile:   a2aFile,
				Enum:      enumKey,
			}

			result, err := d.Dispatch(ctx, prompt, opts)
			if err != nil {
				return err
			}

			if dryRun {
				fmt.Printf("  %s  %s  (score %d, %s)\n",
					cortexStyle.Render("Primary  →"),
					result.Head.Name, result.Head.CapScore, result.Head.Source)
				if len(result.Fallbacks) > 0 {
					fmt.Println(dimStyle.Render("  Fallback chain:"))
					for i, f := range result.Fallbacks {
						line := fmt.Sprintf("    %d. %-28s score %d  %s", i+1, f.Name, f.CapScore, f.Source)
						fmt.Println(dimStyle.Render(line))
					}
				}
				return nil
			}

			fmt.Println()
			fmt.Printf("  %s %s  %s  %dms\n",
				cortexStyle.Render("▶"),
				dimStyle.Render(result.Head.Name),
				dimStyle.Render(fmt.Sprintf("(%d→%d tokens)", result.InputTokens, result.OutputTokens)),
				result.Duration.Milliseconds(),
			)
			fmt.Println(dimStyle.Render("  " + strings.Repeat("─", 56)))
			fmt.Println()
			fmt.Println(result.Output)
			fmt.Println()
			return nil
		},
	}

	cmd.Flags().StringVarP(&tier, "tier", "t", "", "target tier (expert/complex/standard/simple/local)")
	cmd.Flags().BoolVarP(&localOnly, "local", "l", false, "force local heads only")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show selected head without executing")
	cmd.Flags().StringVarP(&system, "system", "s", "", "system prompt")
	cmd.Flags().StringVar(&a2aFile, "a2a", "", "path to A2A handoff JSON (prepends structured context to prompt)")
	cmd.Flags().StringVar(&enumKey, "enum", "", "routing enum key for cost logging (e.g. SIMPLE)")
	return cmd
}

// ── edit ──────────────────────────────────────────────────────────────────────

func cmdEdit() *cobra.Command {
	var (
		file       string
		enum       string
		prompt     string
		noValidate bool
	)

	cmd := &cobra.Command{
		Use:   "edit",
		Short: "Atomic, validated, rollback-safe file edit via a Hydra Head",
		RunE: func(_ *cobra.Command, _ []string) error {
			ctx := context.Background()
			result, err := editor.Edit(ctx, editor.Request{
				File:     file,
				Enum:     enum,
				Prompt:   prompt,
				Validate: !noValidate,
			})
			if err != nil {
				return err
			}
			raw, _ := json.MarshalIndent(result, "", "  ")
			fmt.Println(string(raw))
			if result.Status != "ok" {
				os.Exit(2)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&file, "file", "", "absolute path to file (required)")
	cmd.Flags().StringVar(&enum, "enum", "", "routing enum key, e.g. SIMPLE (required)")
	cmd.Flags().StringVar(&prompt, "prompt", "", "edit instruction (required)")
	cmd.Flags().BoolVar(&noValidate, "no-validate", false, "skip extension validator")
	_ = cmd.MarkFlagRequired("file")
	_ = cmd.MarkFlagRequired("enum")
	_ = cmd.MarkFlagRequired("prompt")
	return cmd
}

// ── review ────────────────────────────────────────────────────────────────────

func cmdReview() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "review",
		Short: "Review, approve, reject, or QA-check Hydra-edited files",
	}

	// summary
	sum := &cobra.Command{
		Use:   "summary [file...]",
		Short: "JSON diff stats for edited files (reads last logs if no files given)",
		RunE: func(_ *cobra.Command, args []string) error {
			result, err := review.Summary(args)
			if err != nil {
				return err
			}
			raw, _ := json.MarshalIndent(result, "", "  ")
			fmt.Println(string(raw))
			return nil
		},
	}

	// diff
	diff := &cobra.Command{
		Use:   "diff <file>",
		Short: "Print unified diff for a file",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			out, err := review.Diff(args[0])
			if err != nil {
				return err
			}
			fmt.Print(out)
			return nil
		},
	}

	// approve
	approve := &cobra.Command{
		Use:   "approve <file>",
		Short: "Accept changes (removes backup for non-git workspaces)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			result, err := review.Approve(args[0])
			if err != nil {
				return err
			}
			raw, _ := json.MarshalIndent(result, "", "  ")
			fmt.Println(string(raw))
			return nil
		},
	}

	// reject
	reject := &cobra.Command{
		Use:   "reject <file>",
		Short: "Rollback file to pre-edit state",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			result, err := review.Reject(args[0])
			if err != nil {
				return err
			}
			raw, _ := json.MarshalIndent(result, "", "  ")
			fmt.Println(string(raw))
			return nil
		},
	}

	// qa
	var qaTier int
	qa := &cobra.Command{
		Use:   "qa <file>",
		Short: "Send file diff to a Hydra Head for LLM code review",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			ctx := context.Background()
			result, err := review.QA(ctx, args[0], qaTier)
			if err != nil {
				return err
			}
			raw, _ := json.MarshalIndent(result, "", "  ")
			fmt.Println(string(raw))
			return nil
		},
	}
	qa.Flags().IntVar(&qaTier, "tier", 4, "reviewer tier (default 4 = HARD/GPT-OSS)")

	cmd.AddCommand(sum, diff, approve, reject, qa)
	return cmd
}

// ── parallel ──────────────────────────────────────────────────────────────────

func cmdParallel() *cobra.Command {
	var tasksFile string

	cmd := &cobra.Command{
		Use:   "parallel",
		Short: "Fan N tasks out to N Hydra Heads simultaneously",
		RunE: func(_ *cobra.Command, _ []string) error {
			raw, err := os.ReadFile(tasksFile)
			if err != nil {
				return fmt.Errorf("reading tasks file: %w", err)
			}
			var tasks []parallel.Task
			if err := json.Unmarshal(raw, &tasks); err != nil {
				return fmt.Errorf("invalid JSON in %s: %w", tasksFile, err)
			}

			ctx := context.Background()
			results, err := parallel.Run(ctx, tasks)
			if err != nil {
				return err
			}

			out, _ := json.MarshalIndent(results, "", "  ")
			fmt.Println(string(out))

			// Exit non-zero if any task failed.
			for _, r := range results {
				var s struct{ Status string }
				if json.Unmarshal(r.Raw(), &s) == nil && s.Status == "fail" {
					os.Exit(1)
				}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&tasksFile, "tasks", "", "path to tasks JSON file (required)")
	_ = cmd.MarkFlagRequired("tasks")
	return cmd
}

// ── cost ──────────────────────────────────────────────────────────────────────

func cmdCost() *cobra.Command {
	var jsonOut bool

	printJSON := func(v any) {
		raw, _ := json.MarshalIndent(v, "", "  ")
		fmt.Println(string(raw))
	}

	cmd := &cobra.Command{
		Use:   "cost",
		Short: "Show spend summaries from cost.jsonl",
		RunE: func(_ *cobra.Command, _ []string) error {
			r, err := cost.Summary()
			if err != nil {
				return err
			}
			if jsonOut {
				printJSON(r)
				return nil
			}
			cost.RenderSummary(r)
			return nil
		},
	}
	cmd.PersistentFlags().BoolVar(&jsonOut, "json", false, "machine-readable JSON output")

	cmd.AddCommand(
		&cobra.Command{
			Use: "today", Short: "Today's per-tier breakdown",
			RunE: func(_ *cobra.Command, _ []string) error {
				rows, err := cost.Today()
				if err != nil {
					return err
				}
				if jsonOut {
					printJSON(rows)
					return nil
				}
				cost.RenderTable("Today's spend by tier", rows)
				return nil
			},
		},
		&cobra.Command{
			Use: "all", Short: "All-time per-tier breakdown",
			RunE: func(_ *cobra.Command, _ []string) error {
				rows, err := cost.All()
				if err != nil {
					return err
				}
				if jsonOut {
					printJSON(rows)
					return nil
				}
				cost.RenderTable("All-time spend by tier", rows)
				return nil
			},
		},
		&cobra.Command{
			Use: "by-pool", Short: "All-time per-pool totals",
			RunE: func(_ *cobra.Command, _ []string) error {
				rows, err := cost.ByPool()
				if err != nil {
					return err
				}
				if jsonOut {
					printJSON(rows)
					return nil
				}
				cost.RenderTable("All-time spend by pool", rows)
				return nil
			},
		},
		&cobra.Command{
			Use:  "by-task <task_id>",
			Short: "Spending for a specific task",
			Args: cobra.ExactArgs(1),
			RunE: func(_ *cobra.Command, args []string) error {
				totals, err := cost.ByTask(args[0])
				if err != nil {
					return err
				}
				if jsonOut {
					printJSON(totals)
					return nil
				}
				fmt.Printf("\n  Task %s\n", args[0])
				fmt.Printf("    calls   %d\n    tok     %d+%d\n    cost    $%.6f\n    wall    %ds\n\n",
					totals.Calls, totals.PromptTokens, totals.ResponseTokens,
					totals.EstCostUSD, totals.WallSeconds)
				return nil
			},
		},
		&cobra.Command{
			Use:  "by-run <run_id>",
			Short: "Spending for a playbook run",
			Args: cobra.ExactArgs(1),
			RunE: func(_ *cobra.Command, args []string) error {
				r, err := cost.ByRun(args[0])
				if err != nil {
					return err
				}
				if jsonOut {
					printJSON(r)
					return nil
				}
				fmt.Printf("\n  Run %s\n", args[0])
				fmt.Printf("    calls   %d\n    cost    $%.6f\n    wall    %ds\n",
					r.Totals.Calls, r.Totals.EstCostUSD, r.Totals.WallSeconds)
				cost.RenderTable("Per-tier", r.ByTier)
				return nil
			},
		},
	)

	tail := &cobra.Command{
		Use:  "tail [N]",
		Short: "Last N calls (default 10)",
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			n := 10
			if len(args) > 0 {
				fmt.Sscanf(args[0], "%d", &n)
			}
			rows, err := cost.Tail(n)
			if err != nil {
				return err
			}
			if jsonOut {
				printJSON(rows)
				return nil
			}
			cost.RenderTail(rows)
			return nil
		},
	}

	var since string
	jsonCmd := &cobra.Command{
		Use:  "json",
		Short: "Raw JSONL rows (optionally filtered by --since)",
		RunE: func(_ *cobra.Command, _ []string) error {
			rows, err := cost.JSON(since)
			if err != nil {
				return err
			}
			printJSON(rows)
			return nil
		},
	}
	jsonCmd.Flags().StringVar(&since, "since", "", "ISO timestamp prefix filter (e.g. 2026-06-01)")

	cmd.AddCommand(tail, jsonCmd)
	return cmd
}

// ── run (playbook state machine) ──────────────────────────────────────────────

func cmdRun() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Manage playbook runs (state machine for multi-stage AI workflows)",
	}
	cmd.AddCommand(
		cmdRunStart(),
		cmdRunNext(),
		cmdRunComplete(),
		cmdRunStatus(),
		cmdRunList(),
		cmdRunShow(),
		cmdRunWorklog(),
		cmdRunLedger(),
		cmdRunTicket(),
		cmdRunTicketComment(),
		cmdRunPrune(),
		cmdRunChildren(),
		cmdRunRotateLogs(),
	)
	return cmd
}

func cmdRunStart() *cobra.Command {
	var (
		workspace string
		intent    string
		hasUI     bool
		devFacing bool
		needsDS   bool
		parentID  string
		dryRun    bool
	)
	cmd := &cobra.Command{
		Use:   "start <playbook>",
		Short: "Start a new playbook run",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			runID, err := company.Start(args[0], company.StartOptions{
				Workspace:         workspace,
				Intent:            intent,
				HasUI:             hasUI,
				DevFacing:         devFacing,
				NeedsDesignSystem: needsDS,
				ParentRun:         parentID,
			})
			if dryRun {
				fmt.Fprintln(os.Stderr, "warning: --dry-run not yet implemented for run start; run will be created normally")
			}
			if err != nil {
				return err
			}
			fmt.Println(runID)
			return nil
		},
	}
	cmd.Flags().StringVarP(&workspace, "workspace", "w", "", "workspace path (default: current dir)")
	cmd.Flags().StringVar(&intent, "intent", "", "short description of the task")
	cmd.Flags().BoolVar(&hasUI, "has-ui", false, "task involves UI changes")
	cmd.Flags().BoolVar(&devFacing, "dev-facing", false, "task is developer-facing")
	cmd.Flags().BoolVar(&needsDS, "needs-design-system", false, "task requires design system")
	cmd.Flags().StringVar(&parentID, "parent", "", "parent run ID for child runs")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print manifest without creating run")
	return cmd
}

func cmdRunNext() *cobra.Command {
	return &cobra.Command{
		Use:   "next <run-id>",
		Short: "Advance to the next stage of a run",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			result, err := company.Next(args[0])
			if err != nil {
				return err
			}
			raw, err := json.Marshal(result)
			if err != nil {
				return err
			}
			fmt.Println(string(raw))
			return nil
		},
	}
}

func cmdRunComplete() *cobra.Command {
	var (
		finding  string
		blocking bool
		output   string
	)
	cmd := &cobra.Command{
		Use:   "complete <run-id> <stage-name>",
		Short: "Mark a stage as complete",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			findings := []string{}
			if finding != "" {
				severity := "info"
				if blocking {
					severity = "blocking"
				}
				findings = append(findings, severity+": "+finding)
			}
			state, err := company.Complete(args[0], args[1], company.CompleteOptions{
				Findings: findings,
				Output:   output,
			})
			if err != nil {
				return err
			}
			raw, err := json.Marshal(state)
			if err != nil {
				return err
			}
			fmt.Println(string(raw))
			return nil
		},
	}
	cmd.Flags().StringVar(&finding, "finding", "", "finding note to attach")
	cmd.Flags().BoolVar(&blocking, "blocking", false, "mark finding as blocking")
	cmd.Flags().StringVar(&output, "output", "", "output/result from this stage")
	return cmd
}

func cmdRunStatus() *cobra.Command {
	return &cobra.Command{
		Use:   "status <run-id>",
		Short: "Print run status",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return company.PrintStatus(args[0])
		},
	}
}

func cmdRunList() *cobra.Command {
	var workspace string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all runs in a workspace",
		RunE: func(_ *cobra.Command, _ []string) error {
			runs, err := company.List(workspace)
			if err != nil {
				return err
			}
			for _, r := range runs {
				fmt.Printf("%-40s  %-12s  %s\n", r.RunID, r.Status, r.Playbook)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&workspace, "workspace", "w", "", "workspace path (default: current dir)")
	return cmd
}

func cmdRunShow() *cobra.Command {
	return &cobra.Command{
		Use:   "show <run-id>",
		Short: "Show full run details as JSON",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			result, err := company.Show(args[0])
			if err != nil {
				return err
			}
			raw, err := json.MarshalIndent(result, "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(raw))
			return nil
		},
	}
}

func cmdRunWorklog() *cobra.Command {
	return &cobra.Command{
		Use:   "worklog <run-id>",
		Short: "Print worklog markdown section for a run",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			md, err := company.Worklog(args[0])
			if err != nil {
				return err
			}
			fmt.Print(md)
			return nil
		},
	}
}

func cmdRunLedger() *cobra.Command {
	var workspace string
	cmd := &cobra.Command{
		Use:   "ledger",
		Short: "Regenerate HYDRA.md ledger for a workspace",
		RunE: func(_ *cobra.Command, _ []string) error {
			return company.Ledger(workspace)
		},
	}
	cmd.Flags().StringVarP(&workspace, "workspace", "w", "", "workspace path (default: current dir)")
	return cmd
}

func cmdRunTicket() *cobra.Command {
	return &cobra.Command{
		Use:   "ticket <run-id> <ref>",
		Short: "Associate a ticket reference with a run",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			return company.SetTicket(args[0], args[1], "")
		},
	}
}

func cmdRunTicketComment() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "ticket-comment <run-id> <body>",
		Short: "Post a comment to the associated ticket",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			result, _, err := company.TicketComment(args[0], args[1], dryRun)
			if err != nil {
				return err
			}
			if dryRun {
				fmt.Println(dimStyle.Render("[dry-run] would post:"))
				fmt.Println(result.Body)
			} else {
				fmt.Println(dimStyle.Render("Posted to " + result.Ticket))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print comment without posting")
	return cmd
}

func cmdRunPrune() *cobra.Command {
	var (
		workspace string
		olderThan string
		keepMin   int
		dryRun    bool
	)
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Remove old completed runs",
		RunE: func(_ *cobra.Command, _ []string) error {
			pruned, kept, err := company.Prune(workspace, company.PruneOptions{
				OlderThan: olderThan,
				DryRun:    dryRun,
				KeepMin:   keepMin,
			})
			if err != nil {
				return err
			}
			fmt.Printf("pruned %d  kept %d\n", pruned, kept)
			return nil
		},
	}
	cmd.Flags().StringVarP(&workspace, "workspace", "w", "", "workspace path")
	cmd.Flags().StringVar(&olderThan, "older-than", "30d", "prune runs older than this (e.g. 7d, 2h)")
	cmd.Flags().IntVar(&keepMin, "keep-min", 5, "minimum number of runs to keep")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print what would be pruned without deleting")
	return cmd
}

func cmdRunChildren() *cobra.Command {
	return &cobra.Command{
		Use:   "children <parent-run-id>",
		Short: "List child runs of a parent run",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			children, err := company.Children(args[0])
			if err != nil {
				return err
			}
			for _, c := range children {
				fmt.Printf("%-40s  %-12s  %s\n", c.RunID, c.Status, c.Playbook)
			}
			return nil
		},
	}
}

func cmdRunRotateLogs() *cobra.Command {
	var (
		maxSize string
		keep    int
	)
	cmd := &cobra.Command{
		Use:   "rotate-logs",
		Short: "Rotate dispatch.jsonl and cost.jsonl",
		RunE: func(_ *cobra.Command, _ []string) error {
			n, err := company.RotateLogs(maxSize, keep)
			if err != nil {
				return err
			}
			fmt.Printf("rotated %d file(s)\n", n)
			return nil
		},
	}
	cmd.Flags().StringVar(&maxSize, "max-size", "10MB", "rotate files larger than this")
	cmd.Flags().IntVar(&keep, "keep", 5, "number of rotated files to keep")
	return cmd
}

// ── styles ────────────────────────────────────────────────────────────────────

var (
	cortexStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)
