// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/ledger"
	"github.com/ankit373/hydra/internal/testutil"
)

// ── tui --snapshot ────────────────────────────────────────────────────────────

// `hyctl tui --snapshot` is what the docs and the README preview are generated
// from, and the only non-interactive way into the cockpit. Its flag validation
// is the whole of its logic.
func TestCLI_TuiSnapshot_FlagValidation(t *testing.T) {
	cliSandbox(t)

	// The full six-view frame, plus the shortcut glossary.
	out, cobraOut, err := run(t, "tui", "--snapshot")
	if err != nil {
		t.Fatalf("`hyctl tui --snapshot` failed: %v (%s)", err, cobraOut)
	}
	for _, want := range []string{"VIEW 1/6", "VIEW 2/6", "VIEW 3/6", "VIEW 4/6", "VIEW 5/6", "VIEW 6/6", "GLOSSARY"} {
		if !strings.Contains(out, want) {
			t.Errorf("the snapshot is missing %q:\n%s", want, out)
		}
	}

	// A single view renders only that one.
	single, _, err := run(t, "tui", "--snapshot", "--view", "1")
	if err != nil {
		t.Fatalf("`--snapshot --view 1` failed: %v", err)
	}
	if strings.Contains(single, "VIEW 1/6") {
		t.Errorf("--view rendered the labelled six-view frame:\n%s", single)
	}
	if strings.TrimSpace(single) == "" {
		t.Error("--view 1 rendered nothing")
	}

	// The activity view (index 3) renders too.
	actOut, _, err := run(t, "tui", "--snapshot", "--view", "3")
	if err != nil {
		t.Fatalf("`--snapshot --view 3` (activity) failed: %v", err)
	}
	if !strings.Contains(actOut, "activity") {
		t.Errorf("--view 3 does not mention the activity view:\n%s", actOut)
	}

	// An out-of-range view must be refused with the valid range named. Before
	// this was checked it was an unchecked slice index, a panic.
	for _, v := range []string{"-1", "6", "99"} {
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
		t.Errorf("verify passed on parameters that were never approved, the "+
			"binding is not a binding:\n%s", out)
	}
}

// `mcp verify` used to trust the on-disk parameters_hash verbatim: hand-edit
// it to the hash of parameters that were never actually approved, and it
// reported MATCH, only `mcp verify-chain` caught the tamper, and the two
// were never composed. Recomputing/validating against the hash chain here
// closes that (#500).
func TestCLI_MCPVerify_DetectsTamperedParametersHash(t *testing.T) {
	s := populated(t)

	approved := `{"path":"/etc/hosts","mode":"read"}`
	if code, out := runBinary(t, s, "mcp", "record",
		"--tool", "fs.read", "--action", "read", "--decision", "allow",
		"--resource", "/etc/hosts", "--agent", "test-agent",
		"--params", approved); code != 0 {
		t.Fatalf("`hyctl mcp record` exited %d:\n%s", code, out)
	}

	// Simulate an attacker hand-editing the ledger line: retarget
	// parameters_hash to the hash of parameters that were never approved,
	// without touching the event's own chain hash, exactly what a raw text
	// edit (not a re-recording) would do.
	malicious := map[string]any{"path": "/etc/shadow", "mode": "write"}
	maliciousHash, err := ledger.HashParams(malicious)
	if err != nil {
		t.Fatal(err)
	}
	ledgerPath := filepath.Join(s.HydraHome, "mcp_ledger.jsonl")
	raw, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	var evt map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &evt); err != nil {
		t.Fatal(err)
	}
	evt["parameters_hash"] = maliciousHash
	tamperedLine, err := json.Marshal(evt)
	if err != nil {
		t.Fatal(err)
	}
	lines[len(lines)-1] = string(tamperedLine)
	if err := os.WriteFile(ledgerPath, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Before #500 this reported MATCH: the malicious parameters hash exactly
	// the (tampered) field on disk, and nothing checked the field itself was
	// ever genuine.
	maliciousParams := `{"path":"/etc/shadow","mode":"write"}`
	code, out := runBinary(t, s, "mcp", "verify", "fs.read",
		"--resource", "/etc/hosts", "--params", maliciousParams)
	if code == 0 {
		t.Errorf("verify MATCHed a hand-edited parameters_hash instead of detecting the broken chain:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(out), "chain") {
		t.Errorf("verify output does not explain the chain-tamper cause:\n%s", out)
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
// must fail and say so, not report a successful refresh of nothing, the
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
		{"NaN confidence", []string{"dispatch", "--confidence", "NaN", "x"}},
		{"confidence above 1 in dry-run", []string{"dispatch", "--dry-run", "--confidence", "1.5", "x"}},
		{"+Inf confidence in dry-run", []string{"dispatch", "--dry-run", "--confidence", "+Inf", "x"}},
		{"unknown swarm mode", []string{"dispatch", "--swarm", "--swarm-mode", "sideways", "x"}},
		// #453: --dry-run must reject a bad --swarm-mode exactly like a real
		// run does, not print a plan for a mode Run would then refuse.
		{"unknown swarm mode with dry run", []string{"dispatch", "--swarm", "--swarm-mode", "sideways", "--dry-run", "x"}},
		{"unknown tier name", []string{"dispatch", "--tier", "expert", "x"}},
		{"tier zero", []string{"dispatch", "--tier", "0", "x"}},
		{"negative tier", []string{"dispatch", "--tier", "-1", "x"}},
		{"tier above max", []string{"dispatch", "--tier", "11", "x"}},
		{"unrecognized enum", []string{"dispatch", "--enum", "NOTAREALENUM", "x"}},
		{"swarm, numeric tier out of range", []string{"dispatch", "--swarm", "--tier", "99", "x"}},
		{"swarm, unknown named tier", []string{"dispatch", "--swarm", "--tier", "not-a-real-tier", "x"}},
		{"confidence, numeric tier out of range", []string{"dispatch", "--confidence", "0.9", "--tier", "99", "x"}},
		{"swarm, bad judge tier", []string{"dispatch", "--swarm", "--swarm-judge-tier", "99", "x"}},
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

// A named --tier absent from config (#451) and an out-of-range numeric --tier
// (#454) are both config/input problems, not routability problems. Each must
// say so distinctly, naming what was actually wrong, rather than falling
// through to the generic "no routable heads" message that blames the head pool.
func TestCLI_Dispatch_TierErrorsNameTheActualProblem(t *testing.T) {
	populated(t)

	cases := []struct {
		name   string
		tier   string
		wantIn []string
	}{
		{"unknown named tier", "expert", []string{"expert"}},
		{"tier zero", "0", []string{"0"}},
		{"negative tier", "-1", []string{"-1"}},
		{"tier above max", "11", []string{"11"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, cobraOut, err := run(t, "dispatch", "--tier", tc.tier, "x")
			if err == nil {
				t.Fatalf("`hyctl dispatch --tier %s` was accepted", tc.tier)
			}
			combined := err.Error() + cobraOut
			for _, want := range tc.wantIn {
				if !strings.Contains(combined, want) {
					t.Errorf("error = %q, want it to mention %q", combined, want)
				}
			}
			if strings.Contains(combined, "no routable heads") || strings.Contains(combined, "no available heads") {
				t.Errorf("error = %q, blames routability instead of the actual %s", combined, tc.name)
			}
		})
	}
}

// A --tier/--swarm-judge-tier/--enum rejection must name the actual problem,
// not just fail for an unrelated reason (e.g. no heads discovered) that would
// have rejected the command anyway. Each of these used to be silently
// swallowed: an invalid --tier fell back to CapScoreSelector's full fan-out
// under --swarm/--confidence, an unrecognized --enum resolved to "no
// restriction", and a bad --swarm-judge-tier fell back to the CapScore judge
// with no message anywhere (#501).
func TestCLI_Dispatch_RejectionsNameTheActualProblem(t *testing.T) {
	populated(t)

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"unrecognized enum", []string{"dispatch", "--enum", "NOTAREALENUM", "x"}, "NOTAREALENUM"},
		{"swarm tier out of range", []string{"dispatch", "--swarm", "--tier", "99", "x"}, "tier"},
		{"swarm unknown tier name", []string{"dispatch", "--swarm", "--tier", "not-a-real-tier", "x"}, "tier"},
		{"confidence tier out of range", []string{"dispatch", "--confidence", "0.9", "--tier", "99", "x"}, "tier"},
		{"bad swarm judge tier", []string{"dispatch", "--swarm", "--swarm-judge-tier", "99", "x"}, "judge"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, cobraOut, err := run(t, tc.args...)
			if err == nil {
				t.Fatalf("`hyctl %s` was accepted", strings.Join(tc.args, " "))
			}
			if msg := err.Error() + cobraOut; !strings.Contains(msg, tc.want) {
				t.Errorf("error = %q, want it to mention %q", msg, tc.want)
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
