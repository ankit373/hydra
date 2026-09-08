// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

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
	"github.com/ankit373/hydra/internal/egress"
	"github.com/ankit373/hydra/internal/entropy"
	"github.com/ankit373/hydra/internal/evalset"
	"github.com/ankit373/hydra/internal/graph"
	"github.com/ankit373/hydra/internal/health"
	"github.com/ankit373/hydra/internal/ledger"
	"github.com/ankit373/hydra/internal/mcpregistry"
	"github.com/ankit373/hydra/internal/ope"
	"github.com/ankit373/hydra/internal/optimal"
	"github.com/ankit373/hydra/internal/oracle"
	"github.com/ankit373/hydra/internal/otlp"
	"github.com/ankit373/hydra/internal/parallel"
	"github.com/ankit373/hydra/internal/payload"
	"github.com/ankit373/hydra/internal/pending"
	"github.com/ankit373/hydra/internal/policy"
	"github.com/ankit373/hydra/internal/pricing"
	"github.com/ankit373/hydra/internal/probe"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/rank"
	"github.com/ankit373/hydra/internal/review"
	"github.com/ankit373/hydra/internal/rollup"
	"github.com/ankit373/hydra/internal/runid"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/security"
	"github.com/ankit373/hydra/internal/shellpath"
	"github.com/ankit373/hydra/internal/swarm"
	"github.com/ankit373/hydra/internal/trust"
	"github.com/ankit373/hydra/internal/tui"
	"github.com/ankit373/hydra/internal/update"
	"github.com/ankit373/hydra/internal/util"

	_ "github.com/ankit373/hydra/internal/provider/agy"
	_ "github.com/ankit373/hydra/internal/provider/cli"
	_ "github.com/ankit373/hydra/internal/provider/env"
	_ "github.com/ankit373/hydra/internal/provider/port"
)

func main() {
	// Cancelling this is what tears a run's heads down, now that each has a
	// process group of its own and the terminal's Ctrl+C no longer reaches it
	// (#738). First, because Adopt below can spend 3s in a login shell.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		// Restore the default action, so a second Ctrl+C kills a teardown that
		// has itself wedged rather than being swallowed like the first.
		stop()
	}()

	// Before any command discovers heads. A hyctl or `hyctl tui` started by
	// something other than a shell, a launcher or an IDE, gets a PATH with no
	// CLI head in it at all (#689). No-op when a shell already set one.
	shellpath.Adopt()

	// Fire update check in the background, never blocks startup.
	updateCh := update.CheckAsync()

	if err := rootCmd().ExecuteContext(ctx); err != nil {
		if ctx.Err() != nil {
			// 130 is the shell's convention for "killed by SIGINT", and exit
			// codes are a contract here (see exit_code_test.go).
			fmt.Fprintln(os.Stderr, "  interrupted")
			os.Exit(130)
		}
		fmt.Fprintln(os.Stderr, "Error:", err)
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
		// Check still in flight, skip notification rather than block.
	}
}

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "hyctl",
		Short: "Multi-model AI orchestration, one Cortex, many Heads",
		// `hyctl --version` used to answer "unknown flag" even though a version
		// subcommand existed. It is the near-universal convention, so people
		// type it first. Same text as the subcommand, from one function.
		Version: build.Version,
		// A runtime error (e.g. "no hydra config") used to dump the full flags
		// block for every one of ~25 subcommands, drowning the one line that
		// actually mattered. Propagates to every subcommand, including a
		// flag-parsing error (unknown flag, bad value), which now reads the
		// same way: one line naming the problem. Run --help for the flag list
		// (#464).
		SilenceUsage: true,
		// main prints the error instead, in Cobra's own format, so that a
		// cancelled run can read as "interrupted" rather than as a failure.
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !config.Exists() {
				return runInit(cmd.Context())
			}
			return cmd.Help()
		},
	}
	root.SetVersionTemplate(versionText())
	root.AddCommand(
		cmdInit(), cmdProbe(), cmdStatus(), cmdTui(), cmdDispatch(),
		cmdEdit(), cmdReview(), cmdParallel(), cmdCost(), cmdStats(),
		cmdPricing(), cmdTrust(), cmdGraph(), cmdContext(), cmdMCP(), cmdOracle(), cmdEval(), cmdTrace(), cmdModels(),
		cmdWorkflow(),
		cmdSecurity(), cmdAsk(), cmdVersion(), cmdUpgrade(),
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

// ── upgrade ───────────────────────────────────────────────────────────────────

// installScriptCommand is a var so a test can override it without touching
// the network, matching the pattern update.ReleaseURL uses for the same
// reason.
var installScriptCommand = "curl -fsSL https://raw.githubusercontent.com/ankit373/hydra/main/install.sh | sh"

func cmdUpgrade() *cobra.Command {
	return &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade hyctl to the latest release",
		Long: "Re-runs install.sh, the same curl installer documented for a fresh " +
			"install. It downloads the latest release, verifies its checksum, and " +
			"mv's the new binary over the old one, a rename, not a rewrite, so " +
			"this process keeps running on its already-loaded pages until it exits " +
			"and the new binary takes effect on the next invocation.\n\n" +
			"Skipped for a Homebrew install: overwriting Homebrew's symlink here " +
			"would desync it from `brew`'s own bookkeeping. Run `brew upgrade " +
			"hyctl` there instead, the same command the update banner already " +
			"recommends.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUpgrade(cmd.OutOrStdout())
		},
	}
}

// executablePath is a var so a test can point runUpgrade at a fake path
// (e.g. one inside a fake Cellar) without depending on where the test binary
// itself happens to live.
var executablePath = os.Executable

// runUpgrade re-runs install.sh in place. HYDRA_BIN is pointed at the
// currently running binary's own directory so the exact binary on PATH gets
// replaced, rather than install.sh falling back to its own default (which may
// not be the same directory this process was launched from).
func runUpgrade(w io.Writer) error {
	exe, exeErr := executablePath()
	if exeErr == nil && isHomebrewInstall(exe) {
		fmt.Fprintln(w, dimStyle.Render("  hyctl was installed via Homebrew, run: brew upgrade hyctl"))
		return nil
	}

	fmt.Fprintln(w, dimStyle.Render("  Running install.sh..."))
	c := exec.Command("sh", "-c", installScriptCommand)
	c.Stdout = w
	c.Stderr = w
	if exeErr == nil {
		c.Env = append(os.Environ(), "HYDRA_BIN="+filepath.Dir(exe))
	}
	return c.Run()
}

// isHomebrewInstall reports whether exePath resolves into a Homebrew Cellar,
// true for both /usr/local/Cellar and /opt/homebrew/Cellar on macOS, and
// Linuxbrew's /home/linuxbrew/.linuxbrew/Cellar.
func isHomebrewInstall(exePath string) bool {
	real, err := filepath.EvalSymlinks(exePath)
	if err != nil {
		real = exePath
	}
	return strings.Contains(real, "/Cellar/")
}

// ── tui (interactive cockpit) ───────────────────────────────────────────────────

func cmdTui() *cobra.Command {
	var snapshot bool
	var snapView int
	c := &cobra.Command{
		Use:   "tui",
		Short: "Interactive cockpit, chat, route work, and watch spend live",
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
			// Mouse support is for wheel scrolling; the keyboard remains
			// fully sufficient on terminals without it.
			p := tea.NewProgram(tui.NewCockpit(), tea.WithAltScreen(), tea.WithMouseCellMotion())
			_, err := p.Run()
			return err
		},
	}
	c.Flags().BoolVar(&snapshot, "snapshot", false, "Render one static frame and exit (docs/preview)")
	c.Flags().IntVar(&snapView, "view", 0, "With --snapshot: render a single view (0 chat, 1 agents, 2 models, 3 activity, 4 usage, 5 audit)")
	return c
}

// ── init ──────────────────────────────────────────────────────────────────────

func cmdInit() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "First-run wizard: discover Heads and choose your Cortex",
		RunE:  func(cmd *cobra.Command, _ []string) error { return runInit(cmd.Context()) },
	}
}

// requireTerminal refuses to open an interactive UI when there is nothing to
// render it on.
//
// Without this the wizard hangs. On unix Bubble Tea fails opening /dev/tty, so
// the symptom is at least an error; on Windows there is no equivalent failure
// and it blocks reading stdin forever, so `hyctl init` in a Dockerfile, a CI
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

func runInit(ctx context.Context) error {
	if err := requireTerminal("hyctl init"); err != nil {
		return err
	}
	// Provision the access policy before anything else. Until #722 nothing
	// ever wrote this file, so LoadPolicy returned a default-allow policy on
	// every install and the gate in the dispatch path recorded without ever
	// blocking. Failing to write it is not fatal: init's job is to get the
	// user running, and hyctl security reports the fail-open state loudly.
	if created, err := ledger.EnsurePolicy(ledger.DefaultPolicyPath()); err != nil {
		fmt.Println(warnStyle.Render("  could not write the access policy: " + err.Error()))
	} else if created {
		fmt.Println(dimStyle.Render("  Wrote access policy to " + ledger.DefaultPolicyPath()))
	}

	fmt.Println(dimStyle.Render("  Scanning your machine for AI models..."))
	result := probe.Run(ctx)

	if len(result.Heads) == 0 {
		// Nothing found, guide the user through installing something.
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

// probeHeadJSON is probe --json's per-head shape. provider.Head has no json
// tags of its own, it is an internal discovery record, not a wire format,
// so this stays a local, deliberately-chosen subset rather than exposing
// every internal field probe.Run happens to populate.
type probeHeadJSON struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Provider  string `json:"provider"`
	Source    string `json:"source"`
	CapScore  int    `json:"cap_score"`
	LocalOnly bool   `json:"local_only"`
	IsCortex  bool   `json:"is_cortex"`
	// Routable is false when discovery found the head but no executor can
	// drive it (e.g. the Ollama binary with its server not running), the
	// same distinction the human table marks with ✗ (#248).
	Routable         bool   `json:"routable"`
	UnroutableReason string `json:"unroutable_reason,omitempty"`
	// What the provider reports about the weights actually loaded, omitted
	// when it reports nothing. Ollama is the only source of these today.
	Quant  string `json:"quant,omitempty"`
	Params string `json:"params,omitempty"`
	// CtxMax is the architectural ceiling, not the window the head runs at:
	// a 40960 model still runs at its server's default. It is what caps a
	// declared window in internal/budget (#764).
	CtxMax int `json:"ctx_max,omitempty"`
}

func cmdProbe() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "probe",
		Short: "Scan machine for available AI Heads",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !jsonOut {
				fmt.Println(dimStyle.Render("  Scanning..."))
			}
			result := probe.Run(cmd.Context())
			cortexName := "none"
			if result.Cortex != nil {
				cortexName = result.Cortex.Name
			}
			if jsonOut {
				hs, now := health.Open(health.DefaultPath()), time.Now()
				heads := make([]probeHeadJSON, len(result.Heads))
				for i, h := range result.Heads {
					why := health.Reason(hs, h, now)
					ctxMax, _ := budget.ContextCeiling(h)
					heads[i] = probeHeadJSON{
						ID: h.ID, Name: h.Name, Provider: h.Provider, Source: h.Source,
						CapScore: h.CapScore, LocalOnly: h.LocalOnly,
						IsCortex: result.Cortex != nil && h.ID == result.Cortex.ID,
						Routable: why == "", UnroutableReason: why,
						Quant: h.Meta["model_quant"], Params: h.Meta["model_params"],
						CtxMax: ctxMax,
					}
				}
				warnings := result.Warnings
				if warnings == nil {
					warnings = []string{}
				}
				// A JSON caller cannot derive the catalogue size from the heads,
				// so it would have no way to tell an allowlist of 12 from all
				// there is (#752).
				enabled, catalogue := openRouterCounts()
				return json.NewEncoder(os.Stdout).Encode(map[string]any{
					"cortex": cortexName, "heads": heads, "warnings": warnings,
					"openrouter": map[string]int{"enabled": enabled, "catalogue": catalogue},
				})
			}
			fmt.Println(tui.Splash(cortexName))
			// A provider that failed outright (e.g. a corrupted models.json
			// overlay) doesn't even reach the unroutable-head accounting below,
			// its heads never existed here, so without this it degrades to a
			// silently smaller head list, contradicting probe's own "marks
			// unroutable heads with the reason" promise (#248).
			for _, w := range result.Warnings {
				fmt.Printf("  %s %s\n", warnStyle.Render("⚠"), dimStyle.Render(w))
			}
			if len(result.Heads) == 0 {
				fmt.Println("  No models found.")
				return nil
			}
			// Only Ollama reports a quant today, so the column is dead space
			// on a machine with no local models (#762).
			var anyQuant bool
			// A name past the column pushed every later column right. The fixed
			// width had always done that; adding one made it plain, so size the
			// column to the names, capped to stay inside a normal terminal.
			const minHead, maxHead = 30, 44
			headWidth := minHead
			for _, h := range result.Heads {
				anyQuant = anyQuant || h.Meta["model_quant"] != ""
				if n := utf8.RuneCountInString(h.Name); n > headWidth {
					headWidth = min(n, maxHead)
				}
			}
			header := fmt.Sprintf("  %-*s  %-5s  %-5s  %s", headWidth, "Head", "Score", "Src", "Provider")
			rowOf := func(h provider.Head) string {
				return fmt.Sprintf("%-*s  %-5d  %-5s  %s", headWidth, h.Name, h.CapScore, h.Source, h.Provider)
			}
			if anyQuant {
				header = fmt.Sprintf("  %-*s  %-8s  %-5s  %-5s  %s", headWidth, "Head", "Quant", "Score", "Src", "Provider")
				rowOf = func(h provider.Head) string {
					return fmt.Sprintf("%-*s  %-8s  %-5d  %-5s  %s",
						headWidth, h.Name, h.Meta["model_quant"], h.CapScore, h.Source, h.Provider)
				}
			}
			fmt.Println(header)
			// Derived, not a second constant to keep in sync with the widths.
			fmt.Println("  " + strings.Repeat("─", len(strings.TrimSpace(header))))
			// Discovery finding a head is not the same as Hydra being able to
			// drive it. Listing both identically is what let `probe` advertise
			// the Ollama binary that `dispatch --local` then refused (#248), so
			// unroutable heads are marked and carry their reason.
			var unroutable int
			hs, now := health.Open(health.DefaultPath()), time.Now()
			for _, h := range result.Heads {
				marker := "  "
				if result.Cortex != nil && h.ID == result.Cortex.ID {
					marker = cortexStyle.Render("→ ")
				}
				why := health.Reason(hs, h, now)
				if why != "" {
					marker = warnStyle.Render("✗ ")
					unroutable++
				}
				row := rowOf(h)
				if why == "" {
					fmt.Printf("%s%s\n", marker, row)
					continue
				}
				fmt.Printf("%s%s\n", marker, dimStyle.Render(row))
				fmt.Printf("    %s\n", dimStyle.Render("↳ "+why))
			}
			if unroutable > 0 {
				fmt.Printf("\n  %s\n", dimStyle.Render(fmt.Sprintf(
					"✗ = discovered but not routable (%d of %d), dispatch will skip these.",
					unroutable, len(result.Heads))))
			}
			if enabled, catalogue := openRouterCounts(); enabled > 0 {
				fmt.Printf("\n  %s\n", dimStyle.Render(
					openRouterNote(enabled, catalogue, config.Path())))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable JSON output")
	return cmd
}

// ── status ────────────────────────────────────────────────────────────────────

func cmdStatus() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show current Cortex, Head configuration, and budget utilisation",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("no config found, run: hyctl init")
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
			// Discovery, not cfg.Tiers: that is a snapshot `hyctl init` wrote
			// once, and it advertised three heads that could not serve (#714).
			// Measured at 10-40ms, so there is nothing to save by trusting it.
			result := probe.Run(cmd.Context())
			hs, now := health.Open(health.DefaultPath()), time.Now()
			reason := func(h provider.Head) string { return health.Reason(hs, h, now) }

			fmt.Println()
			for _, w := range result.Warnings {
				fmt.Printf("  %s %s\n", warnStyle.Render("⚠"), dimStyle.Render(w))
			}
			fmt.Print(headTiers(result.Heads, reason))
			fmt.Println()

			// Budget section, read from state.json (written by dispatcher).
			names := make(map[string]string, len(result.Heads))
			for _, h := range result.Heads {
				names[h.ID] = h.Name
			}
			printBudgetStatus(names)
			return nil
		},
	}
}

// names maps a head ID to its display name; a nil map leaves every row on its ID.
func printBudgetStatus(names map[string]string) {
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
	// accumulated, show the rate-aware effective mode (first-passage risk), a
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
	// Sorted on the label, because ranging the map reordered the table on every
	// invocation, and ordering by an ID the table does not show reads as random.
	type row struct {
		label string
		id    string
	}
	rows := make([]row, 0, len(state.Budget))
	for id := range state.Budget {
		// Discovery's display name where it has one, so this table names a head
		// the way `probe` and the tier table above do (#714).
		label := id
		if n := names[id]; n != "" {
			label = n
		}
		rows = append(rows, row{label: label, id: id})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].label != rows[j].label {
			return rows[i].label < rows[j].label
		}
		return rows[i].id < rows[j].id
	})
	for _, r := range rows {
		snap := state.Budget[r.id]
		pct := int(toFloat(snap["pct"]))
		used := int(toFloat(snap["used"]))
		window := int(toFloat(snap["window"]))
		mode, _ := snap["mode"].(string)
		bar := budgetBar(pct)
		fmt.Printf("  %-20s  %s %3d%%  %-8s  %s\n",
			truncLabel(r.label, 20),
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
	// state.json is written by another process, and by hand, per the
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
		maxCost   float64
		noStream  bool
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
		RunE: func(cmd *cobra.Command, args []string) error {
			// Checked before the enum name: IsKnownEnum reports false when the
			// routing table could not be loaded at all, so a broken routing.yaml
			// otherwise came back as "unknown --enum SIMPLE", blaming a valid key
			// for a file problem (#720).
			if err := dispatch.RoutingError(); err != nil {
				return err
			}
			// An unrecognized --enum must fail here, before anything routes: its
			// zero value is byte-identical to "no enum given" everywhere else in
			// this function, so a typo silently routed to the single strongest
			// (most expensive) head instead of being reported (#501).
			if enumKey != "" && !dispatch.IsKnownEnum(enumKey) {
				return fmt.Errorf("unknown --enum %q: not a recognized routing enum key", enumKey)
			}

			prompt := strings.Join(args, " ")
			ctx := cmd.Context()

			// One invocation is one run with one logical task, whichever path
			// below handles it, so a swarm's attempts and the dispatch that
			// drove them share an identity in the logs (#181).
			//
			// Resolve rather than mint: runid ranks explicit > env > generated,
			// so calling New() here and passing the result as the explicit value
			// silently outranked HYDRA_RUN_ID and made it dead at the only entry
			// point that matters (#204).
			runID, taskID := runid.ResolveRun(""), runid.ResolveTask("")

			// Mark the run live for its whole duration. Without this
			// runlog.LiveRuns() is always empty and nothing, cockpit or desktop
			// Fleet: can distinguish a running agent from a finished one.
			//
			// --dry-run executes nothing in every mode it combines with (plain,
			// --swarm, --confidence): no head is chosen, no output produced.
			// Logging one anyway leaves a permanent, contentless Fleet card
			// behind on every preview, 0ms elapsed, $0.00, no agents, nothing
			// to say why, indistinguishable from a broken reconstruction (#379).
			if !dryRun {
				hb := runlog.StartHeartbeat(ctx, runID, runlog.HeartbeatInterval)
				defer hb.Stop()

				rl := runlog.New(runID)
				_ = rl.Append(runlog.Event{Kind: runlog.KindRunStarted, TaskID: taskID, Detail: promptPreview(prompt)})
				defer func() {
					_ = rl.Append(runlog.Event{Kind: runlog.KindRunFinished, TaskID: taskID})
				}()
			}

			// editor and review record outcomes under the target file's own
			// domain, so a --file run has to look up that same cell rather than
			// "default", which nothing fills (#785). An explicit --domain always
			// wins; this only supplies the one the writers would have used.
			if file != "" && !cmd.Flags().Changed("domain") {
				if d := trust.DomainForFile(file); d != trust.DefaultDomain {
					domain = d
				}
			}

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
			//
			// Validate the raw flag the moment it was set, before --file's
			// derived target (always in [0.5, maxConfidence]) can override an
			// invalid explicit value and mask it. `> 0` alone let NaN/negative
			// values slip through in both dry-run and real execution, since NaN
			// compares false against every bound (#501); a confidence of exactly
			// 0 is otherwise indistinguishable from the flag never being passed
			// at all, so it is only rejected when the user actually typed it.
			if cmd.Flags().Changed("confidence") &&
				(math.IsNaN(confidence) || confidence <= 0 || confidence >= 1) {
				return fmt.Errorf("--confidence must be in (0,1), got %v", confidence)
			}
			effectiveConf := confidence
			// Classified once, here, the only place a plain, swarm, or SPRT
			// dispatch first needs it, and threaded through so nothing downstream
			// re-runs DetectPII/InjectionMarker on the same prompt (#522).
			promptClass := policy.Classify(prompt)
			touchesPII := promptClass.PII
			// The plain dispatch path (d.Dispatch) reads cfg.Policies["pii"] itself
			// and forces LocalOnly. The SPRT/swarm branches below take a shortcut
			// straight past it and only ever saw --local, so a PII-touching prompt
			// silently went out to remote heads the instant touchesPII made
			// effectiveConf > 0 and picked one of those branches instead (#500).
			if d.PIILocalOnly() && touchesPII {
				localOnly = true
			}
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
					// bar itself is unchanged, only the claim about it.
					if g.Empty() {
						fmt.Printf("  %s\n", warnStyle.Render(
							"graph: no graph at "+graphPath+", radius 1.00 is a default, not a measurement"))
					} else if !g.Knows(file) {
						fmt.Printf("  %s\n", warnStyle.Render(
							"graph: "+file+" is not in the graph, radius 1.00 is a default, not a measurement"))
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
			// branch reaches, so `--dry-run --confidence 0.95` fired a paid
			// ensemble and printed no plan at all (#167).
			if dryRun && (effectiveConf > 0 || doSwarm) {
				mode := swarm.SwarmMode(swarmMode)
				if mode == "" {
					mode = swarm.ModeBest
				}
				sw := swarm.New(d, d.Heads(), d)
				// Every field a real Run()/RunSPRT() call reads must appear here too,
				// or --dry-run previews something other than what execution does
				// (#167, #501, #530), this literal backs both the --swarm and the
				// --confidence dry-run preview.
				planOpts := swarm.Options{
					Mode:          mode,
					TierHint:      tier,
					HeadIDs:       headIDs,
					MaxHeads:      swarmMaxHeads,
					MaxEstCostUSD: swarmMaxCost,
					LocalOnly:     localOnly,
					System:        system,
					A2AFile:       a2aFile,
					JudgeTierHint: swarmJudge,
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
					A2AFile:       a2aFile,
					// JudgeTierHint is not a no-op here: RunSPRT's behavioral
					// equivalence judge (judgeEquivalence) dispatches through it to
					// decide whether two candidate answers agree. It just isn't a
					// ModeBest-style answer-picker, so it is wired in and validated
					// like every other tier hint rather than rejected as
					// inapplicable (#530).
					JudgeTierHint:  swarmJudge,
					Confidence:     effectiveConf,
					Domain:         domain,
					RunID:          runID,
					TaskID:         taskID,
					Classification: &promptClass,
				})
				if err != nil {
					if errors.Is(err, trust.ErrNoEvidence) {
						return noEvidenceError(domain)
					}
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
					Mode:           mode,
					TierHint:       tier,
					HeadIDs:        headIDs,
					MaxHeads:       swarmMaxHeads,
					MaxEstCostUSD:  swarmMaxCost,
					LocalOnly:      localOnly,
					System:         system,
					A2AFile:        a2aFile,
					JudgeTierHint:  swarmJudge,
					RunID:          runID,
					TaskID:         taskID,
					Classification: &promptClass,
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
				TierHint:       tierHint,
				LocalOnly:      localOnly,
				DryRun:         dryRun,
				System:         system,
				A2AFile:        a2aFile,
				Enum:           enumKey,
				RunID:          runID,
				TaskID:         taskID,
				MaxCostUSD:     maxCost,
				Classification: &promptClass,
			}

			// Streamed on a terminal only. A pipe keeps the buffered rendering
			// below byte for byte, so nothing that parses hyctl's output can be
			// broken by an interactive nicety. --dry-run runs no head at all.
			var sr *streamRenderer
			if !dryRun && !noStream && isatty.IsTerminal(os.Stdout.Fd()) {
				w, h := terminalSize()
				sr = newStreamRenderer(os.Stdout, runID, w, h, d.CapturesPayloads())
				opts.OnStream = sr.Handle
			}

			result, err := d.Dispatch(ctx, prompt, opts)
			// A parked task is not a failure, but it is not success either:
			// nothing ran. Print what is being asked and how to answer, then
			// exit non-zero so a script cannot mistake it for completed work.
			// The question is not repeated in the returned error.
			var parked *dispatch.ParkedError
			if errors.As(err, &parked) {
				fmt.Println()
				fmt.Printf("  %s %s\n", warnStyle.Render("⏸ waiting on you"), dimStyle.Render(parked.Head))
				fmt.Println(dimStyle.Render("  " + strings.Repeat("─", 56)))
				fmt.Printf("\n  %s\n\n", parked.Question)
				fmt.Printf("  %s\n", dimStyle.Render("answer:  hyctl ask answer "+parked.TaskID+" \"...\""))
				fmt.Printf("  %s\n\n", dimStyle.Render("refuse:  hyctl ask decline "+parked.TaskID))
				return fmt.Errorf("task %s is waiting on an answer", parked.TaskID)
			}
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

			// The answer is already on screen, delta by delta, so reprinting
			// Output would show it twice. Only the numbers are still missing,
			// since they do not exist until the call returns.
			if sr != nil && sr.Streamed() {
				sr.Finish(result.InputTokens, result.OutputTokens, result.Duration, result.TTFT)
				printOutputWarning(result.OutputProvenance)
				return nil
			}
			if sr != nil {
				sr.Stop()
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
			printOutputWarning(result.OutputProvenance)
			return nil
		},
	}

	// Read off routing.yaml rather than restated, so the help cannot advertise
	// a name that does not resolve. It listed "local" while `--tier local`
	// errored, and both lists were maintained by hand (#782).
	cmd.Flags().StringVarP(&tier, "tier", "t", "", "target tier: 1-10, or a name ("+strings.Join(dispatch.TierNames(), "/")+")")
	cmd.Flags().BoolVarP(&localOnly, "local", "l", false, "force local heads only")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show selected head without executing")
	cmd.Flags().StringVarP(&system, "system", "s", "", "system prompt")
	cmd.Flags().StringVar(&a2aFile, "a2a", "", "path to A2A handoff JSON (prepends structured context to prompt)")
	cmd.Flags().StringVar(&enumKey, "enum", "", "routing enum key, e.g. SIMPLE, selects the tier when --tier is unset")
	cmd.Flags().Float64Var(&maxCost, "max-cost", 0, "refuse a candidate head if its estimated cost exceeds this USD (denial-of-wallet guard)")
	cmd.Flags().BoolVar(&noStream, "no-stream", false, "print the answer in one block instead of as it arrives (piped output never streams)")
	// swarm flags
	cmd.Flags().BoolVar(&doSwarm, "swarm", false, "fan prompt out to multiple heads simultaneously")
	cmd.Flags().StringVar(&swarmMode, "swarm-mode", "best", "response strategy: best|race|all")
	cmd.Flags().StringVar(&swarmHeads, "swarm-heads", "", "comma-separated head IDs to target (overrides --tier)")
	cmd.Flags().IntVar(&swarmMaxHeads, "swarm-max-heads", 0, "max heads to fire (default 5)")
	cmd.Flags().Float64Var(&swarmMaxCost, "swarm-max-cost", 0, "refuse swarm if preflight cost estimate exceeds this USD")
	cmd.Flags().StringVar(&swarmJudge, "swarm-judge-tier", "", "tier for judge head in best mode (default: tier 1 / cortex)")
	// trust / SPRT flags
	cmd.Flags().Float64Var(&confidence, "confidence", 0, "route via SPRT ensemble until this P(correct) is reached, e.g. 0.95")
	// The real value, not a claim about it in the description: the flag defaulted
	// to "" while --help promised "default", so the refusal quoted an empty
	// domain and printed two commands ending in a bare `--domain ` (#732).
	cmd.Flags().StringVar(&domain, "domain", trust.DefaultDomain, "calibration domain for --confidence")
	cmd.Flags().StringVar(&file, "file", "", "target file, derives a confidence target from its blast radius, so this alone selects the SPRT ensemble")
	cmd.Flags().StringVar(&graphPath, "graph", "graph.json", "path to the dependency graph used with --file")
	cmd.Flags().BoolVar(&irreversible, "irreversible", false, "change cannot be cheaply undone, raises the required confidence")
	cmd.Flags().BoolVar(&production, "production", false, "target is production, raises the required confidence")
	return cmd
}

// cmdOracle runs a verification oracle and reports its calibrated evidence.
// cmdEval inspects the corpus of oracle-verified examples.
func cmdEval() *cobra.Command {
	var jsonOut bool
	var failedOnly bool
	var limit int

	cmd := &cobra.Command{
		Use:   "eval",
		Short: "Oracle-verified examples, the labelled corpus, exempt from retention",
		Long: `hyctl eval inspects examples recorded by ` + "`hyctl oracle verify`" + `.

An oracle verdict on a real candidate is ground truth. Unlike traces, these are
kept verbatim and never expire: they are the only corpus the router can be
improved against.`,
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "List recorded examples, newest first",
		RunE: func(_ *cobra.Command, _ []string) error {
			all, err := evalset.Load(evalset.DefaultPath())
			if err != nil {
				return err
			}
			if failedOnly {
				var f []evalset.Example
				for _, e := range all {
					if !e.Passed {
						f = append(f, e)
					}
				}
				all = f
			}
			// Newest first: the recent ones are what a person is looking for.
			for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
				all[i], all[j] = all[j], all[i]
			}
			if limit > 0 && len(all) > limit {
				all = all[:limit]
			}
			if jsonOut {
				raw, _ := json.MarshalIndent(all, "", "  ")
				fmt.Println(string(raw))
				return nil
			}
			if len(all) == 0 {
				fmt.Println("No verified examples yet. Run `hyctl oracle verify --candidate <file> ...` to record one.")
				return nil
			}
			for _, e := range all {
				status := cortexStyle.Render("PASS")
				if !e.Passed {
					status = "FAIL"
				}
				pii := ""
				if e.PII {
					pii = " " + dimStyle.Render("[pii]")
				}
				fmt.Printf("  %s  %-14s %-22s %s%s\n", status, e.Domain, e.Source,
					dimStyle.Render(e.CandidateHash[:8]), pii)
			}
			return nil
		},
	}
	list.Flags().BoolVar(&jsonOut, "json", false, "machine-readable output")
	list.Flags().BoolVar(&failedOnly, "failed", false, "only examples the oracle rejected")
	list.Flags().IntVar(&limit, "limit", 20, "maximum examples to show (0 = all)")

	stats := &cobra.Command{
		Use:   "stats",
		Short: "Corpus size and pass rate by domain",
		RunE: func(_ *cobra.Command, _ []string) error {
			all, err := evalset.Load(evalset.DefaultPath())
			if err != nil {
				return err
			}
			st := evalset.Stats(all)
			if jsonOut {
				raw, _ := json.MarshalIndent(st, "", "  ")
				fmt.Println(string(raw))
				return nil
			}
			if len(st) == 0 {
				fmt.Println("No verified examples yet.")
				return nil
			}
			fmt.Printf("%-20s %8s %8s %8s %10s %6s\n", "DOMAIN", "TOTAL", "PASSED", "FAILED", "PASS RATE", "PII")
			for _, s := range st {
				fmt.Printf("%-20s %8d %8d %8d %9.1f%% %6d\n",
					s.Domain, s.Total, s.Passed, s.Failed, s.PassRate*100, s.WithPII)
			}
			fmt.Printf("\n%s\n", dimStyle.Render(fmt.Sprintf("%d example%s at %s, never pruned", len(all), plural(len(all)), evalset.DefaultPath())))
			return nil
		},
	}
	stats.Flags().BoolVar(&jsonOut, "json", false, "machine-readable output")

	cmd.AddCommand(list, stats)
	return cmd
}

// cmdTrace manages the run-log store.
func cmdTrace() *cobra.Command {
	var olderThan time.Duration
	var jsonOut bool
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "trace",
		Short: "Run traces: view a run's spans, seal old logs, evaluate and export",
	}

	seal := &cobra.Command{
		Use:   "seal",
		Short: "Fold old run logs into compressed monthly segments",
		Long: `hyctl trace seal folds run logs older than --older-than into one
compressed segment per month.

This is lossless relocation, not retention: every event is still readable
through the same ` + "`hyctl`" + ` and desktop views afterwards. What it recovers is
disk. One file per run is charged a whole filesystem block, so 60 short runs
holding ~30 KB of events can occupy 240 KB.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			if dryRun {
				ids, err := runlog.SealCandidates(olderThan)
				if err != nil {
					return err
				}
				if jsonOut {
					raw, _ := json.MarshalIndent(map[string]any{"would_seal": len(ids), "runs": ids}, "", "  ")
					fmt.Println(string(raw))
					return nil
				}
				fmt.Printf("Would seal %d run%s older than %s.\n", len(ids), plural(len(ids)), olderThan)
				return nil
			}
			res, err := runlog.Seal(olderThan)
			if err != nil {
				return err
			}
			if jsonOut {
				raw, _ := json.MarshalIndent(res, "", "  ")
				fmt.Println(string(raw))
				return nil
			}
			if res.Runs == 0 {
				fmt.Printf("Nothing to seal, no run logs older than %s.\n", olderThan)
				return nil
			}
			fmt.Printf("  sealed %d run%s (%d events) across %d month%s\n",
				res.Runs, plural(res.Runs), res.Events, res.Months, plural(res.Months))
			fmt.Printf("  %s\n", dimStyle.Render(fmt.Sprintf(
				"%s on disk -> %s (%.1fx)", humanBytes(res.BytesBefore), humanBytes(res.BytesAfter),
				ratioOf(res.BytesBefore, res.BytesAfter))))
			return nil
		},
	}
	seal.Flags().DurationVar(&olderThan, "older-than", 7*24*time.Hour, "seal runs older than this")
	seal.Flags().BoolVar(&jsonOut, "json", false, "machine-readable output")
	seal.Flags().BoolVar(&dryRun, "dry-run", false, "list what would be sealed without writing")

	var (
		policySpec string
		level      float64
		clipAt     float64
		days       int
	)
	evaluate := &cobra.Command{
		Use:   "evaluate",
		Short: "What a different routing policy would have cost, from the log alone",
		Long: `hyctl trace evaluate estimates the cost of a routing policy you did not run,
from the dispatches you did.

Every dispatch row records the probability the router assigned to the head it
chose. Weighting each row by the ratio of what a candidate policy would have
done to what the router actually did recovers that policy's average cost
without running it.

It only works where the router had some chance of doing what the candidate
policy would do. Where it had none the question is not hard but unanswerable,
and this refuses rather than returning a confident wrong number. Setting
explore_rate in config.toml above 0 is what creates that overlap.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			rows, err := cost.LoadRows(cost.DefaultLogPath())
			if err != nil {
				return err
			}
			if days > 0 {
				rows = rowsWithinDays(rows, days)
			}
			if len(rows) == 0 {
				return fmt.Errorf("no dispatches in the cost log, nothing to evaluate")
			}

			tiers := make([]int, 0, len(rows))
			for _, r := range rows {
				tiers = append(tiers, r.Tier)
			}
			tierSet := ope.TiersIn(tiers)
			policy, err := ope.ParsePolicy(policySpec, tierSet)
			if err != nil {
				return err
			}

			samples := make([]ope.CounterfactualSample, 0, len(rows))
			for _, r := range rows {
				samples = append(samples, ope.CounterfactualSample{
					Value:      r.EstCostUSD,
					LoggedProb: r.ActProb,
					TargetProb: policy.Would(ope.Decision{
						Enum: r.Enum, Tier: r.Tier, Model: r.Model,
						Executor: r.Executor, Pool: r.Pool,
					}),
				})
			}
			est, evalErr := ope.Evaluate(samples, ope.Options{Level: level, ClipAt: clipAt})

			// The logged policy's own average, for comparison. Self-normalised
			// over the same rows so the two numbers are computed the same way.
			var loggedSamples []ope.Sample
			for _, r := range rows {
				loggedSamples = append(loggedSamples, ope.Sample{Value: r.EstCostUSD, Prob: r.KeepProb})
			}
			logged, _, loggedErr := ope.SelfNormalized(loggedSamples)

			if jsonOut {
				out := map[string]any{
					"policy":     policy.Name(),
					"rows":       len(rows),
					"tiers_seen": ope.SortedTiers(tierSet),
					"estimate":   est,
				}
				if evalErr != nil {
					out["refused"] = evalErr.Error()
				}
				if loggedErr == nil {
					out["logged_policy_mean"] = logged
				}
				raw, _ := json.MarshalIndent(out, "", "  ")
				fmt.Println(string(raw))
				return nil
			}

			fmt.Printf("  Policy   %s\n", policy.Name())
			fmt.Printf("  Log      %d dispatches across tiers %v\n", len(rows), ope.SortedTiers(tierSet))
			if loggedErr == nil {
				fmt.Printf("  Actual   $%.5f per dispatch under the policy that ran\n", logged)
			}
			fmt.Println()
			if evalErr != nil {
				fmt.Printf("  %s\n", warnStyle.Render("No answer: "+evalErr.Error()))
				// The two refusals have different remedies, and telling someone
				// with a pre-propensity log to set explore_rate would send them
				// after a setting that cannot fix rows already written.
				if errors.Is(evalErr, ope.ErrNoPropensity) {
					fmt.Printf("  %s\n", dimStyle.Render(
						"These dispatches were logged before Hydra recorded routing propensity. "+
							"Rows written from now on carry it; nothing recovers it for old ones."))
				} else {
					fmt.Printf("  %s\n", dimStyle.Render(
						"The router is argmax by default, so heads it did not pick have probability 0 "+
							"and no amount of logging answers this. Set explore_rate in config.toml above 0."))
				}
				return nil
			}
			fmt.Printf("  Estimate $%.5f per dispatch  [$%.5f, $%.5f] at %.0f%%\n",
				est.Mean, est.Lo, est.Hi, est.Level*100)
			if loggedErr == nil && logged > 0 {
				fmt.Printf("  %s\n", dimStyle.Render(fmt.Sprintf("%.1f%% of what actually ran", est.Mean/logged*100)))
			}
			fmt.Println()
			fmt.Printf("  %s\n", dimStyle.Render(fmt.Sprintf(
				"%d of %d rows support this policy · effective sample size %.1f · %d weights clipped",
				est.Supporting, est.N, est.ESS, est.Clipped)))
			fmt.Printf("  %s\n", dimStyle.Render(est.Method))
			if est.Skipped > 0 {
				fmt.Printf("  %s\n", dimStyle.Render(fmt.Sprintf(
					"%d rows skipped for an unusable propensity", est.Skipped)))
			}
			return nil
		},
	}
	evaluate.Flags().StringVar(&policySpec, "policy", "cheaper",
		"policy to evaluate: "+strings.Join(ope.PolicyNames(), ", "))
	evaluate.Flags().Float64Var(&level, "confidence", ope.DefaultLevel, "confidence level for the interval")
	evaluate.Flags().Float64Var(&clipAt, "clip", ope.DefaultClip, "cap importance weights here (negative = no clipping)")
	evaluate.Flags().IntVar(&days, "days", 0, "only dispatches from the last N days (0 = all)")
	evaluate.Flags().BoolVar(&jsonOut, "json", false, "machine-readable output")

	var (
		endpoint string
		outFile  string
		expDays  int
		limit    int
	)
	export := &cobra.Command{
		Use:   "export",
		Short: "Render the dispatch log as OpenTelemetry spans",
		Long: `hyctl trace export renders dispatches as OTLP spans.

Nothing leaves the machine unless --otlp names an endpoint. With no endpoint
the payload is written to stdout or --out, so you can read exactly what would
be sent before sending it.

The export is a bridge, not a migration: Hydra's own schema stays
authoritative. gen_ai.* attributes are populated where they genuinely
correspond, and tier, enum, cost and routing propensity, which OTel has no
place for, are carried under hydra.* rather than dropped.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			rows, err := cost.LoadRows(cost.DefaultLogPath())
			if err != nil {
				return err
			}
			if expDays > 0 {
				rows = rowsWithinDays(rows, expDays)
			}
			if limit > 0 && len(rows) > limit {
				rows = rows[len(rows)-limit:] // newest
			}
			if len(rows) == 0 {
				return fmt.Errorf("no dispatches in the cost log, nothing to export")
			}
			payload, err := otlp.Build(rows, "hydra", build.Version)
			if err != nil {
				return err
			}
			body, err := otlp.Marshal(payload)
			if err != nil {
				return err
			}

			if endpoint == "" {
				if outFile != "" {
					if err := os.WriteFile(outFile, body, 0o600); err != nil {
						return err
					}
					fmt.Printf("  %d span%s written to %s\n", len(rows), plural(len(rows)), outFile)
					fmt.Printf("  %s\n", dimStyle.Render("Nothing was sent. Pass --otlp <endpoint> to export."))
					return nil
				}
				fmt.Println(string(body))
				return nil
			}
			return postOTLP(endpoint, body, len(rows))
		},
	}
	export.Flags().StringVar(&endpoint, "otlp", "",
		"OTLP/HTTP traces endpoint (e.g. http://localhost:4318/v1/traces). Without it nothing is sent")
	export.Flags().StringVar(&outFile, "out", "", "write the payload to a file instead of stdout")
	export.Flags().IntVar(&expDays, "days", 0, "only dispatches from the last N days (0 = all)")
	export.Flags().IntVar(&limit, "limit", 0, "export at most N of the newest dispatches (0 = all)")

	payloads := &cobra.Command{
		Use:   "payloads",
		Short: "Payload store: what is captured, how much it costs, and what it holds",
		Long: `hyctl trace payloads reports the opt-in store of prompt and response text.

Capture is off unless it was chosen at ` + "`hyctl init`" + ` or set with
capture_payloads in config.toml. Stored text is sampled, every blob records the
probability it was admitted, and anything matching a secret detector is
replaced before it is written.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			// A machine that has never run `hyctl init` has no config, and the
			// answer is knowable anyway: no config means capture was never
			// opted into. Reporting a missing file instead would be an error
			// about Hydra's plumbing in place of the answer asked for.
			cfg, err := config.Load()
			if err != nil {
				cfg = &config.Config{}
			}
			store, err := payload.Open(payload.Dir())
			if err != nil {
				return err
			}
			st := store.Stat()
			if jsonOut {
				raw, _ := json.MarshalIndent(map[string]any{
					"capture_enabled": cfg.CapturePayloads,
					"keep_rate":       payload.KeepRate(cfg),
					"budget_bytes":    payload.Budget(cfg),
					"stats":           st,
				}, "", "  ")
				fmt.Println(string(raw))
				return nil
			}
			if !cfg.CapturePayloads {
				fmt.Println("Payload capture is off. Hydra keeps the statistics either way;")
				fmt.Println("turn capture on with capture_payloads = true in config.toml.")
				if st.Blobs == 0 {
					return nil
				}
				fmt.Println()
			}
			if st.Blobs == 0 {
				fmt.Println("No payloads stored.")
				return nil
			}
			fmt.Printf("  %d payload%s over %d chunk%s in %d pack%s\n",
				st.Manifests, plural(st.Manifests),
				st.Blobs-st.Manifests, plural(st.Blobs-st.Manifests),
				st.Packs, plural(st.Packs))
			// Compression and disk are separate numbers on purpose. A small
			// store is dominated by the filesystem block it is charged, so one
			// combined ratio reads as compression having made things bigger.
			fmt.Printf("  %s of prompt text held in %s (%.1fx) · %s on disk\n",
				humanBytes(st.LogicalBytes), humanBytes(st.PackBytes),
				ratioOf(st.LogicalBytes, st.PackBytes), humanBytes(st.DiskBytes))
			fmt.Printf("  %s\n", dimStyle.Render(fmt.Sprintf(
				"%s unique after chunking, so %s of it repeated across runs",
				humanBytes(st.RawBytes), humanBytes(st.LogicalBytes-st.RawBytes))))
			fmt.Printf("  %s\n", dimStyle.Render(fmt.Sprintf(
				"budget %s, oldest packs dropped past it · admitted at %.0f%%",
				humanBytes(payload.Budget(cfg)), payload.KeepRate(cfg)*100)))
			fmt.Printf("  %s\n", dimStyle.Render(fmt.Sprintf(
				"%d dictionary-compressed · %d contained redacted secrets", st.WithDict, st.WithPII)))
			return nil
		},
	}
	payloads.Flags().BoolVar(&jsonOut, "json", false, "machine-readable output")

	cmd.AddCommand(seal, evaluate, export, payloads, cmdTraceView(), cmdTraceScore())
	return cmd
}

// postOTLP sends the payload to an OTLP/HTTP endpoint.
//
// Sending is the one thing here that leaves the machine, so it is explicit
// about where it went and refuses to report success on a non-2xx.
func postOTLP(endpoint string, body []byte, spans int) error {
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if key := os.Getenv("HYDRA_OTLP_HEADERS"); key != "" {
		// "k=v,k=v", the same shape OTEL_EXPORTER_OTLP_HEADERS uses, so an
		// existing collector's auth config transfers unchanged.
		for _, pair := range strings.Split(key, ",") {
			k, v, ok := strings.Cut(strings.TrimSpace(pair), "=")
			if ok {
				req.Header.Set(k, v)
			}
		}
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("otlp export to %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("otlp export to %s: %s: %s", endpoint, resp.Status, strings.TrimSpace(string(snippet)))
	}
	fmt.Printf("  %d span%s exported to %s\n", spans, plural(spans), endpoint)
	return nil
}

// rowsWithinDays keeps rows whose timestamp is inside the window. A row whose
// timestamp will not parse is kept rather than dropped: silently shrinking the
// corpus a estimate is computed from is worse than including one stale row.
func rowsWithinDays(rows []cost.Row, days int) []cost.Row {
	cutoff := time.Now().AddDate(0, 0, -days)
	out := make([]cost.Row, 0, len(rows))
	for _, r := range rows {
		ts, err := time.Parse(time.RFC3339, r.TS)
		if err != nil || !ts.Before(cutoff) {
			out = append(out, r)
		}
	}
	return out
}

// ratioOf guards the division so an empty seal cannot print Inf or NaN.
func ratioOf(before, after int64) float64 {
	if after <= 0 {
		return 0
	}
	return float64(before) / float64(after)
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit && exp < 3; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

func cmdOracle() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "oracle",
		Short: "Run deterministic verifiers (tests/compile/lint) as evidence sources",
	}
	var source, domain, candidateFile, record, scoreRun, scoreSpan string
	verify := &cobra.Command{
		Use:   "verify <command...>",
		Short: "Run a verifier command; report pass/fail + its calibrated LLR",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Validated before running the verifier at all: a garbage --record
			// used to be silently ignored, no error, no calibration write, no
			// indication anything was wrong, discovered only after the command
			// had already done its real work. Same check `trust record
			// --outcome` already has (#464).
			var recordOutcome trust.Outcome
			if record != "" {
				recordOutcome = trust.ParseOutcome(record)
				if recordOutcome == trust.OutcomeUnknown {
					return fmt.Errorf("--record must be correct|incorrect (got %q)", record)
				}
			}

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
			o := &oracle.CommandOracle{Args: args, Source: src}
			v, err := o.Verify(cmd.Context(), candidate, trust.Task{Domain: domain})
			if err != nil {
				return err
			}

			cal, err := trust.New(trust.DefaultPath())
			if err != nil {
				return err
			}
			if record != "" {
				_ = cal.Update(src, domain, v.Passed, recordOutcome)
			}
			llr := oracle.LLR(cal, src, domain, v)

			// An oracle verdict on a real candidate is ground truth, and the
			// rarest thing Hydra produces. It is kept outside the trace store
			// so no retention pass can ever reach it (#625).
			if candidate != "" {
				breadcrumb, _ := config.Breadcrumb()
				added, aerr := evalset.Add(evalset.DefaultPath(), evalset.Example{
					Domain: domain, Source: src, Candidate: candidate,
					Passed: v.Passed, Detail: v.Detail, Config: breadcrumb,
				})
				switch {
				case aerr != nil:
					// Never fail a verification because its example could not
					// be filed; report it so the loss is visible.
					fmt.Printf("  %s\n", dimStyle.Render("eval set: "+aerr.Error()))
				case added:
					fmt.Printf("  %s\n", dimStyle.Render("recorded to the eval set"))
				}
			}

			// Attribute the verdict to the span that produced the candidate.
			// The oracle has always computed this; until #758 it had nowhere
			// to put it, so a trace never said whether its answer held up.
			if scoreSpan != "" {
				runID := scoreRun
				if runID == "" {
					runs, rErr := runlog.Runs()
					if rErr != nil || len(runs) == 0 {
						return fmt.Errorf("--span given but no run to attach it to; pass --run")
					}
					runID = runs[0]
				}
				val := 0.0
				if v.Passed {
					val = 1
				}
				if sErr := runlog.AppendScore(runID, scoreSpan, runlog.Score{
					Name: "oracle", Value: val, Comment: v.Detail, Source: src,
				}); sErr != nil {
					// Reported, never fatal: the verification itself is the
					// work, and losing its attribution must not fail it.
					fmt.Printf("  %s\n", dimStyle.Render("span score: "+sErr.Error()))
				} else {
					fmt.Printf("  %s\n", dimStyle.Render("scored span "+scoreSpan))
				}
			}

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
	verify.Flags().StringVar(&scoreRun, "run", "", "run holding the span to score (default: newest)")
	verify.Flags().StringVar(&scoreSpan, "span", "", "span this verdict judges, so `hyctl trace view` can show it")
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
			// Explicit --classification and --content-derived PII both take
			// priority; only fall back to the MCP registry's view of this
			// tool's server when the caller supplied neither. Auto-derived
			// the same way policy.ContainsPII already derives "pii" from
			// --content, this is that same mechanism, for MCP server risk.
			classification := chkClassification
			if classification == "" && chkContent == "" {
				if c, ok := mcpregistry.ClassificationForTool(args[0]); ok {
					classification = c
				} else if priorEvents, loadErr := ledger.Load(ledger.DefaultPath()); loadErr == nil {
					// Lower priority than a registry-derived classification,
					// a confirmed quarantine/flag is a stronger claim than
					// "this server hasn't done this before on this machine".
					if c, ok := mcpregistry.BehaviorClassification(priorEvents, args[0], action); ok {
						classification = c
					}
				}
			}
			decision, checkErr := ledger.Check(ledger.DefaultPath(), pol, ledger.CheckRequest{
				Agent: chkAgent, Tool: args[0], Resource: chkResource, Action: action,
				Params: params, Classification: classification, Content: chkContent,
			})
			// Report the decision before any error: a Deny that failed to write
			// to the ledger is still a Deny, and callers gate on exit 3.
			if decision != "" {
				// tool+"/"+resource used to read as one run-together path when
				// resource was itself absolute (e.g. "shell//bin/rm -rf /"),
				// "->" can't collide with path syntax the way "/" does (#464).
				fmt.Printf("  %s  %s %s -> %s (%s)\n", strings.ToUpper(string(decision)),
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
			if recTool == "" {
				return fmt.Errorf("--tool is required")
			}
			if recAgent == "" {
				return fmt.Errorf("--agent is required")
			}
			// Bind whenever --params was supplied, including "{}", so this
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
				fmt.Fprintf(os.Stderr, "  warning: %d unparseable ledger line(s) skipped, "+
					"verification may be against an older approval\n", skipped)
			}
			approval, ok := ledger.LatestBound(events, tool, verResource)
			if !ok {
				return fmt.Errorf("no allowed, parameter-bound ledger event for %s/%s, nothing to verify against", tool, verResource)
			}

			// A parameters_hash read straight off disk proves nothing if the
			// ledger itself was edited after recording, comparing against it
			// verbatim previously reported MATCH even on a hand-tampered hash.
			// `verify-chain` already recomputes and links every chained event;
			// compose it here so a broken chain refuses the approval instead of
			// silently trusting it (#500). Approvals that predate hash-chaining
			// (Hash == "") were never protected either way, so leave them as-is.
			if approval.Hash != "" {
				chain, err := ledger.VerifyChain(ledger.DefaultPath())
				if err != nil {
					return err
				}
				if !chain.Intact {
					fmt.Printf("  %s  ledger hash chain is broken (event index %d), the recorded "+
						"approval cannot be trusted; refusing to verify against it. Run `hyctl mcp verify-chain` for details.\n",
						strings.ToUpper(string(ledger.Deny)), chain.BrokenAt)
					os.Exit(3)
				}
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
				// Same "->" separator as `mcp check` (#464), a "/" read as
				// one run-together path when the resource was itself absolute.
				line := fmt.Sprintf("  %s  %-6s  %-12s %s -> %s %s", e.TS, strings.ToUpper(string(e.Decision)),
					util.SafeTerminal(e.Agent), util.SafeTerminal(e.Tool), util.SafeTerminal(e.Resource),
					dimStyle.Render(string(e.Action)))
				if e.Flagged {
					line += dimStyle.Render(fmt.Sprintf("  [flagged: %s]", util.SafeTerminal(e.FlagReason)))
				}
				fmt.Println(line)
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
			fmt.Printf("\n  ledger events   %d  (%d allowed · %d denied · %d flagged)\n", s.Total, s.Allowed, s.Denied, s.Flagged)
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

	// verify-chain: confirm the ledger's hash chain hasn't been tampered with.
	var chainJSON bool
	verifyChain := &cobra.Command{
		Use:   "verify-chain",
		Short: "Confirm the ledger's hash chain is intact (tamper-evidence)",
		RunE: func(_ *cobra.Command, _ []string) error {
			res, err := ledger.VerifyChain(ledger.DefaultPath())
			if err != nil {
				return err
			}
			if chainJSON {
				return json.NewEncoder(os.Stdout).Encode(res)
			}
			// Each failure mode is a different claim and gets its own words.
			// "Broken at index N" is meaningless for a truncation, where every
			// surviving event verifies and the missing ones left no gap.
			switch {
			case res.Truncated:
				fmt.Printf("  %s  ledger TRUNCATED, the chain anchor names an event that is no longer "+
					"in the log, so records were deleted from the end\n", strings.ToUpper(string(ledger.Deny)))
				os.Exit(3)
			case !res.Intact:
				fmt.Printf("  %s  chain broken at event index %d, the ledger was modified after recording\n",
					strings.ToUpper(string(ledger.Deny)), res.BrokenAt)
				os.Exit(3)
			case res.AnchorMissing:
				fmt.Printf("  %s  %d chained event(s) all verify, but the chain anchor (%s) is missing, "+
					"deletion from the end cannot be ruled out\n",
					warnStyle.Render("WARN"), res.Chained, filepath.Base(ledger.DefaultPath())+".chainhash")
			case res.AnchorStale:
				fmt.Printf("  %s  chain intact, %d chained event(s); the anchor lags the log "+
					"(a dropped anchor write, not a deletion)\n", okStyle.Render("OK"), res.Chained)
			default:
				fmt.Printf("  %s  chain intact, %d chained event(s), %d unchained (pre-dates this feature)\n",
					okStyle.Render("OK"), res.Chained, res.Unchained)
			}
			return nil
		},
	}
	verifyChain.Flags().BoolVar(&chainJSON, "json", false, "machine-readable JSON output")

	cmd.AddCommand(check, record, verify, logCmd, report, verifyChain, cmdMCPRegistry())
	return cmd
}

// cmdMCPRegistry is the Phase 1 CLI wedge: identity-only sync of the
// official MCP registry, a scan of what's installed on this machine, and an
// audit that resolves one against the other. No trust scoring yet.
func cmdMCPRegistry() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "registry",
		Short: "Sync the official MCP registry and audit what's installed on this machine",
	}

	sync := &cobra.Command{
		Use:   "sync",
		Short: "Pull server metadata from the official MCP registry into a local cache",
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Println("  syncing the official MCP registry, thousands of servers, this can take a minute or two...")
			n, err := mcpregistry.Sync(cmd.Context(), func(page, soFar int) {
				fmt.Printf("\r  page %-4d  %d servers so far...", page, soFar)
			})
			fmt.Println()
			if err != nil {
				return err
			}
			fmt.Printf("  synced %d servers from the official MCP registry\n", n)
			return nil
		},
	}

	var scanJSON bool
	scanCmd := &cobra.Command{
		Use:   "scan",
		Short: "List MCP servers installed across this machine's clients",
		RunE: func(_ *cobra.Command, _ []string) error {
			cwd, _ := os.Getwd()
			installed := mcpregistry.Scan(cwd)
			if scanJSON {
				return json.NewEncoder(os.Stdout).Encode(installed)
			}
			if len(installed) == 0 {
				fmt.Println("  no MCP servers found on this machine")
				return nil
			}
			for _, s := range installed {
				fmt.Printf("  %-16s %-8s %s\n", s.Client, s.Scope, s.Name)
			}
			return nil
		},
	}
	scanCmd.Flags().BoolVar(&scanJSON, "json", false, "machine-readable JSON output")

	var auditJSON bool
	audit := &cobra.Command{
		Use:   "audit",
		Short: "Resolve installed MCP servers against the synced registry and score them",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cwd, _ := os.Getwd()
			rpt, err := mcpregistry.Audit(cmd.Context(), cwd)
			if err != nil {
				return err
			}
			if auditJSON {
				return json.NewEncoder(os.Stdout).Encode(rpt)
			}
			if rpt.RegistrySync.IsZero() {
				fmt.Println("  registry never synced, run `hyctl mcp registry sync` first for verification")
			}
			if len(rpt.Entries) == 0 {
				fmt.Println("  no MCP servers found on this machine")
				return nil
			}
			for _, e := range rpt.Entries {
				status := strings.ToUpper(string(e.Status))
				if e.Status == mcpregistry.StatusVerified {
					status = okStyle.Render(status)
				} else {
					status = warnStyle.Render(status)
				}
				fmt.Printf("  %-9s %-16s %-8s %s\n", status, e.Client, e.Scope, e.Name)
				if e.Score != nil {
					fmt.Printf("             score %-20s state %-12s confidence %s\n",
						mcpregistry.FormatScore(*e.Score), e.LifecycleState, mcpregistry.FormatConfidence(e.Score.Confidence))
					for _, cat := range []struct {
						name string
						cs   mcpregistry.CategoryScore
					}{
						{"security", e.Score.SecurityImplementation},
						{"repo health", e.Score.RepositoryHealth},
						{"operational", e.Score.OperationalSecurity},
						{"community", e.Score.CommunityGovernance},
					} {
						for _, sig := range cat.cs.Signals {
							if sig.Available && sig.Detail != "" {
								fmt.Printf("               %-11s %s\n", cat.name, dimStyle.Render(sig.Detail))
							}
						}
					}
				} else if e.NearestMatch != "" {
					fmt.Printf("             %s\n", dimStyle.Render(fmt.Sprintf("nearest known identifier: %q (%d edits away), verify this isn't a typosquat", e.NearestMatch, e.NearestDist)))
				}
			}
			return nil
		},
	}
	audit.Flags().BoolVar(&auditJSON, "json", false, "machine-readable JSON output")

	var exportOut string
	export := &cobra.Command{
		Use:   "export",
		Short: "Render every audited server into a static index.html/index.json (no hosting or publishing)",
		Long: "Writes index.json and index.html to --out, covering only the servers this machine has\n" +
			"audited via `hyctl mcp registry audit`, not the full synced registry. This produces static\n" +
			"files only; it does not publish, host, or deploy anything anywhere.",
		RunE: func(_ *cobra.Command, _ []string) error {
			n, err := mcpregistry.ExportDirectory(exportOut)
			if err != nil {
				return err
			}
			fmt.Printf("  wrote %d server(s) to %s/index.html and %s/index.json\n", n, exportOut, exportOut)
			return nil
		},
	}
	export.Flags().StringVar(&exportOut, "out", "mcp-directory", "output directory for index.html/index.json")

	var backtestJSON bool
	backtest := &cobra.Command{
		Use:   "backtest",
		Short: "Validate the scoring pipeline against known real MCP-security incidents",
		Long: "Checks whether the live pipeline still catches the documented real incidents that\n" +
			"justified building it, the gate this repo's design doc names before a public directory\n" +
			"(Phase 3) can go live. Re-run before any launch decision; this is a point-in-time proof,\n" +
			"not a permanent guarantee.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			known := mcpregistry.Backtest(cmd.Context())
			typo := mcpregistry.BacktestTyposquat()
			if backtestJSON {
				return json.NewEncoder(os.Stdout).Encode(map[string]any{"known_incidents": known, "typosquat_pairs": typo})
			}
			allPassed := true
			fmt.Println("  known-vulnerability match (live OSV.dev check):")
			for _, r := range known {
				status := okStyle.Render("CAUGHT")
				if !r.Caught {
					status = warnStyle.Render("MISSED")
					allPassed = false
				}
				fmt.Printf("    %-8s %s\n", status, r.Incident.Name)
				fmt.Printf("             %s\n", dimStyle.Render(r.Incident.Description))
				if r.Detail != "" {
					fmt.Printf("             %s\n", dimStyle.Render(r.Detail))
				}
			}
			fmt.Println("  typosquat detector (constructed pairs, not a live-registry probe):")
			for _, r := range typo {
				status := okStyle.Render("CAUGHT")
				if !r.Detected {
					status = warnStyle.Render("MISSED")
					allPassed = false
				}
				fmt.Printf("    %-8s %s vs %s (%d edits)\n", status, r.Pair.Registered, r.Pair.Typosquat, r.Distance)
			}
			if !allPassed {
				fmt.Println("\n  backtest FAILED, do not treat the pipeline as launch-ready")
				os.Exit(1)
			}
			fmt.Println("\n  backtest passed")
			return nil
		},
	}
	backtest.Flags().BoolVar(&backtestJSON, "json", false, "machine-readable JSON output")

	var listJSON bool
	list := &cobra.Command{
		Use:   "list",
		Short: "List every MCP server this machine has audited, by trust score",
		RunE: func(_ *cobra.Command, _ []string) error {
			entries, err := mcpregistry.Directory()
			if err != nil {
				return err
			}
			if listJSON {
				return json.NewEncoder(os.Stdout).Encode(entries)
			}
			if len(entries) == 0 {
				fmt.Println("  no audited servers yet, run `hyctl mcp registry audit` first")
				return nil
			}
			fmt.Printf("\n  %-40s %-12s %6s  %s\n", "SERVER", "STATE", "SCORE", "CONFIDENCE")
			fmt.Println("  " + strings.Repeat("─", 78))
			for _, e := range entries {
				state := string(e.LifecycleState)
				switch e.LifecycleState {
				case mcpregistry.StateTrusted:
					state = okStyle.Render(state)
				case mcpregistry.StateFlagged, mcpregistry.StateQuarantined, mcpregistry.StateDelisted:
					state = warnStyle.Render(state)
				}
				fmt.Printf("  %-40.40s %-12s %6s  %s\n", e.Name, state,
					mcpregistry.FormatScore(e.Score), mcpregistry.FormatConfidence(e.Score.Confidence))
			}
			fmt.Println()
			return nil
		},
	}
	list.Flags().BoolVar(&listJSON, "json", false, "machine-readable JSON output")

	clear := &cobra.Command{
		Use:   "clear <server>",
		Short: "Clear a quarantined server back to provisional (false-positive recovery)",
		Long: "Quarantine has no automatic way out by design, an audit run must not un-condemn a\n" +
			"server on its own. This is the manual path the automaton documents, for a server\n" +
			"that was quarantined in error. The cooldown clock resets, so a cleared server\n" +
			"re-earns trust rather than being handed it back.",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			state, err := mcpregistry.Clear(args[0], time.Now().UTC())
			if errors.Is(err, mcpregistry.ErrNotQuarantined) {
				return fmt.Errorf("%s is %s, not quarantined, nothing to clear", args[0], state)
			}
			if err != nil {
				return err
			}
			fmt.Printf("  %s  %s -> %s\n", okStyle.Render("CLEARED"), args[0], state)
			return nil
		},
	}

	cmd.AddCommand(sync, scanCmd, audit, export, backtest, list, clear)
	return cmd
}

// cmdSecurity is the security posture dashboard: ledger accountability,
// per-head risk, and a short list of honest checks, never a manufactured
// score, only what's actually configured and observed.
func cmdSecurity() *cobra.Command {
	var jsonOut, csvOut, execOut, attestOut, whyOut bool
	cmd := &cobra.Command{
		Use:   "security",
		Short: "What the agents on this machine did, and whether you need to act",
		RunE: func(cmd *cobra.Command, _ []string) error {
			heads := probe.Run(cmd.Context()).Heads
			rep, err := security.Build(heads)
			if err != nil {
				return err
			}
			switch {
			case jsonOut:
				return json.NewEncoder(os.Stdout).Encode(rep)
			case attestOut:
				return json.NewEncoder(os.Stdout).Encode(rep.Attestation)
			case execOut:
				fmt.Print(security.ExecutiveSummary(rep.Attestation))
				return nil
			case csvOut:
				return securityCSV(os.Stdout, rep)
			default:
				printSecurityReport(rep, whyOut)
				return nil
			}
		},
	}
	cmd.Flags().BoolVar(&whyOut, "why", false, "full detail: coverage, controls, policy, exposure, threats, and the risk register")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable JSON output")
	cmd.Flags().BoolVar(&csvOut, "csv", false, "one row per OWASP LLM Top-10 category (id,edition,name,status,gap_age_days,detail)")
	cmd.Flags().BoolVar(&execOut, "exec", false, "executive summary: the verdict, open risk by severity, and framework exposure")
	cmd.Flags().BoolVar(&attestOut, "attest", false, "checkable attestation: posture, evidence state, rules in force, and a digest")
	cmd.AddCommand(cmdSecurityTrifecta())
	return cmd
}

func cmdSecurityTrifecta() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "trifecta",
		Short: "Private data, untrusted content, and egress: which dispatches had all three",
		Long: "The lethal trifecta. Any one capability is fine; all three at once is what turns a " +
			"poisoned input into exfiltration with no software vulnerability involved. Counted per " +
			"dispatch, from the provenance the egress gate records.",
		RunE: func(_ *cobra.Command, _ []string) error {
			events, err := ledger.Load(ledger.DefaultPath())
			if err != nil {
				return err
			}
			t := security.AssessTrifecta(events)
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(t)
			}
			printTrifecta(t)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable JSON output")
	return cmd
}

func printTrifecta(t security.Trifecta) {
	fmt.Println()
	if !t.HasData {
		fmt.Println("  " + dimStyle.Render(
			"No dispatch has recorded provenance yet. Run something through `hyctl dispatch` first;"))
		fmt.Println("  " + dimStyle.Render(
			"events written before the egress gate shipped carry none, and are not counted."))
		fmt.Println()
		return
	}

	// The headline is exposure, not presence. A trifecta the gate refused is
	// the control working, and reporting it as a finding trains people to
	// ignore the number.
	switch {
	case t.Exposed() > 0:
		fmt.Printf("  %s  %s\n", cortexStyle.Render("LETHAL TRIFECTA"),
			warnStyle.Render(fmt.Sprintf("%d dispatch(es) had all three legs and were not stopped", t.Exposed())))
	case t.AllThree > 0:
		fmt.Printf("  %s  %s\n", cortexStyle.Render("LETHAL TRIFECTA"),
			okStyle.Render(fmt.Sprintf("%d dispatch(es) had all three legs, all refused", t.AllThree)))
	default:
		fmt.Printf("  %s  %s\n", cortexStyle.Render("LETHAL TRIFECTA"),
			okStyle.Render("no dispatch carried all three legs"))
	}
	fmt.Println(dimStyle.Render(fmt.Sprintf("  over %d dispatch(es) with recorded provenance", t.Evaluated)))
	fmt.Println()

	leg := func(label, detail string, l security.TrifectaLeg) {
		fmt.Printf("  %-20s %s\n", cortexStyle.Render(label),
			dimStyle.Render(fmt.Sprintf("%d dispatch(es), %s", l.Dispatches, detail)))
		for _, it := range l.Top {
			fmt.Printf("      %-44s %s\n", truncateMiddle(it.Name, 44),
				dimStyle.Render(fmt.Sprintf("%d", it.Count)))
		}
	}
	leg("private data", "read local files or environment", t.PrivateData)
	leg("untrusted content", "ingested content Hydra did not author", t.Untrusted)
	leg("external comms", "reached a head that leaves the machine", t.External)

	fmt.Println()
	fmt.Println(dimStyle.Render("  " + t.Caveat))
	fmt.Println()
}

// securityCSV emits the coverage table as one row per finding, the same
// shape GitHub's and AWS Security Hub's security-overview CSV exports use,
// so it can be dropped straight into a tracker or spreadsheet.
// An edition column rather than a bare id: only LLM01 and LLM02 keep their
// number across editions, so a tracker keyed on "LLM06" alone silently follows
// whatever entry takes that slot next (#748).
func securityCSV(w io.Writer, r *security.Report) error {
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"id", "edition", "name", "status", "gap_age_days", "detail"}); err != nil {
		return err
	}
	for _, c := range r.Coverage.Categories {
		if c.Status == security.NotApplicable {
			continue
		}
		if err := cw.Write([]string{
			c.ID, r.Coverage.Edition, c.Name, string(c.Status), strconv.Itoa(c.GapAgeDays), c.Detail,
		}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// printSubprocessBoundary states in words what no number above it can: a
// CLI-agent head runs in a process Hydra does not control, so the coverage
// figures describe what Hydra sends, not everything that leaves (#725).
func printSubprocessBoundary() {
	fmt.Println()
	fmt.Println(dimStyle.Render("  what this does not cover"))
	fmt.Println(dimStyle.Render("  a CLI-agent head (claude, agy, codex, cursor) is an agent in a process Hydra"))
	fmt.Println(dimStyle.Render("  does not control. Above is what Hydra sends and records; what such a head"))
	fmt.Println(dimStyle.Render("  reads and ships on its own is outside it. Only a local-only run stays here."))
}

// printSecurityReport answers one question by default, what did the agents
// on this machine do, and can the record be trusted, and everything else
// only under --why. Nine analyses printed unconditionally is an engineer's
// dashboard, not an answer.
func printSecurityReport(r *security.Report, why bool) {
	fmt.Println()
	printVerdict(r)
	printIncidents(r)
	printEvidenceState(r)
	printSubprocessBoundary()

	if !why {
		fmt.Println()
		fmt.Println(dimStyle.Render("  hyctl security --why    coverage, controls, policy, exposure, risk register"))
		fmt.Println(dimStyle.Render("  hyctl security --json   machine-readable"))
		fmt.Println()
		return
	}

	printRegister(r)

	fmt.Println()
	fmt.Println(dimStyle.Render("  " + strings.Repeat("=", 48)))
	fmt.Println(dimStyle.Render("  detail"))
	printCoverageHeadline(r)
	printAgenticCoverage(r)

	if !r.HasData {
		fmt.Println(dimStyle.Render("  no ledger events yet, nothing has dispatched through hyctl on this machine"))
	} else {
		fmt.Printf("  %s  %d  (%d allowed · %d denied · %d flagged)\n",
			cortexStyle.Render("ledger events"), r.Ledger.Total, r.Ledger.Allowed, r.Ledger.Denied, r.Ledger.Flagged)
	}

	if len(r.ByHead) > 0 {
		fmt.Println()
		fmt.Println(dimStyle.Render("  " + strings.Repeat("─", 48)))
		fmt.Printf("  %-24s %8s %8s\n", "HEAD", "DENIED", "FLAGGED")
		fmt.Println(dimStyle.Render("  " + strings.Repeat("─", 48)))
		for _, h := range r.ByHead {
			fmt.Printf("  %-24.24s %8d %8d\n", util.SafeTerminal(h.Head), h.Denied, h.Flagged)
		}
	}

	fmt.Println()
	fmt.Println(dimStyle.Render("  " + strings.Repeat("─", 48)))
	fmt.Println("  checks:")
	for _, c := range r.Checks {
		status := c.Status
		if strings.Contains(strings.ToLower(status), "broken") {
			status = warnStyle.Render(status)
		} else if status == "intact" || strings.HasSuffix(status, "refusal(s)") {
			status = okStyle.Render(status)
		}
		fmt.Printf("    %-26s %s\n", c.Name, status)
		fmt.Println(dimStyle.Render("      " + util.SafeTerminal(c.Detail)))
	}

	printControls(r)
	printPolicyAudit(r)
	printExposures(r)
	printThreats(r)

	if len(r.Actions) > 0 {
		fmt.Println()
		fmt.Println(dimStyle.Render("  " + strings.Repeat("─", 48)))
		fmt.Println(warnStyle.Render("  action queue (next hardening backlog, most urgent first):"))
		for _, a := range r.Actions {
			age := ""
			if a.AgeDays > 0 {
				age = dimStyle.Render(fmt.Sprintf(" · %dd", a.AgeDays))
			}
			fmt.Printf("    %s %s%s, %s\n", actionPriorityTag(a.Priority),
				util.SafeTerminal(a.Title), age, util.SafeTerminal(a.Detail))
		}
	}
	fmt.Println()
}

// printVerdict is the single line a CISO reads, plus the condition that
// produced it. Never a blended score, a state with its trigger named.
func printVerdict(r *security.Report) {
	p := r.Posture
	label := okStyle.Render("OK")
	switch p.Verdict {
	case security.VerdictActNow:
		label = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("196")).Render("ACT NOW")
	case security.VerdictAttention:
		label = warnStyle.Render("ATTENTION")
	}
	fmt.Printf("  %s  %s\n", cortexStyle.Render("VERDICT"), label)
	// The trigger quotes an incident narrative, which carries an attacker's
	// own tool name, the one line on this screen most worth forging.
	fmt.Println(dimStyle.Render("    " + util.SafeTerminal(p.Trigger)))
	// Stages of the incident the verdict quotes, so the shape of the attack
	// rides with the sentence instead of being repeated under it.
	if in, ok := citedIncident(r); ok {
		fmt.Println(dimStyle.Render(fmt.Sprintf("    %s · %s → %s · %d event(s) · likelihood %d × impact %d",
			stagesText(in.Stages), shortTS(in.Start), shortTS(in.End), len(in.Events), in.Likelihood, in.Impact)))
	}
	if len(p.Because) > 1 {
		fmt.Println(dimStyle.Render(fmt.Sprintf("    +%d more condition(s) below", len(p.Because)-1)))
	}
	if p.Verdict == security.VerdictOK {
		fmt.Println(dimStyle.Render("    checked: " + strings.Join(p.Checked, ", ")))
	}
}

// printEvidenceState is the second half of the developer's question: not just
// what happened, but whether the record of it can be believed.
func printEvidenceState(r *security.Report) {
	fmt.Println()
	// "0 events, intact" over a log that has never been written reads as a
	// clean bill of health. Nothing recorded is not the same as nothing wrong.
	if !r.HasData {
		fmt.Println(dimStyle.Render(
			"  no ledger events yet, nothing has dispatched through hyctl on this machine,"))
		fmt.Println(dimStyle.Render(
			"  so there is no record to judge and this is not a clean result"))
		return
	}
	ev := r.Attestation.Evidence
	chain := okStyle.Render("intact")
	switch {
	case ev.Truncated:
		chain = warnStyle.Render("TRUNCATED, records were deleted from the end")
	case !ev.ChainIntact:
		chain = warnStyle.Render("BROKEN, the log was modified after recording")
	case ev.Events > 0 && ev.ChainedEvents == 0:
		chain = warnStyle.Render("unverifiable, no hash chain on any record")
	case ev.AnchorMissing:
		chain = warnStyle.Render("unanchored, truncation would not be detected")
	}
	fmt.Printf("  %s  %d blocked · %d flagged\n",
		cortexStyle.Render("activity"), r.Ledger.Denied, r.Ledger.Flagged)
	fmt.Printf("  %s  %d event(s), %d hash-chained, %s\n",
		cortexStyle.Render("evidence"), ev.Events, ev.ChainedEvents, chain)
}

// printIncidents shows correlated sequences rather than scattered rows.
// citedIncident is the incident the verdict already quotes, if any.
func citedIncident(r *security.Report) (security.Incident, bool) {
	for _, in := range r.Incidents {
		if in.Narrative != "" && strings.Contains(r.Posture.Trigger, in.Narrative) {
			return in, true
		}
	}
	return security.Incident{}, false
}

func stagesText(stages []security.Stage) string {
	out := make([]string, 0, len(stages))
	for _, s := range stages {
		out = append(out, string(s))
	}
	return strings.Join(out, " → ")
}

// printIncidents lists the incidents the verdict does NOT already quote,
// printing the cited one again puts the same sentence on screen twice.
func printIncidents(r *security.Report) {
	cited, hasCited := citedIncident(r)
	rest := make([]security.Incident, 0, len(r.Incidents))
	for _, in := range r.Incidents {
		if hasCited && in.ID == cited.ID {
			continue
		}
		rest = append(rest, in)
	}
	if len(rest) == 0 {
		return
	}
	fmt.Println()
	fmt.Printf("  %s\n", cortexStyle.Render("other incidents"))
	for _, in := range security.TopIncidents(rest, 3) {
		fmt.Printf("    %s %s\n", severityTag(in.Severity), util.SafeTerminal(in.Narrative))
		fmt.Println(dimStyle.Render(fmt.Sprintf("      %s → %s · %d event(s) · likelihood %d × impact %d",
			shortTS(in.Start), shortTS(in.End), len(in.Events), in.Likelihood, in.Impact)))
	}
	if n := len(rest); n > 3 {
		fmt.Println(dimStyle.Render(fmt.Sprintf("    … %d more", n-3)))
	}
}

// printRegister is the governed view: what is open, how overdue, what it is
// worth, and which frameworks it bears on.
func printRegister(r *security.Report) {
	reg := r.Register
	if len(reg.Risks) == 0 {
		return
	}
	fmt.Println()
	fmt.Printf("  %s   %s\n", cortexStyle.Render("risk register"),
		dimStyle.Render(fmt.Sprintf("Σ modelled defect cost $%.0f (per-occurrence, not annualised) · %d past SLA", reg.SumDefectCostUSD, reg.Breached)))
	fmt.Printf("    %-10s %-42s %-9s %8s %12s\n", "ID", "RISK", "SEVERITY", "DUE", "COST/DEFECT")
	for _, k := range topRisks(reg.Risks, 6) {
		due := dimStyle.Render(fmt.Sprintf("%dd", k.DueInDays))
		if k.Breached {
			due = warnStyle.Render(fmt.Sprintf("%dd", k.DueInDays))
		}
		fmt.Printf("    %-10s %-42.42s %-9s %8s %12s\n",
			k.ID, util.SafeTerminal(k.Title), severityTag(k.Severity), due, fmt.Sprintf("$%.0f", k.DefectCostUSD))
		if len(k.Frameworks) > 0 {
			fmt.Println(dimStyle.Render("               " + frameworksText(k.Frameworks)))
		}
	}
	if n := len(reg.Risks); n > 6 {
		fmt.Println(dimStyle.Render(fmt.Sprintf("    … %d more", n-6)))
	}
}

func topRisks(rs []security.Risk, n int) []security.Risk {
	if len(rs) <= n {
		return rs
	}
	return rs[:n]
}

func frameworksText(fs []security.FrameworkRef) string {
	parts := make([]string, 0, len(fs))
	for _, f := range fs {
		parts = append(parts, f.Framework+" "+f.Control)
	}
	return strings.Join(parts, " · ")
}

func severityTag(s security.Severity) string {
	switch s {
	case security.SeverityCritical:
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("196")).Render("CRITICAL")
	case security.SeverityHigh:
		return warnStyle.Render("HIGH    ")
	case security.SeverityMedium:
		return dimStyle.Render("MEDIUM  ")
	default:
		return dimStyle.Render("LOW     ")
	}
}

// shortTS trims an RFC3339 stamp to the time of day for a dense table.
func shortTS(ts string) string {
	if len(ts) >= 19 {
		return ts[11:19]
	}
	return ts
}

// printControls answers "does each declared control actually run", the
// question a config file cannot answer about itself. A control that is
// configured but cannot fire reads as protection everywhere it is listed
// while doing nothing, which is worse than a control that is simply absent.
func printControls(r *security.Report) {
	if len(r.Controls) == 0 {
		return
	}
	fmt.Println()
	fmt.Println(dimStyle.Render("  " + strings.Repeat("─", 48)))
	fmt.Printf("  %s\n", cortexStyle.Render("control effectiveness"))
	for _, c := range r.Controls {
		var tag string
		switch c.Status() {
		case "inert":
			tag = warnStyle.Render("INERT  ")
		case "limited":
			tag = warnStyle.Render("LIMITED")
		case "absent":
			tag = dimStyle.Render("absent ")
		default:
			tag = okStyle.Render("active ")
		}
		// Mark rows established by reading the source rather than by
		// observation, so the reader knows which claims are evidence.
		src := ""
		if !c.Verified {
			src = dimStyle.Render(" [source-derived]")
		}
		fmt.Printf("    %s %s%s\n", tag, c.Name, src)
		fmt.Println(dimStyle.Render("      " + util.SafeTerminal(c.Detail)))
	}
}

// printPolicyAudit is the real policy readout: which rules fire, which never
// have, which never can, and whether the default lets everything through.
func printPolicyAudit(r *security.Report) {
	a := r.PolicyAudit
	fmt.Println()
	fmt.Println(dimStyle.Render("  " + strings.Repeat("─", 48)))
	fmt.Printf("  %s\n", cortexStyle.Render("policy audit"))

	posture := okStyle.Render("fail-closed (default deny)")
	if a.FailOpen {
		posture = warnStyle.Render("fail-open (default allow)")
	}
	fmt.Printf("    %-26s %s\n", "posture", posture)

	if len(a.Rules) == 0 {
		fmt.Println(dimStyle.Render("    no rules defined, nothing is scoped"))
		return
	}
	fmt.Printf("    %-4s %-30s %-7s %6s  %s\n", "#", "RULE", "DECIDES", "HITS", "")
	for _, rule := range a.Rules {
		note := ""
		switch {
		case rule.ShadowedBy != nil:
			note = warnStyle.Render(fmt.Sprintf("UNREACHABLE, rule %d always matches first", *rule.ShadowedBy))
		case rule.Dead:
			note = dimStyle.Render("never matched")
		}
		fmt.Printf("    %-4d %-30.30s %-7s %6d  %s\n",
			rule.Index, util.SafeTerminal(rule.Summary), rule.Decision, rule.Hits, note)
	}
	fmt.Println(dimStyle.Render(fmt.Sprintf("    %d access(es) fell through to the %s default", a.DefaultHits, a.Default)))
}

// printOutputWarning flags a response that carries credential-shaped content.
// It prints after the output rather than before, so the warning is the last
// thing on screen when the orchestration protocol's next step is to apply that
// output to disk.
func printOutputWarning(p egress.Part) {
	if p.Sens < egress.Secret {
		return
	}
	fmt.Printf("  %s the response carries %s. Review before applying it.\n\n",
		warnStyle.Render("⚠"), util.SafeTerminal(strings.Join(p.Reasons, ", ")))
}

// printExposures answers the question a PII count never could: did any of it
// leave the machine?
func printExposures(r *security.Report) {
	if len(r.Exposures) == 0 {
		return
	}
	fmt.Println()
	fmt.Println(dimStyle.Render("  " + strings.Repeat("─", 48)))
	fmt.Printf("  %s\n", cortexStyle.Render("sensitive data exposure"))
	for _, e := range r.Exposures {
		where := okStyle.Render("local")
		switch {
		case e.Remote && e.Known:
			where = warnStyle.Render("REMOTE")
		case e.Remote:
			// Treated as remote (fail-closed) but not observed as such,
			// distinguished so an offline head can't read as a real leak.
			where = dimStyle.Render("UNKNOWN")
		}
		types := strings.Join(e.PIITypes, ", ")
		if types == "" {
			types = "unclassified type"
		}
		fmt.Printf("    %-7s %-18.18s %-24.24s %s\n", where,
			util.SafeTerminal(e.Head), util.SafeTerminal(e.Resource), dimStyle.Render(types))
	}
}

// printThreats is the forensic breakdown behind the blocked/flagged counts:
// what was actually attempted, against what, and how dangerous the operation
// was.
func printThreats(r *security.Report) {
	th := r.Threats
	if len(th.ByMarker) == 0 && len(th.ProbedResources) == 0 && len(th.ByAction) == 0 {
		return
	}
	fmt.Println()
	fmt.Println(dimStyle.Render("  " + strings.Repeat("─", 48)))
	fmt.Printf("  %s\n", cortexStyle.Render("threat breakdown"))
	printCountList("injection markers tried", th.ByMarker)
	printCountList("resources probed (repeat denials)", th.ProbedResources)
	printCountList("by action", th.ByAction)
}

func printCountList(title string, counts []security.Count) {
	if len(counts) == 0 {
		return
	}
	fmt.Printf("    %s\n", dimStyle.Render(title+":"))
	for _, c := range counts {
		fmt.Printf("      %-40.40s %d\n", util.SafeTerminal(c.Label), c.Count)
	}
}

// actionPriorityTag renders an Action's priority as a short colored tag.
// Colors mirror budgetModeStyle's own critical/warning/dim convention rather
// than inventing a new palette.
func actionPriorityTag(p security.ActionPriority) string {
	switch p {
	case security.PriorityNow:
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("196")).Render("[NOW]")
	case security.PrioritySoon:
		return warnStyle.Render("[SOON]")
	default:
		return dimStyle.Render("[WATCH]")
	}
}

// printAgenticCoverage is the second table: the OWASP Top 10 for Agentic
// Applications. Rendered beside the LLM list rather than instead of it,
// because they answer different questions: what a model says, and what a
// system does. Hydra is squarely the second.
func printAgenticCoverage(r *security.Report) {
	a := r.Agentic
	if len(a.Categories) == 0 {
		return
	}
	summary := fmt.Sprintf("%.0f%%", a.PercentCovered)
	fmt.Printf("  %s  %s  (%d/%d, %d partial)\n",
		cortexStyle.Render("OWASP Agentic Top-10 coverage"), okStyle.Render(summary),
		a.Covered, a.Applicable, a.Partial)
	fmt.Println(dimStyle.Render("  " + strings.Repeat("─", 48)))
	for _, c := range a.Categories {
		if c.Status == security.NotApplicable {
			continue
		}
		label := string(c.Status)
		switch c.Status {
		case security.Enforced, security.Configured:
			label = okStyle.Render(label)
		case security.Partial:
			label = dimStyle.Render(label)
		case security.Gap:
			label = warnStyle.Render(label)
		}
		fmt.Printf("    %-6s %-32s %s\n", c.ID, truncLabel(c.Name, 32), label)
	}
	fmt.Println()
}

// printCoverageHeadline is the KPI tile: coverage against the OWASP LLM Top
// 10, never presented as "you are X% secure", always labeled against the
// named taxonomy it measures. A broken ledger chain hard-overrides it,
// since none of the other evidence can be trusted once the ledger itself
// might have been tampered with.
func printCoverageHeadline(r *security.Report) {
	if !r.IntegrityIntact {
		fmt.Printf("  %s  %s\n", cortexStyle.Render("OWASP LLM Top-10 coverage"),
			warnStyle.Render("INTEGRITY COMPROMISED, ledger tampering detected, score withheld"))
		fmt.Println()
		return
	}
	// A fail-open policy withholds the score for the same reason tampering
	// does: with every access defaulting to allow, the gate in the dispatch
	// path records and blocks nothing, so a coverage number describes
	// mechanisms that are not actually in force.
	if pa := r.PolicyAudit; pa.FailOpen {
		fmt.Printf("  %s  %s\n", cortexStyle.Render("OWASP LLM Top-10 coverage"),
			warnStyle.Render("NO DEFAULT-DENY POLICY, anything no rule names is allowed, score withheld"))
		fmt.Println(dimStyle.Render(fmt.Sprintf(
			"    %d rule(s) in force, default %q; %d of %d recorded accesses matched no rule",
			len(pa.Rules), pa.Default, pa.DefaultHits, pa.Evaluated)))
		fmt.Println()
		return
	}
	cov := r.Coverage
	pct := fmt.Sprintf("%.0f%%", cov.PercentCovered)
	fmt.Printf("  %s  %s  (%d/%d applicable categories)\n",
		cortexStyle.Render("OWASP LLM Top-10 coverage"), okStyle.Render(pct), cov.Covered, cov.Applicable)
	if r.Trend.Available {
		arrow := "→"
		style := dimStyle
		if r.Trend.DeltaPct > 0 {
			arrow, style = "↑", okStyle
		} else if r.Trend.DeltaPct < 0 {
			arrow, style = "↓", warnStyle
		}
		fmt.Println(style.Render(fmt.Sprintf("    %s %+.0f%% since %s (was %.0f%%)",
			arrow, r.Trend.DeltaPct, relativeTime(r.Trend.FirstTS), r.Trend.FirstPct)))
	}
	fmt.Println(dimStyle.Render("  " + strings.Repeat("─", 48)))
	for _, c := range cov.Categories {
		if c.Status == security.NotApplicable {
			continue
		}
		label := string(c.Status)
		switch c.Status {
		case security.Enforced:
			label = okStyle.Render(label)
		case security.Partial:
			label = dimStyle.Render(label + " (detective only)")
		case security.Gap:
			label = warnStyle.Render(label)
			if c.GapAgeDays > 0 {
				label += dimStyle.Render(fmt.Sprintf(" (%dd)", c.GapAgeDays))
			}
		}
		fmt.Printf("    %-6s %-32s %s\n", c.ID, c.Name, label)
	}
	fmt.Println()
}

// relativeTime renders an RFC3339 timestamp as a human-relative duration
// ("3 hours ago"). Falls back to the raw string if it doesn't parse, a
// display glitch, never a crash, over a timestamp some future format change
// didn't anticipate.
func relativeTime(ts string) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ts
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		n := int(d / time.Minute)
		return fmt.Sprintf("%d minute%s ago", n, plural(n))
	case d < 24*time.Hour:
		n := int(d / time.Hour)
		return fmt.Sprintf("%d hour%s ago", n, plural(n))
	default:
		n := int(d / (24 * time.Hour))
		return fmt.Sprintf("%d day%s ago", n, plural(n))
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// cmdModels manages the runtime-extensible model capability registry.
func cmdModels() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "models",
		Short: "Model capability scores: list what is known, and record new ones without recompiling",
	}
	overlay := capabilities.DefaultOverlayPath()

	var jsonOut bool
	list := &cobra.Command{
		Use:   "list",
		Short: "List known models and whether this machine can actually run each",
		RunE: func(cmd *cobra.Command, _ []string) error {
			db, err := capabilities.Load(overlay)
			if err != nil {
				return err
			}
			// Joined to discovery: this is a catalogue of scores, and rendering
			// it without saying which entries are live listed API providers with
			// no key set as if they were available (#742).
			rows := liveModelRows(db.Entries(), probe.Run(cmd.Context()).Heads)
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(rows)
			}
			fmt.Printf("\n  %-26s %-14s %6s  %-8s %s\n", "ID", "PROVIDER", "SCORE", "SOURCE", "ROUTABLE")
			fmt.Println("  " + strings.Repeat("─", 72))
			var live int
			for _, r := range rows {
				src := dimStyle.Render(r.Source)
				if r.Source == "user" {
					src = cortexStyle.Render("user")
				}
				routable := warnStyle.Render("✗")
				if r.Live {
					live++
					routable = okStyle.Render("✓")
				}
				fmt.Printf("  %-26.26s %-14.14s %6d  %-8s %s\n",
					r.ID, r.Provider, r.CapScore, src, routable)
			}
			fmt.Println("  " + strings.Repeat("─", 72))
			fmt.Printf("  %s\n", dimStyle.Render(fmt.Sprintf(
				"%d of %d can be routed to right now; the rest are capability scores for "+
					"models nothing here discovers. `hyctl probe` says why.", live, len(rows))))
			fmt.Println()
			return nil
		},
	}
	list.Flags().BoolVar(&jsonOut, "json", false, "machine-readable JSON output")

	var addName, addProvider string
	var addScore int
	add := &cobra.Command{
		Use:   "add <id>",
		Short: "Record a capability score for a model (a provider must still discover it)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if addScore < 0 || addScore > 100 {
				return fmt.Errorf("--cap-score must be 0-100, got %d", addScore)
			}
			e := capabilities.Entry{ID: args[0], Name: addName, Provider: addProvider, CapScore: addScore}
			if e.Name == "" {
				e.Name = args[0]
			}

			// Check against the built-in catalog alone (no overlay), this is
			// "does this id shadow a curated entry", which is a different
			// question from AddModel's "did the overlay already have it".
			builtin, err := capabilities.Load("")
			if err != nil {
				return err
			}
			prior, overridesBuiltin := builtin.Entry(args[0])

			replaced, err := capabilities.AddModel(overlay, e)
			if err != nil {
				return err
			}
			switch {
			case overridesBuiltin:
				fmt.Printf("  overriding built-in model %s (was: %s, score %d) → %s, score %d in %s\n",
					e.ID, prior.Provider, prior.CapScore, e.Provider, e.CapScore, overlay)
			case replaced:
				fmt.Printf("  updated %s (%s, score %d) → %s\n", e.ID, e.Provider, e.CapScore, overlay)
			default:
				// Provider is optional, and interpolating an empty one rendered
				// "recorded kimi-k4 (, score 70)".
				where := fmt.Sprintf("score %d", e.CapScore)
				if e.Provider != "" {
					where = fmt.Sprintf("%s, score %d", e.Provider, e.CapScore)
				}
				fmt.Printf("  recorded %s (%s) → %s\n", e.ID, where, overlay)
			}
			// "added" on its own read as "this model is now routable", which an
			// overlay entry never makes it (#742).
			fmt.Printf("  %s\n", dimStyle.Render(addedModelNote(e)))
			return nil
		},
	}
	add.Flags().StringVar(&addName, "name", "", "display name (default: the id)")
	add.Flags().StringVar(&addProvider, "provider", "", "provider, e.g. moonshot / openai / local")
	add.Flags().IntVar(&addScore, "cap-score", 70, "capability score 0-100")

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
			// An empty live catalogue is not "nothing new to import", it means
			// no model pricing was available at all, which can only happen when
			// there is no cache and the fetch did not land. Reporting
			// "imported 0 models" for that reads as a completed sync against an
			// already-complete catalogue, so the user never learns the fetch
			// failed. Tier pricing is still loaded, which is why Load() itself
			// cannot report this.
			if len(models) == 0 {
				return fmt.Errorf("no model catalogue available, the OpenRouter " +
					"fetch has not landed. Run `hyctl pricing refresh` first, or " +
					"check network access")
			}
			added, skipped := 0, 0
			// Must include the overlay, not just the embedded catalog, otherwise
			// a model the user already synced and hand-tuned via `models add`
			// looks "unknown" here and gets silently overwritten back to a fresh
			// heuristic capScore on every re-run (#505).
			known, err := capabilities.Load(overlay)
			if err != nil {
				return err
			}
			for _, id := range models {
				if syncFilter != "" && !strings.Contains(strings.ToLower(id), strings.ToLower(syncFilter)) {
					continue
				}
				// Don't clobber a model already known (built-in or user).
				if known.Source(id) != "" {
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
			fmt.Printf("\n  %s %d models (%d already known, skipped). capScores are provisional, refine with `hyctl models add`.\n\n", word, added, skipped)
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
		Short: "Measure context-window quality, signal density, not just length",
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
			impact := g.Impact(file)
			radius := impact.Radius
			task := trust.Task{BlastRadius: radius}
			dm := trust.NewDefectModel()

			deps := impact.Dependents
			pFactor := impact.Percolation
			// A radius of 1.0 means either "nothing depends on this" or "I have
			// no idea", opposite conclusions that used to render identically.
			// internal/a2a reported 6 dependents and 97.4% required confidence
			// with the graph, and "subcritical, edits stay local" at 90.0%
			// without it (#251).
			loaded, known := !g.Empty(), impact.Known
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
				fmt.Printf("    %s\n", warnStyle.Render("no graph at "+graphPath+", nothing was analysed"))
			} else if !known {
				fmt.Printf("    %s\n", warnStyle.Render("not in the graph, check the path, or reindex"))
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
			// unknown file, but with no graph at all it is 0.0, and rendering
			// that as "edits stay local" is a safety claim from no data.
			if loaded {
				if g.Percolates() {
					core := "periphery"
					if pFactor > 1.0 {
						core = fmt.Sprintf("core (+%.0f%%)", (pFactor-1)*100)
					}
					fmt.Printf("    graph κ                %.2f  %s\n",
						g.Kappa(), dimStyle.Render("supercritical, cascades possible; this file is "+core))
				} else {
					fmt.Printf("    graph κ                %.2f  %s\n",
						g.Kappa(), dimStyle.Render("subcritical, edits stay local"))
				}
			}
			fmt.Printf("    defect cost            $%.2f%s\n", dm.CostUSD(task), suffix)
			fmt.Printf("    demands confidence     %.1f%%%s\n\n", dm.RequiredConfidence(task)*100, suffix)
			if !measured {
				fmt.Printf("  %s\n\n", dimStyle.Render(
					"A radius of 1.00× here means \"unknown\", not \"safe\", generate a graph to get a real bar."))
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
			loaded := !g.Empty()
			var unknown []string
			if loaded {
				for _, f := range args {
					if !g.Knows(f) {
						unknown = append(unknown, f)
					}
				}
			}
			if parJSON {
				return json.NewEncoder(os.Stdout).Encode(map[string]any{
					"files": args, "serial_fraction": serial, "coordination_k": k,
					"optimal_agents": n, "speedup": speedup,
					"graph_loaded": loaded, "unknown_files": unknown,
				})
			}
			fmt.Printf("\n  %s\n", cortexStyle.Render(fmt.Sprintf("%d file(s)", len(args))))
			// Coupling silently treats an unmatched file as having no impact set,
			// which biases k toward kMin (looks "safe to parallelize"), say so
			// rather than let a default read as a measurement (#448).
			if !loaded {
				fmt.Printf("    %s\n", warnStyle.Render(
					"no graph at "+parGraph+", coordination cost k is a default, not a measurement"))
			} else if len(unknown) > 0 {
				fmt.Printf("    %s\n", warnStyle.Render(
					"not in the graph: "+strings.Join(unknown, ", ")+", their contribution to k is a default, not a measurement"))
			}
			suffix := ""
			if !loaded || len(unknown) > 0 {
				suffix = "  " + dimStyle.Render("(partially default)")
			}
			fmt.Printf("    coordination cost k    %.3f%s\n", k, suffix)
			fmt.Printf("    optimal agents n*      %d\n", n)
			fmt.Printf("    speedup S(n*)          %.2f×\n", speedup)
			if speedup < 1 {
				fmt.Printf("    %s\n", dimStyle.Render("→ heavily coupled, parallelism doesn't pay; edit serially"))
			}
			fmt.Println()
			return nil
		},
	}
	parallelCmd.Flags().StringVar(&parGraph, "graph", "graph.json", "path to the dependency graph (graph.json)")
	parallelCmd.Flags().BoolVar(&parJSON, "json", false, "machine-readable JSON output")
	parallelCmd.Flags().Float64Var(&serial, "serial", optimal.DefaultSerialFraction, "serial (non-parallelizable) work fraction s")

	var genOut, genExclude string
	generate := &cobra.Command{
		Use:   "generate [path]",
		Short: "Generate graph.json from a Go module's package-import graph (go list -json)",
		Long: "Generate graph.json from a Go module's package-import graph via `go list -json`.\n" +
			"Go package granularity only, not a general tree-sitter indexer. For other\n" +
			"languages, use Graphify or any indexer that produces the same {nodes,edges} schema.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			var exclude []string
			for _, e := range strings.Split(genExclude, ",") {
				if e = strings.TrimSpace(e); e != "" {
					exclude = append(exclude, e)
				}
			}
			doc, err := graph.GenerateGo(dir, exclude)
			if err != nil {
				return err
			}
			raw, err := json.MarshalIndent(doc, "", "  ")
			if err != nil {
				return err
			}
			if err := os.WriteFile(genOut, raw, 0o644); err != nil {
				return err
			}
			fmt.Printf("  %s %d nodes, %d edges → %s\n",
				cortexStyle.Render("graph:"), len(doc.Nodes), len(doc.Edges), genOut)
			return nil
		},
	}
	generate.Flags().StringVar(&genOut, "out", "graph.json", "output path")
	generate.Flags().StringVar(&genExclude, "exclude", "", "comma-separated module-relative path prefixes to skip (e.g. bench,cmd/specstest)")

	cmd.AddCommand(blast, parallelCmd, generate)
	return cmd
}

// noEvidenceError turns the SPRT refusal into something the reader can act on.
//
// The refusal tests whether any head this run would sample carries evidence in
// this domain. The message used to answer a different question, listing every
// domain in which any source has evidence, so asking for `gotest` was refused
// with "nothing here can judge gotest" directly above a list containing gotest
// (#732). Sources scored in this domain are named first, because a domain that
// has evidence but not from these heads is a different problem with a different
// fix.
func noEvidenceError(domain string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "no head this run would sample has been scored in domain %q, so --confidence "+
		"would sample every head, move the estimate nowhere and hand back 50%%.\n", domain)
	b.WriteString("A source only carries evidence once its verdicts have been scored against outcomes.\n\n")

	if cal, err := trust.New(trust.DefaultPath()); err == nil {
		var here []string
		seen := map[string]bool{}
		var others []string
		for _, st := range cal.Report() {
			if st.D <= 0 {
				continue
			}
			if st.Domain == domain {
				here = append(here, st.Source)
				continue
			}
			if !seen[st.Domain] {
				seen[st.Domain] = true
				others = append(others, st.Domain)
			}
		}
		if len(here) > 0 {
			sort.Strings(here)
			fmt.Fprintf(&b, "  Scored in %q:  %s\n", domain, strings.Join(here, ", "))
			b.WriteString("  ...but none of them is among the heads this run selects.\n")
		}
		if len(others) > 0 {
			sort.Strings(others)
			fmt.Fprintf(&b, "  Other domains with evidence: %s\n", strings.Join(others, ", "))
		}
	}
	b.WriteString("  Record an outcome:     hyctl trust record --source model:<id> --domain " + domain + " --said-correct --outcome correct\n")
	b.WriteString("  Or verify with a test: hyctl oracle verify --candidate <file> --domain " + domain + " -- go test ./...\n")
	b.WriteString("\nWithout --confidence the same prompt routes normally and costs one head.")
	return errors.New(b.String())
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

	// The target and the stop reason, not just the number reached: without them
	// a run that met its target and a run that gave up at the 50% prior render
	// identically, and the second is the one worth knowing about (#732).
	fmt.Printf("\n  %s %s  ·  target %.1f%% → achieved %.1f%%  ·  %d samples  ·  $%.4f\n",
		dimStyle.Render("Decision →"),
		cortexStyle.Render(t.Decision.String()),
		r.Target*100, t.Confidence*100, t.Samples, t.SpentUSD,
	)
	if warn := sprtWarning(r); warn != "" {
		fmt.Printf("  %s\n", warnStyle.Render(warn))
	}
	// The key `hyctl trust explain` takes. It was written to trust.jsonl and
	// printed nowhere, so the best diagnostic in the tool was reachable only by
	// grepping a log file.
	fmt.Printf("  %s\n", dimStyle.Render(
		"why: hyctl trust explain "+trust.TaskHash(r.Prompt)))
	fmt.Println()
	if t.Candidate != "" {
		fmt.Println(t.Candidate)
		fmt.Println()
	}
}

// stalledConfidence reports when the ensemble has not moved off the 50% prior,
// which is a spend report rather than a confidence one. Both conditions are
// required: a run can legitimately land near 50% after real evidence cancelled
// out, and that is not the same as never having any.
func stalledConfidence(meanFinal, autoClearedPct float64) string {
	const prior, tol = 0.5, 0.02
	if autoClearedPct > 0 || meanFinal < prior-tol || meanFinal > prior+tol {
		return ""
	}
	return "achieved confidence has not left the 50% prior and no run has ever " +
		"reached its target.\n  These runs paid for heads without learning anything. " +
		"Calibrate a domain first:\n  hyctl trust calibration   ·   hyctl oracle verify --domain <d> -- <test command>"
}

// sprtWarning names the failure that looks like a result: every source
// contributed zero evidence, so the posterior never left the prior and the
// heads were paid for nothing.
func sprtWarning(r *swarm.SPRTResult) string {
	t := r.Trust
	if t == nil || len(t.Ledger) == 0 {
		return ""
	}
	for _, e := range t.Ledger {
		if e.LLR != 0 {
			return ""
		}
	}
	return fmt.Sprintf(
		"no source carried evidence in %q: %d heads were sampled and the estimate never left the prior.\n"+
			"  Calibrate with `hyctl oracle verify --domain %s -- <test command>`.",
		r.Domain, len(t.Ledger), r.Domain)
}

// logTrustRun appends the SPRT run to ~/.hydra/trust.jsonl (best-effort).
func logTrustRun(r *swarm.SPRTResult, prompt, domain string) {
	domain = trust.Domain(domain)
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
		Hypotheses: r.Trust.Hypotheses,
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
		// A fallback with no reason shown is indistinguishable from a healthy
		// LLM judge run, surface why the primary judge was skipped (#501).
		if r.Verdict.Meta.UsedFallback && r.Verdict.Meta.FallbackReason != "" {
			fmt.Printf("  %s\n", warnStyle.Render("judge fallback: "+r.Verdict.Meta.FallbackReason))
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
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()

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
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
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
		RunE: func(cmd *cobra.Command, _ []string) error {
			raw, err := os.ReadFile(tasksFile)
			if err != nil {
				return fmt.Errorf("reading tasks file: %w", err)
			}
			var tasks []parallel.Task
			if err := json.Unmarshal(raw, &tasks); err != nil {
				return fmt.Errorf("invalid JSON in %s: %w", tasksFile, err)
			}

			ctx := cmd.Context()
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
		// A bare negative N (`cost tail -5`) is indistinguishable from an
		// unrecognized flag to cobra's parser, which rejects it before this
		// RunE ever sees it, hence the escape hatch below (#464).
		Long: "Last N calls (default 10). A negative N needs the flag " +
			"terminator: `hyctl cost tail -- -5`, or cobra reads it as a flag.",
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

// latencyRow is the shape --latency --json emits. Percentiles come from a
// sketch, so they are estimates with a stated relative-error bound rather than
// exact values, the field names say so.
type latencyRow struct {
	Model    string  `json:"model"`
	Tier     int     `json:"tier"`
	Calls    int64   `json:"calls"`
	P50Est   float64 `json:"p50_ms_est"`
	P90Est   float64 `json:"p90_ms_est"`
	P99Est   float64 `json:"p99_ms_est"`
	RelErr   float64 `json:"relative_error_bound"`
	Explored int64   `json:"explored"`
}

// mergeByModel folds every day's rollup into one per (model, tier). Sketches
// merge, which is the only reason a per-day store can answer an all-time p99.
func mergeByModel(rows []rollup.Row) []latencyRow {
	type key struct {
		model string
		tier  int
	}
	acc := map[key]*rollup.Row{}
	for i := range rows {
		r := rows[i]
		k := key{r.Model, r.Tier}
		cur := acc[k]
		if cur == nil {
			c := r
			if c.Latency != nil {
				c.Latency = c.Latency.Clone()
			}
			acc[k] = &c
			continue
		}
		cur.Calls += r.Calls
		cur.Explored += r.Explored
		if cur.Latency != nil && r.Latency != nil {
			_ = cur.Latency.Merge(r.Latency)
		}
	}
	out := make([]latencyRow, 0, len(acc))
	for k, v := range acc {
		lr := latencyRow{Model: k.model, Tier: k.tier, Calls: v.Calls, Explored: v.Explored}
		if v.Latency != nil {
			lr.P50Est, lr.P90Est, lr.P99Est = v.Latency.Quantile(0.5), v.Latency.Quantile(0.9), v.Latency.Quantile(0.99)
			lr.RelErr = v.Latency.Alpha
		}
		out = append(out, lr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Calls > out[j].Calls })
	return out
}

func rollupLatencyJSON(rows []rollup.Row) []latencyRow { return mergeByModel(rows) }

func renderLatency(rows []rollup.Row) {
	merged := mergeByModel(rows)
	if len(merged) == 0 {
		fmt.Println("No dispatches recorded yet.")
		return
	}
	fmt.Printf("%-28s %5s %8s %10s %10s %10s %9s\n", "MODEL", "TIER", "CALLS", "P50 ms", "P90 ms", "P99 ms", "EXPLORED")
	for _, r := range merged {
		fmt.Printf("%-28s %5d %8d %10.0f %10.0f %10.0f %9d\n",
			truncateMiddle(r.Model, 28), r.Tier, r.Calls, r.P50Est, r.P90Est, r.P99Est, r.Explored)
	}
	if len(merged) > 0 {
		fmt.Printf("\nPercentiles are sketch estimates, accurate to +/-%.1f%% relative.\n", merged[0].RelErr*100)
	}
}

// truncateMiddle keeps both ends of a model id, which is where the
// distinguishing parts live (provider prefix and version suffix).
func truncateMiddle(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n < 5 {
		return s[:n]
	}
	half := (n - 1) / 2
	return s[:half] + "\u2026" + s[len(s)-(n-half-1):]
}

func cmdStats() *cobra.Command {
	var (
		days      int
		byModel   bool
		byTier    bool
		byDay     bool
		swarmOnly bool
		sessionID string
		jsonOut   bool
		latency   bool
	)

	printJSON := func(v any) {
		raw, _ := json.MarshalIndent(v, "", "  ")
		fmt.Println(string(raw))
	}

	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Spend analytics, model, tier, day, and swarm breakdowns",
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
			// Percentiles come from rollups, not raw rows: a mean latency
			// answers nothing, and keeping every wall_ms to compute p99 is
			// what internal/sketch exists to avoid (#624).
			if latency {
				rows, err := rollup.Refresh(cost.DefaultLogPath(), rollup.DefaultPath())
				if err != nil {
					return err
				}
				if jsonOut {
					printJSON(rollupLatencyJSON(rows))
					return nil
				}
				renderLatency(rows)
				return nil
			}

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
	cmd.Flags().BoolVar(&latency, "latency", false, "latency percentiles per model, from mergeable sketches (see internal/rollup)")
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
			// Hoist filter once, db.Models() is already sorted.
			filter := strings.ToLower(strings.Join(args, " "))

			type row struct {
				Model            string  `json:"model"`
				InputPerMillion  float64 `json:"input_per_mtok"`
				OutputPerMillion float64 `json:"output_per_mtok"`
				Source           string  `json:"source"` // "openrouter" or "tier"
			}
			// Must start as [] not nil: JSON-encoded nil is `null`, and a script
			// treating this as an array (docs/pricing.md's whole audience) breaks
			// on a filter that matches nothing (#505).
			rows := []row{}
			for _, m := range db.Models() {
				if filter != "" && !strings.Contains(m, filter) {
					continue
				}
				p, _ := db.ModelPrice(m)
				rows = append(rows, row{m, p.InputPerMillion, p.OutputPerMillion, "openrouter"})
			}
			// Tier pricing is what prices CLI-agent heads (claude-core,
			// opus-thinking, …), they never appear in OpenRouter's catalog, so
			// without this merge they can never show up here at all, and a
			// fresh/offline install (no cache, no network) shows a fully empty
			// table instead of the tier fallback that's actually pricing calls.
			for _, te := range db.TierEntries() {
				if filter != "" && !strings.Contains(strings.ToLower(te.ID), filter) {
					continue
				}
				rows = append(rows, row{te.ID, te.InputPerMillion, te.OutputPerMillion, "tier"})
			}

			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(rows)
			}

			fmt.Fprintf(os.Stdout, "%-45s  %10s  %11s  %s\n", "Model", "In $/1M", "Out $/1M", "Source")
			fmt.Fprintln(os.Stdout, strings.Repeat("─", 82))
			for _, r := range rows {
				fmt.Fprintf(os.Stdout, "%-45s  %10.4f  %11.4f  %s\n",
					r.Model, r.InputPerMillion, r.OutputPerMillion, r.Source)
			}
			if len(rows) == 0 {
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
	cmd.AddCommand(cmdTrustCalibration(), cmdTrustRecord(), cmdTrustOutcome(), cmdTrustDefect(),
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
			fmt.Printf("\n  Hydra Trust Benchmark, %d trials/case (vs fixed-%d swarm)\n", r.Trials, r.FixedN)
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
			fmt.Printf("  total spend         $%.4f\n", s.TotalCostUSD)
			// "samples saved 47%" reads as a win directly above a number saying
			// nothing was learned. Said plainly rather than left to be inferred.
			if warn := stalledConfidence(s.MeanFinalConf, s.AutoClearedPct); warn != "" {
				fmt.Printf("\n  %s\n", warnStyle.Render(warn))
			}
			fmt.Println()
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
		Short: "Show the LLR ledger for a past SPRT run, why it stopped where it did",
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
				// Every answer stays under test for the whole run, so the losing
				// ones carry real evidence and are worth seeing (#778). Runs
				// logged before that are ledger-only and print nothing here.
				if len(r.Hypotheses) > 0 {
					fmt.Printf("\n  %-40s  %6s  %10s  %s\n", "HYPOTHESIS", "VOTES", "Λ", "CONFIDENCE")
					fmt.Println("  " + strings.Repeat("─", 74))
					for _, h := range r.Hypotheses {
						fmt.Printf("  %-40.40s  %6d  %+10.3f  %9.1f%%\n",
							truncLabel(strings.Join(strings.Fields(h.Answer), " "), 40), h.Votes, h.Lambda, h.Confidence*100)
					}
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
			fmt.Printf("\n  %-28s %-16s %6s %6s %7s %7s %8s\n", "Source", "Domain", "n", "neg", "se", "sp", "D(nats)")
			fmt.Println("  " + strings.Repeat("─", 87))
			pinned := 0
			for _, s := range stats {
				if s.Neg == 0 {
					pinned++
				}
				fmt.Printf("  %-28.28s %-16s %6.0f %6.0f %7.3f %7.3f %8.3f\n",
					s.Source, truncLabel(s.Domain, 16), s.N, s.Neg, s.Se, s.Sp, s.D)
			}
			// With no negative verdicts TN keeps its bare prior, so sp can never
			// rise above 0.5 and LLR(agree) = ln(se/(1-sp)) stays under ln2.
			// More positives never lift it, so such a cell has to be told apart
			// from one that is merely thin.
			if pinned > 0 {
				fmt.Printf("\n  %s\n", warnStyle.Render(fmt.Sprintf(
					"%d of %d cells have no negative verdicts (neg=0): with no measured specificity, sp "+
						"cannot exceed 0.5 and LLR stays under %.3f nats there however many positives "+
						"arrive. Negatives come from ensemble runs whose answer was verified; attach one "+
						"with `hyctl trust outcome`.",
					pinned, len(stats), math.Ln2)))
			}
			// A family whose members have converged on effectively one vote is a
			// coordination risk the se/sp table above cannot show, two "sources"
			// that always agree are one opinion, not confirming evidence.
			coPath := trust.DefaultCoAgreementPath()
			coupling := trust.AllFamilyCoupling(coPath)
			for _, fam := range trust.KnownFamilies(coPath) {
				if r := coupling[fam]; r.Warn {
					fmt.Printf("\n  %s\n", warnStyle.Render(fmt.Sprintf(
						"false-consensus warning: %q's members measured J=%.2f, effectively one vote, not independent confirmation", fam, r.J)))
				}
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
			if domain == "" {
				return fmt.Errorf("--domain is required")
			}
			cal, err := trust.New(trust.DefaultPath())
			if err != nil {
				return err
			}
			if reason, bad := trust.UnreadableSourceKey(source); bad {
				fmt.Printf("  %s\n", warnStyle.Render(fmt.Sprintf(
					"source %q: %s. `hyctl probe` lists the IDs the router uses.", source, reason)))
			}
			if err := cal.Update(source, domain, saidCorrect, o); err != nil {
				return err
			}
			fmt.Printf("  recorded: %s/%s said_correct=%v outcome=%s → D=%.3f nats\n",
				source, domain, saidCorrect, outcome, cal.D(source, domain))
			return nil
		},
	}
	cmd.Flags().StringVar(&source, "source", "", "evidence source: a head ID from `hyctl probe` (e.g. ollama/qwen3:4b), or verifier:<name>")
	cmd.Flags().StringVar(&domain, "domain", "", "task domain")
	cmd.Flags().BoolVar(&saidCorrect, "said-correct", false, "the source's raw verdict")
	cmd.Flags().StringVar(&outcome, "outcome", "", "ground truth: correct|incorrect")
	return cmd
}

func cmdTrustOutcome() *cobra.Command {
	var outcome string
	cmd := &cobra.Command{
		Use:   "outcome <task_hash>",
		Short: "Attach ground truth to a past SPRT run, training every source that voted in it",
		Long: "Replays a logged run's LLR ledger into calibration now that the answer is known.\n" +
			"Unlike `hyctl trust record`, which can only ever say a source was right or wrong about\n" +
			"its own answer, this records the dissenters too, and a source that disagreed with a\n" +
			"candidate later verified incorrect is the only observation that can move specificity.",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			o := trust.ParseOutcome(outcome)
			if o == trust.OutcomeUnknown {
				return fmt.Errorf("--outcome must be correct|incorrect (got %q)", outcome)
			}
			runs, err := trust.LoadRuns(trust.DefaultLogPath())
			if err != nil {
				return err
			}
			for i := len(runs) - 1; i >= 0; i-- { // newest match first
				r := runs[i]
				if r.TaskHash != args[0] {
					continue
				}
				if len(r.Ledger) == 0 {
					return fmt.Errorf("run %s has no ledger, nothing to train from", r.TaskHash)
				}
				cal, err := trust.New(trust.DefaultPath())
				if err != nil {
					return err
				}
				n, err := trust.ApplyRunOutcome(cal, r.Domain, r.Ledger, o)
				if err != nil {
					return err
				}
				fmt.Printf("\n  run %s  ·  domain %s  ·  outcome %s\n", r.TaskHash, r.Domain, outcome)
				fmt.Printf("  recorded %d of %d ledger entries\n\n", n, len(r.Ledger))
				fmt.Printf("  %-28s %-9s %7s %7s %8s\n", "SOURCE", "VERDICT", "se", "sp", "D(nats)")
				fmt.Println("  " + strings.Repeat("─", 64))
				for _, e := range r.Ledger {
					verdict := "agree"
					if !e.Agreed {
						verdict = "disagree"
					}
					st := calibFor(cal, e.Source, r.Domain)
					fmt.Printf("  %-28.28s %-9s %7.3f %7.3f %8.3f\n", e.Source, verdict, st.Se, st.Sp, st.D)
				}
				fmt.Println()
				return nil
			}
			return fmt.Errorf("no run found with task_hash %q", args[0])
		},
	}
	cmd.Flags().StringVar(&outcome, "outcome", "", "ground truth for the run's answer: correct|incorrect")
	_ = cmd.MarkFlagRequired("outcome")
	return cmd
}

// calibFor picks one cell out of a Report, so the outcome command can show what
// each voter's rates became without a second Calibrator accessor.
func calibFor(cal *trust.Calibrator, source, domain string) trust.Stat {
	for _, s := range cal.Report() {
		if s.Source == source && s.Domain == domain {
			return s
		}
	}
	return trust.Stat{Source: source, Domain: domain}
}

func cmdTrustDefect() *cobra.Command {
	var domain, file, graphPath string
	var blast float64
	var irreversible, pii, production, jsonOut bool
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
				// A miss here still yields the 1.0 default, indistinguishable
				// from a genuinely-measured low-risk file unless the source
				// tag says so (#448).
				if g.Empty() || !g.Knows(file) {
					blastSource = "graph:miss"
				} else {
					blastSource = "graph"
				}
			}
			// NaN/+Inf would otherwise render as a nonsensical "$NaN" / "NaN%"
			// recommendation in text mode and crash json.Encode outright
			// ("json: unsupported value: NaN") in --json mode (#501).
			if math.IsNaN(blast) || math.IsInf(blast, 0) {
				return fmt.Errorf("--blast must be a finite number, got %v", blast)
			}

			task := trust.Task{
				Domain:       domain,
				BlastRadius:  blast,
				Irreversible: irreversible,
				TouchesPII:   pii,
				Production:   production,
			}
			// CostUSD/RequiredConfidence clamp BlastRadius<=0 to 1.0 internally;
			// display that same clamped value rather than the raw input, or a
			// `--blast 0` run shows "blast=0.00" next to a cost/confidence that
			// was actually computed at 1.0 (#501).
			usedBlast := trust.NormalizeBlastRadius(blast)
			cost := dm.CostUSD(task)
			conf := dm.RequiredConfidence(task)

			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(map[string]any{
					"domain": domain, "blast_radius": usedBlast, "blast_source": blastSource,
					"irreversible": irreversible, "pii": pii, "production": production,
					"defect_cost_usd": cost, "required_confidence": conf,
				})
			}
			fmt.Printf("  defect cost ≈ $%.2f  (blast=%.2f [%s] irreversible=%v pii=%v prod=%v)\n",
				cost, usedBlast, blastSource, irreversible, pii, production)
			fmt.Printf("  → demands confidence ≥ %.1f%%  (use with: hyctl dispatch --confidence %.3f)\n",
				conf*100, conf)
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
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable JSON output")
	return cmd
}

// ── styles ────────────────────────────────────────────────────────────────────

var (
	cortexStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	warnStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	okStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42"))
)

// promptPreview shortens a prompt for a log Detail field. Run events carry a
// short human label, never the full text, the atomic-append guarantee that
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

// ── ask ───────────────────────────────────────────────────────────────────────

// cmdAsk answers the tasks Hydra parked waiting on a human.
//
// The desktop app grows its own surface for these (#583), but hyctl is the
// whole interface for anyone not running the app, without this a policy that
// asks can park a task nothing is able to resume.
func cmdAsk() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ask",
		Short: "See and answer tasks waiting on your decision",
		Long: "A ledger policy can answer `ask` instead of allow or deny. Dispatch then stops\n" +
			"before running anything and parks the task until you answer it here.",
	}

	var jsonOut bool
	list := &cobra.Command{
		Use:   "list",
		Short: "List tasks waiting on an answer",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			qs, err := pending.List()
			// Reported, not returned: an unreadable file must not hide the
			// questions that are readable, or a parked task gets forgotten
			// because a different one is corrupt.
			if err != nil {
				fmt.Fprintf(os.Stderr, "  %s %v\n", warnStyle.Render("⚠"), err)
			}
			if jsonOut {
				if qs == nil {
					qs = []pending.Question{}
				}
				return json.NewEncoder(os.Stdout).Encode(qs)
			}
			if len(qs) == 0 {
				fmt.Printf("\n  %s\n\n", dimStyle.Render("nothing is waiting on you"))
				return nil
			}
			fmt.Println()
			for _, q := range qs {
				fmt.Printf("  %s  %s  %s\n",
					cortexStyle.Render(q.TaskID),
					dimStyle.Render(q.Head),
					dimStyle.Render("asked "+humanAge(time.Since(q.AskedAt))))
				fmt.Printf("    %s\n\n", q.Question)
			}
			fmt.Printf("  %s\n\n", dimStyle.Render("answer with: hyctl ask answer <task-id> \"...\""))
			return nil
		},
	}
	list.Flags().BoolVar(&jsonOut, "json", false, "machine-readable JSON output")

	answer := &cobra.Command{
		Use:   "answer <task-id> <answer>",
		Short: "Answer a parked task and run it",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := dispatch.New(cmd.Context())
			if err != nil {
				return err
			}
			res, err := d.Resume(cmd.Context(), args[0], args[1])
			if err != nil {
				return err
			}
			fmt.Println()
			fmt.Printf("  %s %s  %dms\n",
				cortexStyle.Render("▶"),
				dimStyle.Render(res.Head.Name),
				res.Duration.Milliseconds())
			fmt.Println(dimStyle.Render("  " + strings.Repeat("─", 56)))
			fmt.Printf("\n%s\n\n", res.Output)
			return nil
		},
	}

	decline := &cobra.Command{
		Use:   "decline <task-id> [reason]",
		Short: "Refuse a parked task without running it",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(_ *cobra.Command, args []string) error {
			reason := ""
			if len(args) == 2 {
				reason = args[1]
			}
			if err := dispatch.Decline(args[0], reason); err != nil {
				return err
			}
			fmt.Printf("\n  %s %s\n\n", dimStyle.Render("declined"), args[0])
			return nil
		},
	}

	cmd.AddCommand(list, answer, decline)
	return cmd
}

// humanAge renders a duration the way a person reads an age, not a Go Duration.
func humanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
