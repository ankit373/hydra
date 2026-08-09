// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/ankit373/hydra/internal/budget"
	"github.com/ankit373/hydra/internal/build"
	"github.com/ankit373/hydra/internal/capabilities"
	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/cost"
	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/editor"
	"github.com/ankit373/hydra/internal/entropy"
	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/graph"
	"github.com/ankit373/hydra/internal/ledger"
	"github.com/ankit373/hydra/internal/optimal"
	"github.com/ankit373/hydra/internal/oracle"
	"github.com/ankit373/hydra/internal/parallel"
	"github.com/ankit373/hydra/internal/policy"
	"github.com/ankit373/hydra/internal/pricing"
	"github.com/ankit373/hydra/internal/probe"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/rank"
	"github.com/ankit373/hydra/internal/review"
	"github.com/ankit373/hydra/internal/runid"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/swarm"
	"github.com/ankit373/hydra/internal/trust"
	"github.com/ankit373/hydra/internal/tui"
	"github.com/ankit373/hydra/internal/update"

	_ "github.com/ankit373/hydra/internal/provider/agy"
	_ "github.com/ankit373/hydra/internal/provider/cli"
	_ "github.com/ankit373/hydra/internal/provider/env"
	_ "github.com/ankit373/hydra/internal/provider/port"
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
			fmt.Fprintf(os.Stderr, "\n  %s  hyctl %s is available → brew upgrade hyctl\n",
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
		Use:   "hyctl",
		Short: "Multi-model AI orchestration — one Cortex, many Heads",
		// `hyctl --version` used to answer "unknown flag" even though a version
		// subcommand existed. It is the near-universal convention, so people
		// type it first. Same text as the subcommand, from one function.
		Version: build.Version,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !config.Exists() {
				return runInit()
			}
			return cmd.Help()
		},
	}
	root.SetVersionTemplate(versionText())
	root.AddCommand(
		cmdInit(), cmdProbe(), cmdStatus(), cmdTui(), cmdDispatch(),
		cmdEdit(), cmdReview(), cmdParallel(), cmdCost(), cmdStats(),
		cmdPricing(), cmdTrust(), cmdGraph(), cmdContext(), cmdMCP(), cmdOracle(), cmdModels(),
		cmdVersion(),
	)
	return root
}

// ── version ───────────────────────────────────────────────────────────────────

// versionText is shared by the `version` subcommand and the root `--version`
// flag, so the two can never drift into reporting differently.
func versionText() string {
	return fmt.Sprintf("  hydra %s\n  commit:  %s\n  built:   %s\n  by:      %s\n",
		build.Version, build.Commit, build.Date, build.BuiltBy)
}

func cmdVersion() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version, commit, and build info",
		Run: func(_ *cobra.Command, _ []string) {
			fmt.Print(versionText())
			// Update notice is printed by main() after Execute returns.
		},
	}
}

// ── tui (interactive cockpit) ───────────────────────────────────────────────────

func cmdTui() *cobra.Command {
	var snapshot bool
	var snapView int
	c := &cobra.Command{
		Use:   "tui",
		Short: "Interactive cockpit — chat, route work, and watch spend live",
		RunE: func(cmd *cobra.Command, _ []string) error {
			viewSet := cmd.Flags().Changed("view")
			if viewSet {
				if ok, names := tui.ValidSnapshotView(snapView); !ok {
					return fmt.Errorf("--view %d is out of range: valid values are 0..%d (%s)",
						snapView, len(names)-1, strings.Join(names, ", "))
				}
				if !snapshot {
					return fmt.Errorf("--view applies only with --snapshot")
				}
			}
			if snapshot {
				if viewSet {
					fmt.Print(tui.CockpitSnapshotView(snapView))
				} else {
					fmt.Print(tui.CockpitSnapshot())
				}
				fmt.Println()
				return nil
			}
			if err := requireTerminal("hyctl tui"); err != nil {
				return err
			}
			p := tea.NewProgram(tui.NewCockpit(), tea.WithAltScreen())
			_, err := p.Run()
			return err
		},
	}
	c.Flags().BoolVar(&snapshot, "snapshot", false, "Render one static frame and exit (docs/preview)")
	c.Flags().IntVar(&snapView, "view", 0, "With --snapshot: render a single view (0 chat+code, 1 dashboard, 2 agent-tree)")
	return c
}

// ── init ──────────────────────────────────────────────────────────────────────

func cmdInit() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "First-run wizard: discover Heads and choose your Cortex",
		RunE:  func(_ *cobra.Command, _ []string) error { return runInit() },
	}
}

// requireTerminal refuses to open an interactive UI when there is nothing to
// render it on.
//
// Without this the wizard hangs. On unix Bubble Tea fails opening /dev/tty, so
// the symptom is at least an error; on Windows there is no equivalent failure
// and it blocks reading stdin forever — so `hyctl init` in a Dockerfile, a CI
// job, or any piped invocation wedges the build with no output. Found by the
// Windows leg of the test matrix, which is the only place the difference shows.
//
// stdin *and* stdout: a piped stdin with a terminal stdout still has no way to
// answer a prompt.
func requireTerminal(cmd string) error {
	if isatty.IsTerminal(os.Stdin.Fd()) && isatty.IsTerminal(os.Stdout.Fd()) {
		return nil
	}
	return fmt.Errorf("%s needs an interactive terminal, and this one is not "+
		"attached to a TTY.\n  In a script or container, configure Hydra by "+
		"writing ~/.hydra/config.toml directly, or run this from a real shell", cmd)
}

func runInit() error {
	if err := requireTerminal("hyctl init"); err != nil {
		return err
	}
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
			// Discovery finding a head is not the same as Hydra being able to
			// drive it. Listing both identically is what let `probe` advertise
			// the Ollama binary that `dispatch --local` then refused (#248), so
			// unroutable heads are marked and carry their reason.
			var unroutable int
			for _, h := range result.Heads {
				marker := "  "
				if result.Cortex != nil && h.ID == result.Cortex.ID {
					marker = cortexStyle.Render("→ ")
				}
				why := executor.Unroutable(h)
				if why != "" {
					marker = warnStyle.Render("✗ ")
					unroutable++
				}
				row := fmt.Sprintf("%-30s  %-5d  %-5s  %s", h.Name, h.CapScore, h.Source, h.Provider)
				if why == "" {
					fmt.Printf("%s%s\n", marker, row)
					continue
				}
				fmt.Printf("%s%s\n", marker, dimStyle.Render(row))
				fmt.Printf("    %s\n", dimStyle.Render("↳ "+why))
			}
			if unroutable > 0 {
				fmt.Printf("\n  %s\n", dimStyle.Render(fmt.Sprintf(
					"✗ = discovered but not routable (%d of %d) — dispatch will skip these.",
					unroutable, len(result.Heads))))
			}
			return nil
		},
	}
}

// ── status ────────────────────────────────────────────────────────────────────

func cmdStatus() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show current Cortex, Head configuration, and budget utilisation",
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("no config found — run: hyctl init")
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

			// Budget section — read from state.json (written by dispatcher).
			printBudgetStatus()
			return nil
		},
	}
}

func printBudgetStatus() {
	statePath := filepath.Join(config.Dir(), "logs", "state.json")
	raw, err := os.ReadFile(statePath)
	if err != nil {
		return
	}
	var state struct {
		ClaudePct        int                       `json:"claude_pct"`
		ClaudePctHistory []int                     `json:"claude_pct_history"`
		Budget           map[string]map[string]any `json:"budget"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return
	}

	// claude_pct block (orchestrator context window). When enough history has
	// accumulated, show the rate-aware effective mode (first-passage risk) — a
	// fast burn escalates above the static level band before it crosses a line.
	if state.ClaudePct > 0 {
		burnRate, risk := budget.RiskFromHistory(state.ClaudePctHistory)
		eff := budget.EffectiveMode(state.ClaudePct, risk)
		bar := budgetBar(state.ClaudePct)
		line := fmt.Sprintf("  %s  %s %3d%%  %s",
			dimStyle.Render("Claude  :"),
			bar, state.ClaudePct, budgetModeStyle(eff.String()).Render(eff.String()),
		)
		if eff > budget.ModeFor(state.ClaudePct) {
			line += "  " + dimStyle.Render(fmt.Sprintf("↑ burning +%.0f%%/step, %.0f%% risk of 80%%",
				burnRate, risk*100))
		}
		fmt.Println(line)
	}

	if len(state.Budget) == 0 {
		return
	}

	fmt.Println()
	fmt.Println(dimStyle.Render("  " + strings.Repeat("─", 48)))
	fmt.Printf("  %-20s  %6s  %8s  %s\n", "Model", "  Used", " Window", "Mode")
	fmt.Println(dimStyle.Render("  " + strings.Repeat("─", 48)))
	for modelID, snap := range state.Budget {
		pct := int(toFloat(snap["pct"]))
		used := int(toFloat(snap["used"]))
		window := int(toFloat(snap["window"]))
		mode, _ := snap["mode"].(string)
		bar := budgetBar(pct)
		fmt.Printf("  %-20s  %s %3d%%  %-8s  %s\n",
			truncLabel(modelID, 20),
			bar, pct,
			tokenLabel(used, window),
			budgetModeStyle(mode).Render(mode),
		)
	}
	fmt.Println()
}

func toFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	}
	return 0
}

func budgetBar(pct int) string {
	const width = 10
	filled := pct * width / 100
	if filled > width {
		filled = width
	}
	// state.json is written by another process — and by hand, per the
	// orchestrator protocol's `jq '.claude_pct = 52'`. A negative value there
	// reached strings.Repeat with a negative count, which panics: `hyctl status`
	// crashed on a malformed field it only meant to display.
	if filled < 0 {
		filled = 0
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	return budgetModeStyle(budget.ModeFor(pct).String()).Render(bar)
}

// tokenLabel formats used/window as a human-readable string without integer truncation.
func tokenLabel(used, window int) string {
	f := func(n int) string {
		if n >= 1_000_000 {
			return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
		}
		if n >= 1_000 {
			return fmt.Sprintf("%dk", n/1000)
		}
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%s/%s", f(used), f(window))
}

func budgetModeStyle(mode string) lipgloss.Style {
	switch mode {
	case "emergency":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
	case "critical":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	case "warning":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	case "caution":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("226"))
	case "compact":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
	default:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("82"))
	}
}

func truncLabel(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
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
		// swarm flags
		doSwarm       bool
		swarmMode     string
		swarmHeads    string
		swarmMaxHeads int
		swarmMaxCost  float64
		swarmJudge    string
		// trust / SPRT flags
		confidence   float64
		domain       string
		file         string
		graphPath    string
		irreversible bool
		production   bool
	)

	cmd := &cobra.Command{
		Use:   "dispatch <prompt>",
		Short: "Route a prompt to the best available Head",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			prompt := strings.Join(args, " ")
			ctx := context.Background()

			// One invocation is one run with one logical task, whichever path
			// below handles it — so a swarm's attempts and the dispatch that
			// drove them share an identity in the logs (#181).
			//
			// Resolve rather than mint: runid ranks explicit > env > generated,
			// so calling New() here and passing the result as the explicit value
			// silently outranked HYDRA_RUN_ID and made it dead at the only entry
			// point that matters (#204).
			runID, taskID := runid.ResolveRun(""), runid.ResolveTask("")

			// Mark the run live for its whole duration. Without this
			// runlog.LiveRuns() is always empty and nothing — cockpit or desktop
			// Fleet — can distinguish a running agent from a finished one.
			hb := runlog.StartHeartbeat(ctx, runID, runlog.HeartbeatInterval)
			defer hb.Stop()

			rl := runlog.New(runID)
			_ = rl.Append(runlog.Event{Kind: runlog.KindRunStarted, TaskID: taskID, Detail: promptPreview(prompt)})
			defer func() {
				_ = rl.Append(runlog.Event{Kind: runlog.KindRunFinished, TaskID: taskID})
			}()

			d, err := dispatch.New(ctx)
			if err != nil {
				return err
			}

			var headIDs []string
			if swarmHeads != "" {
				for _, id := range strings.Split(swarmHeads, ",") {
					if id = strings.TrimSpace(id); id != "" {
						headIDs = append(headIDs, id)
					}
				}
			}

			// ── SPRT confidence mode ──────────────────────────────────────
			// Triggered by --confidence, or by any real risk signal (--file's
			// blast radius, --irreversible, --production, or PII auto-detected
			// in the prompt). Risk raises the bar but never lowers a target the
			// user explicitly asked for.
			effectiveConf := confidence
			touchesPII := policy.ContainsPII(policy.Request{Prompt: prompt})
			if file != "" || irreversible || production || touchesPII {
				radius := 1.0
				if file != "" {
					g, err := graph.Load(graphPath)
					if err != nil {
						return err
					}
					radius = g.BlastRadiusForFile(file)
					// --file exists to RAISE the bar for risky files. With no graph
					// it silently never raises, while printing a line that reads
					// exactly like blast-radius-aware routing happened (#251). The
					// bar itself is unchanged — only the claim about it.
					if g.Empty() {
						fmt.Printf("  %s\n", warnStyle.Render(
							"graph: no graph at "+graphPath+" — radius 1.00 is a default, not a measurement"))
					} else if !g.Knows(file) {
						fmt.Printf("  %s\n", warnStyle.Render(
							"graph: "+file+" is not in the graph — radius 1.00 is a default, not a measurement"))
					}
				}
				task := trust.Task{
					Domain:       domain,
					BlastRadius:  radius,
					Irreversible: irreversible,
					TouchesPII:   touchesPII,
					Production:   production,
				}
				derived := trust.NewDefectModel().RequiredConfidence(task)
				fmt.Printf("  %s blast=%.2f irreversible=%v pii=%v prod=%v → demands confidence ≥ %.1f%%\n",
					dimStyle.Render("defect:"), task.BlastRadius, irreversible, touchesPII, production, derived*100)
				if derived > effectiveConf {
					effectiveConf = derived
				}
			}
			// --dry-run must mean "spend nothing" in every mode. It was read only
			// by the single-dispatch path far below, which neither ensemble
			// branch reaches — so `--dry-run --confidence 0.95` fired a paid
			// ensemble and printed no plan at all (#167).
			if dryRun && (effectiveConf > 0 || doSwarm) {
				mode := swarm.SwarmMode(swarmMode)
				if mode == "" {
					mode = swarm.ModeBest
				}
				sw := swarm.New(d, d.Heads(), d)
				planOpts := swarm.Options{
					Mode:          mode,
					TierHint:      tier,
					HeadIDs:       headIDs,
					MaxHeads:      swarmMaxHeads,
					MaxEstCostUSD: swarmMaxCost,
					LocalOnly:     localOnly,
					System:        system,
					Confidence:    effectiveConf,
					Domain:        domain,
				}
				heads, estUSD, err := sw.Plan(prompt, planOpts)
				if err != nil {
					return err
				}
				printEnsemblePlan(heads, estUSD, effectiveConf, mode, swarmMaxCost)
				return nil
			}

			if effectiveConf > 0 {
				sw := swarm.New(d, d.Heads(), d)
				res, err := sw.RunSPRT(ctx, prompt, swarm.Options{
					TierHint:      tier,
					HeadIDs:       headIDs,
					MaxHeads:      swarmMaxHeads,
					MaxEstCostUSD: swarmMaxCost,
					LocalOnly:     localOnly,
					System:        system,
					Confidence:    effectiveConf,
					Domain:        domain,
					RunID:         runID,
					TaskID:        taskID,
				})
				if err != nil {
					return err
				}
				printSPRTResult(res)
				logTrustRun(res, prompt, domain)
				return nil
			}

			// ── swarm mode ────────────────────────────────────────────────
			if doSwarm {
				mode := swarm.SwarmMode(swarmMode)
				if mode == "" {
					mode = swarm.ModeBest
				}
				sw := swarm.New(d, d.Heads(), d)
				result, err := sw.Run(ctx, prompt, swarm.Options{
					Mode:          mode,
					TierHint:      tier,
					HeadIDs:       headIDs,
					MaxHeads:      swarmMaxHeads,
					MaxEstCostUSD: swarmMaxCost,
					LocalOnly:     localOnly,
					System:        system,
					JudgeTierHint: swarmJudge,
					RunID:         runID,
					TaskID:        taskID,
				})
				if err != nil {
					return err
				}
				printSwarmResult(result)
				return nil
			}

			// ── normal single dispatch ─────────────────────────────────────
			// An enum is a routing instruction, not just a cost label: resolve
			// it to a tier when no explicit --tier was given (#165).
			tierHint := tier
			if tierHint == "" && enumKey != "" {
				tierHint = dispatch.EnumToTier(enumKey)
			}
			opts := dispatch.Options{
				TierHint:  tierHint,
				LocalOnly: localOnly,
				DryRun:    dryRun,
				System:    system,
				A2AFile:   a2aFile,
				Enum:      enumKey,
				RunID:     runID,
				TaskID:    taskID,
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
	cmd.Flags().StringVar(&enumKey, "enum", "", "routing enum key, e.g. SIMPLE — selects the tier when --tier is unset")
	// swarm flags
	cmd.Flags().BoolVar(&doSwarm, "swarm", false, "fan prompt out to multiple heads simultaneously")
	cmd.Flags().StringVar(&swarmMode, "swarm-mode", "best", "response strategy: best|race|all")
	cmd.Flags().StringVar(&swarmHeads, "swarm-heads", "", "comma-separated head IDs to target (overrides --tier)")
	cmd.Flags().IntVar(&swarmMaxHeads, "swarm-max-heads", 0, "max heads to fire (default 5)")
	cmd.Flags().Float64Var(&swarmMaxCost, "swarm-max-cost", 0, "refuse swarm if preflight cost estimate exceeds this USD")
	cmd.Flags().StringVar(&swarmJudge, "swarm-judge-tier", "", "tier for judge head in best mode (default: tier 1 / cortex)")
	// trust / SPRT flags
	cmd.Flags().Float64Var(&confidence, "confidence", 0, "route via SPRT ensemble until this P(correct) is reached, e.g. 0.95")
	cmd.Flags().StringVar(&domain, "domain", "", "calibration domain for --confidence (default: \"default\")")
	cmd.Flags().StringVar(&file, "file", "", "target file — derives a confidence target from its blast radius, so this alone selects the SPRT ensemble")
	cmd.Flags().StringVar(&graphPath, "graph", "graph.json", "path to the dependency graph used with --file")
	cmd.Flags().BoolVar(&irreversible, "irreversible", false, "change cannot be cheaply undone — raises the required confidence")
	cmd.Flags().BoolVar(&production, "production", false, "target is production — raises the required confidence")
	return cmd
}

// cmdOracle runs a verification oracle and reports its calibrated evidence.
func cmdOracle() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "oracle",
		Short: "Run deterministic verifiers (tests/compile/lint) as evidence sources",
	}
	var source, domain, candidateFile, record string
	verify := &cobra.Command{
		Use:   "verify <command...>",
		Short: "Run a verifier command; report pass/fail + its calibrated LLR",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			candidate := ""
			if candidateFile != "" {
				raw, err := os.ReadFile(candidateFile)
				if err != nil {
					return err
				}
				candidate = string(raw)
			}
			src := source
			if src == "" {
				src = "verifier:" + args[0]
			}
			o := &oracle.CommandOracle{Template: strings.Join(args, " "), Source: src}
			v, err := o.Verify(context.Background(), candidate, trust.Task{Domain: domain})
			if err != nil {
				return err
			}

			cal, err := trust.New(trust.DefaultPath())
			if err != nil {
				return err
			}
			if record != "" {
				if out := trust.ParseOutcome(record); out != trust.OutcomeUnknown {
					_ = cal.Update(src, domain, v.Passed, out)
				}
			}
			llr := oracle.LLR(cal, src, domain, v)

			status := cortexStyle.Render("PASS")
			if !v.Passed {
				status = "FAIL"
			}
			fmt.Printf("\n  %s  %s\n", status, dimStyle.Render(src))
			if v.Detail != "" {
				fmt.Printf("  %s\n", dimStyle.Render(v.Detail))
			}
			fmt.Printf("  calibrated evidence  %+.3f nats\n\n", llr)
			if !v.Passed {
				os.Exit(1)
			}
			return nil
		},
	}
	verify.Flags().StringVar(&source, "source", "", "calibration source id (default: verifier:<cmd>)")
	verify.Flags().StringVar(&domain, "domain", "", "task domain")
	verify.Flags().StringVar(&candidateFile, "candidate", "", "file holding the answer to verify (for {file}/{answer})")
	verify.Flags().StringVar(&record, "record", "", "train calibration with the true outcome: correct|incorrect")
	cmd.AddCommand(verify)
	return cmd
}

// cmdMCP is the local MCP accountability ledger + policy gate.
func cmdMCP() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Local accountability ledger: record and gate what agents touch",
	}

	// check: evaluate the policy for an access and record the decision.
	var chkAgent, chkResource, chkAction, policyPath, chkParams, chkContent, chkClassification string
	check := &cobra.Command{
		Use:   "check <tool>",
		Short: "Evaluate the policy for a tool/resource access and record it",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			pol, err := ledger.LoadPolicy(policyPath)
			if err != nil {
				return err
			}
			action, err := ledger.ParseAction(chkAction)
			if err != nil {
				return fmt.Errorf("--action: %w", err)
			}
			var params map[string]any
			if chkParams != "" {
				if params, err = ledger.DecodeParams(chkParams); err != nil {
					return fmt.Errorf("--params: %w", err)
				}
			}
			decision, checkErr := ledger.Check(ledger.DefaultPath(), pol, ledger.CheckRequest{
				Agent: chkAgent, Tool: args[0], Resource: chkResource, Action: action,
				Params: params, Classification: chkClassification, Content: chkContent,
			})
			// Report the decision before any error: a Deny that failed to write
			// to the ledger is still a Deny, and callers gate on exit 3.
			if decision != "" {
				fmt.Printf("  %s  %s %s/%s (%s)\n", strings.ToUpper(string(decision)),
					chkAgent, args[0], chkResource, action)
			}
			if checkErr != nil {
				fmt.Fprintf(os.Stderr, "  ledger error: %v\n", checkErr)
			}
			if decision == ledger.Deny {
				os.Exit(3) // non-zero so callers can gate on it
			}
			return checkErr
		},
	}
	check.Flags().StringVar(&chkAgent, "agent", "", "agent making the access")
	check.Flags().StringVar(&chkResource, "resource", "", "resource being accessed")
	check.Flags().StringVar(&chkAction, "action", "read", "read|write|exec|network")
	check.Flags().StringVar(&policyPath, "policy", ledger.DefaultPolicyPath(), "path to the access policy JSON")
	check.Flags().StringVar(&chkParams, "params", "", "JSON object of invocation parameters; hashed and bound to the recorded decision")
	check.Flags().StringVar(&chkContent, "content", "", "raw content being accessed; scanned for PII to auto-derive --classification if unset")
	check.Flags().StringVar(&chkClassification, "classification", "", "explicit data-sensitivity tag (e.g. pii); overrides --content detection")

	// record: append an event directly (for external tools reporting access).
	var recAgent, recTool, recResource, recAction, recDecision, recParams, recClassification string
	record := &cobra.Command{
		Use:   "record",
		Short: "Append an access event to the ledger",
		RunE: func(_ *cobra.Command, _ []string) error {
			action, err := ledger.ParseAction(recAction)
			if err != nil {
				return fmt.Errorf("--action: %w", err)
			}
			decision, err := ledger.ParseDecision(recDecision)
			if err != nil {
				return fmt.Errorf("--decision: %w", err)
			}
			// Bind whenever --params was supplied — including "{}" — so this
			// agrees with `check`, whose binding keys off a non-nil map.
			var hash string
			if recParams != "" {
				params, err := ledger.DecodeParams(recParams)
				if err != nil {
					return fmt.Errorf("--params: %w", err)
				}
				if params != nil {
					if hash, err = ledger.HashParams(params); err != nil {
						return err
					}
				}
			}
			return ledger.Record(ledger.DefaultPath(), ledger.Event{
				Agent: recAgent, Tool: recTool, Resource: recResource,
				Action: action, Decision: decision,
				ParametersHash: hash, Classification: ledger.NormalizeClassification(recClassification),
			})
		},
	}
	record.Flags().StringVar(&recAgent, "agent", "", "agent")
	record.Flags().StringVar(&recTool, "tool", "", "tool")
	record.Flags().StringVar(&recResource, "resource", "", "resource")
	record.Flags().StringVar(&recAction, "action", "read", "read|write|exec|network")
	record.Flags().StringVar(&recDecision, "decision", "allow", "allow|deny")
	record.Flags().StringVar(&recParams, "params", "", "JSON object of invocation parameters; hashed and bound to the recorded event")
	record.Flags().StringVar(&recClassification, "classification", "", "data-sensitivity tag (e.g. pii)")

	// verify: re-check execution-time params against the recorded approval.
	var verResource, verParams string
	var verAnyResource bool
	verify := &cobra.Command{
		Use:   "verify <tool>",
		Short: "Verify execution-time parameters against the hash bound at approval",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			// An empty tool would match an approval recorded for a different
			// tool entirely, so require it explicitly.
			tool := strings.TrimSpace(args[0])
			if tool == "" {
				return fmt.Errorf("tool argument is required")
			}
			if strings.TrimSpace(verResource) == "" && !verAnyResource {
				return fmt.Errorf("--resource is required (the hash covers parameters only, " +
					"so the approval's resource must be matched explicitly); pass --any-resource to override")
			}
			params, err := ledger.DecodeParams(verParams)
			if err != nil {
				return fmt.Errorf("--params: %w", err)
			}
			events, skipped, err := ledger.LoadCounted(ledger.DefaultPath())
			if err != nil {
				return err
			}
			if skipped > 0 {
				fmt.Fprintf(os.Stderr, "  warning: %d unparseable ledger line(s) skipped — "+
					"verification may be against an older approval\n", skipped)
			}
			approval, ok := ledger.LatestBound(events, tool, verResource)
			if !ok {
				return fmt.Errorf("no allowed, parameter-bound ledger event for %s/%s — nothing to verify against", tool, verResource)
			}
			match, err := ledger.VerifyParams(params, approval.ParametersHash)
			if err != nil {
				return err
			}
			// Name the matched approval explicitly: the hash covers the
			// parameters only, so the operator must be able to see which
			// tool/resource the approval was actually recorded for.
			target := fmt.Sprintf("%s/%s at %s", approval.Tool, approval.Resource, approval.TS)
			if !match {
				fmt.Printf("  %s  parameters do NOT match the approval for %s\n",
					strings.ToUpper(string(ledger.Deny)), target)
				os.Exit(3) // non-zero so callers can gate on it
			}
			fmt.Printf("  MATCH  parameters match the approval for %s\n", target)
			return nil
		},
	}
	verify.Flags().StringVar(&verResource, "resource", "", "resource the approval was recorded for (required)")
	verify.Flags().BoolVar(&verAnyResource, "any-resource", false, "match an approval for any resource of this tool (weaker: the hash does not cover the resource)")
	verify.Flags().StringVar(&verParams, "params", "", "JSON object of the parameters about to execute (required)")
	_ = verify.MarkFlagRequired("params")

	// log: list events, optionally filtered.
	var logAgent string
	var logDenied bool
	logCmd := &cobra.Command{
		Use:   "log",
		Short: "List ledger events (newest last)",
		RunE: func(_ *cobra.Command, _ []string) error {
			events, err := ledger.Load(ledger.DefaultPath())
			if err != nil {
				return err
			}
			events = ledger.Filter(events, logAgent, logDenied)
			if len(events) == 0 {
				fmt.Println("  (no matching ledger events)")
				return nil
			}
			for _, e := range events {
				fmt.Printf("  %s  %-6s  %-12s %s/%s %s\n", e.TS, strings.ToUpper(string(e.Decision)),
					e.Agent, e.Tool, e.Resource, dimStyle.Render(string(e.Action)))
			}
			return nil
		},
	}
	logCmd.Flags().StringVar(&logAgent, "agent", "", "filter to one agent")
	logCmd.Flags().BoolVar(&logDenied, "denied", false, "only show denied accesses")

	// report: aggregate summary.
	var repJSON bool
	report := &cobra.Command{
		Use:   "report",
		Short: "Accountability summary: allowed/denied, by agent and tool",
		RunE: func(_ *cobra.Command, _ []string) error {
			events, err := ledger.Load(ledger.DefaultPath())
			if err != nil {
				return err
			}
			s := ledger.Summarize(events)
			if repJSON {
				return json.NewEncoder(os.Stdout).Encode(s)
			}
			fmt.Printf("\n  ledger events   %d  (%d allowed · %d denied)\n", s.Total, s.Allowed, s.Denied)
			if len(s.ByAgent) > 0 {
				fmt.Println("  by agent:")
				for _, kc := range ledger.SortedCounts(s.ByAgent) {
					fmt.Printf("    %-20s %d\n", kc.Key, kc.Count)
				}
			}
			if len(s.ByTool) > 0 {
				fmt.Println("  by tool:")
				for _, kc := range ledger.SortedCounts(s.ByTool) {
					fmt.Printf("    %-20s %d\n", kc.Key, kc.Count)
				}
			}
			fmt.Println()
			return nil
		},
	}
	report.Flags().BoolVar(&repJSON, "json", false, "machine-readable JSON output")

	cmd.AddCommand(check, record, verify, logCmd, report)
	return cmd
}

// cmdModels manages the runtime-extensible model capability registry.
func cmdModels() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "models",
		Short: "Manage the model registry — add new models (e.g. Kimi K2) without recompiling",
	}
	overlay := capabilities.DefaultOverlayPath()

	var jsonOut bool
	list := &cobra.Command{
		Use:   "list",
		Short: "List all models (built-in + your additions), by capability score",
		RunE: func(_ *cobra.Command, _ []string) error {
			db, err := capabilities.Load(overlay)
			if err != nil {
				return err
			}
			entries := db.Entries()
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(entries)
			}
			fmt.Printf("\n  %-26s %-14s %6s  %s\n", "ID", "PROVIDER", "SCORE", "SOURCE")
			fmt.Println("  " + strings.Repeat("─", 58))
			for _, e := range entries {
				src := dimStyle.Render(e.Source)
				if e.Source == "user" {
					src = cortexStyle.Render("user")
				}
				fmt.Printf("  %-26.26s %-14.14s %6d  %s\n", e.ID, e.Provider, e.CapScore, src)
			}
			fmt.Println()
			return nil
		},
	}
	list.Flags().BoolVar(&jsonOut, "json", false, "machine-readable JSON output")

	var addName, addProvider string
	var addScore int
	add := &cobra.Command{
		Use:   "add <id>",
		Short: "Add or update a model in your registry",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if addScore < 0 || addScore > 100 {
				return fmt.Errorf("--cap-score must be 0–100, got %d", addScore)
			}
			e := capabilities.Entry{ID: args[0], Name: addName, Provider: addProvider, CapScore: addScore}
			if e.Name == "" {
				e.Name = args[0]
			}
			replaced, err := capabilities.AddModel(overlay, e)
			if err != nil {
				return err
			}
			verb := "added"
			if replaced {
				verb = "updated"
			}
			fmt.Printf("  %s %s (%s, score %d) → %s\n", verb, e.ID, e.Provider, e.CapScore, overlay)
			return nil
		},
	}
	add.Flags().StringVar(&addName, "name", "", "display name (default: the id)")
	add.Flags().StringVar(&addProvider, "provider", "", "provider, e.g. moonshot / openai / local")
	add.Flags().IntVar(&addScore, "cap-score", 70, "capability score 0–100")

	remove := &cobra.Command{
		Use:   "remove <id>",
		Short: "Remove a model from your registry",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			removed, err := capabilities.RemoveModel(overlay, args[0])
			if err != nil {
				return err
			}
			if !removed {
				return fmt.Errorf("%q is not in your registry (built-ins can't be removed, only overridden)", args[0])
			}
			fmt.Printf("  removed %s\n", args[0])
			return nil
		},
	}

	var syncFilter string
	var syncDry bool
	sync := &cobra.Command{
		Use:   "sync",
		Short: "Import models from the live OpenRouter catalog (provisional capScores)",
		RunE: func(_ *cobra.Command, _ []string) error {
			db := pricing.Load()
			models := db.Models()
			// An empty live catalogue is not "nothing new to import" — it means
			// no model pricing was available at all, which can only happen when
			// there is no cache and the fetch did not land. Reporting
			// "imported 0 models" for that reads as a completed sync against an
			// already-complete catalogue, so the user never learns the fetch
			// failed. Tier pricing is still loaded, which is why Load() itself
			// cannot report this.
			if len(models) == 0 {
				return fmt.Errorf("no model catalogue available — the OpenRouter " +
					"fetch has not landed. Run `hyctl pricing refresh` first, or " +
					"check network access")
			}
			added, skipped := 0, 0
			builtin, _ := capabilities.Load("")
			for _, id := range models {
				if syncFilter != "" && !strings.Contains(strings.ToLower(id), strings.ToLower(syncFilter)) {
					continue
				}
				// Don't clobber a model already known (built-in or user).
				if builtin.Name(id) != id {
					skipped++
					continue
				}
				provider := id
				if i := strings.IndexByte(id, '/'); i > 0 {
					provider = id[:i]
				}
				e := capabilities.Entry{ID: id, Name: id, Provider: provider, CapScore: capabilities.HeuristicCapScore(id)}
				if syncDry {
					fmt.Printf("  + %-40.40s %-12.12s score %d\n", e.ID, e.Provider, e.CapScore)
					added++
					continue
				}
				if _, err := capabilities.AddModel(overlay, e); err != nil {
					return err
				}
				added++
			}
			word := "imported"
			if syncDry {
				word = "would import"
			}
			fmt.Printf("\n  %s %d models (%d already known, skipped). capScores are provisional — refine with `hyctl models add`.\n\n", word, added, skipped)
			return nil
		},
	}
	sync.Flags().StringVar(&syncFilter, "filter", "", "only import models whose id contains this substring")
	sync.Flags().BoolVar(&syncDry, "dry-run", false, "show what would be imported without writing")

	cmd.AddCommand(list, add, remove, sync)
	return cmd
}

// cmdContext inspects context-window quality (signal density, useful tokens).
func cmdContext() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "context",
		Short: "Measure context-window quality — signal density, not just length",
	}
	var jsonOut bool
	var minDensity float64
	entropyCmd := &cobra.Command{
		Use:   "entropy [file]",
		Short: "Signal density + useful tokens of a file (or stdin), with a compact recommendation",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			var raw []byte
			var err error
			if len(args) == 1 && args[0] != "-" {
				raw, err = os.ReadFile(args[0])
			} else {
				raw, err = io.ReadAll(os.Stdin)
			}
			if err != nil {
				return err
			}
			rec := entropy.Governor{MinDensity: minDensity}.Assess(string(raw))
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(map[string]any{
					"tokens": rec.Snap.Tokens, "density": rec.Snap.Density,
					"useful_tokens": rec.Snap.UsefulTokens, "compact": rec.Compact, "reason": rec.Reason,
				})
			}
			fmt.Printf("\n  tokens (est)     %d\n", rec.Snap.Tokens)
			fmt.Printf("  signal density   %.1f%%\n", rec.Snap.Density*100)
			fmt.Printf("  useful tokens    %.0f\n", rec.Snap.UsefulTokens)
			if rec.Compact {
				fmt.Printf("  %s %s\n\n", cortexStyle.Render("⚠ compact:"), rec.Reason)
			} else {
				fmt.Printf("  %s\n\n", dimStyle.Render("✓ "+rec.Reason))
			}
			return nil
		},
	}
	entropyCmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable JSON output")
	entropyCmd.Flags().Float64Var(&minDensity, "min-density", 0, "signal-density floor for the compact recommendation (default 0.35)")
	cmd.AddCommand(entropyCmd)
	return cmd
}

// cmdGraph inspects the code dependency graph Hydra uses for blast-radius routing.
func cmdGraph() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "graph",
		Short: "Inspect the code dependency graph used for blast-radius routing",
	}
	var graphPath string
	var jsonOut bool
	blast := &cobra.Command{
		Use:   "blast <file>",
		Short: "Show a file's blast radius (how much transitively depends on it)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			g, err := graph.Load(graphPath)
			if err != nil {
				return err
			}
			file := args[0]
			radius := g.BlastRadiusForFile(file)
			task := trust.Task{BlastRadius: radius}
			dm := trust.NewDefectModel()

			var deps int
			for _, id := range g.NodesInFile(file) {
				deps += g.DependentCount(id)
			}
			pFactor := g.PercolationFactor(file)
			// A radius of 1.0 means either "nothing depends on this" or "I have
			// no idea" — opposite conclusions that used to render identically.
			// internal/a2a reported 6 dependents and 97.4% required confidence
			// with the graph, and "subcritical — edits stay local" at 90.0%
			// without it (#251).
			loaded, known := !g.Empty(), g.Knows(file)
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(map[string]any{
					"file": file, "blast_radius": radius, "transitive_dependents": deps,
					"defect_cost_usd": dm.CostUSD(task), "required_confidence": dm.RequiredConfidence(task),
					"kappa": g.Kappa(), "percolates": g.Percolates(), "percolation_factor": pFactor,
					"graph_loaded": loaded, "file_in_graph": known,
				})
			}
			fmt.Printf("\n  %s\n", cortexStyle.Render(file))
			if !loaded {
				fmt.Printf("    %s\n", warnStyle.Render("no graph at "+graphPath+" — nothing was analysed"))
			} else if !known {
				fmt.Printf("    %s\n", warnStyle.Render("not in the graph — check the path, or reindex"))
			}
			measured := loaded && known
			suffix := func() string {
				if measured {
					return ""
				}
				return "  " + dimStyle.Render("(default, not measured)")
			}()
			fmt.Printf("    transitive dependents  %d%s\n", deps, suffix)
			fmt.Printf("    blast radius           %.2f×%s\n", radius, suffix)
			// κ is a property of the whole graph, so it stays meaningful for an
			// unknown file — but with no graph at all it is 0.0, and rendering
			// that as "edits stay local" is a safety claim from no data.
			if loaded {
				if g.Percolates() {
					core := "periphery"
					if pFactor > 1.0 {
						core = fmt.Sprintf("core (+%.0f%%)", (pFactor-1)*100)
					}
					fmt.Printf("    graph κ                %.2f  %s\n",
						g.Kappa(), dimStyle.Render("supercritical — cascades possible; this file is "+core))
				} else {
					fmt.Printf("    graph κ                %.2f  %s\n",
						g.Kappa(), dimStyle.Render("subcritical — edits stay local"))
				}
			}
			fmt.Printf("    defect cost            $%.2f%s\n", dm.CostUSD(task), suffix)
			fmt.Printf("    demands confidence     %.1f%%%s\n\n", dm.RequiredConfidence(task)*100, suffix)
			if !measured {
				fmt.Printf("  %s\n\n", dimStyle.Render(
					"A radius of 1.00× here means \"unknown\", not \"safe\" — generate a graph to get a real bar."))
			}
			return nil
		},
	}
	blast.Flags().StringVar(&graphPath, "graph", "graph.json", "path to the dependency graph (graph.json)")
	blast.Flags().BoolVar(&jsonOut, "json", false, "machine-readable JSON output")

	var parGraph string
	var parJSON bool
	var serial float64
	parallelCmd := &cobra.Command{
		Use:   "parallel <file> [file...]",
		Short: "Optimal number of parallel agents for editing these files (Law 4)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			g, err := graph.Load(parGraph)
			if err != nil {
				return err
			}
			k := g.Coupling(args)
			n, speedup := optimal.Agents(serial, k)
			if parJSON {
				return json.NewEncoder(os.Stdout).Encode(map[string]any{
					"files": args, "serial_fraction": serial, "coordination_k": k,
					"optimal_agents": n, "speedup": speedup,
				})
			}
			fmt.Printf("\n  %s\n", cortexStyle.Render(fmt.Sprintf("%d file(s)", len(args))))
			fmt.Printf("    coordination cost k    %.3f\n", k)
			fmt.Printf("    optimal agents n*      %d\n", n)
			fmt.Printf("    speedup S(n*)          %.2f×\n", speedup)
			if speedup < 1 {
				fmt.Printf("    %s\n", dimStyle.Render("→ heavily coupled — parallelism doesn't pay; edit serially"))
			}
			fmt.Println()
			return nil
		},
	}
	parallelCmd.Flags().StringVar(&parGraph, "graph", "graph.json", "path to the dependency graph (graph.json)")
	parallelCmd.Flags().BoolVar(&parJSON, "json", false, "machine-readable JSON output")
	parallelCmd.Flags().Float64Var(&serial, "serial", optimal.DefaultSerialFraction, "serial (non-parallelizable) work fraction s")

	cmd.AddCommand(blast, parallelCmd)
	return cmd
}

// printSPRTResult renders an SPRT confidence run: the LLR ledger, the decision,
// and the winning answer.
func printSPRTResult(r *swarm.SPRTResult) {
	sep := dimStyle.Render("  " + strings.Repeat("─", 60))
	t := r.Trust

	fmt.Println()
	fmt.Printf("  %s [%s]\n\n", cortexStyle.Render("▶ SPRT"), dimStyle.Render(r.Domain))

	fmt.Printf("  %-28s  %-9s  %8s  %10s\n", "SOURCE", "VERDICT", "LLR", "Λ AFTER")
	fmt.Println(sep)
	for _, e := range t.Ledger {
		verdict := "agree"
		if !e.Agreed {
			verdict = "disagree"
		}
		fmt.Printf("  %-28.28s  %-9s  %+8.3f  %+10.3f\n", e.Source, verdict, e.LLR, e.LambdaAfter)
	}
	fmt.Println(sep)

	fmt.Printf("\n  %s %s  ·  confidence %.1f%%  ·  %d samples  ·  $%.4f\n",
		dimStyle.Render("Decision →"),
		cortexStyle.Render(t.Decision.String()),
		t.Confidence*100, t.Samples, t.SpentUSD,
	)
	fmt.Println()
	if t.Candidate != "" {
		fmt.Println(t.Candidate)
		fmt.Println()
	}
}

// logTrustRun appends the SPRT run to ~/.hydra/trust.jsonl (best-effort).
func logTrustRun(r *swarm.SPRTResult, prompt, domain string) {
	if domain == "" {
		domain = "default"
	}
	models := make([]string, 0, len(r.Attempts))
	seen := map[string]bool{}
	for _, a := range r.Attempts {
		if !seen[a.Head.ID] {
			seen[a.Head.ID] = true
			models = append(models, a.Head.ID)
		}
	}
	_, costSource, _ := cost.SourceLabels(anyEstimated(r.Attempts))
	_ = trust.LogRun(trust.DefaultLogPath(), trust.RunLog{
		TaskHash:   trust.TaskHash(prompt),
		Domain:     domain,
		TargetConf: r.Target,
		FinalConf:  r.Trust.Confidence,
		Samples:    r.Trust.Samples,
		Models:     models,
		CostUSD:    r.Trust.SpentUSD,
		CostSource: costSource,
		Decision:   r.Trust.Decision.String(),
		Ledger:     r.Trust.Ledger,
	})
}

func anyEstimated(attempts []swarm.Attempt) bool {
	for _, a := range attempts {
		if a.TokensEstimated {
			return true
		}
	}
	return false
}

// printSwarmResult renders the swarm result to stdout.
// printEnsemblePlan is what --dry-run shows for the swarm and SPRT paths: the
// heads that would be engaged and the cost of one round, so the flag answers
// "what would this spend" rather than spending it.
//
// The head count is an upper bound in SPRT mode: the ensemble stops as soon as
// the log-odds cross the target, so it usually queries fewer. Saying so beats
// printing a number that reads as a promise.
func printEnsemblePlan(heads []provider.Head, estUSD, confidence float64, mode swarm.SwarmMode, maxCostUSD float64) {
	sep := dimStyle.Render("  " + strings.Repeat("─", 60))

	label := fmt.Sprintf("swarm · %s", mode)
	if confidence > 0 {
		label = fmt.Sprintf("SPRT · target %.1f%%", confidence*100)
	}

	fmt.Println()
	fmt.Printf("  %s [%s]\n\n", cortexStyle.Render("▶ DRY RUN"), dimStyle.Render(label))
	fmt.Printf("  %-28s  %7s  %8s\n", "HEAD", "TIER", "SOURCE")
	fmt.Println(sep)
	for _, h := range heads {
		fmt.Printf("  %-28s  %7d  %8s\n", h.Name, rank.UITier(h), h.Source)
	}
	fmt.Println(sep)

	if confidence > 0 {
		fmt.Printf("  %s\n", dimStyle.Render(fmt.Sprintf("at most %d heads queried; SPRT stops early once the target is met", len(heads))))
	}
	fmt.Printf("  %s\n", dimStyle.Render(fmt.Sprintf("estimated cost for one round: $%.4f", estUSD)))
	if maxCostUSD > 0 && estUSD > maxCostUSD {
		fmt.Printf("  %s\n", warnStyle.Render(fmt.Sprintf("this exceeds --swarm-max-cost $%.4f and would be refused", maxCostUSD)))
	}
	fmt.Printf("  %s\n\n", dimStyle.Render("nothing was executed"))
}

func printSwarmResult(r *swarm.SwarmResult) {
	sep := dimStyle.Render("  " + strings.Repeat("─", 60))

	fmt.Println()
	fmt.Printf("  %s [%s · %d heads]\n\n",
		cortexStyle.Render("▶ SWARM"),
		dimStyle.Render(string(r.Mode)),
		len(r.Attempts),
	)

	// Attempt table header.
	fmt.Printf("  %-28s  %-10s  %7s  %7s  %8s\n", "HEAD", "STATUS", "TIME", "SCORE", "COST")
	fmt.Println(sep)

	for i, a := range r.Attempts {
		statusStr := statusIcon(a.Status) + " " + string(a.Status)
		timeStr := "-"
		if a.Duration > 0 {
			timeStr = fmt.Sprintf("%dms", a.Duration.Milliseconds())
		}
		scoreStr := "-"
		if r.Verdict != nil && a.Status == swarm.StatusOK && i < len(r.Verdict.Scores) {
			scoreStr = fmt.Sprintf("%d/100", r.Verdict.Scores[i])
		}
		costStr := fmt.Sprintf("$%.4f", a.EstCostUSD)
		if a.EstCostUSD == 0 {
			costStr = "-"
		}
		winnerMark := "  "
		if a.Rank == 1 {
			winnerMark = cortexStyle.Render("→ ")
		}
		fmt.Printf("%s%-28s  %-10s  %7s  %7s  %8s\n",
			winnerMark, a.Head.Name, statusStr, timeStr, scoreStr, costStr,
		)
	}

	fmt.Println(sep)

	// Verdict line.
	if r.Verdict != nil && r.Winner != nil {
		judgeLabel := "LLM judge"
		if r.Verdict.Meta.UsedFallback {
			judgeLabel = "cap-score fallback"
		}
		fmt.Printf("\n  %s %s  [%s, %dms]\n",
			dimStyle.Render("Winner →"),
			cortexStyle.Render(r.Winner.Head.Name),
			judgeLabel,
			r.Verdict.Meta.Duration.Milliseconds(),
		)
		if r.Verdict.Reason != "" {
			fmt.Printf("  %s\n", dimStyle.Render(`"`+r.Verdict.Reason+`"`))
		}
	} else if r.Winner != nil {
		fmt.Printf("\n  %s %s\n",
			dimStyle.Render("Winner →"),
			cortexStyle.Render(r.Winner.Head.Name),
		)
	}

	fmt.Printf("\n  %s  total $%.4f  ·  wall %dms  ·  %d/%d succeeded\n\n",
		sep,
		r.TotalCostUSD,
		r.WallDuration.Milliseconds(),
		r.SucceededCount(),
		len(r.Attempts),
	)

	// Winner output.
	if r.Winner != nil && r.Winner.Output != "" {
		fmt.Println(r.Winner.Output)
		fmt.Println()
	}
}

func statusIcon(s swarm.HeadStatus) string {
	switch s {
	case swarm.StatusOK:
		return "✓"
	case swarm.StatusCanceled:
		return "·"
	default:
		return "✗"
	}
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

			// Resolve rather than mint, so HYDRA_RUN_ID groups an edit with the
			// invocation that spawned it (#204, #211).
			runID, taskID := runid.ResolveRun(""), runid.ResolveTask("")
			hb := runlog.StartHeartbeat(ctx, runID, runlog.HeartbeatInterval)
			defer hb.Stop()

			result, err := editor.Edit(ctx, editor.Request{
				File:     file,
				Enum:     enum,
				Prompt:   prompt,
				Validate: !noValidate,
				RunID:    runID,
				TaskID:   taskID,
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
			results, err := parallel.Run(ctx, tasks, parallel.Options{RunID: runid.New()})
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
			Use:   "by-task <task_id>",
			Short: "Spending for a specific task",
			Args:  cobra.ExactArgs(1),
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
			Use:   "by-run <run_id>",
			Short: "Spending for a playbook run",
			Args:  cobra.ExactArgs(1),
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
		Use:   "tail [N]",
		Short: "Last N calls (default 10)",
		Args:  cobra.MaximumNArgs(1),
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
		Use:   "json",
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

// ── stats ─────────────────────────────────────────────────────────────────────

func cmdStats() *cobra.Command {
	var (
		days      int
		byModel   bool
		byTier    bool
		byDay     bool
		swarmOnly bool
		sessionID string
		jsonOut   bool
	)

	printJSON := func(v any) {
		raw, _ := json.MarshalIndent(v, "", "  ")
		fmt.Println(string(raw))
	}

	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Spend analytics — model, tier, day, and swarm breakdowns",
		Long: `hyctl stats shows cost.jsonl summaries with rich grouping options.

Examples:
  hyctl stats                  # today summary
  hyctl stats --days 7         # last 7 days, grouped by model
  hyctl stats --model          # all-time by model
  hyctl stats --tier           # all-time by tier
  hyctl stats --day            # all-time by day
  hyctl stats --swarm          # swarm-only stats + winner rate
  hyctl stats --session <id>   # single session/task breakdown
  hyctl stats --json           # machine-readable output`,
		RunE: func(_ *cobra.Command, _ []string) error {
			all, err := cost.LoadAll()
			if err != nil {
				return err
			}

			// Session filter takes priority.
			if sessionID != "" {
				var rows []cost.Row
				for _, r := range all {
					if r.TaskID == sessionID || r.RunID == sessionID {
						rows = append(rows, r)
					}
				}
				if len(rows) == 0 {
					return fmt.Errorf("no entries found for session %q", sessionID)
				}
				groups := cost.ByModel(rows)
				if jsonOut {
					printJSON(groups)
					return nil
				}
				cost.RenderStatsTable(fmt.Sprintf("session %s", sessionID), groups)
				return nil
			}

			rows := cost.FilterDays(all, days)

			period := "all time"
			if days > 0 {
				period = fmt.Sprintf("last %d days", days)
			} else if days == 0 && !byModel && !byTier && !byDay && !swarmOnly {
				// Default: today
				rows = cost.FilterDays(all, 1)
				period = "today"
			}

			if swarmOnly {
				s := cost.SwarmStats(rows)
				if jsonOut {
					printJSON(s)
					return nil
				}
				cost.RenderSwarmStats(s)
				return nil
			}

			if byDay {
				groups := cost.ByDay(rows)
				if jsonOut {
					printJSON(groups)
					return nil
				}
				cost.RenderStatsTable(period+" · by day", groups)
				return nil
			}

			if byTier {
				groups := cost.GroupBy(rows, func(r cost.Row) string {
					if r.Tier == 0 {
						return "unknown"
					}
					return fmt.Sprintf("tier-%d", r.Tier)
				})
				if jsonOut {
					printJSON(groups)
					return nil
				}
				cost.RenderStatsTable(period+" · by tier", groups)
				return nil
			}

			// Default grouping: by model.
			groups := cost.ByModel(rows)
			if jsonOut {
				printJSON(groups)
				return nil
			}
			cost.RenderStatsTable(period+" · by model", groups)

			// Append swarm summary if any swarm rows exist.
			s := cost.SwarmStats(rows)
			if s.Runs > 0 {
				cost.RenderSwarmStats(s)
			}
			return nil
		},
	}

	cmd.Flags().IntVar(&days, "days", 0, "show last N days (0 = today by default)")
	cmd.Flags().BoolVar(&byModel, "model", false, "group by model (default grouping)")
	cmd.Flags().BoolVar(&byTier, "tier", false, "group by tier")
	cmd.Flags().BoolVar(&byDay, "day", false, "group by calendar day")
	cmd.Flags().BoolVar(&swarmOnly, "swarm", false, "swarm-only stats: winner rate, avg wall time, mode breakdown")
	cmd.Flags().StringVar(&sessionID, "session", "", "filter to a single task_id or run_id")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable JSON output")
	return cmd
}

// ── pricing ───────────────────────────────────────────────────────────────────

func cmdPricing() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pricing",
		Short: "Manage live model pricing data (OpenRouter cache)",
	}
	cmd.AddCommand(cmdPricingRefresh(), cmdPricingList())
	return cmd
}

func cmdPricingRefresh() *cobra.Command {
	return &cobra.Command{
		Use:   "refresh",
		Short: "Force-fetch latest pricing from OpenRouter and update local cache",
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(os.Stderr, "Fetching pricing from OpenRouter…")
			n, err := pricing.Refresh()
			if err != nil {
				return fmt.Errorf("pricing refresh: %w", err)
			}
			fmt.Fprintf(os.Stdout, "Cached pricing for %d models.\n", n)
			return nil
		},
	}
}

func cmdPricingList() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list [filter]",
		Short: "List all models and their $/1M token rates (sorted alphabetically)",
		RunE: func(cmd *cobra.Command, args []string) error {
			db := pricing.Load()
			// Hoist filter once — db.Models() is already sorted.
			filter := strings.ToLower(strings.Join(args, " "))

			if jsonOut {
				type row struct {
					Model            string  `json:"model"`
					InputPerMillion  float64 `json:"input_per_mtok"`
					OutputPerMillion float64 `json:"output_per_mtok"`
				}
				var rows []row
				for _, m := range db.Models() {
					if filter != "" && !strings.Contains(m, filter) {
						continue
					}
					p, _ := db.ModelPrice(m)
					rows = append(rows, row{m, p.InputPerMillion, p.OutputPerMillion})
				}
				return json.NewEncoder(os.Stdout).Encode(rows)
			}

			fmt.Fprintf(os.Stdout, "%-55s  %10s  %11s\n", "Model", "In $/1M", "Out $/1M")
			fmt.Fprintln(os.Stdout, strings.Repeat("─", 82))
			count := 0
			for _, m := range db.Models() {
				if filter != "" && !strings.Contains(m, filter) {
					continue
				}
				p, _ := db.ModelPrice(m)
				fmt.Fprintf(os.Stdout, "%-55s  %10.4f  %11.4f\n",
					m, p.InputPerMillion, p.OutputPerMillion)
				count++
			}
			if count == 0 {
				fmt.Fprintln(os.Stdout, "(no models matched)")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

// ── trust ───────────────────────────────────────────────────────────────────────

func cmdTrust() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "trust",
		Short: "Confidence layer: source calibration and defect-cost (Trust Control Plane)",
	}
	cmd.AddCommand(cmdTrustCalibration(), cmdTrustRecord(), cmdTrustDefect(),
		cmdTrustStats(), cmdTrustExplain(), cmdTrustBenchmark())
	return cmd
}

func cmdTrustBenchmark() *cobra.Command {
	var trials int
	var seed int64
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "benchmark",
		Short: "Measure the SPRT ensemble on a synthetic suite (the [MEASURED] Law 3 numbers)",
		RunE: func(_ *cobra.Command, _ []string) error {
			r := trust.Benchmark(trials, seed)
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(r)
			}
			fmt.Printf("\n  Hydra Trust Benchmark — %d trials/case (vs fixed-%d swarm)\n", r.Trials, r.FixedN)
			fmt.Println("  " + strings.Repeat("─", 62))
			fmt.Printf("  %-18s %10s %10s %12s\n", "workload", "samples", "accuracy", "vs fixed-N")
			row := func(c trust.BenchCase) {
				fmt.Printf("  %-18s %10.2f %9.1f%% %11.0f%%\n", c.Label, c.MeanSamples, c.Accuracy*100, c.SavedPct)
			}
			for _, c := range r.Cases {
				row(c)
			}
			fmt.Println("  " + strings.Repeat("─", 62))
			row(r.Blended)
			fmt.Println()
			return nil
		},
	}
	cmd.Flags().IntVar(&trials, "trials", 20000, "trials per difficulty")
	cmd.Flags().Int64Var(&seed, "seed", 42, "PRNG seed (fixed → reproducible)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable JSON output")
	return cmd
}

func cmdTrustStats() *cobra.Command {
	var days int
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "stats",
		Short: "SPRT run rollup: samples saved vs fixed-N, auto-clear rate, achieved vs target confidence",
		RunE: func(_ *cobra.Command, _ []string) error {
			runs, err := trust.LoadRuns(trust.DefaultLogPath())
			if err != nil {
				return err
			}
			if days > 0 {
				cutoff := time.Now().UTC().AddDate(0, 0, -days).Format(time.RFC3339)
				kept := runs[:0]
				for _, r := range runs {
					if r.TS >= cutoff {
						kept = append(kept, r)
					}
				}
				runs = kept
			}
			s := trust.Aggregate(runs, 5) // compare against a fixed-5 swarm
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(s)
			}
			if s.Runs == 0 {
				fmt.Println("\n  No SPRT runs yet. Try `hyctl dispatch --confidence 0.95 --prompt ...`.")
				return nil
			}
			fmt.Printf("\n  Trust stats (%d runs)\n", s.Runs)
			fmt.Println("  " + strings.Repeat("─", 52))
			fmt.Printf("  mean samples        %.2f  (vs fixed-%d swarm)\n", s.MeanSamples, s.FixedSwarmN)
			fmt.Printf("  samples saved       %.0f%%\n", s.SamplesSavedPct)
			fmt.Printf("  auto-cleared        %.0f%%  (reached target without a human)\n", s.AutoClearedPct)
			fmt.Printf("  confidence          target %.1f%% → achieved %.1f%%\n",
				s.MeanTargetConf*100, s.MeanFinalConf*100)
			fmt.Printf("  total spend         $%.4f\n\n", s.TotalCostUSD)
			return nil
		},
	}
	cmd.Flags().IntVar(&days, "days", 0, "only include runs from the last N days")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable JSON output")
	return cmd
}

func cmdTrustExplain() *cobra.Command {
	return &cobra.Command{
		Use:   "explain <task_hash>",
		Short: "Show the LLR ledger for a past SPRT run — why it stopped where it did",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			runs, err := trust.LoadRuns(trust.DefaultLogPath())
			if err != nil {
				return err
			}
			for i := len(runs) - 1; i >= 0; i-- { // newest match first
				r := runs[i]
				if r.TaskHash != args[0] {
					continue
				}
				fmt.Printf("\n  task %s  ·  domain %s  ·  %s\n", r.TaskHash, r.Domain, r.TS)
				fmt.Printf("  target %.1f%% → achieved %.1f%%  ·  %d samples  ·  %s\n\n",
					r.TargetConf*100, r.FinalConf*100, r.Samples, r.Decision)
				fmt.Printf("  %-28s  %-9s  %8s  %10s\n", "SOURCE", "VERDICT", "LLR", "Λ AFTER")
				fmt.Println("  " + strings.Repeat("─", 60))
				for _, e := range r.Ledger {
					verdict := "agree"
					if !e.Agreed {
						verdict = "disagree"
					}
					fmt.Printf("  %-28.28s  %-9s  %+8.3f  %+10.3f\n", e.Source, verdict, e.LLR, e.LambdaAfter)
				}
				fmt.Println()
				return nil
			}
			return fmt.Errorf("no run found with task_hash %q", args[0])
		},
	}
}

func cmdTrustCalibration() *cobra.Command {
	var domain string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "calibration",
		Short: "Per-source sensitivity / specificity / diagnostic power (D)",
		RunE: func(_ *cobra.Command, _ []string) error {
			cal, err := trust.New(trust.DefaultPath())
			if err != nil {
				return err
			}
			stats := cal.Report()
			if domain != "" {
				filtered := stats[:0]
				for _, s := range stats {
					if s.Domain == domain {
						filtered = append(filtered, s)
					}
				}
				stats = filtered
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(stats)
			}
			if len(stats) == 0 {
				fmt.Println("\n  No calibration recorded yet. Feed outcomes with `hyctl trust record`.")
				return nil
			}
			fmt.Printf("\n  %-28s %-10s %6s %7s %7s %8s\n", "Source", "Domain", "n", "se", "sp", "D(nats)")
			fmt.Println("  " + strings.Repeat("─", 74))
			for _, s := range stats {
				fmt.Printf("  %-28.28s %-10.10s %6.0f %7.3f %7.3f %8.3f\n",
					s.Source, s.Domain, s.N, s.Se, s.Sp, s.D)
			}
			fmt.Println()
			return nil
		},
	}
	cmd.Flags().StringVar(&domain, "domain", "", "filter to one domain")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable JSON output")
	return cmd
}

func cmdTrustRecord() *cobra.Command {
	var source, domain, outcome string
	var saidCorrect bool
	cmd := &cobra.Command{
		Use:   "record",
		Short: "Record a source's verdict + ground-truth outcome to train calibration",
		RunE: func(_ *cobra.Command, _ []string) error {
			o := trust.ParseOutcome(outcome)
			if o == trust.OutcomeUnknown {
				return fmt.Errorf("--outcome must be correct|incorrect (got %q)", outcome)
			}
			if source == "" {
				return fmt.Errorf("--source is required")
			}
			cal, err := trust.New(trust.DefaultPath())
			if err != nil {
				return err
			}
			if err := cal.Update(source, domain, saidCorrect, o); err != nil {
				return err
			}
			fmt.Printf("  recorded: %s/%s said_correct=%v outcome=%s → D=%.3f nats\n",
				source, domain, saidCorrect, outcome, cal.D(source, domain))
			return nil
		},
	}
	cmd.Flags().StringVar(&source, "source", "", "evidence source, e.g. model:claude-sonnet or verifier:tests")
	cmd.Flags().StringVar(&domain, "domain", "", "task domain")
	cmd.Flags().BoolVar(&saidCorrect, "said-correct", false, "the source's raw verdict")
	cmd.Flags().StringVar(&outcome, "outcome", "", "ground truth: correct|incorrect")
	return cmd
}

func cmdTrustDefect() *cobra.Command {
	var domain, file, graphPath string
	var blast float64
	var irreversible, pii, production bool
	cmd := &cobra.Command{
		Use:   "defect",
		Short: "Preview the modeled cost of a wrong answer + the confidence it demands",
		RunE: func(_ *cobra.Command, _ []string) error {
			dm := trust.NewDefectModel()

			// --file derives blast radius from the code dependency graph,
			// overriding --blast when a graph is available.
			blastSource := "flag"
			if file != "" {
				g, err := graph.Load(graphPath)
				if err != nil {
					return err
				}
				blast = g.BlastRadiusForFile(file)
				blastSource = "graph"
			}

			task := trust.Task{
				Domain:       domain,
				BlastRadius:  blast,
				Irreversible: irreversible,
				TouchesPII:   pii,
				Production:   production,
			}
			fmt.Printf("  defect cost ≈ $%.2f  (blast=%.2f [%s] irreversible=%v pii=%v prod=%v)\n",
				dm.CostUSD(task), blast, blastSource, irreversible, pii, production)
			fmt.Printf("  → demands confidence ≥ %.1f%%  (use with: hyctl dispatch --confidence %.3f)\n",
				dm.RequiredConfidence(task)*100, dm.RequiredConfidence(task))
			return nil
		},
	}
	cmd.Flags().StringVar(&domain, "domain", "", "task domain")
	cmd.Flags().Float64Var(&blast, "blast", 1.0, "blast-radius multiplier (ignored when --file is set)")
	cmd.Flags().StringVar(&file, "file", "", "derive blast radius from this file's dependents in the code graph")
	cmd.Flags().StringVar(&graphPath, "graph", "graph.json", "path to the dependency graph (graph.json)")
	cmd.Flags().BoolVar(&irreversible, "irreversible", false, "change cannot be cheaply undone")
	cmd.Flags().BoolVar(&pii, "pii", false, "task handles personal data")
	cmd.Flags().BoolVar(&production, "production", false, "target is production")
	return cmd
}

// ── styles ────────────────────────────────────────────────────────────────────

var (
	cortexStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	warnStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
)

// promptPreview shortens a prompt for a log Detail field. Run events carry a
// short human label, never the full text — the atomic-append guarantee that
// makes the run log safe under concurrency is per write() call, so entries must
// stay small.
func promptPreview(s string) string {
	const max = 80
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
