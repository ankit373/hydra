// SPDX-License-Identifier: MIT

//go:build !windows

package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The whole point of the process group: exec.CommandContext kills the head it
// started and nothing else, so a helper the head spawned keeps running with
// the pipe still open. internal/swarm carries an explicit WaitGroup drain to
// work around the symptom.
func TestHarden_CancelKillsTheHelperToo(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "helper.pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// A head that spawns a helper and then waits, the shape every CLI agent has.
	cmd := Harden(exec.CommandContext(ctx, "sh", "-c",
		"sleep 30 & echo $! > "+pidFile+"; sleep 30"))
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the head: %v", err)
	}

	helper := readPID(t, pidFile)
	cancel()
	_ = cmd.Wait()

	if !gone(helper, 5*time.Second) {
		_ = syscall.Kill(helper, syscall.SIGKILL) // never leak it past the test
		t.Fatalf("helper %d survived cancellation of its head", helper)
	}
}

// A head that has already exited is not a cancellation failure: reporting
// ESRCH would surface "no such process" as the reason a run ended.
func TestHarden_CancelIsQuietWhenTheHeadAlreadyExited(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := Harden(exec.CommandContext(ctx, "sh", "-c", "exit 0"))
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the head: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("head should have exited cleanly: %v", err)
	}
	if err := cmd.Cancel(); err != nil {
		t.Errorf("cancelling a finished head reported %v, want nil", err)
	}
}

func TestHarden_SetsTheBoundsItPromises(t *testing.T) {
	cmd := Harden(exec.Command("true"))
	if cmd.WaitDelay != waitDelay {
		t.Errorf("WaitDelay = %v, want %v; without it Wait hangs on a grandchild holding the pipe", cmd.WaitDelay, waitDelay)
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Error("Setpgid is not set, so a cancelled run reaches only the direct child")
	}
}

// readPID waits for the helper to record itself, then returns its pid.
func readPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the helper never recorded its pid to %s", path)
	return 0
}

// gone reports whether the process has left the table. Signal 0 checks for
// existence without delivering anything.
func gone(pid int, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) == syscall.ESRCH {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}
