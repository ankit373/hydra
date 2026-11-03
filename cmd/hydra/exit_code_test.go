// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/testutil"
)

// Exit codes are a contract: `hyctl edit` returns 2 on a failed edit and the
// MCP gate returns 3 on a denial, specifically so a shell script can branch on
// them. They cannot be asserted in-process, os.Exit takes the test binary with
// it, so these drive a real binary.

var hyctlBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "hyctl-cli-contract")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	hyctlBin = filepath.Join(dir, "hyctl")
	if runtime.GOOS == "windows" {
		hyctlBin += ".exe"
	}
	build := exec.Command("go", "build", "-o", hyctlBin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		// Leave hyctlBin empty; the exit-code tests skip and say why rather
		// than failing the whole package for a build-environment problem.
		hyctlBin = ""
		_, _ = os.Stderr.WriteString("cli contract: could not build hyctl: " +
			err.Error() + "\n" + string(out) + "\n")
	}
	os.Exit(m.Run())
}

// requireHyctl skips when TestMain could not build the binary, which is an
// environment problem rather than a failure. One call site, so the suite's
// skip budget counts this concern once (see covergate).
func requireHyctl(t *testing.T) {
	t.Helper()
	if hyctlBin == "" {
		t.Skip("hyctl could not be built in this environment")
	}
}

// exitCode runs the built binary in a fresh sandboxed HOME.
func exitCode(t *testing.T, args ...string) (code int, output string) {
	t.Helper()
	requireHyctl(t)
	return runBinary(t, testutil.NewSandbox(t), args...)
}

// runBinary runs the built binary against an existing sandbox, so a test can
// seed the files a command reads and still observe its exit code.
//
// Any command that calls os.Exit has to be driven this way: in-process, the
// exit takes the test binary down with it. `hyctl edit` returns 2, and the MCP
// gate and the oracle return 3, deliberately, so a shell script can branch on
// the verdict, which means the exit code *is* the contract for those.
func runBinary(t *testing.T, s *testutil.Sandbox, args ...string) (code int, output string) {
	t.Helper()
	requireHyctl(t)

	cmd := exec.Command(hyctlBin, args...)
	cmd.Env = append(os.Environ(),
		"HOME="+s.Home,
		"USERPROFILE="+s.Home,
		"HYDRA_HOME="+s.HydraHome,
		"HYDRA_NO_UPDATE_CHECK=1",
		"PATH="+s.BinDir,
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return 0, string(out)
	}
	var exitErr *exec.ExitError
	if ok := asExitError(err, &exitErr); ok {
		return exitErr.ExitCode(), string(out)
	}
	t.Fatalf("could not run hyctl: %v", err)
	return -1, ""
}

func asExitError(err error, target **exec.ExitError) bool {
	if e, ok := err.(*exec.ExitError); ok {
		*target = e
		return true
	}
	return false
}

// Success is 0 and a typo is not.
func TestExitCodes_SuccessAndFailureAreDistinguishable(t *testing.T) {
	if code, out := exitCode(t, "version"); code != 0 {
		t.Errorf("`hyctl version` exited %d, want 0:\n%s", code, out)
	}
	if code, _ := exitCode(t, "--help"); code != 0 {
		t.Errorf("`hyctl --help` exited %d, want 0", code)
	}
	if code, _ := exitCode(t, "definitely-not-a-command"); code == 0 {
		t.Error("an unknown subcommand exited 0; a script would treat the typo as success")
	}
	if code, _ := exitCode(t, "edit", "--file", "relative.go", "--enum", "SIMPLE", "--prompt", "x"); code == 0 {
		t.Error("`hyctl edit` with a relative path exited 0")
	}
}

// A command with no config must fail rather than proceeding against defaults
// the user never chose.
func TestExitCodes_MissingConfigFails(t *testing.T) {
	code, out := exitCode(t, "dispatch", "--enum", "SIMPLE", "hello")
	if code == 0 {
		t.Errorf("dispatch exited 0 with no config:\n%s", out)
	}
	if !strings.Contains(out, "init") {
		t.Errorf("the failure does not point at `hyctl init`:\n%s", out)
	}
}

// The MCP gate's exit code is what a caller branches on to stop an agent
// touching a file, and `ask` withholds permission exactly as `deny` does.
//
// This asserts the *recorded decision* as well as the code, because the version
// it replaces passed no <tool> argument: Cobra's ExactArgs(1) rejected the call
// with exit 1 before the gate ran, so the test was green on an argument-count
// error and stayed green with os.Exit deleted outright (#1160).
func TestExitCodes_MCPWithheldAccessIsNonZero(t *testing.T) {
	requireHyctl(t)
	for _, tc := range []struct {
		decision string
		wantCode int
	}{{"deny", 3}, {"ask", 4}} {
		t.Run(tc.decision, func(t *testing.T) {
			s := testutil.NewSandbox(t)
			rule := `{"default":"allow","rules":[{"resource":"/etc/shadow","decision":"` +
				tc.decision + `"}]}`
			if err := os.WriteFile(filepath.Join(s.HydraHome, "mcp_policy.json"),
				[]byte(rule), 0o600); err != nil {
				t.Fatal(err)
			}

			code, out := runBinary(t, s, "mcp", "check", "fs", "--action", "read",
				"--resource", "/etc/shadow", "--agent", "test")
			if code != tc.wantCode {
				t.Errorf("`mcp check` against a %q rule exited %d, want %d; a caller "+
					"gating on the code cannot tell this from approval:\n%s",
					tc.decision, code, tc.wantCode, out)
			}
			if got := lastLedgerDecision(t, s); got != tc.decision {
				t.Errorf("the ledger recorded decision %q, want %q: the gate never ran, "+
					"so the exit code above says nothing about it", got, tc.decision)
			}
		})
	}
}

// lastLedgerDecision reads the decision off the newest ledger event, which is
// how this file tells "the gate ran and refused" from "the command failed".
func lastLedgerDecision(t *testing.T, s *testutil.Sandbox) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.HydraHome, "mcp_ledger.jsonl"))
	if err != nil {
		t.Fatalf("no ledger was written, so nothing was gated: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	var evt struct {
		Decision string `json:"decision"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &evt); err != nil {
		t.Fatal(err)
	}
	return evt.Decision
}

// stdout must stay parseable: a banner or a warning belongs on stderr, or it
// lands in whatever is consuming the JSON.
func TestExitCodes_JSONGoesToStdoutAlone(t *testing.T) {
	requireHyctl(t)
	s := testutil.NewSandbox(t)

	cmd := exec.Command(hyctlBin, "models", "list", "--json")
	cmd.Env = append(os.Environ(),
		"HOME="+s.Home, "USERPROFILE="+s.Home, "HYDRA_HOME="+s.HydraHome,
		"HYDRA_NO_UPDATE_CHECK=1", "PATH="+s.BinDir,
	)
	stdout, err := cmd.Output() // stderr deliberately not merged
	if err != nil {
		t.Skipf("`hyctl models list --json` is unavailable here: %v", err)
	}
	trimmed := strings.TrimSpace(string(stdout))
	if trimmed == "" {
		t.Fatal("nothing on stdout")
	}
	if trimmed[0] != '{' && trimmed[0] != '[' {
		t.Errorf("stdout does not start with JSON, something else was printed to "+
			"it:\n%s", trimmed)
	}
}
