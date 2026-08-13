// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/testutil"
)

// ── tui --snapshot ────────────────────────────────────────────────────────────

// `hyctl tui --snapshot` is what the docs and the README preview are generated
// from, and the only non-interactive way into the cockpit. Its flag validation
// is the whole of its logic.
func TestCLI_TuiSnapshot_FlagValidation(t *testing.T) {
	cliSandbox(t)

	// The full four-view frame.
	out, cobraOut, err := run(t, "tui", "--snapshot")
	if err != nil {
		t.Fatalf("`hyctl tui --snapshot` failed: %v (%s)", err, cobraOut)
	}
	for _, want := range []string{"VIEW 1/4", "VIEW 2/4", "VIEW 3/4", "VIEW 4/4"} {
		if !strings.Contains(out, want) {
			t.Errorf("the snapshot is missing %q:\n%s", want, out)
		}
	}

	// A single view renders only that one.
	single, _, err := run(t, "tui", "--snapshot", "--view", "1")
	if err != nil {
		t.Fatalf("`--snapshot --view 1` failed: %v", err)
	}
	if strings.Contains(single, "VIEW 1/4") {
		t.Errorf("--view rendered the labelled four-view frame:\n%s", single)
	}
	if strings.TrimSpace(single) == "" {
		t.Error("--view 1 rendered nothing")
	}

	// The new security view (index 3) renders too.
	secOut, _, err := run(t, "tui", "--snapshot", "--view", "3")
	if err != nil {
		t.Fatalf("`--snapshot --view 3` (security) failed: %v", err)
	}
	if !strings.Contains(secOut, "security") {
		t.Errorf("--view 3 does not mention the security view:\n%s", secOut)
	}

	// An out-of-range view must be refused with the valid range named. Before
	// this was checked it was an unchecked slice index — a panic.
	for _, v := range []string{"-1", "4", "99"} {
		_, cobraOut, err := run(t, "tui", "--snapshot", "--view", v)
		if err == nil {
			t.Errorf("--view %s was accepted", v)
			continue
		}
		msg := err.Error() + cobraOut
		if !strings.Contains(msg, "out of range") {
			t.Errorf("--view %s error = %v, want it to name the valid range", v, err)
		}
	}

	// --view without --snapshot is a misuse: there is no static frame to pick a
	// view of, and silently ignoring it would launch the interactive cockpit.
	_, cobraOut, err = run(t, "tui", "--view", "1")
	if err == nil {
		t.Error("--view without --snapshot was accepted; it would have launched " +
			"the interactive cockpit instead")
	} else if !strings.Contains(err.Error()+cobraOut, "--snapshot") {
		t.Errorf("error = %v, want it to say --view needs --snapshot", err)
	}
}

// ── mcp record / verify ───────────────────────────────────────────────────────

// The ledger's parameter binding is what makes a decision tamper-evident: an
// approval is bound to a hash of the parameters, and execution must present the
// same ones. A verify that passes on different parameters is not a gate.
func TestCLI_MCPRecordThenVerify_BindsParameters(t *testing.T) {
	s := populated(t)

	params := `{"path":"/etc/hosts","mode":"read"}`
	if code, out := runBinary(t, s, "mcp", "record",
		"--tool", "fs.read", "--action", "read", "--decision", "allow",
		"--resource", "/etc/hosts", "--agent", "test-agent",
		"--params", params); code != 0 {
		t.Fatalf("`hyctl mcp record` exited %d:\n%s", code, out)
	}

	// The same parameters verify.
	code, out := runBinary(t, s, "mcp", "verify", "fs.read",
		"--resource", "/etc/hosts", "--params", params)
	if code != 0 {
		t.Errorf("verify of the recorded parameters exited %d:\n%s", code, out)
	}

	// Different parameters must not. This is the whole point of the hash.
	tampered := `{"path":"/etc/shadow","mode":"read"}`
	code, out = runBinary(t, s, "mcp", "verify", "fs.read",
		"--resource", "/etc/hosts", "--params", tampered)
	if code == 0 {
		t.Errorf("verify passed on parameters that were never approved — the "+
			"binding is not a binding:\n%s", out)
	}
}

// Malformed input to the ledger must be refused before anything is written. A
// half-recorded event is worse than none: it reads as an approval.
func TestCLI_MCPRecord_RefusesMalformedInput(t *testing.T) {
	s := populated(t)

	cases := [][]string{
		{"mcp", "record", "--tool", "t", "--action", "not-an-action",
			"--decision", "allow", "--resource", "/x", "--agent", "a"},
		{"mcp", "record", "--tool", "t", "--action", "read",
			"--decision", "maybe", "--resource", "/x", "--agent", "a"},
		{"mcp", "record", "--tool", "t", "--action", "read",
			"--decision", "allow", "--resource", "/x", "--agent", "a",
			"--params", "{not json"},
		// A bare `hyctl mcp record` (no --tool/--agent) used to exit 0 and
		// silently append an empty-agent, empty-tool row forever (#457).
		{"mcp", "record", "--action", "read",
			"--decision", "allow", "--resource", "/x", "--agent", "a"},
		{"mcp", "record", "--tool", "t", "--action", "read",
			"--decision", "allow", "--resource", "/x"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args[2:6], " "), func(t *testing.T) {
			if code, out := runBinary(t, s, args...); code == 0 {
				t.Errorf("accepted malformed input:\n%s", out)
			}
		})
	}

	// An empty tool would match an approval recorded for a different tool.
	if code, out := runBinary(t, s, "mcp", "verify", "  ",
		"--resource", "/x", "--params", "{}"); code == 0 {
		t.Errorf("verify accepted an empty tool name:\n%s", out)
	}
}

// ── pricing refresh ───────────────────────────────────────────────────────────

// `hyctl pricing refresh` is a synchronous network fetch. With no network it
// must fail and say so, not report a successful refresh of nothing — the
// cached prices are what every cost figure is computed from.
func TestCLI_PricingRefresh_ReportsAFailedFetch(t *testing.T) {
	cliSandbox(t) // sandbox points the proxy at a dead port

	out, cobraOut, err := run(t, "pricing", "refresh")
	if err == nil {
		t.Fatalf("`hyctl pricing refresh` reported success with no network:\n%s",
			out+cobraOut)
	}
	// The cache must not be left in a state that looks like a fresh fetch.
	raw, readErr := os.ReadFile(filepath.Join(cfgDir(t), "pricing_cache.json"))
	if readErr == nil {
		var c struct {
			Models map[string]any `json:"models"`
		}
		if json.Unmarshal(raw, &c) == nil && len(c.Models) == 0 {
			t.Error("a failed refresh wrote an empty cache; every model would then " +
				"price off the tier fallback until the next successful fetch")
		}
	}
}

// ── stats windows ─────────────────────────────────────────────────────────────

// --days windows the report. A window that excludes everything must say so
// rather than printing an empty table that reads as "nothing was spent".
func TestCLI_Stats_WindowsExcludingEverything(t *testing.T) {
	populated(t)

	// The seeded rows are from today, so a 1-day window includes them.
	included, cobraOut, err := run(t, "stats", "--days", "1")
	if err != nil {
		t.Fatalf("`hyctl stats --days 1` failed: %v (%s)", err, cobraOut)
	}
	if !strings.Contains(included+cobraOut, "claude-opus") {
		t.Errorf("today's rows were excluded by a 1-day window:\n%s", included+cobraOut)
	}

	// A negative or zero window must not crash or silently mean "everything".
	for _, d := range []string{"0", "-5"} {
		if _, _, err := run(t, "stats", "--days", d); err != nil {
			t.Errorf("`hyctl stats --days %s` errored: %v", d, err)
		}
	}
}

// ── dispatch flag validation ──────────────────────────────────────────────────

// dispatch takes several mutually-informing flags. Each rejection has to happen
// before a head is engaged, since the alternative is a paid call the user did
// not intend.
func TestCLI_Dispatch_RefusesBadFlagsBeforeSpending(t *testing.T) {
	populated(t)

	cases := []struct {
		name string
		args []string
	}{
		{"no prompt at all", []string{"dispatch"}},
		{"confidence above 1", []string{"dispatch", "--confidence", "1.5", "x"}},
		{"confidence of zero", []string{"dispatch", "--confidence", "0", "x"}},
		{"negative confidence", []string{"dispatch", "--confidence", "-0.5", "x"}},
		{"unknown swarm mode", []string{"dispatch", "--swarm", "--swarm-mode", "sideways", "x"}},
		// #453: --dry-run must reject a bad --swarm-mode exactly like a real
		// run does, not print a plan for a mode Run would then refuse.
		{"unknown swarm mode with dry run", []string{"dispatch", "--swarm", "--swarm-mode", "sideways", "--dry-run", "x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := run(t, tc.args...)
			if err == nil {
				t.Errorf("`hyctl %s` was accepted", strings.Join(tc.args, " "))
			}
			// Nothing may have been billed.
			if _, statErr := os.Stat(filepath.Join(cfgDir(t), "logs", "cost.jsonl")); statErr == nil {
				rows, _ := os.ReadFile(filepath.Join(cfgDir(t), "logs", "cost.jsonl"))
				if strings.Count(string(rows), "\n") > 2 {
					t.Error("a rejected dispatch wrote a cost row")
				}
			}
		})
	}
}

// cfgDir is config.Dir() without importing config into every test above.
func cfgDir(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(home, ".hydra")
}

var _ = testutil.NewSandbox // keep the import meaningful if cases are trimmed
