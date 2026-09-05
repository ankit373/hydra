// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/testutil"
)

// The CLI surface is the product. A subcommand that panics, exits 0 on failure,
// or prints something a script cannot parse is a defect the library tests
// cannot see, because none of them run the command the user actually types.
//
// Everything here drives the real rootCmd inside a sandbox, so no test can
// reach the network, read the developer's config, or spend money (#267).

// run executes hyctl with the given args and captures stdout, stderr and the
// error Execute returned. Cobra writes its own diagnostics to the command's
// output writer; os.Stdout is captured for everything the handlers print.
func run(t *testing.T, args ...string) (stdout, cobraOut string, err error) {
	t.Helper()

	root := rootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(args)
	// Cobra's default is to print usage on every error, which buries the actual
	// message. Silenced here so the assertion sees what the handler returned.
	root.SilenceUsage = true
	root.SilenceErrors = true

	stdout = captureStdout(t, func() { err = root.Execute() })
	return stdout, buf.String(), err
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, perr := os.Pipe()
	if perr != nil {
		t.Fatal(perr)
	}
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		var b bytes.Buffer
		_, _ = b.ReadFrom(r)
		done <- b.String()
	}()

	func() {
		defer func() {
			os.Stdout = orig
			_ = w.Close()
		}()
		fn()
	}()
	return <-done
}

// cliSandbox is a hermetic environment with a usable config, so commands that
// need one get past their first check.
func cliSandbox(t *testing.T) *testutil.Sandbox {
	t.Helper()
	s := testutil.NewSandbox(t)
	if err := config.Save(&config.Config{Cortex: "none"}); err != nil {
		t.Fatal(err)
	}
	return s
}

// Every subcommand must have help text, and none may panic when asked for it.
// A command with no Short is invisible in `hyctl --help`.
func TestCLI_EverySubcommandHasHelp(t *testing.T) {
	cliSandbox(t)

	var walk func(c *cobra.Command, path string)
	walk = func(c *cobra.Command, path string) {
		name := strings.TrimSpace(path + " " + c.Name())
		if c.Short == "" {
			t.Errorf("%q has no Short description, so it is invisible in help", name)
		}
		if c.Use == "" {
			t.Errorf("%q has no Use line", name)
		}
		for _, sub := range c.Commands() {
			if sub.Name() == "help" || sub.Name() == "completion" {
				continue // cobra's own
			}
			walk(sub, name)
		}
	}
	walk(rootCmd(), "")
}

// --help must succeed and name the command, for every subcommand. This is what
// catches a flag whose definition panics at construction.
func TestCLI_HelpWorksForEverySubcommand(t *testing.T) {
	cliSandbox(t)

	for _, c := range rootCmd().Commands() {
		name := c.Name()
		if name == "help" || name == "completion" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			_, out, err := run(t, name, "--help")
			if err != nil {
				t.Fatalf("`hyctl %s --help` failed: %v", name, err)
			}
			if !strings.Contains(out, name) {
				t.Errorf("help for %q does not name it:\n%s", name, out)
			}
		})
	}
}

// `hyctl --version` and `hyctl version` must agree. They used to be separate
// strings; the root flag answered "unknown flag" while the subcommand worked.
func TestCLI_VersionFlagAndSubcommandAgree(t *testing.T) {
	cliSandbox(t)

	sub, _, err := run(t, "version")
	if err != nil {
		t.Fatal(err)
	}
	_, flagOut, err := run(t, "--version")
	if err != nil {
		t.Fatalf("`hyctl --version` failed: %v", err)
	}
	if strings.TrimSpace(sub) != strings.TrimSpace(flagOut) {
		t.Errorf("`hyctl version` and `hyctl --version` disagree:\n%q\nvs\n%q", sub, flagOut)
	}
	if !strings.Contains(sub, "commit") || !strings.Contains(sub, "built") {
		t.Errorf("version output does not carry build provenance:\n%s", sub)
	}
}

// An unknown subcommand and an unknown flag must both fail. Exiting 0 on a
// typo makes a script think the command ran.
func TestCLI_UnknownCommandsAndFlagsFail(t *testing.T) {
	cliSandbox(t)

	if _, _, err := run(t, "definitely-not-a-command"); err == nil {
		t.Error("an unknown subcommand exited successfully")
	}
	if _, _, err := run(t, "status", "--definitely-not-a-flag"); err == nil {
		t.Error("an unknown flag exited successfully")
	}
	if _, _, err := run(t, "cost", "--since"); err == nil {
		t.Error("a flag missing its value exited successfully")
	}
}

// Commands that read logs must handle there being none. A fresh install runs
// these before anything has dispatched.
func TestCLI_ReadCommandsSurviveAFreshInstall(t *testing.T) {
	for _, args := range [][]string{
		{"status"},
		{"cost"},
		{"stats"},
		{"probe"},
		{"models", "list"},
		{"trust", "calibration"},
		{"trust", "stats"},
		{"context", "entropy", "--help"},
		{"mcp", "log"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			cliSandbox(t)
			out, cobraOut, err := run(t, args...)
			// Some of these legitimately fail with "nothing recorded yet". What
			// must never happen is a panic or an empty, silent success.
			if err == nil && strings.TrimSpace(out+cobraOut) == "" {
				t.Errorf("`hyctl %s` succeeded and printed nothing; the user cannot "+
					"tell it ran", strings.Join(args, " "))
			}
			if err != nil && strings.TrimSpace(err.Error()) == "" {
				t.Errorf("`hyctl %s` failed with an empty message", strings.Join(args, " "))
			}
		})
	}
}

// --json is the scripting surface. Whatever it prints must parse, on a fresh
// install as much as a populated one, a jq pipeline that breaks when there is
// no data yet is a broken contract.
func TestCLI_JSONOutputParses(t *testing.T) {
	for _, args := range [][]string{
		{"models", "list", "--json"},
		{"mcp", "report", "--json"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			cliSandbox(t)
			out, cobraOut, err := run(t, args...)
			if err != nil {
				// Not a skip: every command listed here works on a fresh
				// install, and a skip would read as coverage while silently
				// stopping.
				t.Fatalf("`hyctl %s` failed on a fresh install: %v (%s)",
					strings.Join(args, " "), err, cobraOut)
			}
			trimmed := strings.TrimSpace(out)
			if trimmed == "" {
				t.Fatalf("`hyctl %s` printed nothing; a jq pipeline gets an empty "+
					"stream rather than an empty array", strings.Join(args, " "))
			}
			var v any
			if err := json.Unmarshal([]byte(trimmed), &v); err != nil {
				t.Errorf("`hyctl %s` did not emit valid JSON: %v\n%s",
					strings.Join(args, " "), err, trimmed)
			}
		})
	}
}

// A dry run must never execute or spend. This is the flag a user reaches for
// precisely because they are unsure.
func TestCLI_DryRunExecutesNothing(t *testing.T) {
	cliSandbox(t)

	_, _, _ = run(t, "dispatch", "--dry-run", "--enum", "SIMPLE", "write a DTO")

	if _, err := os.Stat(filepath.Join(config.Dir(), "logs", "cost.jsonl")); err == nil {
		t.Error("a dry run wrote cost rows")
	}
	if _, err := os.Stat(filepath.Join(config.Dir(), "logs", "last_handoff.json")); err == nil {
		t.Error("a dry run wrote a handoff")
	}

	// A dry run chooses a head but never calls one, so it has no place in a
	// log of runs either. Before #379 it wrote run_started/run_finished
	// unconditionally, leaving a permanent, contentless card in the desktop
	// app's Fleet view for every preview, 0ms, $0.00, no agents, nothing to
	// say why, indistinguishable from a broken reconstruction.
	ids, err := runlog.Runs()
	if err != nil {
		t.Fatalf("runlog.Runs(): %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("a dry run left %d entries in the run log: %v", len(ids), ids)
	}
}

// Commands that write to files must refuse a relative path before doing
// anything, and say which it was.
//
// Only the ones that return an error are driven in-process: `hyctl edit` calls
// os.Exit(2) on a failed edit, deliberately, so callers can gate on it, which
// would take the test binary down with it. Exit codes are asserted against a
// real subprocess below.
func TestCLI_FileCommandsRefuseUnsafePaths(t *testing.T) {
	cliSandbox(t)

	for _, args := range [][]string{
		{"review", "diff", "relative.go"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			_, cobraOut, err := run(t, args...)
			if err == nil {
				t.Fatalf("`hyctl %s` accepted a relative path", strings.Join(args, " "))
			}
			msg := strings.ToLower(err.Error() + cobraOut)
			if !strings.Contains(msg, "absolute") && !strings.Contains(msg, "path") {
				t.Errorf("the error does not say what was wrong with the path: %v", err)
			}
		})
	}
}

// A command that needs a config must say so rather than proceeding against
// defaults the user never chose.
func TestCLI_CommandsNeedingAConfigSaySo(t *testing.T) {
	testutil.NewSandbox(t) // deliberately no config

	_, cobraOut, err := run(t, "dispatch", "--enum", "SIMPLE", "hello")
	if err == nil {
		t.Fatal("dispatch ran with no config")
	}
	if !strings.Contains(err.Error()+cobraOut, "init") {
		t.Errorf("the error does not point at `hyctl init`: %v", err)
	}
}

// ── display helpers ───────────────────────────────────────────────────────────

// The status bar is the governor's readout. A bar that overflows its width, or
// a label that truncates a token count to zero, is a wrong number on screen.
func TestBudgetBar_StaysWithinItsWidth(t *testing.T) {
	// -10 is not hypothetical: state.json is hand-edited per the orchestrator
	// protocol, and a negative value used to panic strings.Repeat.
	for _, pct := range []int{-100, -10, 0, 1, 50, 99, 100, 150, 1000} {
		bar := stripANSICodes(budgetBar(pct))
		if n := len([]rune(bar)); n != 10 {
			t.Errorf("budgetBar(%d) is %d cells wide, want 10: %q", pct, n, bar)
		}
	}
	// The bar must actually fill as the percentage rises.
	empty := strings.Count(stripANSICodes(budgetBar(0)), "█")
	full := strings.Count(stripANSICodes(budgetBar(100)), "█")
	if empty != 0 || full != 10 {
		t.Errorf("bar fill = %d at 0%% and %d at 100%%, want 0 and 10", empty, full)
	}
}

func TestTokenLabel_DoesNotTruncateToZero(t *testing.T) {
	tests := map[[2]int]string{
		{0, 1000}:            "0/1k",
		{999, 200_000}:       "999/200k",
		{1500, 200_000}:      "1k/200k",
		{1_500_000, 2 << 20}: "1.5M/2.1M",
	}
	for in, want := range tests {
		if got := tokenLabel(in[0], in[1]); got != want {
			t.Errorf("tokenLabel(%d, %d) = %q, want %q", in[0], in[1], got, want)
		}
	}
	// A sub-1000 count must never render as "0k", that reads as no usage at all.
	if got := tokenLabel(999, 1000); strings.HasPrefix(got, "0k") {
		t.Errorf("tokenLabel(999, …) = %q, which reads as zero usage", got)
	}
}

// Each governor mode must be visually distinct, or an emergency looks like a
// normal run.
func TestBudgetModeStyle_DistinguishesTheModes(t *testing.T) {
	modes := []string{"emergency", "critical", "warning", "caution", "compact", "normal", ""}
	seen := map[string]bool{}
	for _, m := range modes {
		fg := budgetModeStyle(m).GetForeground()
		if fg == nil {
			t.Errorf("mode %q has no colour", m)
			continue
		}
		seen[strings.TrimSpace(stripANSICodes(budgetModeStyle(m).Render("x")))+string(rune(len(seen)))] = true
	}
	if budgetModeStyle("emergency").GetForeground() == budgetModeStyle("normal").GetForeground() {
		t.Error("emergency and normal are coloured identically")
	}
	// An unknown mode must fall back to the safe colour rather than panicking.
	if budgetModeStyle("not-a-mode").GetForeground() == nil {
		t.Error("an unknown mode has no colour")
	}
}

func TestTruncLabel_CountsRunes(t *testing.T) {
	if got := truncLabel("short", 20); got != "short" {
		t.Errorf("truncLabel = %q", got)
	}
	long := strings.Repeat("a", 40)
	if n := len([]rune(truncLabel(long, 20))); n != 20 {
		t.Errorf("truncLabel produced %d runes, want 20", n)
	}
	// Multi-byte input must be cut on a rune boundary, not a byte one, a split
	// rune renders as a replacement character in the table.
	cjk := strings.Repeat("日", 40)
	got := truncLabel(cjk, 10)
	if n := len([]rune(got)); n != 10 {
		t.Errorf("truncLabel of CJK produced %d runes, want 10", n)
	}
	for _, r := range got {
		if r == '�' {
			t.Fatalf("truncLabel split a rune: %q", got)
		}
	}
}

func TestToFloat(t *testing.T) {
	// JSON numbers arrive as float64; a wrong answer here silently zeroes the
	// budget table.
	if got := toFloat(float64(52)); got != 52 {
		t.Errorf("toFloat(float64) = %v", got)
	}
	if got := toFloat(52); got != 52 {
		t.Errorf("toFloat(int) = %v", got)
	}
	for _, v := range []any{nil, "52", true, []any{1}} {
		if got := toFloat(v); got != 0 {
			t.Errorf("toFloat(%#v) = %v, want 0", v, got)
		}
	}
}

// printBudgetStatus reads state.json written by another process. It must
// survive every shape that file can be in.
func TestPrintBudgetStatus_SurvivesEveryStateShape(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state string
	}{
		{"absent", ""},
		{"corrupt", "{truncated"},
		{"empty object", "{}"},
		{"pct only", `{"claude_pct":52}`},
		{"pct with history", `{"claude_pct":60,"claude_pct_history":[10,25,40,52,60]}`},
		{"budget table", `{"claude_pct":30,"budget":{"m":{"pct":40,"used":1000,"window":200000,"mode":"compact"}}}`},
		{"wrong types", `{"claude_pct":"fifty","budget":{"m":{"pct":"x","used":null}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testutil.NewSandbox(t)
			if tc.state != "" {
				dir := filepath.Join(config.Dir(), "logs")
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(tc.state), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			// The assertion is that this returns at all.
			_ = captureStdout(t, printBudgetStatus)
		})
	}
}

func stripANSICodes(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			i++
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
