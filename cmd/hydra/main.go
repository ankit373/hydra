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

	"github.com/ankit373/hydra/internal/company"
	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/probe"
	"github.com/ankit373/hydra/internal/tui"

	_ "github.com/ankit373/hydra/internal/provider/cli"
	_ "github.com/ankit373/hydra/internal/provider/env"
	_ "github.com/ankit373/hydra/internal/provider/port"
	_ "github.com/ankit373/hydra/internal/provider/agy"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		os.Exit(1)
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
	root.AddCommand(cmdInit(), cmdProbe(), cmdStatus(), cmdDispatch(), cmdRun())
	return root
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
			_ = dryRun // DryRun not in StartOptions; caller can inspect stdout
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
			_ = output
			state, err := company.Complete(args[0], args[1], company.CompleteOptions{
				Findings: findings,
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
			_ = keepMin
			pruned, kept, err := company.Prune(workspace, company.PruneOptions{
				OlderThan: olderThan,
				DryRun:    dryRun,
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
