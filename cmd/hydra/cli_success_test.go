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
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/testutil"
)

// The commands that call os.Exit do so only on their *failure* paths — `hyctl
// edit` exits 2 when the edit fails, the MCP gate exits 3 on a deny, the oracle
// exits 1 on a failing verdict, `parallel` exits 1 when a task fails. Their
// success paths are the larger bodies and run cleanly in process, so this drives
// those: a real dispatch through a fake head planted on $PATH, with no network
// and no API key.
//
// The exit codes themselves stay in exit_code_test.go, against a real binary.

// dispatchable is a sandbox with a config, a workspace rooted at a temp repo,
// and a head that answers with reply. It is the in-process equivalent of a
// machine that has one working CLI agent installed.
func dispatchable(t *testing.T, reply string) (s *testutil.Sandbox, repo string) {
	t.Helper()
	s = testutil.NewSandbox(t)

	if err := config.Save(&config.Config{Cortex: "cody"}); err != nil {
		t.Fatal(err)
	}
	// `cody` rather than `claude`: its capability score puts it at UITier 6, so
	// enum MODERATE routes to it. A tier-1 head is stronger than any enum these
	// commands ask for, and selection would find nothing.
	s.FakeBinary(t, "cody", testutil.EchoScript(reply))

	repo = t.TempDir()
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

	// The workspace has to be declared, and git:"false" so the .hydra-bak
	// backup is the baseline rather than a git index that does not exist.
	regDir := filepath.Join(s.HydraHome, "registry")
	if err := os.MkdirAll(regDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		cwd = repo
	}
	ws := "version: \"1.0\"\nworkspaces:\n  test:\n    root: " + cwd +
		"\n    git: \"false\"\n    allowed_globs: [\"**\"]\n"
	if err := os.WriteFile(filepath.Join(regDir, "workspace.yaml"), []byte(ws), 0o600); err != nil {
		t.Fatal(err)
	}
	return s, cwd
}

func marked(content string) string {
	return "<<<HYDRA_FILE_START>>>\n" + content + "\n<<<HYDRA_FILE_END>>>"
}

// ── dispatch, executed for real ───────────────────────────────────────────────

// The success path of `hyctl dispatch` is the largest uncovered body in the
// package: it selects a head, executes, prints the answer, and logs the spend.
func TestCLI_Dispatch_SucceedsAndLogsTheSpend(t *testing.T) {
	dispatchable(t, "the head's answer")

	out, cobraOut, err := run(t, "dispatch", "--enum", "MODERATE", "write a DTO")
	if err != nil {
		t.Fatalf("`hyctl dispatch` failed against a working head: %v (%s)", err, cobraOut)
	}
	if !strings.Contains(out+cobraOut, "the head's answer") {
		t.Errorf("the answer was not printed:\n%s", out+cobraOut)
	}

	// Every dispatch is recorded in dispatch.jsonl regardless of whether its
	// cost is knowable.
	raw, err := os.ReadFile(filepath.Join(config.Dir(), "logs", "dispatch.jsonl"))
	if err != nil {
		t.Fatalf("no dispatch row for an executed dispatch: %v", err)
	}
	var row map[string]any
	if err := json.Unmarshal([]byte(strings.Split(strings.TrimSpace(string(raw)), "\n")[0]), &row); err != nil {
		t.Fatal(err)
	}
	if row["head"] == nil || row["head"] == "" {
		t.Errorf("the dispatch row names no head: %v", row)
	}
	if row["run_id"] == nil || row["run_id"] == "" {
		t.Errorf("the dispatch row carries no run_id, so nothing correlates it: %v", row)
	}

	// cost.jsonl is deliberately *not* written here. A CLI-agent head reports no
	// token usage, so there is no basis to price the call — and a $0.00 row
	// would read as "this was free", which is the #258/#261 defect class. The
	// call is still recorded above; only the price is withheld.
	if _, statErr := os.Stat(filepath.Join(config.Dir(), "logs", "cost.jsonl")); statErr == nil {
		billed, _ := os.ReadFile(filepath.Join(config.Dir(), "logs", "cost.jsonl"))
		t.Errorf("a head that reported no tokens was given a cost row, which reads "+
			"as a free call:\n%s", billed)
	}

	// And the handoff, which is what the next agent's --a2a reads.
	if _, err := os.Stat(filepath.Join(config.Dir(), "logs", "last_handoff.json")); err != nil {
		t.Errorf("no handoff was written: %v", err)
	}
}

// --tier pins the tier directly, bypassing the enum table.
func TestCLI_Dispatch_TierAndSystemPromptAndA2A(t *testing.T) {
	_, repo := dispatchable(t, "answered")

	if _, cobraOut, err := run(t, "dispatch", "--tier", "6",
		"--system", "be terse", "hello"); err != nil {
		t.Fatalf("`--tier 6 --system` failed: %v (%s)", err, cobraOut)
	}

	// --a2a prepends a prior handoff. A file that is not there must not fail the
	// run: a first dispatch pointed at a not-yet-written handoff still runs.
	if _, _, err := run(t, "dispatch", "--tier", "6",
		"--a2a", filepath.Join(repo, "absent.json"), "hello"); err != nil {
		t.Errorf("an absent --a2a file failed the dispatch: %v", err)
	}

	// A real handoff is read and injected.
	h := filepath.Join(repo, "handoff.json")
	if err := os.WriteFile(h, []byte(`{"from":"agent-1","task":"earlier","prior_output":"before"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, cobraOut, err := run(t, "dispatch", "--tier", "6", "--a2a", h, "continue"); err != nil {
		t.Errorf("a real --a2a handoff failed the dispatch: %v (%s)", err, cobraOut)
	}
}

// --local forces the local tier. With no local head it must refuse rather than
// quietly using a paid one — that is the entire point of the flag.
func TestCLI_Dispatch_LocalOnlyRefusesWhenNothingIsLocal(t *testing.T) {
	dispatchable(t, "answered")

	_, cobraOut, err := run(t, "dispatch", "--local", "keep this on the machine")
	if err == nil {
		t.Fatal("--local dispatched to a non-local head")
	}
	if !strings.Contains(err.Error()+cobraOut, "localOnly=true") {
		t.Errorf("error = %v, want it to name local-only as the cause", err)
	}
}

// A swarm dry run must name the heads and the estimated cost without firing
// anything — #167 made --dry-run mean the same thing in every mode.
func TestCLI_Dispatch_SwarmDryRunFiresNothing(t *testing.T) {
	dispatchable(t, "answered")

	out, cobraOut, err := run(t, "dispatch", "--swarm", "--swarm-mode", "all",
		"--dry-run", "--tier", "6", "implement a rate limiter")
	if err != nil {
		t.Fatalf("a swarm dry run failed: %v (%s)", err, cobraOut)
	}
	if !strings.Contains(out+cobraOut, "DRY RUN") {
		t.Errorf("the plan is not labelled as a dry run:\n%s", out+cobraOut)
	}
	if _, statErr := os.Stat(filepath.Join(config.Dir(), "logs", "cost.jsonl")); statErr == nil {
		t.Error("a swarm dry run wrote cost rows")
	}
	if ids, runErr := runlog.Runs(); runErr != nil || len(ids) != 0 {
		t.Errorf("a swarm dry run left run log entries: ids=%v err=%v", ids, runErr)
	}
}

// A real risk signal — PII in the prompt, or --irreversible/--production —
// must trigger SPRT mode on its own, not just --file's blast radius.
func TestCLI_Dispatch_RiskSignalsTriggerSPRTWithoutFileOrConfidence(t *testing.T) {
	dispatchable(t, "answered")

	out, cobraOut, err := run(t, "dispatch", "--irreversible", "--production",
		"--dry-run", "--tier", "6", "mail alice.smith@example.co.uk about it")
	if err != nil {
		t.Fatalf("dry run failed: %v (%s)", err, cobraOut)
	}
	combined := out + cobraOut
	if !strings.Contains(combined, "SPRT") {
		t.Errorf("a prompt with PII + --irreversible + --production did not select SPRT mode:\n%s", combined)
	}
	if !strings.Contains(combined, "irreversible=true") || !strings.Contains(combined, "pii=true") || !strings.Contains(combined, "prod=true") {
		t.Errorf("the defect line does not reflect all three risk signals:\n%s", combined)
	}
}

// With none of --confidence/--file/--irreversible/--production, and no PII,
// dispatch must not silently enter SPRT mode.
func TestCLI_Dispatch_NoRiskSignalsStaysInSingleDispatchMode(t *testing.T) {
	dispatchable(t, "answered")

	out, cobraOut, err := run(t, "dispatch", "--dry-run", "--tier", "6", "implement a rate limiter")
	if err != nil {
		t.Fatalf("dry run failed: %v (%s)", err, cobraOut)
	}
	if strings.Contains(out+cobraOut, "SPRT") {
		t.Errorf("a plain dispatch with no risk signal entered SPRT mode:\n%s", out+cobraOut)
	}
}

// An SPRT dry run is the same contract for --confidence.
func TestCLI_Dispatch_ConfidenceDryRunFiresNothing(t *testing.T) {
	dispatchable(t, "answered")

	out, cobraOut, err := run(t, "dispatch", "--confidence", "0.9",
		"--dry-run", "--tier", "6", "is this migration safe?")
	if err != nil {
		t.Fatalf("an SPRT dry run failed: %v (%s)", err, cobraOut)
	}
	if !strings.Contains(out+cobraOut, "SPRT") {
		t.Errorf("the plan does not say it is an SPRT run:\n%s", out+cobraOut)
	}
	if _, statErr := os.Stat(filepath.Join(config.Dir(), "logs", "cost.jsonl")); statErr == nil {
		t.Error("an SPRT dry run wrote cost rows")
	}
	if ids, runErr := runlog.Runs(); runErr != nil || len(ids) != 0 {
		t.Errorf("an SPRT dry run left run log entries: ids=%v err=%v", ids, runErr)
	}
}

// A swarm that actually runs must bill every head it fired, not just the winner.
func TestCLI_Dispatch_SwarmBillsEveryHead(t *testing.T) {
	dispatchable(t, "answered")

	if _, cobraOut, err := run(t, "dispatch", "--swarm", "--swarm-mode", "all",
		"--tier", "6", "compare approaches"); err != nil {
		t.Fatalf("a swarm run failed: %v (%s)", err, cobraOut)
	}
	raw, err := os.ReadFile(filepath.Join(config.Dir(), "logs", "cost.jsonl"))
	if err != nil {
		t.Fatalf("a swarm run billed nothing: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatal(err)
		}
		if row["swarm_mode"] != "all" {
			t.Errorf("row is not tagged with the swarm mode `hyctl stats` groups on: %v", row)
		}
	}
}

// ── edit, executed for real ───────────────────────────────────────────────────

func TestCLI_Edit_SucceedsAndRecordsTheEdit(t *testing.T) {
	_, repo := dispatchable(t, marked("package main\n\nfunc main() {}"))

	file := filepath.Join(repo, "main.go")
	if err := os.WriteFile(file, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, cobraOut, err := run(t, "edit", "--file", file,
		"--enum", "MODERATE", "--prompt", "add an empty main", "--no-validate")
	if err != nil {
		t.Fatalf("`hyctl edit` failed: %v (%s)", err, cobraOut)
	}

	// The result is JSON, because callers script against it.
	var res map[string]any
	if jerr := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); jerr != nil {
		t.Fatalf("`hyctl edit` did not emit JSON: %v\n%s", jerr, out)
	}
	if res["status"] != "ok" {
		t.Fatalf("status = %v, error %v", res["status"], res["error"])
	}
	if raw, _ := os.ReadFile(file); !strings.Contains(string(raw), "func main() {}") {
		t.Errorf("the file was not edited: %q", raw)
	}
	// last_edit.json is what `hyctl review` with no arguments reads.
	if _, statErr := os.Stat(filepath.Join(config.Dir(), "logs", "last_edit.json")); statErr != nil {
		t.Errorf("no last_edit.json, so `hyctl review` sees nothing: %v", statErr)
	}
}

// ── parallel, executed for real ───────────────────────────────────────────────

func TestCLI_Parallel_RunsABatchAndPersistsIt(t *testing.T) {
	_, repo := dispatchable(t, marked("edited content"))

	a := filepath.Join(repo, "a.txt")
	b := filepath.Join(repo, "b.txt")
	for _, f := range []string{a, b} {
		if err := os.WriteFile(f, []byte("before\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	spec := filepath.Join(repo, "batch.json")
	body := `[{"label":"one","enum":"MODERATE","file":"` + filepath.ToSlash(a) +
		`","prompt":"x","validate":false},` +
		`{"label":"two","enum":"MODERATE","file":"` + filepath.ToSlash(b) +
		`","prompt":"y","validate":false}]`
	if err := os.WriteFile(spec, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	out, cobraOut, err := run(t, "parallel", "--tasks", spec)
	if err != nil {
		t.Fatalf("`hyctl parallel` failed on a batch of two: %v (%s)", err, cobraOut)
	}
	var results []map[string]any
	if jerr := json.Unmarshal([]byte(strings.TrimSpace(out)), &results); jerr != nil {
		t.Fatalf("not a JSON array: %v\n%s", jerr, out)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want one per task", len(results))
	}
	for _, r := range results {
		if r["status"] != "ok" {
			t.Errorf("task %v failed: %v", r["label"], r["error"])
		}
	}
	// last_parallel.json is what `hyctl review` reads to know what changed.
	if _, statErr := os.Stat(filepath.Join(config.Dir(), "logs", "last_parallel.json")); statErr != nil {
		t.Errorf("the batch was not persisted for review: %v", statErr)
	}
}

// ── oracle, passing verdict ───────────────────────────────────────────────────

// A passing oracle exits 0, so its body runs in process. It must report the
// calibrated evidence, since that number is what outweighs a model's vote.
func TestCLI_OracleVerify_PassingVerdictReportsEvidence(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/usr/bin/true is POSIX; the exit-code half is covered on every " +
			"platform in exit_code_test.go")
	}
	populated(t)

	out, cobraOut, err := run(t, "oracle", "verify", "/usr/bin/true")
	if err != nil {
		t.Fatalf("a passing oracle errored: %v (%s)", err, cobraOut)
	}
	combined := out + cobraOut
	if !strings.Contains(combined, "nats") {
		t.Errorf("the calibrated evidence is not reported:\n%s", combined)
	}

	// --record trains the calibrator from the true outcome, which is how an
	// oracle's weight is learned rather than assumed.
	if _, cobraOut, err := run(t, "oracle", "verify", "/usr/bin/true",
		"--source", "verifier:test", "--domain", "go", "--record", "correct"); err != nil {
		t.Fatalf("`--record correct` failed: %v (%s)", err, cobraOut)
	}
	cal, _, err := run(t, "trust", "calibration")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cal, "verifier:test") {
		t.Errorf("the recorded oracle is not in the calibration table:\n%s", cal)
	}
}

// ── mcp check, allowed ────────────────────────────────────────────────────────

// An allowed check exits 0, so its body runs in process. The decision must be
// recorded either way — an unlogged allow is not accountability.
func TestCLI_MCPCheck_AllowedDecisionIsRecorded(t *testing.T) {
	s := populated(t)

	// A policy that allows reads under /tmp and denies everything else.
	policy := `{"rules":[{"action":"read","resource":"/tmp/**","decision":"allow"}]}`
	polDir := filepath.Join(config.Dir())
	if err := os.MkdirAll(polDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(polDir, "mcp_policy.json"), []byte(policy), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = s

	out, cobraOut, err := run(t, "mcp", "check", "fs.read",
		"--action", "read", "--resource", "/tmp/allowed.txt", "--agent", "test-agent")
	if err != nil {
		t.Fatalf("an allowed check errored: %v (%s)", err, cobraOut)
	}
	if !strings.Contains(strings.ToUpper(out+cobraOut), "ALLOW") {
		t.Errorf("the decision is not stated:\n%s", out+cobraOut)
	}

	log, logCobra, err := run(t, "mcp", "log")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log+logCobra, "test-agent") {
		t.Errorf("the allow was not recorded; an unlogged decision is not "+
			"accountability:\n%s", log+logCobra)
	}
}

// ── trust benchmark ───────────────────────────────────────────────────────────

// `hyctl trust benchmark` reports the ensemble's own numbers. With a calibration
// on disk it must produce a table rather than an empty one.
func TestCLI_TrustBenchmark_RunsAgainstACalibration(t *testing.T) {
	populated(t)

	if _, _, err := run(t, "trust", "record",
		"--source", "model:a", "--domain", "go",
		"--said-correct", "--outcome", "correct"); err != nil {
		t.Fatal(err)
	}
	out, cobraOut, err := run(t, "trust", "benchmark")
	if err != nil {
		t.Fatalf("`hyctl trust benchmark` failed: %v (%s)", err, cobraOut)
	}
	if strings.TrimSpace(out+cobraOut) == "" {
		t.Error("printed nothing with a calibration on disk")
	}
}

// ── stats: every grouping ─────────────────────────────────────────────────────

// `hyctl stats` has six grouping modes and a session filter. Each is a distinct
// answer to "where did the money go", and a mode that silently renders the
// wrong grouping is a wrong answer to that question.
func TestCLI_Stats_EveryGrouping(t *testing.T) {
	populated(t)

	modes := []struct {
		name string
		args []string
		want string // something that must appear
	}{
		{"by model", []string{"stats", "--model"}, "claude-opus"},
		{"by tier", []string{"stats", "--tier"}, "1"},
		{"by day", []string{"stats", "--day"}, "-"},
		{"swarm only", []string{"stats", "--swarm"}, ""},
		{"windowed", []string{"stats", "--days", "7"}, "claude-opus"},
		{"json", []string{"stats", "--json"}, ""},
	}
	for _, m := range modes {
		t.Run(m.name, func(t *testing.T) {
			out, cobraOut, err := run(t, m.args...)
			if err != nil {
				t.Fatalf("`hyctl %s` failed: %v (%s)", strings.Join(m.args, " "), err, cobraOut)
			}
			combined := out + cobraOut
			if strings.TrimSpace(combined) == "" {
				t.Fatal("printed nothing with rows on disk")
			}
			if m.want != "" && !strings.Contains(combined, m.want) {
				t.Errorf("output does not contain %q:\n%s", m.want, combined)
			}
			if m.args[len(m.args)-1] == "--json" {
				var v any
				if jerr := json.Unmarshal([]byte(strings.TrimSpace(out)), &v); jerr != nil {
					t.Errorf("--json is not parseable: %v\n%s", jerr, out)
				}
			}
		})
	}

	// The session filter takes priority over every other flag, and an unknown
	// session must error rather than rendering an empty table that reads as
	// "this session spent nothing".
	out, cobraOut, err := run(t, "stats", "--session", "run-1")
	if err != nil {
		t.Fatalf("`--session run-1` failed: %v (%s)", err, cobraOut)
	}
	if !strings.Contains(out+cobraOut, "run-1") {
		t.Errorf("the session is not named in the report:\n%s", out+cobraOut)
	}
	if _, _, err := run(t, "stats", "--session", "no-such-session"); err == nil {
		t.Error("an unknown session exited 0, reporting an empty breakdown as if " +
			"the session had spent nothing")
	}
}

// ── pricing list: filter and JSON ─────────────────────────────────────────────

func TestCLI_PricingList_FilterAndJSON(t *testing.T) {
	populated(t)

	// The JSON surface must be an array even when the filter matches nothing —
	// a jq pipeline iterating it should get zero elements, not a parse error.
	out, _, err := run(t, "pricing", "list", "--json")
	if err != nil {
		t.Fatalf("`pricing list --json` failed: %v", err)
	}
	trimmed := strings.TrimSpace(out)
	if trimmed != "null" {
		var rows []map[string]any
		if jerr := json.Unmarshal([]byte(trimmed), &rows); jerr != nil {
			t.Fatalf("not a JSON array: %v\n%s", jerr, trimmed)
		}
	}

	// A filter narrows the table rather than emptying it.
	all, _, err := run(t, "pricing", "list")
	if err != nil {
		t.Fatal(err)
	}
	filtered, _, err := run(t, "pricing", "list", "claude")
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) >= len(all) && strings.Count(all, "\n") > 4 {
		t.Errorf("a filter did not narrow the table: %d vs %d bytes",
			len(filtered), len(all))
	}
}

// ── models sync ───────────────────────────────────────────────────────────────

// `hyctl models sync` imports the OpenRouter catalogue. With no network it must
// fail rather than reporting a successful import of nothing, which would leave
// the overlay looking populated.
func TestCLI_ModelsSync_ReportsAFailedFetch(t *testing.T) {
	populated(t)

	out, cobraOut, err := run(t, "models", "sync")
	if err == nil {
		t.Errorf("`hyctl models sync` reported success with no network:\n%s", out+cobraOut)
	}
	if !strings.Contains(err.Error(), "pricing refresh") {
		t.Errorf("the error does not tell the user what to do: %v", err)
	}

	// --dry-run hits the same wall: it cannot preview an import from a
	// catalogue it does not have.
	if _, _, err := run(t, "models", "sync", "--dry-run"); err == nil {
		t.Error("`models sync --dry-run` reported a preview with no catalogue")
	}
}

// `models add` validates before writing: a model with no provider or a
// nonsensical score cannot be routed to, so accepting it writes an overlay
// entry that fails at selection time instead.
func TestCLI_ModelsAdd_ValidatesBeforeWriting(t *testing.T) {
	populated(t)

	before, _, err := run(t, "models", "list")
	if err != nil {
		t.Fatal(err)
	}

	// A duplicate id must be refused rather than silently shadowing the first.
	if _, _, err := run(t, "models", "add", "dup-test",
		"--name", "Dup", "--provider", "p", "--cap-score", "50"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, "models", "add", "dup-test",
		"--name", "Dup Again", "--provider", "p", "--cap-score", "60"); err == nil {
		listed, _, _ := run(t, "models", "list")
		if strings.Count(listed, "dup-test") > 1 {
			t.Error("adding a duplicate id created two entries; selection would " +
				"pick one arbitrarily")
		}
	}
	_ = before
}

// ── cost by-pool and totals ───────────────────────────────────────────────────

func TestCLI_Cost_RemainingSubcommands(t *testing.T) {
	populated(t)

	for _, args := range [][]string{
		{"cost", "tail"},
		{"cost", "tail", "1"},
		{"cost", "json", "--since", "2020-01-01T00:00:00Z"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			out, cobraOut, err := run(t, args...)
			if err != nil {
				t.Fatalf("`hyctl %s` failed: %v (%s)", strings.Join(args, " "), err, cobraOut)
			}
			if strings.TrimSpace(out+cobraOut) == "" {
				t.Error("printed nothing with rows on disk")
			}
		})
	}

	// An unparsable --since must be refused rather than silently returning
	// everything, which would look like the filter worked.
	if _, _, err := run(t, "cost", "json", "--since", "yesterday"); err == nil {
		t.Error("an unparsable --since was accepted; the user would get unfiltered " +
			"output believing it was filtered")
	}
}

// ── mcp report --json ─────────────────────────────────────────────────────────

func TestCLI_MCPReport_JSONShape(t *testing.T) {
	s := populated(t)

	// Seed a decision so the report has something to summarise.
	_, _ = runBinary(t, s, "mcp", "check", "fs.read",
		"--action", "read", "--resource", "/tmp/x", "--agent", "a")

	out, _, err := run(t, "mcp", "report", "--json")
	if err != nil {
		t.Fatalf("`mcp report --json` failed: %v", err)
	}
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		t.Fatal("printed nothing")
	}
	var v any
	if jerr := json.Unmarshal([]byte(trimmed), &v); jerr != nil {
		t.Errorf("not valid JSON: %v\n%s", jerr, trimmed)
	}
}

// ── context entropy --json ────────────────────────────────────────────────────

func TestCLI_ContextEntropy_JSONAndStdin(t *testing.T) {
	populated(t)

	f := filepath.Join(t.TempDir(), "sample.go")
	if err := os.WriteFile(f, []byte(strings.Repeat("func f() {}\n", 60)), 0o600); err != nil {
		t.Fatal(err)
	}

	out, _, err := run(t, "context", "entropy", f, "--json")
	if err != nil {
		t.Fatalf("`context entropy --json` failed: %v", err)
	}
	var doc map[string]any
	if jerr := json.Unmarshal([]byte(strings.TrimSpace(out)), &doc); jerr != nil {
		t.Fatalf("not a JSON object: %v\n%s", jerr, out)
	}
	// The useful-token figure is what the compaction governor acts on.
	if len(doc) == 0 {
		t.Errorf("the JSON carries no fields:\n%s", out)
	}
}

// ── mcp: the whole surface ────────────────────────────────────────────────────

// The ledger is the accountability record for what agents touched. Every view
// of it is how a human answers "what did this agent do", so each has to work
// against real events rather than only on an empty ledger.
func TestCLI_MCP_EverySurfaceAgainstRealEvents(t *testing.T) {
	s := populated(t)

	// A policy that allows reads under /tmp and denies everything else.
	//
	// "default":"deny" is required: a zero Default is treated as Allow by
	// design — Hydra records everything but blocks nothing unless a rule says
	// so — so without it a non-matching resource falls through to allowed.
	policy := `{"default":"deny","rules":[` +
		`{"action":"read","resource":"/tmp/**","decision":"allow"}]}`
	if err := os.WriteFile(filepath.Join(config.Dir(), "mcp_policy.json"),
		[]byte(policy), 0o600); err != nil {
		t.Fatal(err)
	}

	// An allow and a deny, both recorded.
	if _, _, err := run(t, "mcp", "check", "fs.read", "--action", "read",
		"--resource", "/tmp/ok.txt", "--agent", "agent-a"); err != nil {
		t.Fatalf("the allowed check errored: %v", err)
	}
	// The denied one exits 3, so it goes through the binary.
	if code, _ := runBinary(t, s, "mcp", "check", "fs.read", "--action", "read",
		"--resource", "/etc/shadow", "--agent", "agent-b"); code != 3 {
		t.Errorf("a denied check exited %d, want 3 so callers can gate on it", code)
	}

	t.Run("log lists both", func(t *testing.T) {
		out, cobraOut, err := run(t, "mcp", "log")
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"agent-a", "agent-b"} {
			if !strings.Contains(out+cobraOut, want) {
				t.Errorf("the log omits %s:\n%s", want, out+cobraOut)
			}
		}
	})

	t.Run("log --agent filters", func(t *testing.T) {
		out, cobraOut, err := run(t, "mcp", "log", "--agent", "agent-a")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out+cobraOut, "agent-b") {
			t.Errorf("--agent agent-a showed agent-b's events:\n%s", out+cobraOut)
		}
		if !strings.Contains(out+cobraOut, "agent-a") {
			t.Errorf("--agent agent-a showed nothing:\n%s", out+cobraOut)
		}
	})

	t.Run("log --denied shows only denials", func(t *testing.T) {
		out, cobraOut, err := run(t, "mcp", "log", "--denied")
		if err != nil {
			t.Fatal(err)
		}
		combined := out + cobraOut
		if strings.Contains(combined, "/tmp/ok.txt") {
			t.Errorf("--denied included an allowed access:\n%s", combined)
		}
		if !strings.Contains(combined, "agent-b") && !strings.Contains(combined, "shadow") {
			t.Errorf("--denied showed no denial:\n%s", combined)
		}
	})

	t.Run("report summarises both outcomes", func(t *testing.T) {
		out, cobraOut, err := run(t, "mcp", "report")
		if err != nil {
			t.Fatal(err)
		}
		combined := strings.ToLower(out + cobraOut)
		if !strings.Contains(combined, "allow") || !strings.Contains(combined, "den") {
			t.Errorf("the report does not distinguish allowed from denied:\n%s", out+cobraOut)
		}
	})

	t.Run("report --json parses and carries counts", func(t *testing.T) {
		out, _, err := run(t, "mcp", "report", "--json")
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if jerr := json.Unmarshal([]byte(strings.TrimSpace(out)), &doc); jerr != nil {
			t.Fatalf("not a JSON object: %v\n%s", jerr, out)
		}
		if len(doc) == 0 {
			t.Errorf("the JSON report carries no fields:\n%s", out)
		}
	})
}

// ── cost: every breakdown ─────────────────────────────────────────────────────

// `hyctl cost` has five breakdowns plus a tail and a raw JSON view. Each answers
// a different question about where the money went.
func TestCLI_Cost_EveryBreakdown(t *testing.T) {
	populated(t)

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"cost", "today"}, ""},
		{[]string{"cost", "all"}, ""},
		{[]string{"cost", "by-pool"}, ""},
		{[]string{"cost", "today", "--json"}, ""},
		{[]string{"cost", "all", "--json"}, ""},
		{[]string{"cost", "by-pool", "--json"}, ""},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			out, cobraOut, err := run(t, tc.args...)
			if err != nil {
				t.Fatalf("failed: %v (%s)", err, cobraOut)
			}
			combined := out + cobraOut
			if strings.TrimSpace(combined) == "" {
				t.Fatal("printed nothing with rows on disk")
			}
			if tc.args[len(tc.args)-1] == "--json" {
				var v any
				if jerr := json.Unmarshal([]byte(strings.TrimSpace(out)), &v); jerr != nil {
					t.Errorf("--json is not parseable: %v\n%s", jerr, out)
				}
			}
		})
	}

	// The per-run and per-task views must scope to their id, and a tail of N
	// must not exceed the rows that exist.
	byRun, _, err := run(t, "cost", "by-run", "run-1", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(byRun) != "" {
		var v any
		if jerr := json.Unmarshal([]byte(strings.TrimSpace(byRun)), &v); jerr != nil {
			t.Errorf("by-run --json is not parseable: %v\n%s", jerr, byRun)
		}
	}
	tail, cobraOut, err := run(t, "cost", "tail", "99")
	if err != nil {
		t.Fatalf("`cost tail 99` failed with 2 rows on disk: %v (%s)", err, cobraOut)
	}
	if strings.TrimSpace(tail+cobraOut) == "" {
		t.Error("a tail larger than the log printed nothing")
	}
}

// ── models: list shapes ───────────────────────────────────────────────────────

func TestCLI_Models_ListShapesAndFilter(t *testing.T) {
	populated(t)

	if _, _, err := run(t, "models", "add", "listed-model",
		"--name", "Listed", "--provider", "vendor", "--cap-score", "77"); err != nil {
		t.Fatal(err)
	}

	// The table view names it.
	out, cobraOut, err := run(t, "models", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out+cobraOut, "listed-model") {
		t.Errorf("the added model is not listed:\n%s", out+cobraOut)
	}

	// The JSON view carries its score, which is what selection ranks on.
	jsonOut, _, err := run(t, "models", "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(jsonOut, "listed-model") {
		t.Errorf("the JSON view omits the added model:\n%s", jsonOut)
	}
	if !strings.Contains(jsonOut, "77") {
		t.Errorf("the JSON view omits the capability score selection ranks on:\n%s", jsonOut)
	}
	var v any
	if jerr := json.Unmarshal([]byte(strings.TrimSpace(jsonOut)), &v); jerr != nil {
		t.Errorf("not valid JSON: %v", jerr)
	}
}

// ── probe ─────────────────────────────────────────────────────────────────────

// `hyctl probe` is the first thing a user runs when routing surprises them. It
// must list a head it can drive and mark one it cannot, with the reason (#248).
func TestCLI_Probe_MarksUnroutableHeads(t *testing.T) {
	s, _ := dispatchable(t, "answered")

	// An ollama binary with no server behind it: discovered, not routable.
	s.FakeBinary(t, "ollama")

	out, cobraOut, err := run(t, "probe")
	if err != nil {
		t.Fatalf("`hyctl probe` failed: %v (%s)", err, cobraOut)
	}
	combined := out + cobraOut
	if !strings.Contains(combined, "cody") && !strings.Contains(strings.ToLower(combined), "cody") {
		t.Errorf("probe does not list the routable head:\n%s", combined)
	}
	// The unroutable one must be marked, and the reason given — pointing at
	// probe is only useful if probe explains itself (#248).
	if strings.Contains(combined, "ollama") && !strings.Contains(combined, "✗") {
		t.Errorf("the unroutable ollama binary is listed without being marked:\n%s", combined)
	}
}

// The policy's default is permissive by design: Hydra records every access but
// blocks nothing unless a rule says so. That is a deliberate choice and worth
// pinning, because the opposite default would silently break every agent the
// first time a policy file appeared.
func TestCLI_MCPCheck_DefaultIsPermissiveUntilToldOtherwise(t *testing.T) {
	s := populated(t)

	// A policy with rules but no explicit default.
	permissive := `{"rules":[{"action":"write","resource":"/etc/**","decision":"deny"}]}`
	if err := os.WriteFile(filepath.Join(config.Dir(), "mcp_policy.json"),
		[]byte(permissive), 0o600); err != nil {
		t.Fatal(err)
	}

	// A resource no rule mentions is allowed.
	if code, out := runBinary(t, s, "mcp", "check", "fs.read", "--action", "read",
		"--resource", "/var/data/x", "--agent", "a"); code != 0 {
		t.Errorf("an unmentioned resource exited %d; the documented default is "+
			"allow:\n%s", code, out)
	}
	// The rule that does mention it still denies.
	if code, out := runBinary(t, s, "mcp", "check", "fs.write", "--action", "write",
		"--resource", "/etc/passwd", "--agent", "a"); code != 3 {
		t.Errorf("a rule-matched deny exited %d, want 3:\n%s", code, out)
	}
}

// ── mcp check: classification ─────────────────────────────────────────────────

// The ledger is classification-aware: a policy rule can key on a
// data-sensitivity tag, and the tag is derived from the content being accessed
// when it is not given explicitly. That derivation is what makes a PII rule
// apply to content nobody remembered to label.
func TestCLI_MCPCheck_ClassificationFromContentAndExplicit(t *testing.T) {
	s := populated(t)

	// Deny anything classified as PII, allow the rest.
	policy := `{"rules":[{"classification":"pii","decision":"deny"}]}`
	if err := os.WriteFile(filepath.Join(config.Dir(), "mcp_policy.json"),
		[]byte(policy), 0o600); err != nil {
		t.Fatal(err)
	}

	// Content that looks like PII must be classified as such without anyone
	// saying so, and therefore denied.
	code, out := runBinary(t, s, "mcp", "check", "fs.read",
		"--action", "read", "--resource", "/tmp/customers.csv", "--agent", "a",
		"--content", "name,ssn\nAda Lovelace,123-45-6789")
	if code != 3 {
		t.Errorf("PII content exited %d, want the deny code 3 — the classification "+
			"was not derived from the content:\n%s", code, out)
	}

	// Content with nothing sensitive in it is not classified, so the PII rule
	// does not match and the permissive default applies.
	code, out = runBinary(t, s, "mcp", "check", "fs.read",
		"--action", "read", "--resource", "/tmp/notes.txt", "--agent", "a",
		"--content", "just some ordinary notes about the build")
	if code != 0 {
		t.Errorf("ordinary content exited %d, want 0:\n%s", code, out)
	}

	// An explicit --classification overrides the content scan, so an operator
	// can label something the detector would miss.
	code, out = runBinary(t, s, "mcp", "check", "fs.read",
		"--action", "read", "--resource", "/tmp/notes.txt", "--agent", "a",
		"--content", "nothing sensitive here", "--classification", "pii")
	if code != 3 {
		t.Errorf("an explicit --classification pii exited %d, want 3 — the "+
			"explicit tag must beat the content scan:\n%s", code, out)
	}
}

// A malformed --params or a policy file that will not parse must be refused.
// A policy that silently fails to load is a gate that is not gating.
func TestCLI_MCPCheck_RefusesBadInput(t *testing.T) {
	s := populated(t)

	if code, out := runBinary(t, s, "mcp", "check", "fs.read",
		"--action", "read", "--resource", "/tmp/x", "--agent", "a",
		"--params", "{not json"); code == 0 {
		t.Errorf("a malformed --params was accepted:\n%s", out)
	}
	if code, out := runBinary(t, s, "mcp", "check", "fs.read",
		"--action", "not-an-action", "--resource", "/tmp/x", "--agent", "a"); code == 0 {
		t.Errorf("an unknown --action was accepted:\n%s", out)
	}

	// A policy file that does not parse must fail loudly rather than falling
	// back to "no rules", which would allow everything.
	if err := os.WriteFile(filepath.Join(config.Dir(), "mcp_policy.json"),
		[]byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out := runBinary(t, s, "mcp", "check", "fs.read",
		"--action", "read", "--resource", "/tmp/x", "--agent", "a"); code == 0 {
		t.Errorf("an unparsable policy was treated as no rules, which allows "+
			"everything:\n%s", out)
	}
}

// ── models add: validation ────────────────────────────────────────────────────

// A model's capability score is what selection ranks on, so a nonsensical one
// would place a head at a tier that does not reflect it.
func TestCLI_ModelsAdd_ValidatesTheCapabilityScore(t *testing.T) {
	populated(t)

	for _, score := range []string{"-1", "101", "1000"} {
		t.Run("score "+score, func(t *testing.T) {
			_, cobraOut, err := run(t, "models", "add", "bad-score-"+score,
				"--name", "X", "--provider", "p", "--cap-score", score)
			if err == nil {
				t.Fatalf("--cap-score %s was accepted", score)
			}
			if !strings.Contains(err.Error()+cobraOut, "0") {
				t.Errorf("the error does not state the valid range: %v", err)
			}
		})
	}

	// The boundaries are valid.
	for _, score := range []string{"0", "100"} {
		if _, _, err := run(t, "models", "add", "edge-"+score,
			"--name", "X", "--provider", "p", "--cap-score", score); err != nil {
			t.Errorf("--cap-score %s was refused: %v", score, err)
		}
	}

	// The name defaults to the id rather than being written empty — an unnamed
	// model renders as a blank row in `hyctl models list`.
	if _, _, err := run(t, "models", "add", "unnamed-model",
		"--provider", "p", "--cap-score", "50"); err != nil {
		t.Fatal(err)
	}
	out, _, err := run(t, "models", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "unnamed-model") {
		t.Errorf("a model added without --name is not listed:\n%s", out)
	}
}

// Overriding a built-in is allowed — that is how a user retunes a score without
// a rebuild — but removing one is not, since the built-in would come straight
// back and the removal would look like it worked.
func TestCLI_ModelsRemove_CannotRemoveABuiltIn(t *testing.T) {
	populated(t)

	// `claude` is in the embedded data.json.
	_, cobraOut, err := run(t, "models", "remove", "claude")
	if err == nil {
		t.Fatal("a built-in model was removed; it would reappear on the next run")
	}
	if !strings.Contains(err.Error()+cobraOut, "built-in") {
		t.Errorf("the error does not explain why: %v", err)
	}

	// Overriding it is fine, and then the override can be removed.
	if _, _, err := run(t, "models", "add", "claude",
		"--name", "My Claude", "--provider", "anthropic", "--cap-score", "99"); err != nil {
		t.Fatalf("overriding a built-in was refused: %v", err)
	}
	if _, _, err := run(t, "models", "remove", "claude"); err != nil {
		t.Errorf("removing an override was refused: %v", err)
	}
}

// ── graph: json and parallel ──────────────────────────────────────────────────

func TestCLI_Graph_ParallelAndJSON(t *testing.T) {
	populated(t)
	g := seedGraph(t)

	out, cobraOut, err := run(t, "graph", "parallel",
		"internal/auth/token.go", "internal/api/login.go", "--graph", g, "--json")
	if err != nil {
		t.Fatalf("`graph parallel --json` failed: %v (%s)", err, cobraOut)
	}
	var doc map[string]any
	if jerr := json.Unmarshal([]byte(strings.TrimSpace(out)), &doc); jerr != nil {
		t.Fatalf("not a JSON object: %v\n%s", jerr, out)
	}
	if len(doc) == 0 {
		t.Errorf("the JSON carries no fields:\n%s", out)
	}

	// A graph file that is not there must be an error, not a silent empty graph
	// that reports every file as safe to change.
	if _, _, err := run(t, "graph", "blast", "x.go",
		"--graph", filepath.Join(t.TempDir(), "absent.json")); err == nil {
		out, _, _ := run(t, "graph", "blast", "x.go",
			"--graph", filepath.Join(t.TempDir(), "absent.json"))
		if !strings.Contains(strings.ToLower(out), "no graph") &&
			!strings.Contains(strings.ToLower(out), "not") {
			t.Errorf("a missing graph reported a radius without saying it had no "+
				"data:\n%s", out)
		}
	}
}

// ── review qa ─────────────────────────────────────────────────────────────────

// `hyctl review qa` sends a diff to a head for review. With no diff there is
// nothing to pay for, and it must refuse rather than dispatching an empty prompt.
func TestCLI_ReviewQA_RefusesWithNoDiff(t *testing.T) {
	_, repo := dispatchable(t, "APPROVED looks fine")

	clean := filepath.Join(repo, "clean.go")
	if err := os.WriteFile(clean, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, cobraOut, err := run(t, "review", "qa", clean); err == nil {
		t.Error("`review qa` dispatched a review for a file with no diff")
	} else if !strings.Contains(err.Error()+cobraOut, "no diff") {
		t.Errorf("error = %v, want it to say there is no diff", err)
	}

	// With a real diff it dispatches and returns the verdict.
	edited := filepath.Join(repo, "edited.go")
	if err := os.WriteFile(edited, []byte("package main\n\nfunc f() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(edited+".hydra-bak", []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, cobraOut, err := run(t, "review", "qa", edited, "--tier", "6")
	if err != nil {
		t.Fatalf("`review qa` failed on a real diff: %v (%s)", err, cobraOut)
	}
	if !strings.Contains(out+cobraOut, "APPROVED") {
		t.Errorf("the head's verdict is not reported:\n%s", out+cobraOut)
	}
}

// ── init: the wizard needs a terminal ─────────────────────────────────────────

// `hyctl init` and the bare `hyctl` on a fresh install both open an interactive
// wizard. There is no unit test for "the wizard rendered", but there is one for
// the thing that actually goes wrong in practice: run non-interactively — from
// a CI job, a Dockerfile, a script — it must fail fast with a message naming the
// cause, not hang waiting for input nobody is going to give it.
func TestCLI_Init_FailsFastWithNoTerminal(t *testing.T) {
	testutil.NewSandbox(t) // no config: this is a fresh install

	done := make(chan error, 1)
	go func() {
		_, _, err := run(t, "init")
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("`hyctl init` reported success with no terminal to render on")
		}
		msg := strings.ToLower(err.Error())
		if !strings.Contains(msg, "terminal") {
			t.Errorf("error = %v, want it to name the missing terminal", err)
		}
		// The message has to say what to do instead, since the caller is a
		// script that cannot answer a prompt.
		if !strings.Contains(msg, "config.toml") {
			t.Errorf("error = %v, want it to point at configuring Hydra directly", err)
		}
	case <-time.After(20 * time.Second):
		// The failure mode this guards, and it was real: on Windows there is no
		// /dev/tty to fail opening, so the wizard blocked reading stdin
		// forever and a Dockerfile or CI job running `hyctl init` wedged with
		// no output. Only the Windows leg of the matrix showed it.
		t.Fatal("`hyctl init` hung with no terminal instead of failing")
	}
}

// The bare `hyctl` with no config runs the wizard rather than printing help —
// a first-run user who types just the binary name should be set up, not handed
// a flag list. With a config it prints help instead.
func TestCLI_BareInvocation_WizardOnFirstRunHelpAfterwards(t *testing.T) {
	t.Run("no config runs the wizard", func(t *testing.T) {
		testutil.NewSandbox(t)

		done := make(chan error, 1)
		go func() {
			_, _, err := run(t)
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil {
				t.Error("the bare command succeeded with no config and no terminal; " +
					"it should have tried the wizard and failed on the TTY")
			}
		case <-time.After(20 * time.Second):
			t.Fatal("the bare command hung")
		}
	})

	t.Run("with a config prints help", func(t *testing.T) {
		cliSandbox(t) // saves a config

		out, cobraOut, err := run(t)
		if err != nil {
			t.Fatalf("the bare command failed with a config present: %v", err)
		}
		combined := out + cobraOut
		if !strings.Contains(combined, "Available Commands") &&
			!strings.Contains(combined, "Usage") {
			t.Errorf("the bare command did not print help:\n%s", combined)
		}
		// Help must list the subcommands a user needs next.
		for _, want := range []string{"dispatch", "status", "cost"} {
			if !strings.Contains(combined, want) {
				t.Errorf("help omits %q:\n%s", want, combined)
			}
		}
	})
}

// `hyctl tui` opens the same kind of interactive UI and needs the same guard —
// otherwise it is the second command that hangs a script.
func TestCLI_Tui_FailsFastWithNoTerminal(t *testing.T) {
	cliSandbox(t)

	done := make(chan error, 1)
	go func() {
		_, _, err := run(t, "tui")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("`hyctl tui` reported success with no terminal")
		}
		if !strings.Contains(strings.ToLower(err.Error()), "terminal") {
			t.Errorf("error = %v, want it to name the missing terminal", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("`hyctl tui` hung with no terminal")
	}

	// --snapshot is the non-interactive path and must still work: it is what
	// the docs preview is generated from, in CI, with no terminal.
	out, _, err := run(t, "tui", "--snapshot")
	if err != nil {
		t.Fatalf("`hyctl tui --snapshot` needs no terminal but failed: %v", err)
	}
	if strings.TrimSpace(out) == "" {
		t.Error("--snapshot rendered nothing")
	}
}
