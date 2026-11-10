// SPDX-License-Identifier: MIT

//go:build linux || darwin

package sandbox

import (
	"os"
	"os/exec"
	"reflect"
	"testing"
)

func TestWithLimits_RefusesNilEnv(t *testing.T) {
	cmd := exec.Command("/bin/echo", "hello")
	// cmd.Env left nil on purpose: this is the exact footgun the doc comment
	// warns callers about, and it must fail loudly rather than silently
	// narrow the child's environment to just the two limit variables.
	if err := WithLimits(cmd, 512, 0); err == nil {
		t.Error("expected an error for a nil cmd.Env, got nil")
	}
}

func TestWithLimits_NoOpBelowZero(t *testing.T) {
	cmd := exec.Command("/bin/echo", "hello", "world")
	cmd.Env = []string{"FOO=bar"}
	wantPath, wantArgs, wantEnv := cmd.Path, append([]string{}, cmd.Args...), append([]string{}, cmd.Env...)

	if err := WithLimits(cmd, 0, 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd.Path != wantPath || !reflect.DeepEqual(cmd.Args, wantArgs) || !reflect.DeepEqual(cmd.Env, wantEnv) {
		t.Errorf("WithLimits(0, 0) changed cmd: path=%q args=%v env=%v", cmd.Path, cmd.Args, cmd.Env)
	}
}

func TestWithLimits_RewritesForSelfReexec(t *testing.T) {
	cmd := exec.Command("/bin/echo", "hello", "world")
	cmd.Env = []string{"FOO=bar"}

	if err := WithLimits(cmd, 512, 30); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Path != self {
		t.Errorf("cmd.Path = %q, want the running binary %q", cmd.Path, self)
	}

	wantArgs := []string{self, RlimitExecArg, "--", "/bin/echo", "hello", "world"}
	if !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Errorf("cmd.Args = %v, want %v", cmd.Args, wantArgs)
	}

	wantEnv := []string{"FOO=bar", "HYDRA_RLIMIT_MEM_MB=512", "HYDRA_RLIMIT_CPU_SECONDS=30"}
	if !reflect.DeepEqual(cmd.Env, wantEnv) {
		t.Errorf("cmd.Env = %v, want %v (original entries must survive, not just the new ones)", cmd.Env, wantEnv)
	}
}

func TestWithLimits_OnlyCPU(t *testing.T) {
	cmd := exec.Command("/bin/echo")
	cmd.Env = []string{"FOO=bar"}
	if err := WithLimits(cmd, 0, 10); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"FOO=bar", "HYDRA_RLIMIT_CPU_SECONDS=10"}
	if !reflect.DeepEqual(cmd.Env, want) {
		t.Errorf("cmd.Env = %v, want %v (no memory var when memMB<=0)", cmd.Env, want)
	}
}

// TestRlimitExecHelperProcess is not a real test: syscall.Exec replaces the
// calling process, so RunRlimitExec can only be exercised in a throwaway
// subprocess of the test binary itself, never inline here. Outside that
// subprocess (HYDRA_TEST_RLIMIT_HELPER unset) it is a no-op, so `go test`
// still sees and passes it in the ordinary run.
func TestRlimitExecHelperProcess(t *testing.T) {
	if os.Getenv("HYDRA_TEST_RLIMIT_HELPER") != "1" {
		return
	}
	if err := RunRlimitExec([]string{"true"}); err != nil {
		os.Stderr.WriteString(err.Error())
		os.Exit(1)
	}
	os.Exit(2) // unreachable on success: exec replaced this process
}

// TestRunRlimitExec drives the helper above as a real subprocess so the
// syscall.Setrlimit + syscall.Exec path actually runs, end to end, without
// ever calling RunRlimitExec from the test binary's own process.
func TestRunRlimitExec(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestRlimitExecHelperProcess$")
	cmd.Env = append(os.Environ(),
		"HYDRA_TEST_RLIMIT_HELPER=1",
		"HYDRA_RLIMIT_MEM_MB=64",
		"HYDRA_RLIMIT_CPU_SECONDS=5",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper process failed: %v, output: %s", err, out)
	}
}

func TestRunRlimitExec_LookPathFails(t *testing.T) {
	// A bare name with no "/" goes through exec.LookPath, which fails safely
	// (no exec is ever attempted) for a binary that cannot exist.
	if err := RunRlimitExec([]string{"hydra-test-no-such-binary-xyz"}); err == nil {
		t.Error("expected an error for an unresolvable command, got nil")
	}
}

func TestRunRlimitExec_ExecFails(t *testing.T) {
	// An absolute path skips LookPath and goes straight to syscall.Exec,
	// which fails safely (returns an error rather than replacing the
	// process) for a path that exists but is not executable.
	if err := RunRlimitExec([]string{"/dev/null"}); err == nil {
		t.Error("expected an error execing a non-executable path, got nil")
	}
}

func TestRunRlimitExec_NoTarget(t *testing.T) {
	if err := RunRlimitExec(nil); err == nil {
		t.Error("expected an error for an empty argv, got nil")
	}
}

func TestRunRlimitExec_BadEnvValue(t *testing.T) {
	t.Setenv(RlimitMemEnv, "not-a-number")
	if err := RunRlimitExec([]string{"true"}); err == nil {
		t.Error("expected an error for an unparseable limit, got nil")
	}
}

func TestStripRlimitEnv(t *testing.T) {
	in := []string{"FOO=bar", RlimitMemEnv + "=512", "BAZ=qux", RlimitCPUEnv + "=30"}
	got := stripRlimitEnv(in)
	want := []string{"FOO=bar", "BAZ=qux"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("stripRlimitEnv(%v) = %v, want %v (the real target must not see Hydra's own limit config)", in, got, want)
	}
}
