// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

// buildHyctl compiles the real binary once per test run: the point of this
// file is proving cmdRlimitExec's cobra wiring works from the actual
// compiled entry point, which internal/sandbox's own tests cannot reach
// (they exercise RunRlimitExec directly, never rootCmd()).
func buildHyctl(t *testing.T) string {
	t.Helper()
	bin := t.TempDir() + "/hyctl-e2e"
	cmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build hyctl for e2e test: %v\n%s", err, out)
	}
	return bin
}

// The real binary, invoked exactly as sandbox.WithLimits would invoke it,
// must still run an ordinary command when no limit applies to it.
func TestRlimitExecE2E_PassthroughRunsTheRealTarget(t *testing.T) {
	bin := buildHyctl(t)
	out, err := exec.Command(bin, "__rlimit-exec", "--", "/bin/echo", "hello", "world").CombinedOutput()
	if err != nil {
		t.Fatalf("unexpected error: %v, output: %s", err, out)
	}
	if got, want := string(out), "hello world\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

// The real binary, given a CPU-seconds ceiling, must actually have the
// kernel enforce it against a genuinely runaway process: this is what
// sandbox.WithLimits exists to guarantee end to end, not just that it
// rewrites an exec.Cmd correctly.
func TestRlimitExecE2E_KernelKillsARunawayProcess(t *testing.T) {
	bin := buildHyctl(t)
	cmd := exec.Command(bin, "__rlimit-exec", "--", "/bin/sh", "-c", "i=0; while true; do i=$((i+1)); done")
	cmd.Env = append(cmd.Environ(), "HYDRA_RLIMIT_CPU_SECONDS=1")

	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected the runaway process to be killed, it exited cleanly instead")
	}
	// A generous ceiling: RLIMIT_CPU counts CPU time, not wall clock, but a
	// process this tight in a loop consumes it at roughly 1:1, and giving
	// real CI runners room for scheduling noise beats a flake that looks
	// like a real regression.
	if elapsed > 10*time.Second {
		t.Errorf("process ran for %s, want it killed near the 1s CPU ceiling", elapsed)
	}
	t.Logf("killed after %s: %v", elapsed, err)
}

// Negative control for the test above: the identical command, with no
// ceiling configured, must not die on its own within the same window —
// otherwise the kill above could be coincidence rather than enforcement.
func TestRlimitExecE2E_NoLimitSurvives(t *testing.T) {
	bin := buildHyctl(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "__rlimit-exec", "--", "/bin/sh", "-c", "i=0; while true; do i=$((i+1)); done")
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected the context deadline to end the process; it exited on its own instead")
	}
	if ctx.Err() == nil {
		t.Fatalf("process ended for a reason other than the test's own deadline: %v", err)
	}
}

// A memory ceiling must never surface as an error to the caller: enforced
// for real on Linux, a documented no-op on Darwin (RLIMIT_AS/RLIMIT_DATA
// fail with EINVAL there), but the real binary must run the target either way.
func TestRlimitExecE2E_MemoryLimitRunsTheTarget(t *testing.T) {
	bin := buildHyctl(t)
	cmd := exec.Command(bin, "__rlimit-exec", "--", "/bin/echo", "ok")
	cmd.Env = append(cmd.Environ(), "HYDRA_RLIMIT_MEM_MB=64")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("unexpected error with a memory ceiling set: %v, output: %s", err, out)
	}
}
