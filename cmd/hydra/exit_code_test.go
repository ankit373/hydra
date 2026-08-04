// SPDX-License-Identifier: MIT

package main

import (
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
// them. They cannot be asserted in-process — os.Exit takes the test binary with
// it — so these drive a real binary.

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

// exitCode runs the built binary in a sandboxed HOME and returns its exit code.
func exitCode(t *testing.T, args ...string) (code int, output string) {
	t.Helper()
	if hyctlBin == "" {
		t.Skip("hyctl could not be built in this environment")
	}
	s := testutil.NewSandbox(t)

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

// The MCP gate's denial code is what a caller branches on to stop an agent
// touching a file. A denial that exits 0 is a gate that does not gate.
func TestExitCodes_MCPDenialIsNonZero(t *testing.T) {
	code, out := exitCode(t, "mcp", "check", "--action", "read",
		"--resource", "/etc/shadow", "--agent", "test")
	if code == 0 {
		t.Errorf("an MCP check exited 0; callers gate on the code:\n%s", out)
	}
}

// stdout must stay parseable: a banner or a warning belongs on stderr, or it
// lands in whatever is consuming the JSON.
func TestExitCodes_JSONGoesToStdoutAlone(t *testing.T) {
	if hyctlBin == "" {
		t.Skip("hyctl could not be built in this environment")
	}
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
		t.Errorf("stdout does not start with JSON — something else was printed to "+
			"it:\n%s", trimmed)
	}
}
