// SPDX-License-Identifier: MIT

//go:build !windows

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/testutil"
)

// Ctrl+C has to reach the subprocess, not just hyctl. Nothing caught SIGINT
// before #738: it worked only because the shell signals the whole foreground
// process group, which stopped being true the moment sandbox.Harden put heads
// in a group of their own.
//
// `oracle verify` is the shortest path that really runs one, so this covers
// the whole chain: signal → context → exec.CommandContext → Harden's group kill.
func TestInterrupt_KillsTheSubprocessAndExits130(t *testing.T) {
	requireHyctl(t)
	s := testutil.NewSandbox(t)

	// The verifier announces itself before blocking, so the signal below lands
	// while it is genuinely running. Signalling on a timer instead raced
	// hyctl's own startup and failed under load.
	//
	// Absolute paths and a shell builtin for the marker: the sandbox strips
	// PATH to its own bin dir, which macOS's sh papered over and Ubuntu's dash
	// did not.
	started := filepath.Join(t.TempDir(), "verifier-started")
	cmd := exec.Command(hyctlBin, "oracle", "verify", "--",
		"/bin/sh", "-c", ": > "+started+"; /bin/sleep 60")
	cmd.Env = append(os.Environ(),
		"HOME="+s.Home,
		"USERPROFILE="+s.Home,
		"HYDRA_HOME="+s.HydraHome,
		"HYDRA_NO_UPDATE_CHECK=1",
		"PATH="+s.BinDir,
	)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting hyctl: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	waitForFile(t, started, done, &out)

	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("signalling hyctl: %v", err)
	}

	select {
	case err := <-done:
		var code int
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		}
		// 130 is the shell's convention for SIGINT, and exit codes are a
		// contract here (see exit_code_test.go). A verifier killed mid-run is
		// also not a failing verifier: -1 here would mean hyctl died itself,
		// and 1 would mean the oracle recorded a FAIL verdict for it.
		if code != 130 {
			t.Errorf("exit code %d after SIGINT, want 130 (err: %v)\n%s", code, err, out.String())
		}
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("hyctl ignored SIGINT and sat out the 60s sleep, so the signal never reached the verifier")
	}
}

// waitForFile blocks until the verifier has started, failing loudly if hyctl
// exits first rather than leaving a confusing timeout.
func waitForFile(t *testing.T, path string, done <-chan error, out *bytes.Buffer) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("hyctl exited before the verifier ran (%v):\n%s", err, out.String())
		default:
		}
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the verifier never started:\n%s", out.String())
}
