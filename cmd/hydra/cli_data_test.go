// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/testutil"
)

// The first half of the CLI contract drove every subcommand on an empty
// machine, which reaches each RunE's first guard and stops. This drives them
// against the files they actually read, so the body a user exercises runs.
//
// The data is seeded on disk rather than through the commands that write it:
// a contract that depends on Hydra's own writer being correct cannot tell a
// broken reader from a broken writer.

// seed writes a file under ~/.hydra, creating parents.
func seed(t *testing.T, rel, content string) string {
	t.Helper()
	path := filepath.Join(config.Dir(), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// populated is a sandbox with a config and one of everything Hydra reads.
func populated(t *testing.T) *testutil.Sandbox {
	t.Helper()
	s := testutil.NewSandbox(t)
	if err := config.Save(&config.Config{Cortex: "none"}); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	seed(t, "logs/cost.jsonl", strings.Join([]string{
		`{"ts":"` + now + `","tier":1,"enum":"CORE","model":"claude-opus","executor":"agy",` +
			`"pool":"claude","prompt_tokens":1200,"response_tokens":600,"est_cost_usd":0.0450,` +
			`"wall_ms":3100,"tokens_source":"actual","cost_source":"estimated",` +
			`"task_id":"task-1","run_id":"run-1"}`,
		`{"ts":"` + now + `","tier":10,"enum":"GRUNT","model":"qwen2.5-coder:7b",` +
			`"executor":"ollama","pool":"local","prompt_tokens":300,"response_tokens":150,` +
			`"est_cost_usd":0,"wall_ms":800,"tokens_source":"estimated",` +
			`"cost_source":"estimated","task_id":"task-2","run_id":"run-1",` +
			`"swarm_mode":"best","swarm_winner":true}`,
	}, "\n")+"\n")

	seed(t, "trust.jsonl",
		`{"ts":"`+now+`","task_hash":"abc123","domain":"go","target_conf":0.95,`+
			`"final_conf":0.96,"samples":3,"models":["claude","qwen"],"cost_usd":0.04,`+
			`"cost_source":"estimated","decision":"accept","ledger":[`+
			`{"source":"model:claude","agreed":true,"llr":1.2,"lambda_after":1.2},`+
			`{"source":"model:qwen","agreed":false,"llr":-0.4,"lambda_after":0.8}]}`+"\n")

	seed(t, "logs/state.json",
		`{"claude_pct":52,"claude_pct_history":[10,25,40,52],"last_model":"claude-opus",`+
			`"last_tier":1,"last_status":"ok","budget":{"claude-opus":{"used":120000,`+
			`"window":200000,"pct":60,"mode":"compact","source":"estimate"}}}`+"\n")

	return s
}

// ── mcp ───────────────────────────────────────────────────────────────────────

// A flagged event (heuristic prompt-injection marker matched) must be visible
// in both `mcp log` and `mcp report`, otherwise the signal lands in the
// JSONL and is invisible everywhere an operator actually looks.
func TestCLI_MCPLogAndReport_SurfaceFlaggedEvents(t *testing.T) {
	testutil.NewSandbox(t)
	now := time.Now().UTC().Format(time.RFC3339)
	seed(t, "mcp_ledger.jsonl", strings.Join([]string{
		`{"ts":"` + now + `","agent":"hydra-dispatch","tool":"claude","resource":"","action":"exec","decision":"allow","flagged":true,"flag_reason":"ignore previous instructions"}`,
		`{"ts":"` + now + `","agent":"hydra-dispatch","tool":"claude","resource":"","action":"exec","decision":"allow"}`,
	}, "\n")+"\n")

	logOut, logCobraOut, err := run(t, "mcp", "log")
	if err != nil {
		t.Fatalf("`hyctl mcp log` failed: %v", err)
	}
	combined := logOut + logCobraOut
	if !strings.Contains(combined, "flagged") || !strings.Contains(combined, "ignore previous instructions") {
		t.Errorf("`mcp log` does not surface the flagged event:\n%s", combined)
	}

	repOut, repCobraOut, err := run(t, "mcp", "report", "--json")
	if err != nil {
		t.Fatalf("`hyctl mcp report --json` failed: %v", err)
	}
	var s struct {
		Flagged int `json:"flagged"`
	}
	if err := json.Unmarshal([]byte(repOut+repCobraOut), &s); err != nil {
		t.Fatalf("report --json did not parse: %v\n%s", err, repOut+repCobraOut)
	}
	if s.Flagged != 1 {
		t.Errorf("report flagged count = %d, want 1", s.Flagged)
	}
}

// Agent/Tool are attacker/agent-controlled: any local MCP tool or misbehaving
// agent can set them via `hyctl mcp record` or a real dispatch. `mcp log`
// already sanitized them at render (util.SafeTerminal), but `mcp report`
// printed them raw, and a control-heavy value forges a fake verdict line on
// the terminal. Sanitizing once at Record ingest fixes both by construction,
// so nobody has to remember to do it at every render site (#500).
func TestCLI_MCPRecord_SanitizesEscapesForBothLogAndReport(t *testing.T) {
	testutil.NewSandbox(t)

	evilTool := "evil\x1b[2K\rFAKE OK no findings"
	if _, cobraOut, err := run(t, "mcp", "record",
		"--agent", "auditor", "--tool", evilTool,
		"--resource", "/x", "--decision", "allow"); err != nil {
		t.Fatalf("`hyctl mcp record` failed: %v (%s)", err, cobraOut)
	}

	logOut, logCobraOut, err := run(t, "mcp", "log")
	if err != nil {
		t.Fatalf("`hyctl mcp log` failed: %v", err)
	}
	logCombined := logOut + logCobraOut
	if strings.Contains(logCombined, "\x1b[2K") || strings.Contains(logCombined, "\rFAKE OK") {
		t.Errorf("`mcp log` printed a raw escape/CR sequence:\n%q", logCombined)
	}
	if !strings.Contains(logCombined, "FAKE OK") {
		t.Errorf("`mcp log` lost the tool name's ordinary text entirely:\n%q", logCombined)
	}

	repOut, repCobraOut, err := run(t, "mcp", "report")
	if err != nil {
		t.Fatalf("`hyctl mcp report` failed: %v", err)
	}
	repCombined := repOut + repCobraOut
	if strings.Contains(repCombined, "\x1b[2K") || strings.Contains(repCombined, "\rFAKE OK") {
		t.Errorf("`mcp report` printed a raw escape/CR sequence, the exact defect #500 reports:\n%q", repCombined)
	}
	if !strings.Contains(repCombined, "FAKE OK") {
		t.Errorf("`mcp report` lost the tool name's ordinary text entirely:\n%q", repCombined)
	}
}

// `hyctl mcp verify-chain` is the tamper-evidence check over the ledger,
// it must report intact after ordinary recording and confirm gate-ability
// (a non-zero exit) when it isn't, matching the other ledger verify commands.
func TestCLI_MCPVerifyChain_ReportsIntactAfterOrdinaryRecording(t *testing.T) {
	testutil.NewSandbox(t)

	for i := 0; i < 3; i++ {
		if _, cobraOut, err := run(t, "mcp", "record", "--agent", "a", "--tool", "t", "--decision", "allow"); err != nil {
			t.Fatalf("`hyctl mcp record` failed: %v (%s)", err, cobraOut)
		}
	}

	out, cobraOut, err := run(t, "mcp", "verify-chain")
	if err != nil {
		t.Fatalf("`hyctl mcp verify-chain` failed on an untampered ledger: %v", err)
	}
	combined := out + cobraOut
	if !strings.Contains(strings.ToLower(combined), "intact") {
		t.Errorf("verify-chain did not report intact:\n%s", combined)
	}
}

// `hyctl security` is the security posture dashboard, it must run clean on
// an empty machine (no ledger yet) and surface real data once the ledger has
// events, never panicking on either state.
func TestCLI_Security_HandlesEmptyAndSeededLedger(t *testing.T) {
	testutil.NewSandbox(t)

	emptyOut, emptyCobraOut, err := run(t, "security")
	if err != nil {
		t.Fatalf("`hyctl security` failed on an empty machine: %v", err)
	}
	if !strings.Contains(strings.ToLower(emptyOut+emptyCobraOut), "no ledger events") {
		t.Errorf("`security` on an empty machine should say so plainly:\n%s", emptyOut+emptyCobraOut)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	seed(t, "mcp_ledger.jsonl", strings.Join([]string{
		`{"ts":"` + now + `","agent":"hydra-dispatch","tool":"claude","resource":"","action":"exec","decision":"allow"}`,
		`{"ts":"` + now + `","agent":"hydra-dispatch","tool":"sketchy","resource":"","action":"exec","decision":"deny","reason":"denied by ledger policy"}`,
	}, "\n")+"\n")

	out, cobraOut, err := run(t, "security", "--json")
	if err != nil {
		t.Fatalf("`hyctl security --json` failed: %v", err)
	}
	var rep struct {
		Ledger struct{ Total, Denied int } `json:"ledger"`
		ByHead []struct {
			Head   string `json:"head"`
			Denied int    `json:"denied"`
		} `json:"byHead"`
		IntegrityIntact bool `json:"integrityIntact"`
		Coverage        struct {
			Applicable     int     `json:"applicable"`
			Covered        int     `json:"covered"`
			PercentCovered float64 `json:"percentCovered"`
		} `json:"coverage"`
		Actions []struct {
			ID       string `json:"id"`
			Priority string `json:"priority"`
		} `json:"actions"`
	}
	if err := json.Unmarshal([]byte(out+cobraOut), &rep); err != nil {
		t.Fatalf("security --json did not parse: %v\n%s", err, out+cobraOut)
	}
	if rep.Ledger.Total != 2 || rep.Ledger.Denied != 1 {
		t.Errorf("Ledger = %+v, want Total=2 Denied=1", rep.Ledger)
	}
	if len(rep.ByHead) != 1 || rep.ByHead[0].Head != "sketchy" {
		t.Errorf("ByHead = %+v, want exactly the denied head", rep.ByHead)
	}
	if !rep.IntegrityIntact {
		t.Error("IntegrityIntact = false on an untampered ledger")
	}
	if rep.Coverage.Applicable != 8 {
		t.Errorf("Coverage.Applicable = %d, want 8", rep.Coverage.Applicable)
	}
	if len(rep.Actions) == 0 {
		t.Error("Actions is empty despite real coverage gaps existing")
	}

	// Text output must show the headline coverage score and the action queue
	//, the KPI/feedback-loop surface, not just the raw tables.
	textOut, textCobraOut, err := run(t, "security")
	if err != nil {
		t.Fatalf("`hyctl security` failed: %v", err)
	}
	combined := textOut + textCobraOut
	// The default surface is the answer, not the dashboard: a verdict, the
	// activity, and whether the record can be trusted.
	for _, want := range []string{"VERDICT", "activity", "evidence"} {
		if !strings.Contains(combined, want) {
			t.Errorf("default `security` output missing %q:\n%s", want, combined)
		}
	}

	// Coverage and the action queue moved behind --why when the surface was
	// re-aimed at "what did the agent do"; they must still be reachable.
	whyOut, whyCobraOut, err := run(t, "security", "--why")
	if err != nil {
		t.Fatalf("`hyctl security --why` failed: %v", err)
	}
	why := whyOut + whyCobraOut
	if !strings.Contains(why, "OWASP LLM Top-10 coverage") {
		t.Errorf("--why output missing the coverage headline:\n%s", why)
	}
	if !strings.Contains(why, "action queue") {
		t.Errorf("--why output missing the action queue section:\n%s", why)
	}
	if len(why) <= len(combined) {
		t.Error("--why should be a superset of the default surface")
	}
}

// ── trust ─────────────────────────────────────────────────────────────────────

// `hyctl trust explain` is the audit trail behind a confidence number: it must
// print the whole evidence ledger, and it must fail on a hash it does not have
// rather than printing nothing and exiting 0.
func TestCLI_TrustExplain_PrintsTheLedger(t *testing.T) {
	populated(t)

	out, cobraOut, err := run(t, "trust", "explain", "abc123")
	if err != nil {
		t.Fatalf("`hyctl trust explain` failed against a real run: %v", err)
	}
	combined := out + cobraOut
	for _, want := range []string{"abc123", "go", "model:claude", "model:qwen", "agree", "disagree"} {
		if !strings.Contains(combined, want) {
			t.Errorf("the ledger omits %q, so the confidence is unauditable:\n%s", want, combined)
		}
	}
	if !strings.Contains(combined, "96.0%") || !strings.Contains(combined, "95.0%") {
		t.Errorf("target and achieved confidence are not both shown:\n%s", combined)
	}

	if _, _, err := run(t, "trust", "explain", "no-such-hash"); err == nil {
		t.Error("explain on an unknown hash exited 0; a script would read that as found")
	}
}

func TestCLI_TrustStats_SummarisesRealRuns(t *testing.T) {
	populated(t)

	out, cobraOut, err := run(t, "trust", "stats")
	if err != nil {
		t.Fatalf("`hyctl trust stats` failed against a real log: %v", err)
	}
	if strings.TrimSpace(out+cobraOut) == "" {
		t.Fatal("`hyctl trust stats` printed nothing with a run on disk")
	}
}

// `trust record` feeds the calibrator that every confidence number is derived
// from. What it writes must come back out of `trust calibration`.
func TestCLI_TrustRecordThenCalibration_RoundTrips(t *testing.T) {
	populated(t)

	if _, cobraOut, err := run(t, "trust", "record",
		"--source", "model:test-head", "--domain", "go",
		"--said-correct", "--outcome", "correct"); err != nil {
		t.Fatalf("`hyctl trust record` failed: %v (%s)", err, cobraOut)
	}

	out, cobraOut, err := run(t, "trust", "calibration")
	if err != nil {
		t.Fatalf("`hyctl trust calibration` failed after a record: %v", err)
	}
	if !strings.Contains(out+cobraOut, "test-head") {
		t.Errorf("the recorded source is not in the calibration table; every "+
			"confidence number is derived from it:\n%s", out+cobraOut)
	}
}

// calibration is append-only, so a blank --domain writes a permanent, unfixable
// row. It must be refused up front, exactly like --source and --outcome.
func TestCLI_TrustRecord_RequiresDomain(t *testing.T) {
	populated(t)

	_, cobraOut, err := run(t, "trust", "record",
		"--source", "model:test-head", "--said-correct", "--outcome", "correct")
	if err == nil {
		t.Fatal("`hyctl trust record` without --domain was accepted")
	}
	if !strings.Contains(err.Error()+cobraOut, "--domain") {
		t.Errorf("error = %v, want it to name --domain", err)
	}
}

// Two domains sharing a ten-character prefix (e.g. "trust-bench" and
// "trust-bench-v2") must not render as the identical truncated label, that
// makes rows with different n/D visually indistinguishable.
func TestCLI_TrustCalibration_DistinguishesDomainsWithSharedPrefix(t *testing.T) {
	populated(t)

	if _, cobraOut, err := run(t, "trust", "record",
		"--source", "model:x", "--domain", "trust-bench",
		"--said-correct", "--outcome", "correct"); err != nil {
		t.Fatalf("`hyctl trust record` (trust-bench) failed: %v (%s)", err, cobraOut)
	}
	if _, cobraOut, err := run(t, "trust", "record",
		"--source", "model:x", "--domain", "trust-bench-v2",
		"--said-correct", "--outcome", "incorrect"); err != nil {
		t.Fatalf("`hyctl trust record` (trust-bench-v2) failed: %v (%s)", err, cobraOut)
	}

	out, cobraOut, err := run(t, "trust", "calibration")
	if err != nil {
		t.Fatalf("`hyctl trust calibration` failed: %v", err)
	}
	table := out + cobraOut

	var domains []string
	for _, line := range strings.Split(table, "\n") {
		if !strings.Contains(line, "model:x") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			t.Fatalf("row for model:x has no domain column: %q", line)
		}
		domains = append(domains, fields[1])
	}
	if len(domains) != 2 {
		t.Fatalf("want 2 rows for model:x, got %d (%v):\n%s", len(domains), domains, table)
	}
	if domains[0] == domains[1] {
		t.Errorf("both domains rendered as the same label %q, the rows are "+
			"visually indistinguishable:\n%s", domains[0], table)
	}
}

// The defect model sets how much confidence a task needs. PII and production
// must both raise the bar, that is the whole point of the flags.
func TestCLI_TrustDefect_RaisesTheBarForRiskyWork(t *testing.T) {
	populated(t)

	plain, _, err := run(t, "trust", "defect")
	if err != nil {
		t.Fatal(err)
	}
	risky, _, err := run(t, "trust", "defect", "--pii", "--production")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(plain) == "" || strings.TrimSpace(risky) == "" {
		t.Fatal("`hyctl trust defect` printed nothing")
	}
	if plain == risky {
		t.Errorf("--pii --production did not change the required confidence:\n%s", plain)
	}
}

// A non-finite --blast must never reach the model: text mode used to print a
// nonsensical "$NaN" recommendation (exit 0), and --json crashed outright with
// a leaked Go internal error ("json: unsupported value: NaN", exit 1) instead
// of a clean CLI error (#501).
func TestCLI_TrustDefect_RejectsNonFiniteBlast(t *testing.T) {
	populated(t)

	for _, blast := range []string{"NaN", "+Inf", "-Inf"} {
		t.Run("text/"+blast, func(t *testing.T) {
			_, _, err := run(t, "trust", "defect", "--blast", blast)
			if err == nil {
				t.Errorf("`trust defect --blast %s` was accepted", blast)
			}
		})
		t.Run("json/"+blast, func(t *testing.T) {
			_, _, err := run(t, "trust", "defect", "--blast", blast, "--json")
			if err == nil {
				t.Errorf("`trust defect --blast %s --json` was accepted", blast)
			}
		})
	}
}

// The JSON blast_radius must match the value CostUSD/RequiredConfidence
// actually used, not the raw --blast input: BlastRadius<=0 is internally
// clamped to 1.0, and the CLI used to display the unclamped input instead.
func TestCLI_TrustDefect_JSONBlastRadiusMatchesTheUsedValue(t *testing.T) {
	populated(t)

	for _, blast := range []string{"0", "-5"} {
		out, cobraOut, err := run(t, "trust", "defect", "--blast", blast, "--json")
		if err != nil {
			t.Fatalf("`trust defect --blast %s --json` failed: %v (%s)", blast, err, cobraOut)
		}
		var got map[string]any
		if jerr := json.Unmarshal([]byte(out), &got); jerr != nil {
			t.Fatalf("output is not JSON: %v\n%s", jerr, out)
		}
		if br, _ := got["blast_radius"].(float64); br != 1 {
			t.Errorf("--blast %s: blast_radius = %v, want 1 (the clamped value CostUSD used)", blast, got["blast_radius"])
		}
	}
}

// ── graph ─────────────────────────────────────────────────────────────────────

func seedGraph(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "graph.json")
	doc := `{"nodes":[
	  {"id":"internal/auth/token.go","file":"internal/auth/token.go"},
	  {"id":"internal/api/login.go","file":"internal/api/login.go"},
	  {"id":"internal/api/refresh.go","file":"internal/api/refresh.go"},
	  {"id":"cmd/server/main.go","file":"cmd/server/main.go"}],
	 "edges":[
	  {"from":"internal/api/login.go","to":"internal/auth/token.go"},
	  {"from":"internal/api/refresh.go","to":"internal/auth/token.go"},
	  {"from":"cmd/server/main.go","to":"internal/api/login.go"}]}`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// `hyctl graph blast` is what tells a user a change is not local. A hub and a
// leaf must not report the same thing.
func TestCLI_GraphBlast_DistinguishesAHubFromALeaf(t *testing.T) {
	populated(t)
	g := seedGraph(t)

	hub, cobraOut, err := run(t, "graph", "blast", "internal/auth/token.go", "--graph", g)
	if err != nil {
		t.Fatalf("`hyctl graph blast` failed: %v (%s)", err, cobraOut)
	}
	if strings.TrimSpace(hub) == "" {
		t.Fatal("`hyctl graph blast` printed nothing for a file with dependents")
	}

	leaf, _, err := run(t, "graph", "blast", "cmd/server/main.go", "--graph", g)
	if err != nil {
		t.Fatal(err)
	}
	if hub == leaf {
		t.Errorf("a file with two dependents reads identically to a leaf:\n%s", hub)
	}

	// A file the graph has never seen must say so rather than reporting a
	// blast radius of zero, which reads as "safe to change".
	unknown, cobraOut, err := run(t, "graph", "blast", "not/in/the/graph.go", "--graph", g)
	if err == nil && !strings.Contains(strings.ToLower(unknown+cobraOut), "not") {
		t.Errorf("an unknown file reported a radius without saying it is unknown:\n%s",
			unknown+cobraOut)
	}
}

// `hyctl graph parallel` answers how many agents to fan out. One file cannot
// need more than one agent.
func TestCLI_GraphParallel_ScalesWithTheWork(t *testing.T) {
	populated(t)
	g := seedGraph(t)

	one, cobraOut, err := run(t, "graph", "parallel", "internal/auth/token.go", "--graph", g)
	if err != nil {
		t.Fatalf("`hyctl graph parallel` failed: %v (%s)", err, cobraOut)
	}
	if strings.TrimSpace(one) == "" {
		t.Fatal("`hyctl graph parallel` printed nothing")
	}

	many, _, err := run(t, "graph", "parallel",
		"internal/auth/token.go", "internal/api/login.go", "cmd/server/main.go", "--graph", g)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(many) == "" {
		t.Fatal("`hyctl graph parallel` printed nothing for three files")
	}
}

// `hyctl graph generate` must produce a graph.json that `hyctl graph blast`
// can actually read, against a real Go module, not a hand-built fixture.
func TestCLI_GraphGenerate_ProducesAGraphBlastCanRead(t *testing.T) {
	s := populated(t)
	if !s.AllowHostBinary(t, "go") {
		t.Skip("go is not on the host PATH, which should be impossible here")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/gentest\n\ngo 1.21\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for rel, body := range map[string]string{
		"a/a.go": "package a\n",
		"b/b.go": "package b\n\nimport _ \"example.com/gentest/a\"\n",
	} {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(dir, "graph.json")

	genOut, cobraOut, err := run(t, "graph", "generate", dir, "--out", out)
	if err != nil {
		t.Fatalf("`hyctl graph generate` failed: %v (%s)", err, cobraOut)
	}
	if !strings.Contains(genOut+cobraOut, "nodes") {
		t.Errorf("generate did not report a node count:\n%s", genOut+cobraOut)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("no graph.json was written: %v", err)
	}

	blastOut, cobraOut, err := run(t, "graph", "blast", "b", "--graph", out)
	if err != nil {
		t.Fatalf("`hyctl graph blast` could not read the generated graph: %v (%s)", err, cobraOut)
	}
	if strings.Contains(strings.ToLower(blastOut+cobraOut), "not in the graph") {
		t.Errorf("blast did not recognize a node generate produced:\n%s", blastOut+cobraOut)
	}
}

// ── cost and stats over real rows ─────────────────────────────────────────────

func TestCLI_CostSubcommands_AgainstRealRows(t *testing.T) {
	populated(t)

	t.Run("by-run", func(t *testing.T) {
		out, cobraOut, err := run(t, "cost", "by-run", "run-1")
		if err != nil {
			t.Fatalf("`hyctl cost by-run` failed: %v", err)
		}
		if strings.TrimSpace(out+cobraOut) == "" {
			t.Error("printed nothing for a run that exists")
		}
		// An unknown run must be an error, not a silent $0.00.
		if _, _, err := run(t, "cost", "by-run", "no-such-run"); err == nil {
			t.Error("an unknown run id exited 0, reporting $0.00 spent")
		}
	})

	t.Run("by-task", func(t *testing.T) {
		if _, _, err := run(t, "cost", "by-task", "task-1"); err != nil {
			t.Fatalf("`hyctl cost by-task` failed: %v", err)
		}
		if _, _, err := run(t, "cost", "by-task", "no-such-task"); err == nil {
			t.Error("an unknown task id exited 0")
		}
	})

	t.Run("tail", func(t *testing.T) {
		out, cobraOut, err := run(t, "cost", "tail", "1")
		if err != nil {
			t.Fatalf("`hyctl cost tail` failed: %v", err)
		}
		if strings.TrimSpace(out+cobraOut) == "" {
			t.Error("printed nothing")
		}
	})

	t.Run("json is parseable and carries the rows", func(t *testing.T) {
		out, _, err := run(t, "cost", "json")
		if err != nil {
			t.Fatalf("`hyctl cost json` failed: %v", err)
		}
		var rows []map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &rows); err != nil {
			t.Fatalf("not valid JSON: %v\n%s", err, out)
		}
		if len(rows) != 2 {
			t.Errorf("got %d rows, want the 2 on disk", len(rows))
		}
	})
}

func TestCLI_Stats_AgainstRealRows(t *testing.T) {
	populated(t)

	for _, args := range [][]string{
		{"stats"},
		{"stats", "--days", "7"},
		{"stats", "--days", "1"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			out, cobraOut, err := run(t, args...)
			if err != nil {
				t.Fatalf("`hyctl %s` failed against real rows: %v",
					strings.Join(args, " "), err)
			}
			combined := out + cobraOut
			if strings.TrimSpace(combined) == "" {
				t.Fatal("printed nothing with rows on disk")
			}
			// The expensive model is the one the user needs to see.
			if !strings.Contains(combined, "claude-opus") && !strings.Contains(combined, "1") {
				t.Errorf("the report names neither the model nor a count:\n%s", combined)
			}
		})
	}
}

// `hyctl status` against a populated state.json runs the governor readout,
// the budget table, the bar and the rate-aware mode.
func TestCLI_Status_RendersTheGovernor(t *testing.T) {
	populated(t)

	out, cobraOut, err := run(t, "status")
	if err != nil {
		t.Fatalf("`hyctl status` failed against a populated state.json: %v", err)
	}
	combined := out + cobraOut
	if !strings.Contains(combined, "52") {
		t.Errorf("the claude_pct readout is missing:\n%s", combined)
	}
	if !strings.Contains(combined, "claude-opus") {
		t.Errorf("the per-model budget table is missing:\n%s", combined)
	}
}

// ── mcp ledger ────────────────────────────────────────────────────────────────

// The MCP gate is what stops an agent touching a file it should not, and its
// exit code is what a caller branches on, so it runs as a real binary.
func TestCLI_MCP_GateRecordsItsDecision(t *testing.T) {
	s := populated(t)

	code, out := runBinary(t, s, "mcp", "check", "read",
		"--resource", "/tmp/some-file.txt", "--agent", "test-agent")
	// Allowed or denied is the policy's call; what matters is that the decision
	// was made and is visible.
	if code != 0 && code != 3 {
		t.Errorf("`hyctl mcp check` exited %d, want 0 (allow) or 3 (deny):\n%s", code, out)
	}

	if _, logOut := runBinary(t, s, "mcp", "log"); !strings.Contains(logOut, "test-agent") {
		t.Errorf("the check was not recorded in the ledger; an unlogged decision "+
			"is not accountability:\n%s", logOut)
	}
	if code, out := runBinary(t, s, "mcp", "report"); code != 0 {
		t.Errorf("`hyctl mcp report` exited %d:\n%s", code, out)
	}
}

// ── models overlay ────────────────────────────────────────────────────────────

// `hyctl models add` exists so a new model can be routed to without a rebuild.
// What it writes must appear in `list` and disappear on `remove`.
func TestCLI_Models_AddListRemoveRoundTrips(t *testing.T) {
	populated(t)

	if _, cobraOut, err := run(t, "models", "add", "kimi-k3",
		"--name", "Kimi K3", "--provider", "moonshot", "--cap-score", "85"); err != nil {
		t.Fatalf("`hyctl models add` failed: %v (%s)", err, cobraOut)
	}

	out, cobraOut, err := run(t, "models", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out+cobraOut, "kimi-k3") {
		t.Fatalf("the added model is not in the list; it could never be routed to:\n%s",
			out+cobraOut)
	}

	if _, _, err := run(t, "models", "remove", "kimi-k3"); err != nil {
		t.Fatalf("`hyctl models remove` failed: %v", err)
	}
	out, cobraOut, err = run(t, "models", "list")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out+cobraOut, "kimi-k3") {
		t.Errorf("the model survived removal:\n%s", out+cobraOut)
	}

	// Removing something that was never added must say so rather than
	// reporting success.
	if _, _, err := run(t, "models", "remove", "never-existed"); err == nil {
		t.Error("removing an unknown model exited 0")
	}
}

// `models add` on an id that already exists in the embedded built-in catalog
// silently shadowed it with the same "added X" phrasing whether X was
// brand-new or a full override of a curated, heavily-routed identity like
// "claude" (#505). This must read differently from a routine add.
func TestCLI_ModelsAdd_WarnsWhenShadowingABuiltin(t *testing.T) {
	populated(t)

	out, cobraOut, err := run(t, "models", "add", "claude",
		"--name", "Claude (tuned)", "--provider", "anthropic", "--cap-score", "50")
	if err != nil {
		t.Fatalf("`hyctl models add claude` failed: %v (%s)", err, cobraOut)
	}
	combined := out + cobraOut
	if !strings.Contains(combined, "overriding built-in") {
		t.Errorf("overriding a built-in id printed the generic message, not a distinct "+
			"warning:\n%s", combined)
	}

	// A brand-new id must still get the ordinary phrasing.
	out, cobraOut, err = run(t, "models", "add", "kimi-k4", "--cap-score", "70")
	if err != nil {
		t.Fatalf("`hyctl models add kimi-k4` failed: %v (%s)", err, cobraOut)
	}
	combined = out + cobraOut
	if strings.Contains(combined, "overriding built-in") {
		t.Errorf("a brand-new model was reported as overriding a built-in:\n%s", combined)
	}
	if !strings.Contains(combined, "added") {
		t.Errorf("a brand-new model add did not say it was added:\n%s", combined)
	}
}

// `models sync` re-run used to look up "already known" against the embedded
// catalog only, never the overlay, so a model the user had already synced
// and hand-tuned via `models add` looked unrecognized and was silently
// overwritten back to a fresh heuristic capScore on every re-run, all while
// the printed message claimed it was "already known, skipped" (#505).
func TestCLI_ModelsSync_DoesNotRevertUserTunedCapScore(t *testing.T) {
	populated(t)
	seedPricingCache(t, map[string]struct{ In, Out float64 }{
		"testvendor/test-model-xyz": {1.0, 2.0},
	})

	// First sync imports it at the heuristic score.
	if _, cobraOut, err := run(t, "models", "sync"); err != nil {
		t.Fatalf("first `hyctl models sync` failed: %v (%s)", err, cobraOut)
	}

	// The user tunes it by hand afterwards.
	if _, cobraOut, err := run(t, "models", "add", "testvendor/test-model-xyz",
		"--provider", "testvendor", "--cap-score", "99"); err != nil {
		t.Fatalf("`hyctl models add` failed: %v (%s)", err, cobraOut)
	}

	// Re-running sync must not revert the hand-tuned score, and must say so.
	out, cobraOut, err := run(t, "models", "sync")
	if err != nil {
		t.Fatalf("second `hyctl models sync` failed: %v (%s)", err, cobraOut)
	}
	combined := out + cobraOut
	if !strings.Contains(combined, "1 already known, skipped") {
		t.Errorf("sync did not report the user-tuned model as already known:\n%s", combined)
	}

	listOut, listCobra, err := run(t, "models", "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var entries []struct {
		ID       string `json:"id"`
		CapScore int    `json:"capScore"`
	}
	if err := json.Unmarshal([]byte(listOut), &entries); err != nil {
		t.Fatalf("models list --json did not parse: %v (%s)", err, listOut+listCobra)
	}
	found := false
	for _, e := range entries {
		if e.ID == "testvendor/test-model-xyz" {
			found = true
			if e.CapScore != 99 {
				t.Errorf("capScore = %d after re-sync, want 99 (the user's tuning), "+
					"sync silently reverted it", e.CapScore)
			}
		}
	}
	if !found {
		t.Fatal("synced model missing from `models list` entirely")
	}
}

// seedPricingCache writes a fresh pricing_cache.json so `pricing.Load()` sees
// live OpenRouter-shaped data without reaching the network.
func seedPricingCache(t *testing.T, models map[string]struct{ In, Out float64 }) {
	t.Helper()
	m := make(map[string]map[string]float64, len(models))
	for id, p := range models {
		m[id] = map[string]float64{"input_per_mtok": p.In, "output_per_mtok": p.Out}
	}
	raw, err := json.Marshal(map[string]any{
		"fetched_at": time.Now().UTC().Format(time.RFC3339),
		"source":     "test",
		"models":     m,
	})
	if err != nil {
		t.Fatal(err)
	}
	seed(t, "pricing_cache.json", string(raw))
}

// ── oracle ────────────────────────────────────────────────────────────────────

// An oracle is a high-D evidence source, its verdict can outweigh several
// models' votes, so a pass and a fail must be distinguishable by exit code,
// which is what a caller gates on.
func TestCLI_OracleVerify_PassAndFailDifferByExitCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/usr/bin/true and /usr/bin/false are POSIX; the same contract is " +
			"covered platform-independently by the empty-template case below")
	}
	s := populated(t)

	passCode, passOut := runBinary(t, s, "oracle", "verify", "/usr/bin/true")
	failCode, failOut := runBinary(t, s, "oracle", "verify", "/usr/bin/false")

	if passCode != 0 {
		t.Errorf("a passing oracle exited %d:\n%s", passCode, passOut)
	}
	if failCode == 0 {
		t.Errorf("a failing oracle exited 0; a caller gating on the code would "+
			"treat the failure as a pass:\n%s", failOut)
	}
	if passOut == failOut {
		t.Errorf("a passing and a failing oracle print the same thing:\n%s", passOut)
	}
}

// A verifier argument containing a space (a shell -c script) must reach the
// verifier exactly as typed, not get corrupted by joining argv into a string
// and re-splitting it on whitespace (#444). Before the fix, `sh -c "exit 1"`
// silently became `sh -c exit 1`, which bash runs as `sh -c 'exit'`, a false
// PASS for a command that actually exits 1.
func TestCLI_OracleVerify_ArgumentWithSpaceIsNotCorrupted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Log("windows: no /bin/sh-style true/false to drive exit codes here")
		return
	}
	s := populated(t)

	code, out := runBinary(t, s, "oracle", "verify", "--", "sh", "-c", "exit 1")
	if code == 0 {
		t.Errorf("`sh -c \"exit 1\"` exited 0, the quoted argument's space was "+
			"re-tokenized into two argv elements:\n%s", out)
	}
}

// An empty template is a misconfiguration, not a passing verdict: an oracle
// that votes yes when unset produces confident false evidence, and its LLR
// outweighs several models.
func TestCLI_OracleVerify_EmptyTemplateIsNotAPass(t *testing.T) {
	s := populated(t)
	if code, out := runBinary(t, s, "oracle", "verify"); code == 0 {
		t.Errorf("`hyctl oracle verify` with no command exited 0:\n%s", out)
	}
}

// ── context ───────────────────────────────────────────────────────────────────

// `hyctl context entropy` measures signal density. Repetitive text and dense
// text must not measure the same, or the compaction governor has no signal.
func TestCLI_ContextEntropy_DistinguishesDenseFromRepetitive(t *testing.T) {
	populated(t)

	dir := t.TempDir()
	dense := filepath.Join(dir, "dense.txt")
	repetitive := filepath.Join(dir, "repetitive.txt")
	if err := os.WriteFile(dense, []byte(strings.Repeat(
		"func RotateSigningKey(ctx context.Context, ks KeyStore) error { return ks.Commit(ctx) }\n", 40)),
		0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repetitive, []byte(strings.Repeat("a", 4000)), 0o600); err != nil {
		t.Fatal(err)
	}

	denseOut, cobraOut, err := run(t, "context", "entropy", dense)
	if err != nil {
		t.Fatalf("`hyctl context entropy` failed: %v (%s)", err, cobraOut)
	}
	repOut, _, err := run(t, "context", "entropy", repetitive)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(denseOut) == "" || strings.TrimSpace(repOut) == "" {
		t.Fatal("`hyctl context entropy` printed nothing")
	}
	if denseOut == repOut {
		t.Errorf("dense and highly repetitive text measure identically, so the "+
			"compaction governor has no signal:\n%s", denseOut)
	}

	// A file that is not there must be an error, not a density of zero.
	if _, _, err := run(t, "context", "entropy", filepath.Join(dir, "absent.txt")); err == nil {
		t.Error("entropy of a missing file exited 0")
	}
}

// ── parallel ──────────────────────────────────────────────────────────────────

// `hyctl parallel` takes a batch spec. A malformed one must be refused before
// anything is dispatched, a partially-parsed batch would run some tasks.
func TestCLI_Parallel_RefusesAMalformedSpec(t *testing.T) {
	s := populated(t)

	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out := runBinary(t, s, "parallel", "--tasks", bad); code == 0 {
		t.Errorf("a malformed batch spec was accepted:\n%s", out)
	}

	empty := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(empty, []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out := runBinary(t, s, "parallel", "--tasks", empty); code == 0 {
		t.Errorf("an empty batch was accepted; there is nothing to run:\n%s", out)
	}

	if code, out := runBinary(t, s, "parallel", "--tasks", filepath.Join(dir, "absent.json")); code == 0 {
		t.Errorf("a missing spec file was accepted:\n%s", out)
	}
}

// ── review ────────────────────────────────────────────────────────────────────

// `hyctl review summary` with no arguments reads what Hydra last edited. With
// nothing recorded it must say so rather than reporting a clean tree.
func TestCLI_ReviewSummary_WithNothingRecorded(t *testing.T) {
	populated(t)

	out, cobraOut, err := run(t, "review", "summary")
	if err != nil {
		t.Fatalf("`hyctl review summary` failed with no edits recorded: %v", err)
	}
	if strings.TrimSpace(out+cobraOut) == "" {
		t.Error("printed nothing; the user cannot tell it ran from a clean tree")
	}
}

// Every --json surface must emit parseable JSON once there is data behind it.
// A jq pipeline that works on a populated machine and breaks on a fresh one,
// or vice versa, is a broken contract either way.
func TestCLI_JSONSurfaces_ParseAgainstRealData(t *testing.T) {
	g := ""
	for _, tc := range []struct {
		name string
		args []string
		want string // "object" or "array"
	}{
		// Two different shapes on purpose: `cost --json` is the summary
		// object, `cost json` is the raw row array. A script written against
		// one and pointed at the other breaks, so both are pinned.
		{"cost --json", []string{"cost", "--json"}, "object"},
		{"cost json", []string{"cost", "json"}, "array"},
		{"models list --json", []string{"models", "list", "--json"}, "any"},
		{"graph blast --json", []string{"graph", "blast", "internal/auth/token.go"}, "object"},
		{"context entropy --json", []string{"context", "entropy"}, "object"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			populated(t)
			args := append([]string(nil), tc.args...)

			switch {
			case tc.name == "graph blast --json":
				g = seedGraph(t)
				args = append(args, "--graph", g, "--json")
			case tc.name == "context entropy --json":
				f := filepath.Join(t.TempDir(), "sample.go")
				if err := os.WriteFile(f, []byte(strings.Repeat("func f() {}\n", 50)), 0o600); err != nil {
					t.Fatal(err)
				}
				args = append(args, f, "--json")
			}

			out, cobraOut, err := run(t, args...)
			if err != nil {
				t.Fatalf("`hyctl %s` failed with data on disk: %v (%s)",
					strings.Join(args, " "), err, cobraOut)
			}
			trimmed := strings.TrimSpace(out)
			if trimmed == "" {
				t.Fatalf("printed nothing; a jq pipeline gets an empty stream rather " +
					"than an empty document")
			}
			var v any
			if err := json.Unmarshal([]byte(trimmed), &v); err != nil {
				t.Fatalf("not valid JSON: %v\n%s", err, trimmed)
			}
			switch tc.want {
			case "array":
				if _, ok := v.([]any); !ok {
					t.Errorf("emitted %T, want a JSON array, a jq pipeline iterating "+
						"it would break", v)
				}
			case "object":
				if _, ok := v.(map[string]any); !ok {
					t.Errorf("emitted %T, want a JSON object", v)
				}
			}
		})
	}
}

// `hyctl pricing list` reads the tier table that prices every CLI-agent head.
// It must work offline, since that table is embedded precisely so it does.
func TestCLI_PricingList_WorksOffline(t *testing.T) {
	populated(t)

	out, cobraOut, err := run(t, "pricing", "list")
	if err != nil {
		t.Fatalf("`hyctl pricing list` failed with no network: %v (%s)", err, cobraOut)
	}
	if strings.TrimSpace(out+cobraOut) == "" {
		t.Fatal("printed nothing; the embedded tier table is what makes this work offline")
	}

	// A filter that matches nothing must say so rather than printing an empty
	// table that reads as "no models exist".
	none, noneCobra, err := run(t, "pricing", "list", "definitely-not-a-model-xyz")
	if err == nil && strings.TrimSpace(none+noneCobra) == "" {
		t.Error("a filter matching nothing printed nothing at all")
	}
}

// `pricing list --json` used to emit `null` (a nil slice) instead of `[]` when
// a filter matched nothing, valid JSON, but a script/agent iterating the
// result as an array breaks on it (#505). docs/pricing.md exists precisely
// because this output is meant to be machine-consumed.
func TestCLI_PricingListJSON_EmitsEmptyArrayNotNullWhenNothingMatches(t *testing.T) {
	populated(t)

	out, cobraOut, err := run(t, "pricing", "list", "--json", "definitely-not-a-model-xyz")
	if err != nil {
		t.Fatalf("`hyctl pricing list --json <no-match>` failed: %v (%s)", err, cobraOut)
	}
	trimmed := strings.TrimSpace(out)
	if trimmed == "null" {
		t.Fatalf("emitted the literal null; a script iterating this as an array panics:\n%s", out)
	}
	var v []any
	if err := json.Unmarshal([]byte(trimmed), &v); err != nil {
		t.Fatalf("not a JSON array: %v\n%s", err, trimmed)
	}
	if v == nil {
		t.Error("json.Unmarshal produced a nil slice, the wire form was null, not []")
	}
	if len(v) != 0 {
		t.Errorf("got %d rows for a filter that should match nothing: %v", len(v), v)
	}
}

// `pricing list` used to only walk the OpenRouter-fetched map, so CLI-agent
// heads (claude-core, opus-thinking, …), priced from registry/pricing.yaml,
// keyed by tier rather than a model name, could never appear here at all,
// and a fresh/offline install showed a fully empty table (#505).
func TestCLI_PricingList_MergesTierPricing(t *testing.T) {
	populated(t)

	out, cobraOut, err := run(t, "pricing", "list", "claude-core")
	if err != nil {
		t.Fatalf("`hyctl pricing list claude-core` failed: %v (%s)", err, cobraOut)
	}
	if !strings.Contains(out+cobraOut, "claude-core") {
		t.Fatalf("tier-based pricing for claude-core never showed up:\n%s", out+cobraOut)
	}

	jsonOut, jsonCobra, err := run(t, "pricing", "list", "--json")
	if err != nil {
		t.Fatalf("`hyctl pricing list --json` failed: %v (%s)", err, jsonCobra)
	}
	var rows []struct {
		Model  string `json:"model"`
		Source string `json:"source"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &rows); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, jsonOut)
	}
	var sawTier bool
	for _, r := range rows {
		if r.Model == "claude-core" {
			sawTier = true
			if r.Source != "tier" {
				t.Errorf("claude-core source = %q, want %q", r.Source, "tier")
			}
		}
	}
	if !sawTier {
		t.Fatalf("claude-core is absent from `pricing list --json`:\n%s", jsonOut)
	}
}

// `hyctl review` against a real edited file: summary reports the change, diff
// shows it, and approve consumes the backup so the next edit has a fresh
// baseline.
func TestCLI_Review_AgainstARealEdit(t *testing.T) {
	populated(t)

	repo := t.TempDir()
	// A .git marker that is not a repository, so the .hydra-bak backup is the
	// baseline, the path a non-git user is on.
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	file := filepath.Join(cwd, "edited.go")
	if err := os.WriteFile(file, []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file+".hydra-bak", []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	summary, cobraOut, err := run(t, "review", "summary", file)
	if err != nil {
		t.Fatalf("`hyctl review summary` failed on a real edit: %v (%s)", err, cobraOut)
	}
	if !strings.Contains(summary+cobraOut, "edited.go") {
		t.Errorf("the summary does not name the edited file:\n%s", summary+cobraOut)
	}

	diff, cobraOut, err := run(t, "review", "diff", file)
	if err != nil {
		t.Fatalf("`hyctl review diff` failed: %v (%s)", err, cobraOut)
	}
	if !strings.Contains(diff+cobraOut, "func main") {
		t.Errorf("the diff does not show the added line:\n%s", diff+cobraOut)
	}

	if _, cobraOut, err := run(t, "review", "approve", file); err != nil {
		t.Fatalf("`hyctl review approve` failed: %v (%s)", err, cobraOut)
	}
	if _, err := os.Stat(file + ".hydra-bak"); err == nil {
		t.Error("approve left the backup behind; the next edit would see a stale baseline")
	}
	if raw, _ := os.ReadFile(file); !strings.Contains(string(raw), "func main") {
		t.Errorf("approve changed the file: %q", raw)
	}
}

// Reject restores the file from the backup, the other half of the review pair.
func TestCLI_ReviewReject_RestoresTheFile(t *testing.T) {
	populated(t)

	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
	cwd, _ := os.Getwd()

	file := filepath.Join(cwd, "reverted.go")
	original := "package main\n"
	if err := os.WriteFile(file, []byte("package main\n\nfunc bad() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file+".hydra-bak", []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, cobraOut, err := run(t, "review", "reject", file); err != nil {
		t.Fatalf("`hyctl review reject` failed: %v (%s)", err, cobraOut)
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("reject removed a file that had a baseline to restore: %v", err)
	}
	if string(raw) != original {
		t.Errorf("file = %q after reject, want the backup restored", raw)
	}
	if _, err := os.Stat(file + ".hydra-bak"); err == nil {
		t.Error("reject left the backup behind")
	}
}
